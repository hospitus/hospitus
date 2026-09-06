// Package crypto provides symmetric encryption utilities for HOSPITUS secrets.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
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

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		if !os.IsNotExist(err) {
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

		// Create it exclusively. Two daemons starting together would otherwise
		// each generate a key and the second write would strand whatever the
		// first had already encrypted.
		f, createErr := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		switch {
		case createErr == nil:
			_, writeErr := f.Write(key)
			closeErr := f.Close()
			if writeErr != nil {
				return nil, fmt.Errorf("failed to write master key: %w", writeErr)
			}
			if closeErr != nil {
				return nil, fmt.Errorf("failed to write master key: %w", closeErr)
			}
			keyData = key
		case os.IsExist(createErr):
			// Another process created it first; its key is the one on disk.
			keyData, err = os.ReadFile(keyPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read master key: %w", err)
			}
			if len(keyData) != keySize {
				return nil, fmt.Errorf("master key must be %d bytes, got %d", keySize, len(keyData))
			}
		default:
			return nil, fmt.Errorf("failed to create master key: %w", createErr)
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
