package security

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"

	"golang.org/x/crypto/ssh"
)

// mockAddr implements net.Addr
type mockAddr struct {
	addr string
}

func (m mockAddr) Network() string { return "tcp" }
func (m mockAddr) String() string  { return m.addr }

func TestSSHManager_TOFU(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewSSHManager(tempDir)

	if err := mgr.EnsureKeys(); err != nil {
		t.Fatalf("EnsureKeys failed: %v", err)
	}

	cfg, err := mgr.GetClientConfig("testuser")
	if err != nil {
		t.Fatalf("GetClientConfig failed: %v", err)
	}

	cb := cfg.HostKeyCallback

	// Generate a dummy host key
	priv1, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub1, _ := ssh.NewPublicKey(&priv1.PublicKey)

	remote1 := mockAddr{addr: "10.0.0.1:22"}

	// 1. First use should succeed (TOFU)
	err = cb("10.0.0.1:22", remote1, pub1)
	if err != nil {
		t.Errorf("Expected first use to succeed, got: %v", err)
	}

	// 2. Second use with same key should succeed
	err = cb("10.0.0.1:22", remote1, pub1)
	if err != nil {
		t.Errorf("Expected second use to succeed, got: %v", err)
	}

	// 3. Second use with different key should fail (MITM)
	priv2, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub2, _ := ssh.NewPublicKey(&priv2.PublicKey)
	err = cb("10.0.0.1:22", remote1, pub2)
	if err == nil {
		t.Errorf("Expected MITM attack to fail, but it succeeded")
	}
}
