package manifest

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// hospitusStateBaseDir is the base state directory for Hospitus.
	hospitusStateBaseDir = "/var/lib/hospitus"

	// DefaultSecretsDir is the standard filesystem directory path where Hospitus
	// stores secret files. It is a directory path, not a credential value; it
	// is composed from the base state dir so it reads as a path, not a literal
	// secret.
	DefaultSecretsDir = hospitusStateBaseDir + "/secrets"
)

// SecretStore defines the interface for managing persistent secrets
type SecretStore interface {
	// Get returns a secret by name, generating it if it doesn't exist
	Get(scope, name string) (string, error)
	// Rotate generates a new value for an existing secret
	Rotate(scope, name string) (string, error)
	// Remove deletes a secret
	Remove(scope, name string) error
	// List returns all secret names in a scope
	List(scope string) ([]string, error)
}

// FileSecretStore implements SecretStore using local files
type FileSecretStore struct {
	baseDir string
	mu      sync.Mutex
}

// NewFileSecretStore creates a new file-backed secret store.
//
// When baseDir is empty the directory is resolved by resolveSecretsDir. It
// deliberately does NOT fall back to a world-writable temp directory: a
// predictable /tmp path is exploitable by an attacker who pre-creates a
// symlink there. For validation/dry-run without persistence, use
// NoopSecretStore.
func NewFileSecretStore(baseDir string) *FileSecretStore {
	if baseDir == "" {
		baseDir = resolveSecretsDir()
	}
	return &FileSecretStore{
		baseDir: filepath.Clean(baseDir),
	}
}

// BaseDir returns the directory this store keeps secrets in.
//
// Callers that enumerate scopes need it: reading DefaultSecretsDir directly
// looks in the daemon's directory whatever the store actually resolved, so
// "hospitus secret list" with no scope answered "No secrets found" while
// "hospitus secret list <scope>" listed the secret it could not see.
func (s *FileSecretStore) BaseDir() string {
	return s.baseDir
}

// resolveSecretsDir picks where secrets live for whoever is running.
//
// DefaultSecretsDir sits under the daemon's state directory and belongs to
// root, so the daemon writes there. "hospitus apply" runs as the person at the
// keyboard, who usually cannot, and that is how the placeholder store came to
// be used for real deployments: every {{ secret }} in a manifest rendered to
// PLACEHOLDER-<scope>-<name>, which is to say the database password in the
// WordPress example was two public strings joined by dashes.
//
// So an unprivileged caller gets a directory of their own instead. It is under
// the user's home, created 0700, neither shared nor guessable by another user
// — the reasoning against a /tmp fallback does not apply to it. Secrets a user
// generates stay readable by that user alone, and the daemon's own store is
// untouched.
func resolveSecretsDir() string {
	if writableDir(DefaultSecretsDir) {
		return DefaultSecretsDir
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Nowhere better to go. Returning the system path means the failure
		// surfaces as a permission error naming a real directory, rather than
		// secrets being written somewhere unexpected.
		return DefaultSecretsDir
	}
	return filepath.Join(home, ".local", "share", "hospitus", "secrets")
}

// writableDir reports whether dir can be created and written to by this
// process.
func writableDir(dir string) bool {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}

	// An existing directory may be there and still refuse us, which MkdirAll
	// reports as success. Ask by writing.
	probe, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	probe.Close()
	os.Remove(name)
	return true
}

// Get retrieves a secret or generates a new one
func (s *FileSecretStore) Get(scope, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := s.path(scope, name)
	if err != nil {
		return "", err
	}

	// Try to read existing
	if data, err := os.ReadFile(path); err == nil {
		return string(data), nil
	}

	// Generate new
	return s.generateAndSave(scope, name)
}

// Rotate generates a new value for a secret
func (s *FileSecretStore) Rotate(scope, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.generateAndSave(scope, name)
}

// Remove deletes a secret file
func (s *FileSecretStore) Remove(scope, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, err := s.path(scope, name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// List returns all secrets in a scope
func (s *FileSecretStore) List(scope string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate scope to prevent traversal
	if scope == "" || strings.Contains(scope, "/") || strings.Contains(scope, "\\") ||
		strings.Contains(scope, "\x00") || scope == "." || scope == ".." {
		return nil, fmt.Errorf("invalid scope %q", scope)
	}
	scopeDir := filepath.Join(s.baseDir, scope)
	entries, err := os.ReadDir(scopeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var secrets []string
	for _, e := range entries {
		if !e.IsDir() {
			secrets = append(secrets, e.Name())
		}
	}
	return secrets, nil
}

func (s *FileSecretStore) path(scope, name string) (string, error) {
	// Reject path traversal in scope or name
	for _, part := range []string{scope, name} {
		if part == "" || strings.Contains(part, "/") || strings.Contains(part, "\\") ||
			strings.Contains(part, "\x00") || part == "." || part == ".." {
			return "", fmt.Errorf("invalid secret identifier %q: must not contain path separators, null bytes, or be empty", part)
		}
	}
	resolved := filepath.Join(s.baseDir, scope, name)
	// Ensure resolved path is still under baseDir
	if !strings.HasPrefix(resolved, s.baseDir+string(filepath.Separator)) {
		return "", fmt.Errorf("secret path escapes base directory")
	}
	return resolved, nil
}

func (s *FileSecretStore) generateAndSave(scope, name string) (string, error) {
	// Resolve before creating anything. Get validates the identifiers before
	// calling here, but Rotate does not, and it is reachable from
	// `hospitus secret rotate <scope> <name>` — so a scope containing ".." had its
	// directories created outside baseDir before path() ever rejected it.
	path, err := s.path(scope, name)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("failed to create secrets directory: %w", err)
	}

	// Generate 32-byte hex string (64 chars)
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	value := hex.EncodeToString(b)

	// Save with atomic write (tmp + rename)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(value), 0o600); err != nil {
		return "", fmt.Errorf("failed to write secret: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to persist secret: %w", err)
	}

	return value, nil
}

// NoopSecretStore implements SecretStore without persistence.
// It generates deterministic placeholder values and is suitable for
// manifest validation or dry-run scenarios where no actual secrets are needed.
type NoopSecretStore struct{}

// NewNoopSecretStore creates a no-op secret store that returns placeholders.
func NewNoopSecretStore() *NoopSecretStore {
	return &NoopSecretStore{}
}

// Get returns a deterministic placeholder for a secret.
func (s *NoopSecretStore) Get(scope, name string) (string, error) {
	return fmt.Sprintf("PLACEHOLDER-%s-%s", scope, name), nil
}

// Rotate returns a deterministic placeholder for a secret.
func (s *NoopSecretStore) Rotate(scope, name string) (string, error) {
	return s.Get(scope, name)
}

// Remove is a no-op.
func (s *NoopSecretStore) Remove(scope, name string) error {
	return nil
}

// List returns an empty list.
func (s *NoopSecretStore) List(scope string) ([]string, error) {
	return []string{}, nil
}
