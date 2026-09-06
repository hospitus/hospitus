package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditLogger_Creation(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "audit.log")

	config := AuditLoggerConfig{
		LogFile:    logFile,
		MaxSizeMB:  1,
		MaxAgeDays: 7,
		MaxBackups: 3,
		Compress:   false,
		AsyncWrite: false,
		BufferSize: 100,
	}

	logger, err := NewAuditLogger(config)
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}
	defer logger.Close()

	// Verify log file was created
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Error("Log file was not created")
	}
}

func TestAuditLogger_LogEvent(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "audit.log")

	config := AuditLoggerConfig{
		LogFile:    logFile,
		MaxSizeMB:  1,
		MaxAgeDays: 7,
		MaxBackups: 3,
		Compress:   false,
		AsyncWrite: false,
		BufferSize: 100,
	}

	logger, err := NewAuditLogger(config)
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}
	defer logger.Close()

	// Log an event
	event := &AuditEvent{
		EventType:  EventAuthSuccess,
		Severity:   SeverityInfo,
		ClientIP:   "192.168.1.100",
		APIKeyHash: "test_hash_123",
		Message:    "Test authentication",
	}

	if err := logger.Log(event); err != nil {
		t.Fatalf("Failed to log event: %v", err)
	}

	// Read and verify the log file
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	var logged AuditEvent
	if err := json.Unmarshal(data, &logged); err != nil {
		t.Fatalf("Failed to unmarshal logged event: %v", err)
	}

	if logged.EventType != EventAuthSuccess {
		t.Errorf("Expected EventType %s, got %s", EventAuthSuccess, logged.EventType)
	}
	if logged.ClientIP != "192.168.1.100" {
		t.Errorf("Expected ClientIP 192.168.1.100, got %s", logged.ClientIP)
	}
}

func TestAuditLogger_MultipleEvents(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "audit.log")

	config := AuditLoggerConfig{
		LogFile:    logFile,
		MaxSizeMB:  1,
		MaxAgeDays: 7,
		MaxBackups: 3,
		Compress:   false,
		AsyncWrite: false,
		BufferSize: 100,
	}

	logger, err := NewAuditLogger(config)
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}
	defer logger.Close()

	// Log multiple events
	events := []*AuditEvent{
		{EventType: EventAuthSuccess, Severity: SeverityInfo, ClientIP: "192.168.1.1", Message: "Auth 1"},
		{EventType: EventAuthFailure, Severity: SeverityWarning, ClientIP: "192.168.1.2", Message: "Auth 2"},
		{EventType: EventRateLimitExceeded, Severity: SeverityWarning, ClientIP: "192.168.1.3", Message: "Rate limit"},
	}

	for _, event := range events {
		if err := logger.Log(event); err != nil {
			t.Fatalf("Failed to log event: %v", err)
		}
	}

	// Count lines in log file
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}

	if lines != 3 {
		t.Errorf("Expected 3 log lines, got %d", lines)
	}
}

func TestAuditLogger_HelperMethods(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "audit.log")

	config := AuditLoggerConfig{
		LogFile:    logFile,
		MaxSizeMB:  1,
		MaxAgeDays: 7,
		MaxBackups: 3,
		Compress:   false,
		AsyncWrite: false,
		BufferSize: 100,
	}

	logger, err := NewAuditLogger(config)
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}
	defer logger.Close()

	// Test helper methods
	logger.LogAuthSuccess("192.168.1.1", "hash1")
	logger.LogAuthFailure("192.168.1.2", "invalid key")
	logger.LogRateLimitExceeded("192.168.1.3", "/api/instances")
	logger.LogSuspiciousActivity("192.168.1.4", "Brute force attempt", map[string]interface{}{
		"attempts": 10,
	})
	logger.LogResourceAccess("192.168.1.5", "hash2", "GET", "/api/instances/test")
	logger.LogSecurityConfigChange("TLS enabled", map[string]interface{}{
		"cert": "/path/to/cert",
	})

	// Read log file and verify
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}

	if lines != 6 {
		t.Errorf("Expected 6 log lines, got %d", lines)
	}
}

func TestAuditLogger_Query(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "audit.log")

	config := AuditLoggerConfig{
		LogFile:    logFile,
		MaxSizeMB:  1,
		MaxAgeDays: 7,
		MaxBackups: 3,
		Compress:   false,
		AsyncWrite: false,
		BufferSize: 100,
	}

	logger, err := NewAuditLogger(config)
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}
	defer logger.Close()

	// Log test events
	now := time.Now()
	events := []*AuditEvent{
		{
			Timestamp: now.Add(-2 * time.Hour),
			EventType: EventAuthSuccess,
			Severity:  SeverityInfo,
			ClientIP:  "192.168.1.1",
			Message:   "Test 1",
		},
		{
			Timestamp: now.Add(-1 * time.Hour),
			EventType: EventAuthFailure,
			Severity:  SeverityWarning,
			ClientIP:  "192.168.1.2",
			Message:   "Test 2",
		},
		{
			Timestamp: now,
			EventType: EventRateLimitExceeded,
			Severity:  SeverityWarning,
			ClientIP:  "192.168.1.1",
			Message:   "Test 3",
		},
	}

	for _, event := range events {
		if err := logger.Log(event); err != nil {
			t.Fatalf("Failed to log event: %v", err)
		}
	}

	// Test query by time range
	query := AuditLogQuery{
		StartTime: now.Add(-90 * time.Minute),
		EndTime:   now.Add(1 * time.Minute),
	}

	results, err := logger.Query(query)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(results))
	}

	// Test query by event type
	query = AuditLogQuery{
		EventTypes: []AuditEventType{EventAuthSuccess},
	}

	results, err = logger.Query(query)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("Expected 1 result, got %d", len(results))
	}

	// Test query by severity
	query = AuditLogQuery{
		Severities: []AuditSeverity{SeverityWarning},
	}

	results, err = logger.Query(query)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(results))
	}

	// Test query by client IP
	query = AuditLogQuery{
		ClientIP: "192.168.1.1",
	}

	results, err = logger.Query(query)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 results for IP 192.168.1.1, got %d", len(results))
	}

	// Test query with limit
	query = AuditLogQuery{
		Limit: 1,
	}

	results, err = logger.Query(query)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("Expected 1 result with limit, got %d", len(results))
	}
}

func TestAuditLogger_AsyncWrite(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "audit.log")

	config := AuditLoggerConfig{
		LogFile:    logFile,
		MaxSizeMB:  1,
		MaxAgeDays: 7,
		MaxBackups: 3,
		Compress:   false,
		AsyncWrite: true,
		BufferSize: 100,
	}

	logger, err := NewAuditLogger(config)
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}

	// Log events asynchronously
	for i := 0; i < 10; i++ {
		event := &AuditEvent{
			EventType: EventAuthSuccess,
			Severity:  SeverityInfo,
			ClientIP:  "192.168.1.1",
			Message:   "Async test",
		}
		if err := logger.Log(event); err != nil {
			t.Fatalf("Failed to log event: %v", err)
		}
	}

	// Close logger to flush async events
	logger.Close()

	// Verify events were written
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}

	if lines != 10 {
		t.Errorf("Expected 10 log lines, got %d", lines)
	}
}

func TestAuditLogger_Rotation(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "audit.log")

	// Use very small max size to trigger rotation
	config := AuditLoggerConfig{
		LogFile:    logFile,
		MaxSizeMB:  0, // Will be treated as 1 byte for testing
		MaxAgeDays: 7,
		MaxBackups: 3,
		Compress:   false,
		AsyncWrite: false,
		BufferSize: 100,
	}

	logger, err := NewAuditLogger(config)
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}
	defer logger.Close()

	// Log multiple events to trigger rotation
	// Note: In a real scenario, rotation happens based on file size
	// This test is simplified
	for i := 0; i < 5; i++ {
		event := &AuditEvent{
			EventType: EventAuthSuccess,
			Severity:  SeverityInfo,
			ClientIP:  "192.168.1.1",
			Message:   "Rotation test event with some content to increase size",
			Details: map[string]interface{}{
				"index": i,
				"data":  "Lorem ipsum dolor sit amet, consectetur adipiscing elit",
			},
		}
		if err := logger.Log(event); err != nil {
			t.Fatalf("Failed to log event: %v", err)
		}
	}

	// Verify current log file exists
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Error("Current log file does not exist")
	}

	// The active file existing proves nothing about rotation. Check something
	// was rotated out and that its records are still readable — that is what
	// makes the rotated file useful rather than merely present.
	rotated, err := filepath.Glob(logFile + ".*")
	if err != nil {
		t.Fatal(err)
	}
	if len(rotated) == 0 {
		t.Fatal("no rotated file was produced")
	}
	data, err := os.ReadFile(rotated[0])
	if err != nil {
		t.Fatalf("the rotated log cannot be read: %v", err)
	}
	if len(data) == 0 {
		t.Errorf("the rotated log %s is empty", rotated[0])
	}
	var ev AuditEvent
	if err := json.Unmarshal([]byte(strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)[0]), &ev); err != nil {
		t.Errorf("the rotated log does not hold a readable record: %v", err)
	}
}

func TestDefaultAuditLoggerConfig(t *testing.T) {
	config := DefaultAuditLoggerConfig()

	if config.LogFile != "/var/log/hospitus/audit.log" {
		t.Errorf("Expected default log file /var/log/hospitus/audit.log, got %s", config.LogFile)
	}
	if config.MaxSizeMB != 100 {
		t.Errorf("Expected MaxSizeMB 100, got %d", config.MaxSizeMB)
	}
	if config.MaxAgeDays != 90 {
		t.Errorf("Expected MaxAgeDays 90, got %d", config.MaxAgeDays)
	}
	if config.MaxBackups != 10 {
		t.Errorf("Expected MaxBackups 10, got %d", config.MaxBackups)
	}
	if !config.Compress {
		t.Error("Expected Compress to be true")
	}
	if !config.AsyncWrite {
		t.Error("Expected AsyncWrite to be true")
	}
	if config.BufferSize != 1000 {
		t.Errorf("Expected BufferSize 1000, got %d", config.BufferSize)
	}
}

func TestGlobalAuditLogger(t *testing.T) {
	// Point the global logger at a temp file so the test neither writes to the
	// system default path (/var/log/hospitus) nor depends on running as root.
	cfg := DefaultAuditLoggerConfig()
	cfg.LogFile = filepath.Join(t.TempDir(), "audit.log")
	cfg.AsyncWrite = false
	if err := InitGlobalAuditLogger(cfg); err != nil {
		t.Fatalf("InitGlobalAuditLogger: %v", err)
	}

	// GetGlobalAuditLogger always returns a usable, non-nil logger (a no-op
	// logger is substituted when a real one is unavailable), so it must never
	// be nil and must be safe to log through.
	logger := GetGlobalAuditLogger()
	if logger == nil {
		t.Fatal("GetGlobalAuditLogger returned nil")
	}
	if err := logger.Log(&AuditEvent{
		EventType: EventAuthSuccess,
		Severity:  SeverityInfo,
		Message:   "global logger smoke test",
	}); err != nil {
		t.Errorf("Log through global logger failed: %v", err)
	}
}

// TestCloseIsIdempotent covers a second Close: it used to close l.done twice
// and panic, which takes the process with it during shutdown.
func TestCloseIsIdempotent(t *testing.T) {
	l, err := NewAuditLogger(AuditLoggerConfig{
		LogFile: filepath.Join(t.TempDir(), "audit.log"), AsyncWrite: true, BufferSize: 4,
	})
	if err != nil {
		t.Fatalf("NewAuditLogger: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestAuditLogTightensExistingPermissions covers a log left behind by an
// earlier install: OpenFile's mode applies only when it creates the file.
func TestAuditLogTightensExistingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := NewAuditLogger(AuditLoggerConfig{LogFile: path})
	if err != nil {
		t.Fatalf("NewAuditLogger: %v", err)
	}
	defer l.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("audit log left at %04o, want no group or other access", info.Mode().Perm())
	}
}

// TestRotationNamesDoNotCollide covers two rotations inside the same second:
// the second rename would replace the first rotated file, and its events.
func TestRotationNamesDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "audit.log")
	now := time.Now()

	first, err := uniqueRotationName(logFile, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("older events\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := uniqueRotationName(logFile, now)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatalf("both rotations chose %s; the first file would be overwritten", first)
	}
	if _, err := os.Stat(first); err != nil {
		t.Errorf("the earlier rotated log went missing: %v", err)
	}
}

// TestQueryStopsOnMalformedLog covers a corrupt record: json.Decoder does not
// resynchronise after a syntax error, so skipping one re-reads it for ever.
// The call has to return rather than spin.
func TestQueryStopsOnMalformedLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	good := `{"event_type":"auth_success","severity":"info","timestamp":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(good+"\n{ this is not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := NewAuditLogger(AuditLoggerConfig{LogFile: path})
	if err != nil {
		t.Fatalf("NewAuditLogger: %v", err)
	}
	defer l.Close()

	done := make(chan struct{})
	var events []*AuditEvent
	var qErr error
	go func() {
		events, qErr = l.Query(AuditLogQuery{})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Query never returned: the decoder is being re-read in a loop")
	}
	if qErr == nil {
		t.Error("a malformed audit log was reported as read cleanly")
	}
	if len(events) != 1 {
		t.Errorf("got %d events before the corruption, want the 1 good record", len(events))
	}
}
