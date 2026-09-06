// Package api implements Hospitus's HTTP API.
//
// Resource routes live under /api/v1 — providers, instances, stacks, volumes,
// backups, jobs and keys. /health sits outside it and takes no API key;
// /metrics also sits outside it but requires a key unless MetricsPublic is
// enabled. Requests and responses are JSON; a body naming a field the target
// struct does not have is rejected rather than ignored.
//
// Every route is registered in one place, (*Server).registerRoutes, and the
// handlers are grouped by domain across the *_handlers.go files beside it.
//
// See docs/book/src/developer-guide/api-reference.md.
package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/hospitus/hospitus/internal/auth"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/internal/security"
	"github.com/hospitus/hospitus/pkg/backup"
	"github.com/hospitus/hospitus/pkg/dataset"
	"github.com/hospitus/hospitus/pkg/firewall"
	"github.com/hospitus/hospitus/pkg/job"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/network"
	"github.com/hospitus/hospitus/pkg/orchestration"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/storage"
)

// Datastore is the interface for instance metadata storage
type Datastore interface {
	CreateInstance(ctx context.Context, instance *datastore.Instance) error
	GetInstance(ctx context.Context, id string) (*datastore.Instance, error)
	GetInstanceByName(ctx context.Context, name string) (*datastore.Instance, error)
	ListInstances(ctx context.Context, filter datastore.InstanceFilter) ([]*datastore.Instance, error)
	UpdateInstanceState(ctx context.Context, id string, state provider.InstanceState) error
	UpdateInstanceSpec(ctx context.Context, id string, spec provider.InstanceSpec) error
	UpdateInstanceHandle(ctx context.Context, id string, handle provider.InstanceHandle) error
	UpdateBackupConfig(ctx context.Context, id string, config *datastore.InstanceBackupConfig) error
	RenameInstance(ctx context.Context, oldName, newName string) error
	DeleteInstance(ctx context.Context, id string) error
	GetEvents(ctx context.Context, instanceID string, limit int) ([]*datastore.Event, error)
	Ping(ctx context.Context) error

	CreateJob(ctx context.Context, j *job.Job) error
	GetJob(ctx context.Context, id string) (*job.Job, error)
	ListJobs(ctx context.Context, status *job.JobStatus) ([]*job.Job, error)
	UpdateJob(ctx context.Context, j *job.Job) error
	DeleteJob(ctx context.Context, id string) error
	FailInterruptedJobs(ctx context.Context) (int, error)
	CleanupOldJobs(ctx context.Context, maxAge time.Duration) (int, error)
}

// Server represents the HTTP API server.
//
// Architecture:
//   - Mux: HTTP router (using standard library)
//   - Datastore: Persistent instance metadata
//   - Registry: Provider registry for VM/container operations
//   - HTTPServer: Underlying HTTP server
//   - AuthProvider: Authentication provider (API keys)
//   - RateLimiters: Per-IP rate limiters
type Server struct {
	mux              *http.ServeMux
	datastore        Datastore
	registry         *provider.Registry
	httpServer       *http.Server
	addr             string
	authProvider     auth.AuthProvider
	rateLimiters     map[string]*rate.Limiter
	authRateLimiters map[string]*rate.Limiter // Rate limiters for auth attempts
	rateLimiterMu    sync.RWMutex
	config           *ServerConfig
	cancelCleanup    context.CancelFunc // Cancel function for rate limiter cleanup
	stackManager     *orchestration.StackManager
	backupManager    *backup.BackupManager
	sysCollector     *security.SystemCollector
	sshManager       *security.SSHManager
	bridges          network.Lister
	logger           *slog.Logger
	jobManager       *job.JobManager
	firewallMgr      *firewall.Manager
	// storage serves the volume endpoints. It is nil where no backend exists
	// (ZFS volumes are a FreeBSD and Linux feature), and the handlers answer
	// "not implemented" rather than failing per operation.
	storage               storage.Manager
	activeConsoleSessions sync.Map // key: sessionID (string) -> value: *ConsoleSession
}

// ServerConfig contains server configuration
type ServerConfig struct {
	// Authentication
	EnableAuth bool
	APIKeys    []string

	// AllowNoAuth must be explicitly set to true to run without authentication.
	// This prevents accidental deployment without authentication.
	AllowNoAuth bool

	// MetricsPublic allows unauthenticated access to /metrics endpoints.
	// Useful for Prometheus scraping without an API key.
	// Default: false (metrics require auth).
	MetricsPublic bool

	// CORS
	AllowedOrigins []string

	// Rate Limiting
	EnableRateLimit   bool
	RequestsPerSecond int
	BurstSize         int

	// TLS
	TLSCert string
	TLSKey  string

	// AllowInsecureTLS must be explicitly set to true to run without TLS.
	// This prevents accidental deployment without encryption.
	AllowInsecureTLS bool

	// Timeouts
	WriteTimeout time.Duration // HTTP write timeout (default: 30m for long exec commands)
	ReadTimeout  time.Duration // HTTP read timeout (default: 15s)
	IdleTimeout  time.Duration // HTTP idle timeout (default: 60s)

	// Trusted Proxies
	// Only trust X-Forwarded-For from these IPs/CIDRs
	// Examples: ["127.0.0.1", "10.0.0.0/8", "172.16.0.0/12"]
	// If empty, X-Forwarded-For headers are ignored (safest default)
	TrustedProxies []string

	// Logging
	LogLevel  string
	LogFormat string

	DataDir string

	// AuditLogFile is where security audit events are appended. When empty it
	// defaults to <DataDir>/logs/audit.log: the daemon already owns DataDir, so
	// that path is writable wherever the daemon can run at all. Packaging that
	// wants the conventional /var/log location passes it explicitly.
	AuditLogFile string
}

// instanceDatasetResolver reports the ZFS dataset holding an instance, read from
// what its provider recorded rather than derived from its name.
//
// The jail provider puts the dataset in the instance handle. A bhyve VM's disks
// are ZVOLs, so the dataset is their parent. A provider with neither — podman,
// qemu on a file image — has nothing to snapshot, and says so instead of naming
// a dataset that does not exist.
// ensureHospitusDataset refuses a dataset name that does not sit strictly below
// the parent this daemon manages.
func ensureHospitusDataset(name string) error {
	parent := dataset.Parent()
	if name == "" || strings.ContainsAny(name, "@ \t\n") || strings.Contains(name, "/../") || strings.HasSuffix(name, "/..") {
		return fmt.Errorf("dataset %q is not a name this daemon manages", name)
	}
	if parent == "" || !strings.HasPrefix(name, parent+"/") || len(name) <= len(parent)+1 {
		return fmt.Errorf("dataset %q is outside %s", name, parent)
	}
	return nil
}

func instanceDatasetResolver(ds Datastore) backup.DatasetResolver {
	return func(ctx context.Context, instanceID string) (string, error) {
		instance, err := ds.GetInstance(ctx, instanceID)
		if err != nil {
			return "", fmt.Errorf("cannot locate the storage of instance %s: %w", instanceID, err)
		}
		if recorded, ok := instance.Handle.Metadata["zfs_dataset"].(string); ok && recorded != "" {
			// The handle is written by the provider but merged into from the
			// API, and this name reaches `zfs snapshot -r`, `zfs send` and
			// `zfs rollback -r`.
			if err := ensureHospitusDataset(recorded); err != nil {
				return "", err
			}
			return recorded, nil
		}
		// A ZVOL path is /dev/zvol/<dataset>, and the dataset to snapshot is its
		// parent: taking disk0 alone would leave the VM's other disks behind.
		for _, disk := range instance.Spec.Disks {
			zvol := strings.TrimPrefix(disk.Path, "/dev/zvol/")
			if zvol == disk.Path {
				continue
			}
			if i := strings.LastIndex(zvol, "/"); i > 0 {
				return zvol[:i], nil
			}
		}
		return "", fmt.Errorf("instance %s is served by %s, which stores it outside ZFS; there is nothing to snapshot",
			instanceID, instance.Provider)
	}
}

// newStackManagerWithHealth builds the stack manager with the health checker a
// "healthy" dependency condition needs.
//
// Without one, waitForDependencies waits five seconds and reports every
// dependency healthy, so a service starts while the database it depends on may
// still be unable to answer.
func newStackManagerWithHealth(registry *provider.Registry, ds *datastore.Datastore, dataDir string) *orchestration.StackManager {
	sm := orchestration.NewStackManager(registry, ds)
	sm.SetHealthChecker(orchestration.NewHealthChecker(orchestration.NewRegistryExecProvider(registry)))
	// A manifest may read its cloud-init user-data from here and nowhere else:
	// the daemon runs as root, and the file ends up inside the caller's guest.
	if dataDir != "" {
		root := filepath.Join(dataDir, "cloudinit")
		if err := os.MkdirAll(root, 0o700); err != nil {
			logging.WithComponent("api").Warn("cannot create the cloud-init directory; user_data_file will be refused",
				"path", root, logging.FieldError, err)
		} else {
			sm.SetCloudInitRoot(root)
		}
	}
	return sm
}

// NewServer creates a new API server instance.
//
// Parameters:
//   - addr: Listen address (e.g., ":8080" or "127.0.0.1:8080")
//   - ds: Datastore for instance metadata
//   - registry: Provider registry
//   - config: Server configuration (nil for defaults)
func NewServer(addr string, ds Datastore, registry *provider.Registry, config *ServerConfig) (*Server, error) {
	logger := logging.WithComponent("api")

	// Use default config if none provided
	// Enable authentication by default
	// No CORS origins by default - cross-origin access requires explicit
	// configuration, wildcard included
	if config == nil {
		config = &ServerConfig{
			EnableAuth:        true, // Enabled by default for security
			EnableRateLimit:   true,
			RequestsPerSecond: 10,
			BurstSize:         20,
			AllowedOrigins:    []string{}, // Empty by default - require explicit configuration
			WriteTimeout:      30 * time.Minute,
			ReadTimeout:       15 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
	}

	// Apply defaults for zero values
	if config.WriteTimeout == 0 {
		config.WriteTimeout = 30 * time.Minute
	}
	if config.ReadTimeout == 0 {
		config.ReadTimeout = 15 * time.Second
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = 60 * time.Second
	}

	// Apply logging defaults
	if config.LogLevel == "" {
		config.LogLevel = "info"
	}
	if config.LogFormat == "" {
		config.LogFormat = "text"
	}

	// Some managers still need a concrete *datastore.Datastore for internal operations
	var concreteDS *datastore.Datastore
	if d, ok := ds.(*datastore.Datastore); ok {
		concreteDS = d
	}

	// ZFS-backed volumes exist only where ZFS does; elsewhere the volume
	// endpoints report the feature as unavailable.
	platformStorage, storageErr := storage.NewPlatformBackend()
	if storageErr != nil {
		logger.Info("volume storage unavailable on this platform", logging.FieldError, storageErr)
	}

	s := &Server{
		mux:              http.NewServeMux(),
		datastore:        ds,
		registry:         registry,
		addr:             addr,
		config:           config,
		rateLimiters:     make(map[string]*rate.Limiter),
		authRateLimiters: make(map[string]*rate.Limiter), // Initialize auth rate limiters
		stackManager:     newStackManagerWithHealth(registry, concreteDS, config.DataDir),
		jobManager:       job.NewJobManager(job.DefaultJobManagerConfig(), logger),
		backupManager:    backup.NewBackupManager(config.DataDir, instanceDatasetResolver(concreteDS), concreteDS),
		sysCollector:     security.NewSystemCollector(concreteDS, registry),
		sshManager:       security.NewSSHManager(config.DataDir),
		bridges:          network.NewPlatformLister(),
		storage:          platformStorage,
		logger:           logger,
	}

	// Point the storage backend at the same datasets the jail provider uses for
	// volumes, so an existing pool keeps serving the volumes already on it.
	if s.storage != nil {
		storageConfig := storage.Config{
			DataDir:          config.DataDir,
			Backend:          "zfs",
			ZFSParentDataset: dataset.Child("volumes"),
			// The jail provider defaulted new volumes to lz4; keep that rather
			// than letting them inherit whatever the pool is set to.
			Compression: "lz4",
		}
		if err := s.storage.Initialize(context.Background(), storageConfig); err != nil {
			// A host without a pool still serves everything else; the volume
			// endpoints report the feature as unavailable.
			logger.Warn("Storage backend unavailable; volume endpoints will be disabled",
				logging.FieldError, err)
			s.storage = nil
		}
	}

	// Strict authentication configuration
	// In production: use AuthManager with bcrypt-hashed keys
	// In development/no-auth: use SimpleAuthProvider (plaintext, in-memory only)
	if config.EnableAuth {
		if len(config.APIKeys) == 0 {
			if config.AllowNoAuth {
				logger.Warn("Running WITHOUT authentication — INSECURE. Configure API keys for production.")
				logger.Warn("Set 'api_keys' in the config to enable authenticated access.")
				config.EnableAuth = false
			} else {
				logger.Error("Auth enabled but no API keys configured")
				logger.Error("Either configure API keys or explicitly set AllowNoAuth=true")
				logger.Error("Server will start but reject all API requests")
				s.authProvider = auth.NewSimpleAuthProvider([]string{})
			}
		} else {
			// Production mode: use AuthManager with bcrypt hashing
			authMgr := auth.NewAuthManager()
			for i, key := range config.APIKeys {
				keyID := fmt.Sprintf("key-%d", i+1)
				if err := authMgr.AddAPIKey(keyID, "default", key, []string{"*"}, nil); err != nil {
					logger.Error("Failed to register API key", "key_id", keyID, logging.FieldError, err)
				}
			}
			// Rehydrate API keys created at runtime (POST /api/v1/auth/keys),
			// which are persisted to the datastore, so they survive restarts.
			if concreteDS != nil {
				if recs, rerr := concreteDS.ListAPIKeys(context.Background()); rerr == nil {
					for _, rec := range recs {
						authMgr.LoadAPIKey(&auth.APIKey{
							ID:          rec.ID,
							Name:        rec.Name,
							HashedKey:   rec.HashedKey,
							Permissions: rec.Permissions,
							CreatedAt:   rec.CreatedAt,
							ExpiresAt:   rec.ExpiresAt,
						})
					}
					if len(recs) > 0 {
						logger.Info("Reloaded persisted API keys", "count", len(recs))
					}
				} else {
					logger.Warn("Failed to reload persisted API keys", logging.FieldError, rerr)
				}
			}
			s.authProvider = authMgr
			logger.Info("Authentication enabled with bcrypt-hashed API keys", "key_count", len(config.APIKeys))
			// SECURITY: Plaintext keys no longer needed — clear them from config
			// to prevent accidental exposure through logging or memory dumps.
			for i := range config.APIKeys {
				config.APIKeys[i] = ""
			}
			config.APIKeys = nil
		}
	}

	// Persist job state transitions (completed/failed/canceled) so the API and
	// clients see terminal states survive across restarts instead of jobs that
	// stay "pending" forever.
	s.jobManager.SetUpdateHook(func(j *job.Job) {
		s.updateJob(context.Background(), j)
	})

	// Load persisted stacks from database (non-fatal — stacks may not exist yet)
	if concreteDS != nil {
		if err := s.stackManager.LoadStacks(context.Background()); err != nil {
			logger.Warn("Failed to restore stacks from persistence", logging.FieldError, err)
		} else {
			restored := len(s.stackManager.ListStacks())
			if restored > 0 {
				logger.Info("Restored stacks from persistence", "count", restored)
			}
		}
	}

	// Initialize global audit logger (fatal on failure — audit integrity is mandatory)
	auditConfig := security.DefaultAuditLoggerConfig()
	if path := auditLogPath(config); path != "" {
		auditConfig.LogFile = path
	}
	if err := security.InitGlobalAuditLogger(auditConfig); err != nil {
		return nil, fmt.Errorf("failed to initialize audit logger: %w", err)
	}

	s.registerRoutes()

	// Create HTTP server with timeouts
	// Enable WriteTimeout for DoS protection
	// Why these values (configurable via ServerConfig):
	//   - ReadHeaderTimeout: Prevents slowloris header attacks (client sends headers very slowly)
	//   - ReadTimeout: Prevent slow clients from holding connections
	//   - WriteTimeout: For exec/streaming operations that can run long (pkg install, etc.)
	//     NOTE: SetWriteDeadline per-request doesn't work with middleware wrapping,
	//     so we use a generous global timeout. ReadTimeout + auth still protect against abuse.
	//   - IdleTimeout: Clean up idle keepalive connections
	//   - MaxHeaderBytes: Prevent header-stuffing attacks (1MB default, explicit is clearer)
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.withMiddleware(s.mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       config.ReadTimeout,
		WriteTimeout:      config.WriteTimeout,
		IdleTimeout:       config.IdleTimeout,
		MaxHeaderBytes:    1 << 20, // 1 MB
	}

	// Start backup manager
	if s.backupManager != nil {
		if err := s.backupManager.Start(context.Background()); err != nil {
			logger.Warn("Failed to start backup manager", logging.FieldError, err)
		}
	}

	return s, nil
}

// SetStackManager sets the stack manager for orchestration
func (s *Server) SetStackManager(sm *orchestration.StackManager) {
	s.stackManager = sm
}

// GetStackManager returns the stack manager
func (s *Server) GetStackManager() *orchestration.StackManager {
	return s.stackManager
}

// SetBackupManager sets the backup manager
func (s *Server) SetBackupManager(bm *backup.BackupManager) {
	s.backupManager = bm
}

// GetBackupManager returns the backup manager
func (s *Server) GetBackupManager() *backup.BackupManager {
	return s.backupManager
}

// auditLogPath resolves where audit events are written: the configured file if
// one was given, otherwise a path under the data directory. An empty result
// leaves the package default in place, which is what a nil config (tests) wants.
func auditLogPath(config *ServerConfig) string {
	if config == nil {
		return ""
	}
	if config.AuditLogFile != "" {
		return config.AuditLogFile
	}
	if config.DataDir == "" {
		return ""
	}
	return filepath.Join(config.DataDir, "logs", "audit.log")
}

// registerRoutes sets up all HTTP routes.
//
// Route Organization:
//   - Health: /health
//   - Providers: /api/v1/providers/*
//   - Instances: /api/v1/instances/*
//   - AutoStart: /api/v1/autostart/*
//
// Why /api/v1?
//   - Versioned API allows backward compatibility when we add v2
//   - /api prefix clearly separates API from static files (future web UI)
func (s *Server) registerRoutes() {
	// Health check (no /api prefix for load balancers)
	s.mux.HandleFunc("/health", s.handleHealth)

	// Metrics endpoints (for monitoring)
	s.mux.HandleFunc("/metrics", s.handleMetrics)
	s.mux.HandleFunc("/metrics/prometheus", s.handlePrometheusMetrics)

	// Security endpoints
	s.mux.HandleFunc("/security/status", s.handleSecurityStatus)

	// Provider endpoints
	s.mux.HandleFunc("/api/v1/providers", s.handleProviders)
	s.mux.HandleFunc("/api/v1/providers/{name}", s.handleProviderDetail)

	// Instance endpoints
	s.mux.HandleFunc("/api/v1/instances", s.handleInstances)
	s.mux.HandleFunc("/api/v1/instances/{id}", s.handleInstanceDetail)
	s.mux.HandleFunc("/api/v1/instances/{id}/", s.handleInstanceDetail) // catch-all for sub-paths
	s.mux.HandleFunc("/api/v1/instances/{id}/start", s.handleStartInstanceRoute)
	s.mux.HandleFunc("/api/v1/instances/{id}/stop", s.handleStopInstanceRoute)
	s.mux.HandleFunc("/api/v1/instances/{id}/restart", s.handleRestartInstanceRoute)
	s.mux.HandleFunc("/api/v1/instances/{id}/events", s.handleGetInstanceEventsRoute)
	s.mux.HandleFunc("/api/v1/instances/{id}/health", s.handleInstanceHealthRoute)
	s.mux.HandleFunc("POST /api/v1/instances/{id}/exec", s.handleExecRoute)
	s.mux.HandleFunc("GET /api/v1/instances/{id}/metrics", s.handleInstanceMetricsRoute)
	s.mux.HandleFunc("/api/v1/instances/{id}/port-forwards", s.handlePortForwards)

	// Import endpoint
	s.mux.HandleFunc("/api/v1/import", s.handleImport)

	// Image management endpoints
	s.mux.HandleFunc("GET /api/v1/images", s.handleListImages)
	s.mux.HandleFunc("/api/v1/images/fetch", s.handleFetchImage)
	s.mux.HandleFunc("/api/v1/images/refresh", s.handleRefreshCatalog)
	s.mux.HandleFunc("/api/v1/images/", s.handleDeleteImage)

	// Auto-start endpoints
	s.mux.HandleFunc("/api/v1/autostart", s.handleAutoStart)
	s.mux.HandleFunc("/api/v1/autostart/", s.handleAutoStartInstance)

	// Firewall/port forwarding endpoints
	s.mux.HandleFunc("/api/v1/firewall/", s.handleFirewall)

	// Volume management endpoints
	s.mux.HandleFunc("/api/v1/volumes", s.handleVolumes)
	s.mux.HandleFunc("/api/v1/volumes/", s.handleVolumeDetail)

	// Stack orchestration endpoints
	s.mux.HandleFunc("/api/v1/stacks", s.handleStacks)
	s.mux.HandleFunc("/api/v1/stacks/", s.handleStacks)

	// Backup management endpoints
	s.mux.HandleFunc("/api/v1/backups", s.handleBackups)
	s.mux.HandleFunc("/api/v1/backups/", s.handleBackups)

	// Job management endpoints
	s.mux.HandleFunc("/api/v1/jobs", s.handleJobs)
	s.mux.HandleFunc("/api/v1/jobs/stats", s.handleJobStats)
	s.mux.HandleFunc("/api/v1/jobs/{id}", s.handleJobDetail)
	s.mux.HandleFunc("/api/v1/jobs/{id}/cancel", s.handleJobDetail)

	// Network endpoints
	s.mux.HandleFunc("/api/v1/network/bridges", s.handleListBridges)

	// Console session listing endpoint
	s.mux.HandleFunc("GET /api/v1/console/sessions", s.handleListConsoleSessions)

	// Interface management endpoints (multi-NIC)
	s.mux.HandleFunc("/api/v1/instances/{id}/interfaces", s.handleInterfacesRoute)
	s.mux.HandleFunc("/api/v1/instances/{id}/interfaces/", s.handleInterfacesRoute)

	// Service management endpoints
	s.mux.HandleFunc("/api/v1/instances/{id}/services", s.handleServicesRoute)
	s.mux.HandleFunc("/api/v1/instances/{id}/services/", s.handleServicesRoute)

	// FreeBSD-specific endpoints
	s.mux.HandleFunc("/api/v1/instances/{id}/freebsd/rctl", s.handleFreeBSDRoute)
	s.mux.HandleFunc("/api/v1/instances/{id}/freebsd/vnet", s.handleFreeBSDRoute)
	s.mux.HandleFunc("/api/v1/instances/{id}/freebsd/vnet/enable", s.handleFreeBSDRoute)
	s.mux.HandleFunc("/api/v1/instances/{id}/freebsd/vnet/disable", s.handleFreeBSDRoute)

	// Media and boot order endpoints
	s.mux.HandleFunc("/api/v1/instances/{id}/media", s.handleMediaRoute)
	s.mux.HandleFunc("/api/v1/instances/{id}/media/", s.handleMediaRoute)
	s.mux.HandleFunc("/api/v1/instances/{id}/boot-order", s.handleBootOrderRoute)

	// Auth key management (admin only)
	s.mux.HandleFunc("/api/v1/auth/keys", s.handleAuthKeysRoute)
	s.mux.HandleFunc("/api/v1/auth/keys/", s.handleAuthKeysRoute)
}

// jobRetention is how long a finished job stays queryable before it is pruned.
const jobRetention = 7 * 24 * time.Hour

// reconcileJobs settles the jobs table against a daemon that has just started.
//
// A job lives in the worker pool's memory; its row only records where it got
// to. Anything pending or running when the daemon stopped is work nobody is
// doing, and finished rows had nothing to prune them.
func (s *Server) reconcileJobs() {
	if s.datastore == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if n, err := s.datastore.FailInterruptedJobs(ctx); err != nil {
		s.logger.Warn("could not close out interrupted jobs", logging.FieldError, err)
	} else if n > 0 {
		s.logger.Info("closed out jobs interrupted by a daemon restart", "count", n)
	}

	if n, err := s.datastore.CleanupOldJobs(ctx, jobRetention); err != nil {
		s.logger.Warn("could not prune finished jobs", logging.FieldError, err)
	} else if n > 0 {
		s.logger.Info("pruned finished jobs", "count", n, "older_than", jobRetention)
	}
}

// Start starts the HTTP server.
//
// TLS/HTTPS support
// - Uses TLS if cert and key are configured
// - Without certificates it refuses to start unless AllowInsecureTLS is set
//
// This is a blocking call - it runs until the server is shut down.
func (s *Server) Start() error {
	// Warn if authentication is disabled
	if !s.config.EnableAuth {
		s.logger.Warn("API server running without authentication")
		s.logger.Warn("Any client can access all API endpoints; enable auth for production")
	}

	// Warn if CORS allows all origins
	for _, origin := range s.config.AllowedOrigins {
		if origin == "*" {
			s.logger.Warn("CORS allows all origins; configure specific origins for production")
			break
		}
	}

	// Start rate limiter cleanup goroutine
	cleanupCtx, cancel := context.WithCancel(context.Background())
	s.cancelCleanup = cancel
	go s.cleanupRateLimiters(cleanupCtx)

	// Sync instance states on startup to ensure datastore matches reality
	s.SyncInstanceStates()

	// Close out the jobs the previous daemon was running, and drop the ones
	// old enough that nobody is coming back for them.
	s.reconcileJobs()

	// Start auto-start instances in background
	go func() {
		if err := s.StartAutoStartInstances(); err != nil {
			s.logger.Warn("Auto-start instances failed", logging.FieldError, err)
		}
	}()

	// Start with TLS if configured
	if s.config.TLSCert != "" && s.config.TLSKey != "" {
		// Enforce TLS 1.2 minimum — TLS 1.0/1.1 have known weaknesses
		tlsCfg := &tls.Config{
			MinVersion: tls.VersionTLS12,
			CurvePreferences: []tls.CurveID{
				tls.X25519,
				tls.CurveP256,
			},
		}
		s.httpServer.TLSConfig = tlsCfg
		s.logger.Info("API server listening with TLS 1.2+ enabled", "addr", s.addr)
		return s.httpServer.ListenAndServeTLS(s.config.TLSCert, s.config.TLSKey)
	}

	// Require explicit opt-in for non-TLS (avoid accidental production exposure)
	if !s.config.AllowInsecureTLS {
		s.logger.Error("API server has no TLS certificate configured")
		s.logger.Error("Set tls_cert and tls_key, or set allow_insecure_tls=true for development only")
		return fmt.Errorf("TLS required: configure certificates or set allow_insecure_tls=true")
	}
	s.logger.Warn("API server running without TLS; use only for development")
	s.logger.Info("API server listening without TLS", "addr", s.addr)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
//
// Graceful shutdown:
//   - Stops accepting new connections
//   - Waits for existing requests to complete (up to timeout)
//   - Stops the rate limiter cleanup goroutine
//
// The datastore is closed by the caller (cmd/hospitusd), not here.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down API server")

	// Stop rate limiter cleanup goroutine
	if s.cancelCleanup != nil {
		s.cancelCleanup()
	}

	// Shut down job manager, waiting up to the context deadline for running jobs
	if s.jobManager != nil {
		deadline, ok := ctx.Deadline()
		timeout := 30 * time.Second
		if ok {
			timeout = time.Until(deadline)
			if timeout <= 0 {
				timeout = 5 * time.Second
			}
		}
		s.jobManager.Shutdown(timeout)
	}

	return s.httpServer.Shutdown(ctx)
}

// Health check endpoint

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Check datastore connectivity
	ctx := r.Context()
	if err := s.datastore.Ping(ctx); err != nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"status": "unhealthy",
			"error":  "database unavailable",
			"time":   time.Now(),
		})
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "healthy",
		"time":   time.Now(),
	})
}

// Helper methods

// SyncInstanceStates synchronizes instance states from providers to the datastore.
// This is called on startup to ensure the datastore accurately reflects the
// actual state of instances in the system (e.g., after a host reboot).
func (s *Server) SyncInstanceStates() {
	// Use a background context with a generous timeout for synchronization
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	s.logger.Info("Synchronizing instance states from providers")

	instances, err := s.datastore.ListInstances(ctx, datastore.InstanceFilter{})
	if err != nil {
		s.logger.Error("Failed to list instances for state synchronization", logging.FieldError, err)
		return
	}

	for _, inst := range instances {
		prov, err := s.registry.Get(inst.Provider)
		if err != nil {
			s.logger.Warn("Provider not found during state sync",
				logging.FieldInstance, inst.ID,
				logging.FieldProvider, inst.Provider)
			continue
		}

		// Get current state from provider
		currentState, err := prov.GetInstanceState(ctx, inst.Handle)
		if err != nil {
			// A provider that cannot report on an instance is not evidence that
			// the instance is still doing what the datastore last saw. Keeping
			// the stored value here is what left a VM listed as running after
			// its process was gone and its on-disk state with it — and the
			// point of this pass is precisely to correct such drift.
			s.logger.Warn("Cannot determine instance state; marking it unknown",
				logging.FieldInstance, inst.ID,
				logging.FieldError, err)
			if inst.State != provider.StateUnknown {
				if err := s.datastore.UpdateInstanceState(ctx, inst.ID, provider.StateUnknown); err != nil {
					s.logger.Error("Failed to mark instance state unknown during sync",
						logging.FieldInstance, inst.ID,
						logging.FieldError, err)
				}
			}
			continue
		}

		// If state in datastore doesn't match reality, update it
		if currentState != inst.State {
			s.logger.Info("Updating instance state to match reality",
				logging.FieldInstance, inst.ID,
				"old_state", inst.State,
				"new_state", currentState)

			if err := s.datastore.UpdateInstanceState(ctx, inst.ID, currentState); err != nil {
				s.logger.Error("Failed to update instance state during sync",
					logging.FieldInstance, inst.ID,
					logging.FieldError, err)
			}
		}
	}

	s.logger.Info("Instance state synchronization complete")
}

// syncInstanceSpec syncs the instance spec from the provider to the datastore.
// This is called after instance start to update allocated network info.
func (s *Server) syncInstanceSpec(ctx context.Context, instanceID string, prov provider.Provider, handle provider.InstanceHandle) {
	info, err := prov.GetInstanceInfo(ctx, handle)
	if err != nil {
		s.logger.Warn("Failed to get instance info for sync", logging.FieldInstance, instanceID, logging.FieldError, err)
		return
	}

	stored, err := s.datastore.GetInstance(ctx, instanceID)
	if err != nil {
		s.logger.Warn("Failed to read the stored spec for sync", logging.FieldInstance, instanceID, logging.FieldError, err)
		return
	}

	merged := mergeInstanceSpec(stored.Spec, info.Spec)
	if err := s.datastore.UpdateInstanceSpec(ctx, instanceID, merged); err != nil {
		s.logger.Warn("Failed to update instance spec", logging.FieldInstance, instanceID, logging.FieldError, err)
	}
}

// mergeInstanceSpec layers what a provider observed on top of what was asked for.
//
// A provider fills in what it can see — addresses assigned at boot, the image it
// actually resolved — and leaves the rest zero. Storing its answer wholesale
// therefore erased the caller's own request: starting an instance created with
// 2 CPUs and 256 MB left it recorded with neither, so every later list and info
// reported made-up defaults instead of what the instance had been given.
//
// The declared spec stays the source of truth; a discovered value only wins when
// the provider actually has one.
func mergeInstanceSpec(declared, discovered provider.InstanceSpec) provider.InstanceSpec {
	merged := declared

	if discovered.Name != "" {
		merged.Name = discovered.Name
	}
	if discovered.Description != "" {
		merged.Description = discovered.Description
	}
	if discovered.CPUs > 0 {
		merged.CPUs = discovered.CPUs
	}
	if discovered.MemoryMB > 0 {
		merged.MemoryMB = discovered.MemoryMB
	}
	if discovered.Image != "" {
		merged.Image = discovered.Image
	}
	if discovered.OSType != "" {
		merged.OSType = discovered.OSType
	}
	if discovered.OSVersion != "" {
		merged.OSVersion = discovered.OSVersion
	}
	if discovered.Arch != "" {
		merged.Arch = discovered.Arch
	}
	if discovered.Bootloader != "" {
		merged.Bootloader = discovered.Bootloader
	}

	// Networks and disks are where a provider has the most to add — an address
	// allocated by DHCP, a device node assigned at attach — so its list replaces
	// the declared one when it has one at all.
	if len(discovered.Networks) > 0 {
		merged.Networks = discovered.Networks
	}
	if len(discovered.Disks) > 0 {
		merged.Disks = discovered.Disks
	}
	if len(discovered.ProviderConfig) > 0 {
		merged.ProviderConfig = discovered.ProviderConfig
	}
	if len(discovered.Labels) > 0 {
		merged.Labels = discovered.Labels
	}
	if len(discovered.Annotations) > 0 {
		merged.Annotations = discovered.Annotations
	}

	return merged
}

// streamWriter serializes writes to a streaming response.
//
// While a streamed operation runs, two goroutines produce output: the handler
// writes its own progress lines, and a background goroutine drains the
// provider's log records from a pipe. Both target the same ResponseWriter, so
// every write must take this lock — without it the two race, and output is
// interleaved mid-line or lost outright.
type streamWriter struct {
	mu      sync.Mutex
	w       io.Writer
	flusher http.Flusher
}

// Write implements io.Writer, flushing after each successful write so the client
// sees progress as it happens rather than at the end of the operation.
func (sw *streamWriter) Write(p []byte) (int, error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	n, err := sw.w.Write(p)
	if err == nil {
		sw.flusher.Flush()
	}
	return n, err
}

// Printf writes a formatted line to the stream.
//
// Interior newlines are collapsed before the line is written: the protocol is
// line-oriented and the client stops reading an ERROR at its first line, so a
// multi-line provider error silently lost everything after it — which is where
// the useful diagnostics live (a failed QEMU start reported "exit status 1" and
// dropped QEMU's own explanation).
//
// A write error means the client is gone, which the caller cannot act on, so it
// is discarded.
func (sw *streamWriter) Printf(format string, args ...any) {
	_, _ = fmt.Fprint(sw, singleLine(fmt.Sprintf(format, args...)))
}

// singleLine collapses a message into one line, preserving a single trailing
// newline so the stream stays line-delimited. Blank lines are dropped rather
// than turned into empty separators: a provider message formatted as a short
// paragraph would otherwise read as "…found; ; The image must be…".
func singleLine(msg string) string {
	trailing := strings.HasSuffix(msg, "\n")

	body := strings.TrimSuffix(msg, "\n")
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")

	segments := make([]string, 0, strings.Count(body, "\n")+1)
	for _, segment := range strings.Split(body, "\n") {
		if segment = strings.TrimSpace(segment); segment != "" {
			segments = append(segments, segment)
		}
	}
	body = strings.Join(segments, "; ")

	if trailing {
		return body + "\n"
	}
	return body
}

// withStreamingLogger returns a context carrying a slog logger whose output is
// piped to w (flushed as it arrives), so long-running streamed operations can
// emit structured progress to the client. The returned cleanup closes the pipe
// and waits for the forwarding goroutine to drain.
func (s *Server) withStreamingLogger(ctx context.Context, w io.Writer, flusher http.Flusher, action, instanceName, providerName string) (streamCtx context.Context, stream *streamWriter, cleanup func()) {
	stream = &streamWriter{w: w, flusher: flusher}
	pr, pw := io.Pipe()

	// Use centralized logging configuration for the stream
	level := s.config.LogLevel
	if level == "" {
		level = "info"
	}
	format := s.config.LogFormat
	if format == "" {
		format = "text"
	}

	handler := logging.NewHandler(level, format, pw)
	logger := slog.New(handler).With(
		logging.FieldComponent, "api-stream",
		logging.FieldAction, action,
		logging.FieldInstance, instanceName,
		logging.FieldProvider, providerName,
	)

	// If it's a VM-like provider, also add the vm field for consistency
	if providerName == "qemu" || providerName == "bhyve" {
		logger = logger.With(logging.FieldVM, instanceName)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer pr.Close()

		// Copying through stream takes the same lock as the handler's own writes.
		_, _ = io.Copy(stream, pr)
	}()

	// stop is idempotent: handlers call it explicitly once the provider has
	// finished logging, and again through defer on every return path.
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = pw.Close()
			<-done
		})
	}

	return provider.WithLogger(ctx, logger), stream, stop
}

func (s *Server) writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Error("Failed to encode JSON response", logging.FieldError, err)
	}
}

// writeError writes an error response.
func (s *Server) writeError(w http.ResponseWriter, statusCode int, message string) {
	s.writeJSON(w, statusCode, map[string]string{
		"error": message,
	})
}

// maxErrorDetail bounds the detail returned to a client. Provider errors carry
// the output of an external tool, which can be long; the full text is always in
// the server log.
const maxErrorDetail = 2048

// writeLoggedError logs the error server-side and answers the client with a
// stable message plus its cause.
//
// The two fields serve different readers. "error" is a fixed sentence a client
// can match on; "detail" carries the cause a caller can act on: a container
// still running, an image that does not exist, an argument QEMU rejected.
//
// Every route answering this way is authenticated, so the detail reaches no one
// who cannot already read the instance configuration. The cause is reported
// whatever its origin: "database is locked" helps an operator as much as a
// provider message, where a bare "Failed to create snapshot" only sends them to
// the daemon log.
//
// The log keeps the error whole; the client gets one bounded line, because the
// JSON field and the streaming protocol are both line-oriented.
func (s *Server) writeLoggedError(w http.ResponseWriter, statusCode int, clientMsg string, err error) {
	s.logger.Error(clientMsg, logging.FieldError, err)

	detail := stripEcho(clientErrorDetail(err), clientMsg)
	if detail == "" {
		s.writeError(w, statusCode, clientMsg)
		return
	}

	s.writeJSON(w, statusCode, map[string]string{
		"error":  clientMsg,
		"detail": detail,
	})
}

// clientErrorDetail returns the cause of err as an authenticated caller may see
// it, or an empty string when there is nothing to add.
func clientErrorDetail(err error) string {
	if err == nil {
		return ""
	}

	return trimDetail(err.Error())
}

// stripEcho drops the leading part of detail that only repeats clientMsg.
//
// A provider names the operation in its own error, and the handler names it
// again in the message it answers with. Both are right on their own, but joined
// they read "Failed to create snapshot: failed to create snapshot: exit status
// 125", and the one useful clause arrives third. Only an exact restatement is
// removed, so a detail that merely starts with similar words is left alone.
func stripEcho(detail, clientMsg string) string {
	prefix := strings.TrimSuffix(strings.TrimSpace(clientMsg), ":") + ":"
	if len(detail) <= len(prefix) || !strings.EqualFold(detail[:len(prefix)], prefix) {
		return detail
	}

	rest := strings.TrimSpace(detail[len(prefix):])
	if rest == "" {
		return detail
	}
	return rest
}

// trimDetail makes a provider message fit a JSON field: one line, bounded
// length. The untruncated text is in the server log.
func trimDetail(detail string) string {
	detail = strings.TrimSpace(singleLine(detail))
	if len(detail) <= maxErrorDetail {
		return detail
	}
	return detail[:maxErrorDetail] + "… (truncated; see the hospitusd log)"
}

// decodeJSONBody decodes a JSON request body with strict mode enabled.
// Strict mode rejects unknown fields, preventing attackers from injecting
// unexpected parameters that might be processed by downstream code.
// It also validates Content-Type to prevent non-JSON payloads from being decoded.
func (s *Server) decodeJSONBody(r *http.Request, v interface{}) error {
	ct := r.Header.Get("Content-Type")
	// Allow empty Content-Type for backward compatibility, but validate non-empty ones
	if ct != "" && ct != "application/json" && !strings.HasPrefix(ct, "application/json;") {
		return fmt.Errorf("unsupported Content-Type: %s (expected application/json)", ct)
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
}

// cleanupFirewallRules removes all firewall rules for an instance.
// This is called when an instance is deleted to clean up port forwarding and NAT rules.
func (s *Server) cleanupFirewallRules(ctx context.Context, instanceName string) error {
	if s.firewallMgr == nil {
		// Firewall not initialized, nothing to clean up
		return nil
	}

	// Remove all rules for this instance
	return s.firewallMgr.RemoveAllRules(ctx, instanceName)
}
