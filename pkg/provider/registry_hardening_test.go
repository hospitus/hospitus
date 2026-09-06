package provider

import "testing"

// TestRegisterRejectsNilProvider covers the guard before Metadata is called on
// the value.
func TestRegisterRejectsNilProvider(t *testing.T) {
	if err := NewRegistry().Register(nil); err == nil {
		t.Error("Register(nil) was accepted")
	}
}

// TestCapabilitiesAreDetached covers a provider returning the same static value
// on every call: a caller must not be able to edit what the next one sees.
func TestCapabilitiesAreDetached(t *testing.T) {
	c := ProviderCapabilities{
		NetworkTypes:           []NetworkType{NetworkTypeBridge},
		SupportedArchitectures: []string{"amd64"},
	}
	clone := cloneCapabilities(c)
	clone.NetworkTypes[0] = NetworkTypeNAT
	clone.SupportedArchitectures[0] = "riscv64"

	if c.NetworkTypes[0] != NetworkTypeBridge || c.SupportedArchitectures[0] != "amd64" {
		t.Errorf("the caller edited the provider's own capabilities: %+v", c)
	}
}
