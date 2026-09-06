package security

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/hospitus/hospitus/pkg/logging"
)

// SSHManager handles Hospitus cluster-internal SSH keys.
type SSHManager struct {
	keyPath        string
	knownHostsPath string
	mu             sync.Mutex
	logger         *slog.Logger
}

// NewSSHManager creates a manager pointing to the hospitus security directory.
func NewSSHManager(dataDir string) *SSHManager {
	return &SSHManager{
		keyPath:        filepath.Join(dataDir, "security", "cluster_id_rsa"),
		knownHostsPath: filepath.Join(dataDir, "security", "known_hosts"),
		logger:         logging.WithComponent("ssh-manager"),
	}
}

// EnsureKeys checks for the existence of the cluster keypair, generating it if missing.
func (m *SSHManager) EnsureKeys() error {
	m.logger.Debug("Ensuring SSH keys exist", "key_path", m.keyPath)
	if err := os.MkdirAll(filepath.Dir(m.keyPath), 0o700); err != nil {
		return err
	}

	if _, err := os.Stat(m.knownHostsPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to stat known_hosts: %w", err)
		}
		if err := os.WriteFile(m.knownHostsPath, []byte{}, 0o600); err != nil {
			return err
		}
	}

	// An existing key is accepted only after it is checked. Returning on the
	// mere presence of the path let a key left group- or world-readable by an
	// earlier install stay in use, and the cluster credential with it.
	switch info, err := os.Lstat(m.keyPath); {
	case err == nil:
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cluster key %s is not a regular file", m.keyPath)
		}
		if info.Mode().Perm()&0o077 != 0 {
			if chmodErr := os.Chmod(m.keyPath, 0o600); chmodErr != nil {
				return fmt.Errorf("cluster key %s is readable by group or others (%04o) and cannot be tightened: %w",
					m.keyPath, info.Mode().Perm(), chmodErr)
			}
			m.logger.Warn("Tightened permissions on the cluster SSH key",
				"path", m.keyPath, "was", fmt.Sprintf("%04o", info.Mode().Perm()))
		}
		// The public half can be missing or stale — an interrupted generation
		// leaves exactly that. GetPublicKey would then fail, or hand out a key
		// that does not match the private one GetClientConfig signs with.
		return m.ensurePublicKey()
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("failed to stat the cluster key: %w", err)
	}

	m.logger.Info("Generating new cluster SSH keypair")
	// Generate 4096-bit RSA key
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return err
	}

	// Save Private Key
	privBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}
	pubKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return err
	}

	// Both halves are encoded before either is published, and each is published
	// by rename. Writing the private key in place and the public one afterwards
	// leaves a usable-looking key with no public half if anything fails between
	// the two, and every later start skips regeneration because keyPath exists.
	var priv bytes.Buffer
	if err := pem.Encode(&priv, privBlock); err != nil {
		return err
	}
	if err := writeFileAtomic(m.keyPath, priv.Bytes(), 0o600); err != nil {
		return fmt.Errorf("failed to write the cluster key: %w", err)
	}
	if err := writeFileAtomic(m.keyPath+".pub", ssh.MarshalAuthorizedKey(pubKey), 0o600); err != nil {
		// The private half is already published; remove it so the next start
		// regenerates a complete pair instead of skipping on a half-made one.
		_ = os.Remove(m.keyPath)
		return fmt.Errorf("failed to write the cluster public key: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to a temporary file in the same directory and
// renames it into place, so a reader never sees a partial file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// ensurePublicKey derives the public half from the private key on disk and
// republishes it when it is absent or does not match.
func (m *SSHManager) ensurePublicKey() error {
	pubPath := m.keyPath + ".pub"

	keyData, err := os.ReadFile(m.keyPath)
	if err != nil {
		return fmt.Errorf("failed to read the cluster key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return fmt.Errorf("failed to parse the cluster key: %w", err)
	}
	want := ssh.MarshalAuthorizedKey(signer.PublicKey())

	if have, err := os.ReadFile(pubPath); err == nil && bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace(want)) {
		return nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failed to read the cluster public key: %w", err)
	}

	m.logger.Warn("Rewriting the cluster public key from the private key", "path", pubPath)
	return writeFileAtomic(pubPath, want, 0o600)
}

// GetClientConfig returns the SSH config for the Go client.
func (m *SSHManager) GetClientConfig(user string) (*ssh.ClientConfig, error) {
	keyData, err := os.ReadFile(m.keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read cluster key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse cluster key: %w", err)
	}

	return &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: m.tofuHostKeyCallback(),
	}, nil
}

func (m *SSHManager) tofuHostKeyCallback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		m.mu.Lock()
		defer m.mu.Unlock()

		callback, err := knownhosts.New(m.knownHostsPath)
		if err != nil {
			return fmt.Errorf("failed to load known_hosts: %w", err)
		}

		err = callback(hostname, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			return err
		}

		if len(keyErr.Want) > 0 {
			// Key mismatch, potential MITM
			return fmt.Errorf("host key mismatch for %s: %w", hostname, err)
		}

		// TOFU: append to known_hosts
		f, err := os.OpenFile(m.knownHostsPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			return fmt.Errorf("failed to open known_hosts: %w", err)
		}
		defer f.Close()

		line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
		if _, err := f.WriteString(line + "\n"); err != nil {
			return fmt.Errorf("failed to write known_hosts: %w", err)
		}

		return nil
	}
}

// GetPublicKey returns the public key as a string for cluster enrollment.
func (m *SSHManager) GetPublicKey() (string, error) {
	data, err := os.ReadFile(m.keyPath + ".pub")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
