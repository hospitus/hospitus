package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestToInstanceSpecUserDataFile verifies cloud_init.user_data_file is honored
// on the ToInstanceSpec path, not silently ignored (audit HIGH convert.go:835).
func TestToInstanceSpecUserDataFile(t *testing.T) {
	dir := t.TempDir()
	udf := filepath.Join(dir, "user-data.yaml")
	const payload = "#cloud-config\nruncmd:\n  - echo from-file\n"
	if err := os.WriteFile(udf, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewConverterWithCloudInitRoot(dir)
	m := &WorkloadManifest{
		Workload: WorkloadMeta{Name: "vm1"},
		Provider: ProviderSpec{Type: ProviderTypeQEMU},
		Image:    ImageSpec{Source: "cloud:ubuntu-24.04"},
		CloudInit: &CloudInitSpec{
			Enabled:      boolPtr(true),
			UserDataFile: udf,
		},
	}

	spec, err := c.ToInstanceSpec(m)
	if err != nil {
		t.Fatalf("ToInstanceSpec: %v", err)
	}
	if spec.CloudInit == nil || !strings.Contains(spec.CloudInit.UserData, "from-file") {
		t.Errorf("user_data_file content not applied; got CloudInit=%+v", spec.CloudInit)
	}
}

// TestToInstanceSpecUserDataFileMissing verifies a missing user_data_file is a
// hard error rather than being silently ignored.
func TestToInstanceSpecUserDataFileMissing(t *testing.T) {
	c := NewConverterWithCloudInitRoot(t.TempDir())
	m := &WorkloadManifest{
		Workload: WorkloadMeta{Name: "vm2"},
		Provider: ProviderSpec{Type: ProviderTypeQEMU},
		Image:    ImageSpec{Source: "cloud:ubuntu-24.04"},
		CloudInit: &CloudInitSpec{
			Enabled:      boolPtr(true),
			UserDataFile: "/nonexistent/hospitus-user-data.yaml",
		},
	}
	if _, err := c.ToInstanceSpec(m); err == nil {
		t.Error("expected error for missing user_data_file")
	}
}

// TestUserDataFileIsConfined covers the escalation the confinement exists for:
// a stack posted to the API is converted by the daemon, as root, so an
// unconfined path would read any host file into the caller's guest.
func TestUserDataFileIsConfined(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	manifestWith := func(path string) *WorkloadManifest {
		return &WorkloadManifest{
			Workload:  WorkloadMeta{Name: "vm"},
			Provider:  ProviderSpec{Type: ProviderTypeQEMU},
			Image:     ImageSpec{Source: "cloud:ubuntu-24.04"},
			CloudInit: &CloudInitSpec{Enabled: boolPtr(true), UserDataFile: path},
		}
	}

	t.Run("a file outside the root is refused", func(t *testing.T) {
		_, err := NewConverterWithCloudInitRoot(root).ToInstanceSpec(manifestWith(outside))
		if err == nil {
			t.Fatal("a user_data_file outside the cloud-init directory was read")
		}
		if !strings.Contains(err.Error(), "must be under") {
			t.Errorf("error does not say why: %v", err)
		}
	})

	t.Run("a symlink out of the root is refused", func(t *testing.T) {
		link := filepath.Join(root, "link.yaml")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := NewConverterWithCloudInitRoot(root).ToInstanceSpec(manifestWith(link)); err == nil {
			t.Error("a symlink out of the cloud-init directory was followed")
		}
	})

	t.Run("no configured root refuses the field", func(t *testing.T) {
		inside := filepath.Join(root, "user-data.yaml")
		if err := os.WriteFile(inside, []byte("#cloud-config\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewConverter().ToInstanceSpec(manifestWith(inside)); err == nil {
			t.Error("user_data_file was accepted with no directory configured")
		}
	})

	t.Run("a file inside the root is read", func(t *testing.T) {
		inside := filepath.Join(root, "ok.yaml")
		if err := os.WriteFile(inside, []byte("#cloud-config\nruncmd: [true]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		spec, err := NewConverterWithCloudInitRoot(root).ToInstanceSpec(manifestWith(inside))
		if err != nil {
			t.Fatalf("a file inside the cloud-init directory was refused: %v", err)
		}
		if spec.CloudInit == nil || !strings.Contains(spec.CloudInit.UserData, "runcmd") {
			t.Errorf("content not applied: %+v", spec.CloudInit)
		}
	})
}
