package security

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
)

// AuditEventType represents the type of security event.
type AuditEventType string

const (
	// Authentication events
	EventAuthSuccess    AuditEventType = "auth_success"
	EventAuthFailure    AuditEventType = "auth_failure"
	EventAuthInvalidKey AuditEventType = "auth_invalid_key"
	EventAuthMissingKey AuditEventType = "auth_missing_key"

	// Authorization events
	EventAuthzGranted AuditEventType = "authz_granted"
	EventAuthzDenied  AuditEventType = "authz_denied"

	// Rate limiting events
	EventRateLimitExceeded AuditEventType = "rate_limit_exceeded"
	EventRateLimitWarning  AuditEventType = "rate_limit_warning"

	EventSuspiciousActivity AuditEventType = "suspicious_activity"
	EventBruteForceDetected AuditEventType = "brute_force_detected"
	EventIPBlocked          AuditEventType = "ip_blocked"

	// API key lifecycle
	EventAPIKeyGenerated AuditEventType = "api_key_generated"
	EventAPIKeyValidated AuditEventType = "api_key_validated"
	EventAPIKeyRevoked   AuditEventType = "api_key_revoked"

	// Security configuration
	EventSecurityConfigChanged AuditEventType = "security_config_changed"
	EventTLSEnabled            AuditEventType = "tls_enabled"
	EventTLSDisabled           AuditEventType = "tls_disabled"

	// Resource access
	EventResourceCreated  AuditEventType = "resource_created"
	EventResourceModified AuditEventType = "resource_modified"
	EventResourceDeleted  AuditEventType = "resource_deleted"
	EventResourceAccessed AuditEventType = "resource_accessed"
)

// AuditSeverity represents the severity level of an audit event.
type AuditSeverity string

const (
	SeverityInfo     AuditSeverity = "INFO"
	SeverityWarning  AuditSeverity = "WARNING"
	SeverityCritical AuditSeverity = "CRITICAL"
)

// AuditEvent represents a single security audit event.
type AuditEvent struct {
	Timestamp  time.Time              `json:"timestamp"`
	EventType  AuditEventType         `json:"event_type"`
	Severity   AuditSeverity          `json:"severity"`
	ClientIP   string                 `json:"client_ip,omitempty"`
	APIKeyHash string                 `json:"api_key_hash,omitempty"`
	UserAgent  string                 `json:"user_agent,omitempty"`
	Resource   string                 `json:"resource,omitempty"`
	Action     string                 `json:"action,omitempty"`
	Message    string                 `json:"message"`
	Details    map[string]interface{} `json:"details,omitempty"`
}

// AuditLoggerConfig configures the audit logger.
type AuditLoggerConfig struct {
	// LogFile is the path to the audit log file
	LogFile string

	// MaxSizeMB is the maximum size of a log file before rotation (in MB)
	MaxSizeMB int64

	// MaxAgeDays is the maximum age of log files to keep
	MaxAgeDays int

	// MaxBackups is the maximum number of old log files to keep
	MaxBackups int

	// Compress determines if rotated logs should be compressed
	Compress bool

	// AsyncWrite enables asynchronous logging (non-blocking)
	AsyncWrite bool

	// BufferSize is the size of the async write buffer
	BufferSize int
}

// DefaultAuditLoggerConfig returns default configuration.
func DefaultAuditLoggerConfig() AuditLoggerConfig {
	return AuditLoggerConfig{
		LogFile:    "/var/log/hospitus/audit.log",
		MaxSizeMB:  100, // 100 MB
		MaxAgeDays: 90,  // 90 days
		MaxBackups: 10,  // Keep 10 old files
		Compress:   true,
		AsyncWrite: true,
		BufferSize: 1000,
	}
}

// AuditLogger handles security audit logging.
type AuditLogger struct {
	config    AuditLoggerConfig
	file      *os.File
	mu        sync.Mutex
	eventChan chan *AuditEvent
	done      chan struct{}
	wg        sync.WaitGroup
	closed    atomic.Bool
}

var (
	// globalAuditLogger is accessed concurrently; use an atomic pointer so
	// reads and writes are race-free. It stays nil until a real logger is
	// successfully created, allowing initialization to be retried after an
	// earlier failure.
	globalAuditLogger   atomic.Pointer[AuditLogger]
	globalAuditLoggerMu sync.Mutex
	noopAuditLogger     *AuditLogger // No-op logger returned when real logger is unavailable
)

// InitGlobalAuditLogger initializes the global audit logger. It is safe to call
// concurrently and retries after a previous failed attempt (nothing is cached
// unless a logger is successfully created).
func InitGlobalAuditLogger(config AuditLoggerConfig) error {
	globalAuditLoggerMu.Lock()
	defer globalAuditLoggerMu.Unlock()

	if globalAuditLogger.Load() != nil {
		return nil
	}

	logger, err := NewAuditLogger(config)
	if err != nil {
		return err
	}
	globalAuditLogger.Store(logger)
	return nil
}

// GetGlobalAuditLogger returns the global audit logger instance.
// If the logger is not initialized or initialization failed, returns a no-op logger
// that discards all events to prevent nil pointer panics.
func GetGlobalAuditLogger() *AuditLogger {
	if l := globalAuditLogger.Load(); l != nil {
		return l
	}

	// Try to initialize with default config if not already initialized.
	_ = InitGlobalAuditLogger(DefaultAuditLoggerConfig())

	if l := globalAuditLogger.Load(); l != nil {
		return l
	}

	// Initialization failed: surface the fallback instead of failing silently.
	logging.WithComponent("audit").Error("global audit logger unavailable; falling back to no-op logger")
	return getNoopAuditLogger()
}

var noopAuditLoggerOnce sync.Once

// getNoopAuditLogger returns a no-op audit logger that discards all events.
// This is used when the real audit logger cannot be initialized (e.g., in tests).
func getNoopAuditLogger() *AuditLogger {
	noopAuditLoggerOnce.Do(func() {
		// Open /dev/null for the no-op logger to avoid panics
		devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			// If we can't even open /dev/null, create a logger with nil file
			// The Log method will silently fail on nil file
			noopAuditLogger = &AuditLogger{
				config: AuditLoggerConfig{},
				file:   nil,
			}
		} else {
			noopAuditLogger = &AuditLogger{
				config: AuditLoggerConfig{},
				file:   devNull,
			}
		}
	})
	return noopAuditLogger
}

// NewAuditLogger creates a new audit logger.
func NewAuditLogger(config AuditLoggerConfig) (*AuditLogger, error) {
	// Create log directory if it doesn't exist
	logDir := filepath.Dir(config.LogFile)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Open log file
	// Use 0600 permissions to prevent world-readable access
	// Audit logs contain sensitive information (API key hashes, client IPs, access patterns)
	file, err := os.OpenFile(config.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	logger := &AuditLogger{
		config: config,
		file:   file,
	}

	// Start async writer if enabled
	if config.AsyncWrite {
		logger.eventChan = make(chan *AuditEvent, config.BufferSize)
		logger.done = make(chan struct{})
		logger.wg.Add(1)
		go logger.asyncWriter()
	}

	return logger, nil
}

// Log logs an audit event.
func (l *AuditLogger) Log(event *AuditEvent) error {
	// Handle nil file gracefully (for no-op logger)
	if l.file == nil {
		return nil
	}

	// Drop events once the logger is closed. This prevents a send on a
	// drained channel or a write to a closed file from a late caller.
	if l.closed.Load() {
		return nil
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	if l.config.AsyncWrite {
		// Non-blocking async write
		select {
		case l.eventChan <- event:
			return nil
		default:
			// Buffer full, log synchronously
			return l.writeEvent(event)
		}
	}

	// Synchronous write
	return l.writeEvent(event)
}

// writeEvent writes an event to the log file.
func (l *AuditLogger) writeEvent(event *AuditEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Check if rotation is needed
	if err := l.rotateIfNeeded(); err != nil {
		return fmt.Errorf("log rotation failed: %w", err)
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	if _, err := l.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to write event: %w", err)
	}

	return nil
}

// asyncWriter processes events from the channel asynchronously.
func (l *AuditLogger) asyncWriter() {
	defer l.wg.Done()

	for {
		select {
		case event := <-l.eventChan:
			if err := l.writeEvent(event); err != nil {
				// Failed to write event, log to stderr
				fmt.Fprintf(os.Stderr, "Audit log write error: %v\n", err)
			}
		case <-l.done:
			// Drain remaining events
			for {
				select {
				case event := <-l.eventChan:
					_ = l.writeEvent(event)
				default:
					return
				}
			}
		}
	}
}

// rotateIfNeeded rotates the log file if it exceeds the maximum size.
func (l *AuditLogger) rotateIfNeeded() error {
	// Get current file info
	info, err := l.file.Stat()
	if err != nil {
		return err
	}

	// Check if rotation is needed
	if info.Size() < l.config.MaxSizeMB*1024*1024 {
		return nil
	}

	// Close current file
	if err := l.file.Close(); err != nil {
		return err
	}

	// From here the descriptor is closed. Any subsequent failure must reopen
	// the log file so the logger is not left permanently broken.
	rotatedName := fmt.Sprintf("%s.%s", l.config.LogFile, time.Now().Format("2006-01-02-15-04-05"))
	if err := os.Rename(l.config.LogFile, rotatedName); err != nil {
		return l.reopenAfterRotationFailure(err)
	}

	// Compress if enabled (run synchronously to avoid race with cleanupOldLogs)
	if l.config.Compress {
		if err := compressLogFile(rotatedName); err != nil {
			return l.reopenAfterRotationFailure(err)
		}
	}

	// Clean up old logs synchronously (l.mu is already held by writeEvent) to
	// avoid a leaked goroutine racing with the next rotation.
	l.cleanupOldLogs()

	// Open new file
	// Use 0600 permissions for rotated log files
	file, err := os.OpenFile(l.config.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	l.file = file

	return nil
}

// reopenAfterRotationFailure reopens the log file after a rotation step failed
// with the descriptor already closed, so the logger can keep writing. The
// original rotation error is always propagated (wrapped) to the caller.
func (l *AuditLogger) reopenAfterRotationFailure(cause error) error {
	file, err := os.OpenFile(l.config.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("log rotation failed (%w); reopen also failed: %w", cause, err)
	}
	l.file = file
	return fmt.Errorf("log rotation failed: %w", cause)
}

// cleanupOldLogs removes old log files based on retention policy.
func (l *AuditLogger) cleanupOldLogs() {
	logDir := filepath.Dir(l.config.LogFile)
	baseName := filepath.Base(l.config.LogFile)

	files, err := os.ReadDir(logDir)
	if err != nil {
		return
	}

	// Collect rotated log files
	type logFileInfo struct {
		path    string
		modTime time.Time
	}
	var logFiles []logFileInfo

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		name := file.Name()
		// Match rotated files (baseName.YYYY-MM-DD-HH-MM-SS or baseName.YYYY-MM-DD-HH-MM-SS.gz)
		if len(name) > len(baseName) && name[:len(baseName)] == baseName && name[len(baseName)] == '.' {
			info, err := file.Info()
			if err != nil {
				continue
			}

			logFiles = append(logFiles, logFileInfo{
				path:    filepath.Join(logDir, name),
				modTime: info.ModTime(),
			})
		}
	}

	// Remove files older than MaxAgeDays
	cutoff := time.Now().AddDate(0, 0, -l.config.MaxAgeDays)
	for _, lf := range logFiles {
		if lf.modTime.Before(cutoff) {
			_ = os.Remove(lf.path)
		}
	}

	// Remove files exceeding MaxBackups (keep newest N files)
	if l.config.MaxBackups > 0 && len(logFiles) > l.config.MaxBackups {
		// Sort by modification time, newest first
		sort.Slice(logFiles, func(i, j int) bool {
			return logFiles[i].modTime.After(logFiles[j].modTime)
		})
		// Remove the excess oldest files
		for _, lf := range logFiles[l.config.MaxBackups:] {
			_ = os.Remove(lf.path)
		}
	}
}

// Close closes the audit logger and flushes any pending events.
func (l *AuditLogger) Close() error {
	// Mark closed first so any concurrent Log() drops its event instead of
	// racing the shutdown. eventChan is deliberately left unclosed (GC
	// reclaims it) so a late send cannot panic on a closed channel.
	l.closed.Store(true)

	if l.config.AsyncWrite {
		close(l.done)
		l.wg.Wait()
	}

	if l.file == nil {
		return nil
	}
	return l.file.Close()
}

// LogAuthSuccess logs a successful authentication event.
func (l *AuditLogger) LogAuthSuccess(clientIP, apiKeyHash string) {
	_ = l.Log(&AuditEvent{
		EventType:  EventAuthSuccess,
		Severity:   SeverityInfo,
		ClientIP:   clientIP,
		APIKeyHash: apiKeyHash,
		Message:    "Authentication successful",
	})
}

// LogAuthFailure logs a failed authentication event.
func (l *AuditLogger) LogAuthFailure(clientIP, reason string) {
	_ = l.Log(&AuditEvent{
		EventType: EventAuthFailure,
		Severity:  SeverityWarning,
		ClientIP:  clientIP,
		Message:   fmt.Sprintf("Authentication failed: %s", reason),
	})
}

// LogRateLimitExceeded logs a rate limit violation.
func (l *AuditLogger) LogRateLimitExceeded(clientIP, endpoint string) {
	_ = l.Log(&AuditEvent{
		EventType: EventRateLimitExceeded,
		Severity:  SeverityWarning,
		ClientIP:  clientIP,
		Resource:  endpoint,
		Message:   "Rate limit exceeded",
	})
}

// LogSuspiciousActivity logs suspicious activity detection.
func (l *AuditLogger) LogSuspiciousActivity(clientIP, description string, details map[string]interface{}) {
	_ = l.Log(&AuditEvent{
		EventType: EventSuspiciousActivity,
		Severity:  SeverityCritical,
		ClientIP:  clientIP,
		Message:   description,
		Details:   details,
	})
}

// LogResourceAccess logs resource access events.
func (l *AuditLogger) LogResourceAccess(clientIP, apiKeyHash, action, resource string) {
	_ = l.Log(&AuditEvent{
		EventType:  EventResourceAccessed,
		Severity:   SeverityInfo,
		ClientIP:   clientIP,
		APIKeyHash: apiKeyHash,
		Action:     action,
		Resource:   resource,
		Message:    fmt.Sprintf("%s %s", action, resource),
	})
}

// LogSecurityConfigChange logs security configuration changes.
func (l *AuditLogger) LogSecurityConfigChange(description string, details map[string]interface{}) {
	_ = l.Log(&AuditEvent{
		EventType: EventSecurityConfigChanged,
		Severity:  SeverityWarning,
		Message:   description,
		Details:   details,
	})
}

// compressLogFile compresses a log file using gzip.
func compressLogFile(filename string) error {
	sourceFile, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open log file for compression: %w", err)
	}
	defer sourceFile.Close()

	destFile, err := os.OpenFile(filename+".gz", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create gzip file: %w", err)
	}

	gzipWriter := gzip.NewWriter(destFile)

	if _, err := io.Copy(gzipWriter, sourceFile); err != nil {
		gzipWriter.Close()
		destFile.Close()
		return fmt.Errorf("failed to compress log data: %w", err)
	}

	// Explicitly finalize the gzip stream and flush the destination file to
	// disk before removing the original, so a crash cannot leave a truncated
	// archive with the source already deleted.
	if err := gzipWriter.Close(); err != nil {
		destFile.Close()
		return fmt.Errorf("failed to finalize gzip stream: %w", err)
	}
	if err := destFile.Sync(); err != nil {
		destFile.Close()
		return fmt.Errorf("failed to sync gzip file: %w", err)
	}
	if err := destFile.Close(); err != nil {
		return fmt.Errorf("failed to close gzip file: %w", err)
	}

	if err := os.Remove(filename); err != nil {
		return fmt.Errorf("failed to remove original uncompressed log file: %w", err)
	}

	return nil
}

// QueryAuditLogs queries audit logs with filters.
type AuditLogQuery struct {
	StartTime  time.Time
	EndTime    time.Time
	EventTypes []AuditEventType
	Severities []AuditSeverity
	ClientIP   string
	Limit      int
}

// Query searches audit logs based on query parameters.
func (l *AuditLogger) Query(query AuditLogQuery) ([]*AuditEvent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Open log file for reading
	file, err := os.Open(l.config.LogFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var events []*AuditEvent
	decoder := json.NewDecoder(file)

	for {
		var event AuditEvent
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			continue // Skip malformed lines
		}

		// Apply filters
		if !query.StartTime.IsZero() && event.Timestamp.Before(query.StartTime) {
			continue
		}
		if !query.EndTime.IsZero() && event.Timestamp.After(query.EndTime) {
			continue
		}
		if len(query.EventTypes) > 0 && !contains(query.EventTypes, event.EventType) {
			continue
		}
		if len(query.Severities) > 0 && !containsSeverity(query.Severities, event.Severity) {
			continue
		}
		if query.ClientIP != "" && event.ClientIP != query.ClientIP {
			continue
		}

		events = append(events, &event)

		// Apply limit
		if query.Limit > 0 && len(events) >= query.Limit {
			break
		}
	}

	return events, nil
}

func contains(types []AuditEventType, t AuditEventType) bool {
	for _, et := range types {
		if et == t {
			return true
		}
	}
	return false
}

func containsSeverity(severities []AuditSeverity, s AuditSeverity) bool {
	for _, sev := range severities {
		if sev == s {
			return true
		}
	}
	return false
}
