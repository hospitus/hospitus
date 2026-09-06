package crypto

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ── NewEncryptor ──────────────────────────────────────────────────────────────

// TestNewEncryptor_CreatesKeyFile verifies that NewEncryptor creates a new
// AES-256 key file when the data directory is empty.
func TestNewEncryptor_CreatesKeyFile(t *testing.T) {
	dir := t.TempDir()

	enc, err := NewEncryptor(dir)
	if err != nil {
		t.Fatalf("NewEncryptor returned error: %v", err)
	}
	if enc == nil {
		t.Fatal("NewEncryptor returned nil encryptor")
	}

	keyPath := filepath.Join(dir, keyFileName)
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file not created: %v", err)
	}
	if info.Size() != keySize {
		t.Errorf("expected key size %d bytes, got %d", keySize, info.Size())
	}
	// Key file should be readable only by its owner.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected key file permissions 0600, got %04o", perm)
	}
}

// TestNewEncryptor_LoadsExistingKey verifies that a second call to NewEncryptor
// on the same directory reuses the previously generated key, allowing data
// encrypted by the first instance to be decrypted by the second.
func TestNewEncryptor_LoadsExistingKey(t *testing.T) {
	dir := t.TempDir()

	enc1, err := NewEncryptor(dir)
	if err != nil {
		t.Fatalf("first NewEncryptor: %v", err)
	}

	ct, err := enc1.EncryptString("hello")
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}

	enc2, err := NewEncryptor(dir)
	if err != nil {
		t.Fatalf("second NewEncryptor: %v", err)
	}

	got, err := enc2.DecryptString(ct)
	if err != nil {
		t.Fatalf("DecryptString with reloaded key: %v", err)
	}
	if got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
}

// TestNewEncryptor_InvalidKeySize verifies that NewEncryptor rejects a stored
// key file whose length is not exactly the AES-256 key size.
func TestNewEncryptor_InvalidKeySize(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, keyFileName)

	// Write a 10-byte key — not the required AES-256 key size.
	if err := os.WriteFile(keyPath, make([]byte, 10), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := NewEncryptor(dir)
	if err == nil {
		t.Fatal("expected error for invalid key size, got nil")
	}
	if !strings.Contains(err.Error(), "master key must be") {
		t.Errorf("expected 'master key must be' in error, got: %v", err)
	}
}

// TestNewEncryptor_InsecurePermissions verifies that NewEncryptor refuses to
// load an existing key file that is accessible by group or others.
//
// It runs as root too: the refusal reads the mode bits rather than attempting
// an access, so root does not bypass it — and the FreeBSD CI runs as root,
// where a skip would have left this branch uncovered.
func TestNewEncryptor_InsecurePermissions(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, keyFileName)

	// Write a correctly-sized key but with group/other read bits set. Chmod
	// after the write: a restrictive umask would otherwise clear the very bits
	// this test is about.
	if err := os.WriteFile(keyPath, make([]byte, keySize), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	_, err := NewEncryptor(dir)
	if err == nil {
		t.Fatal("expected error for world-readable key file, got nil")
	}
	if !strings.Contains(err.Error(), "insecure permissions") {
		t.Errorf("expected 'insecure permissions' in error, got: %v", err)
	}
}

// TestNewEncryptor_KeyFileIsDir verifies that NewEncryptor returns an error
// when the key path exists as a directory rather than a regular file,
// exercising the "failed to read master key" (non-ENOENT) error branch.
func TestNewEncryptor_KeyFileIsDir(t *testing.T) {
	dir := t.TempDir()
	// Create a directory at the exact path where the key file should live.
	keyPath := filepath.Join(dir, keyFileName)
	if err := os.MkdirAll(keyPath, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	_, err := NewEncryptor(dir)
	if err == nil {
		t.Fatal("expected error when key path is a directory, got nil")
	}
	if !strings.Contains(err.Error(), "failed to read master key") {
		t.Errorf("expected 'failed to read master key' in error, got: %v", err)
	}
}

// TestNewEncryptor_MkdirAllFails verifies that NewEncryptor surfaces a
// MkdirAll failure when the key directory cannot be created.
// Skipped when running as root, which bypasses POSIX permission checks.
func TestNewEncryptor_MkdirAllFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: running as root bypasses permission checks")
	}

	tmpDir := t.TempDir()
	// A read-only (but traversable) parent directory: it can be walked to
	// resolve the missing key file (so os.ReadFile returns ENOENT and the
	// code reaches the key-generation branch), but MkdirAll cannot create a
	// new child directory inside it.
	parent := filepath.Join(tmpDir, "readonly")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	// Restore write permission so test cleanup (RemoveAll) can succeed.
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	// dataDir = parent/child (does not exist yet). keyPath lives under it, so
	// os.ReadFile returns ENOENT and NewEncryptor tries to MkdirAll(dataDir),
	// which fails because parent is not writable.
	dataDir := filepath.Join(parent, "child")
	_, err := NewEncryptor(dataDir)
	if err == nil {
		t.Fatal("expected error when key directory cannot be created, got nil")
	}
	if !strings.Contains(err.Error(), "failed to create key directory") {
		t.Errorf("expected 'failed to create key directory' in error, got: %v", err)
	}
}

// TestNewEncryptor_WriteKeyFails verifies that NewEncryptor returns an error
// when the data directory exists but is not writable (key file cannot be written).
// Skipped when running as root.
func TestNewEncryptor_WriteKeyFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: running as root bypasses permission checks")
	}

	dir := t.TempDir()
	// Remove write permission from the directory.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	// Restore permissions so the test cleanup (RemoveAll) can succeed.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := NewEncryptor(dir)
	if err == nil {
		t.Fatal("expected error for read-only directory, got nil")
	}
}

// ── Encrypt / Decrypt round-trips ────────────────────────────────────────────

// TestEncryptDecrypt_RoundTrip verifies that encrypting then decrypting
// arbitrary bytes always recovers the original plaintext.
func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	enc := newTestEncryptor(t)

	tests := []struct {
		name      string
		plaintext []byte
	}{
		{"empty", []byte{}},
		{"short ascii", []byte("hello")},
		{"binary bytes", []byte{0x00, 0xFF, 0x42, 0x7F}},
		{"unicode emoji", []byte("日本語テスト 🚀")},
		{"large 64KB", makeLargePayload(64 * 1024)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct, err := enc.Encrypt(tt.plaintext)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
			got, err := enc.Decrypt(ct)
			if err != nil {
				t.Fatalf("Decrypt: %v", err)
			}
			if string(got) != string(tt.plaintext) {
				t.Errorf("round-trip mismatch: want len=%d, got len=%d", len(tt.plaintext), len(got))
			}
		})
	}
}

// TestEncrypt_ProducesDifferentCiphertexts verifies that two encryptions of the
// same plaintext yield different ciphertexts due to random nonce generation.
func TestEncrypt_ProducesDifferentCiphertexts(t *testing.T) {
	enc := newTestEncryptor(t)

	ct1, err := enc.EncryptString("same plaintext")
	if err != nil {
		t.Fatalf("EncryptString #1: %v", err)
	}
	ct2, err := enc.EncryptString("same plaintext")
	if err != nil {
		t.Fatalf("EncryptString #2: %v", err)
	}
	if ct1 == ct2 {
		t.Error("two encryptions of the same plaintext must not be identical (nonce reuse)")
	}
}

// ── Decrypt error paths ───────────────────────────────────────────────────────

// TestDecrypt_InvalidBase64 verifies that Decrypt returns a descriptive error
// for malformed base64 input.
func TestDecrypt_InvalidBase64(t *testing.T) {
	enc := newTestEncryptor(t)

	_, err := enc.Decrypt("!!!not-valid-base64!!!")
	if err == nil {
		t.Fatal("expected error for invalid base64, got nil")
	}
	if !strings.Contains(err.Error(), "failed to decode ciphertext") {
		t.Errorf("expected 'failed to decode ciphertext' in error, got: %v", err)
	}
}

// TestDecrypt_TooShort verifies that Decrypt returns "ciphertext too short"
// when the decoded bytes are shorter than the GCM nonce (12 bytes for AES-GCM).
func TestDecrypt_TooShort(t *testing.T) {
	enc := newTestEncryptor(t)

	// 5 bytes is always less than the 12-byte AES-GCM nonce.
	tooShort := base64.StdEncoding.EncodeToString([]byte("short"))
	_, err := enc.Decrypt(tooShort)
	if err == nil {
		t.Fatal("expected error for ciphertext shorter than nonce, got nil")
	}
	if !strings.Contains(err.Error(), "ciphertext too short") {
		t.Errorf("expected 'ciphertext too short' in error, got: %v", err)
	}
}

// TestDecrypt_TamperedCiphertext verifies that Decrypt returns an error when
// the AEAD authentication tag has been corrupted (integrity check fails).
func TestDecrypt_TamperedCiphertext(t *testing.T) {
	enc := newTestEncryptor(t)

	ct, err := enc.EncryptString("sensitive data")
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}

	// Decode, flip the last byte (corrupts the authentication tag), re-encode.
	raw, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	raw[len(raw)-1] ^= 0xFF
	tampered := base64.StdEncoding.EncodeToString(raw)

	_, err = enc.Decrypt(tampered)
	if err == nil {
		t.Fatal("expected error for tampered ciphertext, got nil")
	}
	if !strings.Contains(err.Error(), "failed to decrypt") {
		t.Errorf("expected 'failed to decrypt' in error, got: %v", err)
	}
}

// ── EncryptString / DecryptString ─────────────────────────────────────────────

// TestEncryptDecryptString_RoundTrip verifies the string-convenience wrappers
// across a variety of inputs including empty strings and Unicode.
func TestEncryptDecryptString_RoundTrip(t *testing.T) {
	enc := newTestEncryptor(t)

	tests := []string{
		"",
		"hello, world",
		"unicode: 日本語",
		"special chars: <>&\"'",
		"SQL injection: ' OR '1'='1",
	}

	for _, plain := range tests {
		t.Run(plain, func(t *testing.T) {
			ct, err := enc.EncryptString(plain)
			if err != nil {
				t.Fatalf("EncryptString(%q): %v", plain, err)
			}
			got, err := enc.DecryptString(ct)
			if err != nil {
				t.Fatalf("DecryptString: %v", err)
			}
			if got != plain {
				t.Errorf("round-trip: want %q, got %q", plain, got)
			}
		})
	}
}

// TestDecryptString_PropagatesError verifies that DecryptString surfaces
// the underlying Decrypt error (invalid base64 in this case).
func TestDecryptString_PropagatesError(t *testing.T) {
	enc := newTestEncryptor(t)

	_, err := enc.DecryptString("not-valid-base64!!!")
	if err == nil {
		t.Fatal("expected error from DecryptString with invalid input, got nil")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// newTestEncryptor creates an Encryptor backed by a fresh temporary directory.
func newTestEncryptor(t *testing.T) *Encryptor {
	t.Helper()
	enc, err := NewEncryptor(t.TempDir())
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	return enc
}

// makeLargePayload returns a byte slice of the given size filled with a
// repeating pattern, useful for testing large-data throughput.
func makeLargePayload(size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i & 0xFF)
	}
	return b
}

// TestConcurrentKeyPublication covers two daemons starting against the same
// data directory. Whoever loses the link(2) race must adopt the published key,
// not the one it generated: a second writer would strand everything the first
// had already encrypted.
func TestConcurrentKeyPublication(t *testing.T) {
	dir := t.TempDir()

	const n = 8
	keys := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			e, err := NewEncryptor(dir)
			if err != nil {
				errs[i] = err
				return
			}
			// Round-trip through each encryptor: identical keys mean any of them
			// can read what another wrote.
			ct, err := e.Encrypt([]byte("secret"))
			if err != nil {
				errs[i] = err
				return
			}
			keys[i] = ct
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("encryptor %d: %v", i, err)
		}
	}

	ref, err := NewEncryptor(dir)
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	for i, ct := range keys {
		got, err := ref.Decrypt(ct)
		if err != nil {
			t.Errorf("encryptor %d produced ciphertext the published key cannot read: %v", i, err)
			continue
		}
		if string(got) != "secret" {
			t.Errorf("encryptor %d round-tripped to %q", i, got)
		}
	}
}
