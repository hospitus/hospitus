package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/internal/security"
)

// TestMain points the global audit logger at a writable temp file before any
// NewServer call. NewServer initializes the global audit logger from
// DefaultAuditLoggerConfig(), whose default path (/var/log/hospitus/audit.log) is
// not writable in the test environment. Pre-initializing here makes the
// InitGlobalAuditLogger call inside NewServer short-circuit and reuse this
// logger instead of failing.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hospitus-audit-test")
	if err != nil {
		panic(err)
	}
	cfg := security.DefaultAuditLoggerConfig()
	cfg.LogFile = filepath.Join(dir, "audit.log")
	if err := security.InitGlobalAuditLogger(cfg); err != nil {
		panic(err)
	}

	code := m.Run()

	_ = os.RemoveAll(dir)
	os.Exit(code)
}
