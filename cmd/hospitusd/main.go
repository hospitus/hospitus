// Package main implements the HOSPITUS daemon (hospitusd)
//
// The daemon provides the REST API server that the CLI connects to.
// It manages provider initialization, instance lifecycle, and persistent storage.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/hospitus/hospitus/internal/api"
	"github.com/hospitus/hospitus/internal/crypto"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/config"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/applecontainer"
	"github.com/hospitus/hospitus/pkg/provider/bhyve"
	"github.com/hospitus/hospitus/pkg/provider/jail"
	"github.com/hospitus/hospitus/pkg/provider/podman"
	"github.com/hospitus/hospitus/pkg/provider/qemu"
	"github.com/hospitus/hospitus/pkg/provider/vfkit"
)

var (
	version   = "dev"
	buildTime = "unknown"
	gitCommit = "unknown"
)

func main() {
	// Parse command-line flags
	var (
		addr             = flag.String("addr", "127.0.0.1:8080", "Listen address (default: loopback only; use 0.0.0.0:PORT to expose remotely)")
		dataDir          = flag.String("data-dir", "/var/lib/hospitus", "Data directory")
		stateDir         = flag.String("state-dir", "/var/lib/hospitus/state", "State directory")
		dbPath           = flag.String("db", "/var/lib/hospitus/hospitus.db", "Database path")
		showVersion      = flag.Bool("version", false, "Show version information")
		migrateOnly      = flag.Bool("migrate", false, "Run database migrations and exit")
		migrationStatus  = flag.Bool("migration-status", false, "Show migration status and exit")
		writeTimeout     = flag.Duration("write-timeout", 30*time.Minute, "HTTP write timeout (for long exec commands)")
		readTimeout      = flag.Duration("read-timeout", 15*time.Second, "HTTP read timeout")
		idleTimeout      = flag.Duration("idle-timeout", 60*time.Second, "HTTP idle timeout")
		auditLog         = flag.String("audit-log", "", "Security audit log file (default: <data-dir>/logs/audit.log)")
		logLevel         = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
		logFormat        = flag.String("log-format", "text", "Log format (text, json)")
		allowInsecureTLS = flag.Bool("allow-insecure-tls", false, "Allow running without TLS (development only)")
		tlsCert          = flag.String("tls-cert", "", "Path to TLS certificate file (PEM)")
		tlsKey           = flag.String("tls-key", "", "Path to TLS private key file (PEM)")
		allowNoAuth      = flag.Bool("allow-no-auth", false, "Allow unauthenticated access (development only — never use in production)")
		metricsPublic    = flag.Bool("metrics-public", false, "Allow unauthenticated access to /metrics endpoints (for Prometheus scraping)")
		apiKeyFile       = flag.String("api-key-file", "", "Path to a file containing one API key per line (also honors the HOSPITUS_API_KEY env var, comma-separated)")
		rateLimit        = flag.Bool("rate-limit", true, "Enable per-client HTTP rate limiting")
		rateLimitRPS     = flag.Int("rate-limit-rps", 10, "Rate limit: sustained requests per second per client")
		rateLimitBurst   = flag.Int("rate-limit-burst", 20, "Rate limit: burst size per client")
		apiKeys          []string
		apiKeyFlagUsed   bool
	)
	flag.Func("api-key", "API key for authentication (repeat for multiple keys); visible in ps(1), so prefer --api-key-file on a shared host", func(s string) error {
		if s == "" {
			return fmt.Errorf("api-key cannot be empty")
		}
		apiKeys = append(apiKeys, s)
		apiKeyFlagUsed = true
		return nil
	})
	flag.Parse()

	if *showVersion {
		fmt.Printf("hospitusd version %s\n", version)
		fmt.Printf("  Build time: %s\n", buildTime)
		fmt.Printf("  Git commit: %s\n", gitCommit)
		fmt.Printf("  Go version: %s\n", runtime.Version())
		fmt.Printf("  OS/Arch:    %s/%s\n", runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	// On FreeBSD, all providers (jail, bhyve) require root privileges. Fail fast
	// with a clear message rather than cryptic permission errors later. The check
	// runs after flag parsing so --version and --help stay usable as any user.
	if runtime.GOOS == "freebsd" && os.Getuid() != 0 {
		fmt.Fprintf(os.Stderr, "Error: hospitusd requires root privileges on FreeBSD\n")
		fmt.Fprintf(os.Stderr, "Run with: doas hospitusd ...\n")
		os.Exit(1)
	}

	// Initialize centralized logging before any other work so migration and
	// startup diagnostics are emitted through the configured logger.
	logging.Init(*logLevel, *logFormat, nil)

	if apiKeyFlagUsed {
		slog.Warn("--api-key exposes the key via ps(1) and shell history; prefer --api-key-file")
	}

	// Handle migration-only modes. Ensure the database's parent directory
	// exists first so migrations can create the file on a fresh install.
	if *migrateOnly || *migrationStatus {
		if err := os.MkdirAll(filepath.Dir(*dbPath), 0o750); err != nil {
			slog.Error("Failed to create database directory", logging.FieldError, err, "db_path", *dbPath)
			os.Exit(1)
		}
		runMigrationCommands(*dbPath, *migrateOnly, *migrationStatus)
		return
	}

	// Load API keys from file if specified (avoids secrets appearing in ps(1))
	if *apiKeyFile != "" {
		if info, err := os.Stat(*apiKeyFile); err != nil {
			slog.Error("Failed to stat api-key-file", logging.FieldError, err, "path", *apiKeyFile)
			os.Exit(1)
		} else if info.Mode().Perm()&0o077 != 0 {
			slog.Error("api-key-file is accessible by group/other; tighten it to 0600",
				"path", *apiKeyFile, "mode", info.Mode().Perm().String())
			os.Exit(1)
		}
		data, err := os.ReadFile(*apiKeyFile)
		if err != nil {
			slog.Error("Failed to read api-key-file", logging.FieldError, err, "path", *apiKeyFile)
			os.Exit(1)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				apiKeys = append(apiKeys, line)
			}
		}
		slog.Info("Loaded API keys from file", "path", *apiKeyFile, "count", len(apiKeys))
	}

	// Load API keys from HOSPITUS_API_KEY environment variable (comma-separated)
	if envKeys := os.Getenv("HOSPITUS_API_KEY"); envKeys != "" {
		for _, key := range strings.Split(envKeys, ",") {
			key = strings.TrimSpace(key)
			if key != "" {
				apiKeys = append(apiKeys, key)
			}
		}
		slog.Info("Loaded API keys from HOSPITUS_API_KEY environment variable")
	}

	// Create directories if needed.
	// dataDir is 0750 (owner rwx, group rx); stateDir holds sensitive state and
	// is 0700 (owner only).
	if err := os.MkdirAll(*dataDir, 0o750); err != nil {
		slog.Error("Failed to create data directory", logging.FieldError, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		slog.Error("Failed to create state directory", logging.FieldError, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o750); err != nil {
		slog.Error("Failed to create database directory", logging.FieldError, err, "db_path", *dbPath)
		os.Exit(1)
	}

	slog.Info("HOSPITUS daemon starting",
		"version", version,
		"data_dir", *dataDir,
		"state_dir", *stateDir,
		"db_path", *dbPath,
		"listen_addr", *addr,
		"tls", *tlsCert != "",
		"auth", len(apiKeys) > 0,
	)

	// The daemon lifecycle runs inside a closure so that deferred cleanup
	// (datastore close, context cancellation) always executes before we
	// translate a failure into a non-zero exit code in main.
	run := func() error {
		// Claim the data directory before touching anything inside it. A
		// second daemon here would share every instance record, provider state
		// file and VM process with the first, and quietly undo its work. It
		// belongs inside the closure: os.Exit below skips deferred calls, and
		// this is the whole reason the closure exists.
		unlockDataDir, err := lockDataDir(*dataDir)
		if err != nil {
			slog.Error("Cannot start", logging.FieldError, err)
			return err
		}
		defer unlockDataDir()

		// Initialize encryption
		encryptor, err := crypto.NewEncryptor(*dataDir)
		if err != nil {
			slog.Error("Failed to initialize encryption", logging.FieldError, err)
			return err
		}

		// Initialize datastore
		ds, err := datastore.NewDatastore(*dbPath, encryptor)
		if err != nil {
			slog.Error("Failed to initialize datastore", logging.FieldError, err)
			return err
		}
		defer ds.Close()

		var store api.Datastore = ds

		// Create provider registry
		registry := provider.NewRegistry()

		// Register providers based on platform
		ctx := context.Background()
		registerProviders(ctx, registry, *dataDir, *stateDir, *logLevel, providerSettings())

		// Create API server with configurable timeouts
		serverConfig := &api.ServerConfig{
			WriteTimeout:     *writeTimeout,
			ReadTimeout:      *readTimeout,
			IdleTimeout:      *idleTimeout,
			LogLevel:         *logLevel,
			LogFormat:        *logFormat,
			DataDir:          *dataDir,
			AuditLogFile:     *auditLog,
			AllowInsecureTLS: *allowInsecureTLS,
			TLSCert:          *tlsCert,
			TLSKey:           *tlsKey,
			APIKeys:          apiKeys,
			// EnableAuth must be true for NewServer to wire up the auth provider at
			// all. The server then decides the concrete mode from APIKeys/AllowNoAuth:
			// keys present -> bcrypt AuthManager; no keys + AllowNoAuth -> disabled;
			// no keys + !AllowNoAuth -> fail-closed (reject all). Leaving this false
			// silently skips authentication even when keys are configured.
			EnableAuth:        true,
			AllowNoAuth:       *allowNoAuth,
			MetricsPublic:     *metricsPublic,
			EnableRateLimit:   *rateLimit,
			RequestsPerSecond: *rateLimitRPS,
			BurstSize:         *rateLimitBurst,
		}
		// --allow-no-auth and --allow-insecure-tls are the loopback development
		// configuration; on any other address they would expose a root API in
		// clear text and without a key, so the daemon refuses to start that way.
		if (*allowNoAuth || *allowInsecureTLS) && !loopbackListenAddr(*addr) {
			return fmt.Errorf("--allow-no-auth and --allow-insecure-tls are only accepted on a loopback address, not %q", *addr)
		}
		server, err := api.NewServer(*addr, store, registry, serverConfig)
		if err != nil {
			slog.Error("Failed to create API server", logging.FieldError, err)
			return err
		}
		slog.Info("Configured HTTP timeouts",
			"write_timeout", *writeTimeout,
			"read_timeout", *readTimeout,
			"idle_timeout", *idleTimeout,
		)

		// Initialize firewall manager
		if err := server.InitializeFirewall(ctx); err != nil {
			slog.Warn("Failed to initialize firewall manager; port forwarding features will be unavailable", logging.FieldError, err)
		}

		// Start services with errgroup for better lifecycle management
		importCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		g, gCtx := errgroup.WithContext(importCtx)

		// REST API Server
		g.Go(func() error {
			slog.Info("Starting REST API server", "addr", *addr)
			return server.Start()
		})

		// Handle shutdown signals
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

		slog.Info("HOSPITUS daemon ready", "addr", *addr)

		// Wait for shutdown signal or a service error
		select {
		case sig := <-sigChan:
			slog.Info("Received shutdown signal", "signal", sig)
		case <-gCtx.Done():
			slog.Warn("Service failure detected, shutting down")
		}

		// Graceful shutdown. The HTTP server is stopped by server.Shutdown below
		// (it does not observe gCtx); cancel() only unblocks any work derived
		// from importCtx.
		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("Error during shutdown", logging.FieldError, err)
		}

		// Wait for all goroutines to finish. A graceful shutdown surfaces as
		// http.ErrServerClosed (from server.Start) or context.Canceled; neither
		// is a real failure, so don't exit non-zero on SIGTERM.
		if err := g.Wait(); err != nil &&
			!errors.Is(err, context.Canceled) &&
			!errors.Is(err, http.ErrServerClosed) {
			slog.Error("Daemon stopped with error", logging.FieldError, err)
			return err
		}

		slog.Info("HOSPITUS daemon stopped")
		return nil
	}

	if err := run(); err != nil {
		os.Exit(1)
	}
}

// providerInitTimeout bounds how long a single provider may take to initialize
// so a hung backend (e.g. an unresponsive podman socket) can't stall startup.
const providerInitTimeout = 30 * time.Second

// initProvider initializes a provider under a bounded timeout.
func initProvider(ctx context.Context, p provider.Provider, cfg provider.ProviderConfig) error {
	ctx, cancel := context.WithTimeout(ctx, providerInitTimeout)
	defer cancel()
	return p.Initialize(ctx, cfg)
}

// providerSettings reads the daemon configuration for the settings providers
// consult at runtime.
//
// Without this the settings map stays empty, and a default-deny guard such as
// physical-disk passthrough can never be granted: allowed_physical_disks in
// hospitusd.conf would not reach the provider that reads it.
func providerSettings() map[string]interface{} {
	settings := map[string]interface{}{}

	cfg, err := config.LoadConfigWithDefaults()
	if err != nil || cfg == nil {
		return settings
	}
	if len(cfg.AllowedPhysicalDisks) > 0 {
		settings["allowed_physical_disks"] = cfg.AllowedPhysicalDisks
	}
	return settings
}

// registerProviders registers all available providers
func registerProviders(ctx context.Context, registry *provider.Registry, dataDir, stateDir, logLevel string, settings map[string]interface{}) {
	logger := logging.WithComponent("provider-init")
	providerConfig := provider.ProviderConfig{
		DataDir:  dataDir,
		StateDir: stateDir,
		LogLevel: logLevel,
		Logger:   logger,
		Settings: settings,
	}

	// Log platform-specific provider availability
	if runtime.GOOS == "darwin" {
		logger.Info("Running on macOS; some providers unavailable", "unavailable", []string{"jail", "bhyve"}, "available", []string{"qemu", "podman", "vfkit", "container"})
	}

	// Register jail provider (FreeBSD native, will fail on other platforms)
	if runtime.GOOS == "freebsd" {
		jailProvider := jail.NewJailProvider()
		if err := initProvider(ctx, jailProvider, providerConfig); err != nil {
			logger.Warn("Failed to initialize jail provider; jail management will be unavailable", logging.FieldProvider, "jail", logging.FieldError, err)
		} else {
			if err := registry.Register(jailProvider); err != nil {
				logger.Warn("Failed to register provider", logging.FieldProvider, "jail", logging.FieldError, err)
			} else {
				logger.Info("Registered provider", logging.FieldProvider, "jail")
			}
		}
	}

	// Register bhyve provider on FreeBSD
	if runtime.GOOS == "freebsd" {
		bhyveProvider := bhyve.NewBhyveProvider()
		if err := initProvider(ctx, bhyveProvider, providerConfig); err != nil {
			logger.Warn("Failed to initialize bhyve provider; bhyve VM management will be unavailable", logging.FieldProvider, "bhyve", logging.FieldError, err)
		} else {
			if err := registry.Register(bhyveProvider); err != nil {
				logger.Warn("Failed to register provider", logging.FieldProvider, "bhyve", logging.FieldError, err)
			} else {
				logger.Info("Registered provider", logging.FieldProvider, "bhyve")
			}
		}
	}

	// Register QEMU provider (cross-platform)
	qemuProvider := qemu.NewQEMUProvider()
	if err := initProvider(ctx, qemuProvider, providerConfig); err != nil {
		logger.Warn("Failed to initialize QEMU provider; QEMU VM management will be unavailable", logging.FieldProvider, "qemu", logging.FieldError, err)
	} else {
		if err := registry.Register(qemuProvider); err != nil {
			logger.Warn("Failed to register provider", logging.FieldProvider, "qemu", logging.FieldError, err)
		} else {
			logger.Info("Registered provider", logging.FieldProvider, "qemu")
		}
	}

	// Register vfkit provider (macOS virtual machines through
	// Virtualization.framework). It is macOS-only and needs the vfkit binary,
	// so a Mac without it has one provider fewer.
	if runtime.GOOS == "darwin" {
		vfkitProvider := vfkit.NewVFKitProvider()
		if err := initProvider(ctx, vfkitProvider, providerConfig); err != nil {
			logger.Info("vfkit provider unavailable; macOS VM management will use QEMU only",
				logging.FieldProvider, "vfkit", logging.FieldError, err)
		} else {
			if err := registry.Register(vfkitProvider); err != nil {
				logger.Warn("Failed to register provider", logging.FieldProvider, "vfkit", logging.FieldError, err)
			} else {
				logger.Info("Registered provider", logging.FieldProvider, "vfkit")
			}
		}
	}

	// Register Apple's container tool (Linux containers, each in its own VM).
	// macOS 26+ on Apple silicon, and it needs its background service running,
	// so a Mac without either has one provider fewer.
	if runtime.GOOS == "darwin" {
		appleProvider := applecontainer.NewProvider()
		if err := initProvider(ctx, appleProvider, providerConfig); err != nil {
			logger.Info("Apple container provider unavailable",
				logging.FieldProvider, "container", logging.FieldError, err)
		} else {
			if err := registry.Register(appleProvider); err != nil {
				logger.Warn("Failed to register provider", logging.FieldProvider, "container", logging.FieldError, err)
			} else {
				logger.Info("Registered provider", logging.FieldProvider, "container")
			}
		}
	}

	// Register Podman provider (OCI containers)
	logger.Info("Initializing provider", logging.FieldProvider, "podman")
	podmanProvider := podman.NewPodmanProvider()
	if err := initProvider(ctx, podmanProvider, providerConfig); err != nil {
		logger.Warn("Failed to initialize Podman provider; Podman container management will be unavailable", logging.FieldProvider, "podman", logging.FieldError, err)
	} else {
		if err := registry.Register(podmanProvider); err != nil {
			logger.Warn("Failed to register provider", logging.FieldProvider, "podman", logging.FieldError, err)
		} else {
			logger.Info("Registered provider", logging.FieldProvider, "podman")
		}
	}
	logger.Info("Provider registration complete")
}

// runMigrationCommands handles --migrate and --migration-status flags
func runMigrationCommands(dbPath string, migrate, showStatus bool) {
	if err := doMigrationCommands(dbPath, migrate, showStatus); err != nil {
		os.Exit(1)
	}
}

// doMigrationCommands performs the migration work and returns an error so that
// the deferred datastore close always runs before the process exits.
func doMigrationCommands(dbPath string, migrate, showStatus bool) error {
	ds, err := datastore.NewDatastore(dbPath, nil)
	if err != nil {
		slog.Error("Failed to open database", logging.FieldError, err, "db_path", dbPath)
		return err
	}
	defer ds.Close()

	ctx := context.Background()

	if showStatus {
		status, err := ds.MigrationStatus(ctx)
		if err != nil {
			slog.Error("Failed to get migration status", logging.FieldError, err)
			return err
		}

		fmt.Printf("Migration Status:\n")
		fmt.Printf("  Current version: %d\n", status.CurrentVersion)
		fmt.Printf("  Latest version:  %d\n", status.LatestVersion)
		fmt.Printf("  Pending:         %d\n", status.PendingCount)
		fmt.Println()

		if len(status.Applied) > 0 {
			fmt.Println("Applied migrations:")
			for _, m := range status.Applied {
				fmt.Printf("  %d: %s (applied: %s)\n", m.Version, m.Description, m.AppliedAt.Format(time.RFC3339))
			}
		}
		return nil
	}

	if migrate {
		slog.Info("Running database migrations")
		if err := ds.Migrate(ctx, 0); err != nil {
			slog.Error("Migration failed", logging.FieldError, err)
			return err
		}
		slog.Info("Migrations completed successfully")
	}
	return nil
}

// loopbackListenAddr reports whether addr binds a loopback interface only. An
// empty host means every interface and does not qualify.
func loopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
