package security

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
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

	if _, err := os.Stat(m.keyPath); err == nil {
		return nil
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
	privFile, err := os.OpenFile(m.keyPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer privFile.Close()
	if err := pem.Encode(privFile, privBlock); err != nil {
		return err
	}

	// Save Public Key
	pubKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return err
	}
	pubPath := m.keyPath + ".pub"
	return os.WriteFile(pubPath, ssh.MarshalAuthorizedKey(pubKey), 0o600)
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
