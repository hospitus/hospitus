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
// resolveSecretsDir picks where secrets live when the caller names no
// directory.
//
// The choice depends on what the running process can write, which means the
// daemon and an unprivileged CLI can land in different directories — and a
// secret missing from one is generated afresh rather than read, so the same
// (scope, name) yields two different credentials and whatever was configured
// with the first stops working. Anything that must agree with the daemon
// passes an explicit baseDir to NewFileSecretStore; this fallback is for a
// single-process use, and it is why the daemon's own path is configured.
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
//
// Probing creates the directory: the only reliable test is to make it and
// write in it, so resolveSecretsDir leaves behind every candidate it tried
// before settling on one.
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
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		return string(data), nil
	}
	// An empty file is not a secret. A crash between an old create and its
	// write could leave one, and returning it handed out "" as the password.
	// Removed rather than replaced, so the create below still refuses to
	// clobber a value another process wrote in the meantime.
	if err == nil {
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return "", fmt.Errorf("failed to clear the empty secret %s: %w", name, rmErr)
		}
		return s.generateAndSave(scope, name, false)
	}
	// Only a missing file means "not generated yet". Regenerating on any read
	// error — a permission problem, a transient I/O failure — overwrote a
	// credential that was still there, and everything already using it broke.
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to read secret %s: %w", name, err)
	}

	// Generate new
	return s.generateAndSave(scope, name, false)
}

// Rotate generates a new value for a secret
func (s *FileSecretStore) Rotate(scope, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.generateAndSave(scope, name, true)
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

// generateAndSave writes a new value for a secret.
//
// replace says what to do when the file is already there: Rotate means to
// overwrite it, Get means to keep what is there and return it. Get used to
// overwrite too, so two processes that both found the secret missing each
// generated one and the loser returned a value the winner had replaced.
func (s *FileSecretStore) generateAndSave(scope, name string, replace bool) (string, error) {
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
	// Hex, and deliberately so: a generated secret is interpolated into shell
	// commands and configuration files by the manifests that use it, and 64
	// characters drawn from [0-9a-f] carry no quote, no backslash and no shell
	// metacharacter. Changing this alphabet means auditing every one of those
	// call sites for quoting.
	value := hex.EncodeToString(b)

	// Save with atomic write (tmp + rename). The temporary name is unique
	// rather than "<path>.tmp": two processes generating the same secret at
	// once shared that one name, and each overwrote the other's half-written
	// file before renaming it into place.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secret-*")
	if err != nil {
		return "", fmt.Errorf("failed to write secret: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("failed to secure secret file: %w", err)
	}
	if _, err := tmp.WriteString(value); err != nil {
		tmp.Close()
		return "", fmt.Errorf("failed to write secret: %w", err)
	}
	// Flushed before it is published: a crash between the write and the rename
	// would otherwise leave an empty file under the secret's own name, and Get
	// would hand that out as the password.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("failed to flush secret: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("failed to close secret file: %w", err)
	}

	if replace {
		if err := os.Rename(tmpPath, path); err != nil {
			return "", fmt.Errorf("failed to persist secret: %w", err)
		}
		syncDir(filepath.Dir(path))
		return value, nil
	}

	// Link, not rename: rename replaces, and here the file must not be
	// replaced. Two processes that both found the secret missing each
	// generated one, and the loser returned a value the winner had already
	// overwritten — a password handed to a caller that no longer opens
	// anything. Link refuses when the target exists, and the loser reads what
	// the winner wrote.
	if err := os.Link(tmpPath, path); err != nil {
		if !os.IsExist(err) {
			return "", fmt.Errorf("failed to persist secret: %w", err)
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", fmt.Errorf("failed to read the secret another writer created: %w", readErr)
		}
		if len(existing) == 0 {
			// The same rule Get applies: an empty file is not a secret, and
			// returning it would hand out "" as the password.
			return "", fmt.Errorf("the secret %s exists but is empty", name)
		}
		return string(existing), nil
	}
	syncDir(filepath.Dir(path))

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

// syncDir flushes a directory entry so a rename or link into it survives a
// crash. Best-effort: the secret is already on disk, and a filesystem that
// refuses to sync a directory is not a reason to fail.
func syncDir(path string) {
	d, err := os.Open(path)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
