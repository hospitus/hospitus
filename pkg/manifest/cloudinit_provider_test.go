package manifest

import (
	"strings"
	"testing"
)

// TestCloudInitRefusedOnAJail covers a manifest that asks for provisioning the
// provider cannot deliver.
//
// Only bhyve and qemu attach a seed image a guest agent reads at first boot. A
// jail has neither, and dropping the block silently left an unprovisioned jail
// that looked like the one the manifest described.
func TestCloudInitRefusedOnAJail(t *testing.T) {
	m := &WorkloadManifest{
		Workload:  WorkloadMeta{Name: "web"},
		Provider:  ProviderSpec{Type: "jail"},
		Image:     ImageSpec{Source: "freebsd:14.3-RELEASE"},
		CloudInit: &CloudInitSpec{Enabled: boolPtr(true), Hostname: "web"},
	}

	_, err := NewConverter().ToInstanceSpec(m)
	if err == nil {
		t.Fatal("a cloud_init block on a jail was accepted and silently dropped")
	}
	for _, want := range []string{"jail", "cloud_init", "post_create"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestCloudInitAcceptedOnAVM keeps the refusal from swallowing the case it
// exists to serve.
func TestCloudInitAcceptedOnAVM(t *testing.T) {
	for _, providerType := range []string{"bhyve", "qemu"} {
		m := &WorkloadManifest{
			Workload:  WorkloadMeta{Name: "vm"},
			Provider:  ProviderSpec{Type: providerType},
			Image:     ImageSpec{Source: "cloud:ubuntu-24.04"},
			CloudInit: &CloudInitSpec{Enabled: boolPtr(true), Hostname: "vm"},
		}
		if _, err := NewConverter().ToInstanceSpec(m); err != nil {
			t.Errorf("%s: cloud_init refused: %v", providerType, err)
		}
	}
}

// TestJailWithoutCloudInitIsUnaffected checks the guard only fires on a block
// that was actually asked for.
func TestJailWithoutCloudInitIsUnaffected(t *testing.T) {
	m := &WorkloadManifest{
		Workload: WorkloadMeta{Name: "web"},
		Provider: ProviderSpec{Type: "jail"},
		Image:    ImageSpec{Source: "freebsd:14.3-RELEASE"},
	}
	if _, err := NewConverter().ToInstanceSpec(m); err != nil {
		t.Fatalf("a jail without cloud_init was refused: %v", err)
	}
}

// TestStackInstanceCloudInitRefusedOnAJail covers the second entry point: a
// stack instance converted on its own, which used to generate a seed image for
// a jail without checking the provider at all.
func TestStackInstanceCloudInitRefusedOnAJail(t *testing.T) {
	inst := &InstanceConfig{
		Name:     "db",
		Provider: "jail",
		// A real image source, or the conversion fails on that before it ever
		// reaches the cloud-init guard and the test passes for the wrong
		// reason.
		Image:     ImageSpec{Source: "freebsd:14.3"},
		CloudInit: &CloudInitSpec{Enabled: boolPtr(true)},
	}
	_, err := NewConverter().instanceConfigToSpec(inst)
	if err == nil {
		t.Fatal("a stack instance on a jail generated cloud-init anyway")
	}
	// The guard's own wording: "cloud-init" alone also matches the generic
	// "invalid cloud-init" the validation path returns, so the test passed
	// whether or not the provider check ran.
	if !strings.Contains(err.Error(), "cannot apply a cloud_init block") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}
