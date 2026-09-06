package manifest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/pkg/manifest"
)

// volumeClientStub records the volume calls apply makes. The embedded interface
// covers the rest of the API surface, which this test never reaches.
type volumeClientStub struct {
	cmdutil.APIClientInterface
	created  []client.CreateVolumeRequest
	attached []string
	existing map[string]bool
}

func (s *volumeClientStub) GetVolume(_ context.Context, name string) (*client.VolumeInfo, error) {
	if s.existing[name] {
		return &client.VolumeInfo{Name: name}, nil
	}
	return nil, fmt.Errorf("volume %s not found", name)
}

func (s *volumeClientStub) CreateVolume(_ context.Context, req client.CreateVolumeRequest) (*client.VolumeInfo, error) {
	s.created = append(s.created, req)
	return &client.VolumeInfo{Name: req.Name}, nil
}

func (s *volumeClientStub) AttachVolume(_ context.Context, instanceID, volumeName, mountPoint string) error {
	s.attached = append(s.attached, fmt.Sprintf("%s %s %s", instanceID, volumeName, mountPoint))
	return nil
}

func postgresManifest() *manifest.WorkloadManifest {
	m := &manifest.WorkloadManifest{}
	m.Provider.Type = "jail"
	m.Storage.Volumes = []manifest.VolumeSpec{{
		Name:      "pgdata",
		Size:      "100Gi",
		MountPath: "/var/db/postgres",
		ZFS:       &manifest.ZFSConfig{Compression: "lz4", Quota: "120Gi"},
	}}
	return m
}

// TestManifestVolumeIsCreatedAndAttached covers the PostgreSQL example in the
// manifest guide: a volume declared by size, with a quota and a compression.
//
// The converter only ever turned a volume with a host_path into a mount, so a
// ZFS-backed one was validated and then silently dropped — the workload ran with
// its data on the instance's own dataset.
func TestManifestVolumeIsCreatedAndAttached(t *testing.T) {
	stub := &volumeClientStub{}

	if _, err := applyManagedVolumes(context.Background(), stub, postgresManifest(), "postgres"); err != nil {
		t.Fatalf("applyManagedVolumes: %v", err)
	}

	if len(stub.created) != 1 {
		t.Fatalf("expected one volume to be created, got %d", len(stub.created))
	}
	got := stub.created[0]
	if got.Name != "postgres-pgdata" {
		t.Errorf("volume name = %q, want it scoped to the workload as postgres-pgdata", got.Name)
	}
	// 100Gi and 120Gi as byte counts, so zfs never has to read a "Gi" suffix.
	if got.Size != "107374182400" {
		t.Errorf("size = %q, want 100Gi in bytes", got.Size)
	}
	if got.Quota != "128849018880" {
		t.Errorf("quota = %q, want 120Gi in bytes", got.Quota)
	}
	if got.Compression != "lz4" {
		t.Errorf("compression = %q, want lz4", got.Compression)
	}

	want := "postgres postgres-pgdata /var/db/postgres"
	if len(stub.attached) != 1 || stub.attached[0] != want {
		t.Errorf("attach calls = %v, want [%s]", stub.attached, want)
	}
}

// TestManifestVolumeIsReusedOnReapply keeps a second apply from failing on a
// volume that is already there.
func TestManifestVolumeIsReusedOnReapply(t *testing.T) {
	stub := &volumeClientStub{existing: map[string]bool{"postgres-pgdata": true}}

	if _, err := applyManagedVolumes(context.Background(), stub, postgresManifest(), "postgres"); err != nil {
		t.Fatalf("applyManagedVolumes: %v", err)
	}

	if len(stub.created) != 0 {
		t.Errorf("an existing volume was created again: %v", stub.created)
	}
	if len(stub.attached) != 1 {
		t.Errorf("expected the existing volume to be attached, got %v", stub.attached)
	}
}

// TestHostPathVolumeStaysAMount leaves bind mounts to the instance spec.
func TestHostPathVolumeStaysAMount(t *testing.T) {
	m := &manifest.WorkloadManifest{}
	m.Provider.Type = "jail"
	m.Storage.Volumes = []manifest.VolumeSpec{{
		Name:      "src",
		HostPath:  "/usr/src",
		MountPath: "/usr/src",
	}}

	stub := &volumeClientStub{}
	if _, err := applyManagedVolumes(context.Background(), stub, m, "devbox"); err != nil {
		t.Fatalf("applyManagedVolumes: %v", err)
	}
	if len(stub.created) != 0 || len(stub.attached) != 0 {
		t.Errorf("a host_path volume went through the volume API: created=%v attached=%v",
			stub.created, stub.attached)
	}
}

// TestManifestVolumeRejectsAnUnreadableSize keeps a bad size from reaching zfs.
func TestManifestVolumeRejectsAnUnreadableSize(t *testing.T) {
	m := &manifest.WorkloadManifest{}
	m.Provider.Type = "jail"
	m.Storage.Volumes = []manifest.VolumeSpec{{Name: "data", Size: "plenty", MountPath: "/data"}}

	_, err := applyManagedVolumes(context.Background(), &volumeClientStub{}, m, "box")
	if err == nil || !strings.Contains(err.Error(), "invalid size") {
		t.Errorf("error = %v, want it to name the invalid size", err)
	}
}

// TestVMVolumeStaysADisk covers a sized volume on a VM.
//
// Only a jail has volume management. The converter already turns a sized volume
// into a disk in the instance spec, which is what a VM needs; asking the volume
// API for it failed with "volume management is only supported for jails" over a
// manifest that validates.
func TestVMVolumeStaysADisk(t *testing.T) {
	for _, providerType := range []string{"bhyve", "qemu", "podman"} {
		t.Run(providerType, func(t *testing.T) {
			m := &manifest.WorkloadManifest{}
			m.Provider.Type = providerType
			m.Storage.Volumes = []manifest.VolumeSpec{
				{Name: "pgdata", Size: "100Gi", MountPath: "/var/lib/postgresql"},
			}

			stub := &volumeClientStub{}
			if _, err := applyManagedVolumes(context.Background(), stub, m, "db"); err != nil {
				t.Fatalf("applyManagedVolumes: %v", err)
			}
			if len(stub.created) != 0 || len(stub.attached) != 0 {
				t.Errorf("%s went through the volume API: created=%v attached=%v",
					providerType, stub.created, stub.attached)
			}
		})
	}
}
