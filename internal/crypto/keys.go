// Package crypto provides symmetric encryption utilities for HOSPITUS secrets.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

const (
	keyFileName = "master.key"
	keySize     = 32 // AES-256
)

// Encryptor provides AES-GCM symmetric encryption with file-backed key storage.
type Encryptor struct {
	gcm cipher.AEAD
}

// NewEncryptor loads or creates an AES-256 key from the given directory.
func NewEncryptor(dataDir string) (*Encryptor, error) {
	keyPath := filepath.Join(dataDir, keyFileName)

	keyData, err := readKeyFile(keyPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("failed to read master key: %w", err)
		}
		// Generate new key
		key := make([]byte, keySize)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("failed to generate master key: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
			return nil, fmt.Errorf("failed to create key directory: %w", err)
		}

		// Write the whole key to a temporary file first, then publish it with
		// link(2). Two daemons starting together would otherwise each generate a
		// key and the second write would strand whatever the first had already
		// encrypted; and a reader arriving between create and write would find
		// an empty file where the key should be. link() gives both properties at
		// once: it is atomic, and it refuses when the name already exists.
		tmp, tmpErr := os.CreateTemp(filepath.Dir(keyPath), ".master.key-*")
		if tmpErr != nil {
			return nil, fmt.Errorf("failed to create the master key: %w", tmpErr)
		}
		tmpName := tmp.Name()
		defer func() { _ = os.Remove(tmpName) }()

		if err := tmp.Chmod(0o600); err != nil {
			_ = tmp.Close()
			return nil, fmt.Errorf("failed to secure the master key: %w", err)
		}
		if _, err := tmp.Write(key); err != nil {
			_ = tmp.Close()
			return nil, fmt.Errorf("failed to write the master key: %w", err)
		}
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			return nil, fmt.Errorf("failed to flush the master key: %w", err)
		}
		if err := tmp.Close(); err != nil {
			return nil, fmt.Errorf("failed to write the master key: %w", err)
		}

		switch linkErr := os.Link(tmpName, keyPath); {
		case linkErr == nil:
			keyData = key
		case errors.Is(linkErr, fs.ErrExist):
			// Another process published first; its key is the one on disk.
			keyData, err = readKeyFile(keyPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read the master key another process published: %w", err)
			}
		default:
			return nil, fmt.Errorf("failed to publish the master key: %w", linkErr)
		}
	} else {
		// Existing key loaded from disk: refuse insecure permissions or a
		// wrong-sized key before it is ever used.
		info, statErr := os.Stat(keyPath)
		if statErr != nil {
			return nil, fmt.Errorf("failed to stat master key: %w", statErr)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("master key %s has insecure permissions %04o (must not be accessible by group or others)", keyPath, info.Mode().Perm())
		}
		if len(keyData) != keySize {
			return nil, fmt.Errorf("master key must be %d bytes, got %d", keySize, len(keyData))
		}
	}

	block, err := aes.NewCipher(keyData)
	if err != nil {
		return nil, fmt.Errorf("invalid master key: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	return &Encryptor{gcm: gcm}, nil
}

// Encrypt encrypts plaintext and returns base64-encoded ciphertext (nonce + ciphertext).
func (e *Encryptor) Encrypt(plaintext []byte) (string, error) {
	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := e.gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decodes base64 ciphertext and returns plaintext.
func (e *Encryptor) Decrypt(encoded string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("failed to decode ciphertext: %w", err)
	}

	nonceSize := e.gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := e.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt: %w", err)
	}

	return plaintext, nil
}

// EncryptString encrypts a string and returns base64-encoded ciphertext.
func (e *Encryptor) EncryptString(s string) (string, error) {
	return e.Encrypt([]byte(s))
}

// DecryptString decodes base64 ciphertext and returns plaintext string.
func (e *Encryptor) DecryptString(encoded string) (string, error) {
	plain, err := e.Decrypt(encoded)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// readKeyFile reads the master key without following a symlink.
//
// The key directory is the daemon's own and mode 0700, but a link planted there
// would otherwise make the daemon read, and trust, a file somebody else owns.
func readKeyFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, int64(keySize)+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read master key: %w", err)
	}
	if len(data) != keySize {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", keySize, len(data))
	}
	return data, nil
}
