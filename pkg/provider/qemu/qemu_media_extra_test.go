package qemu

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// persistConfigVM writes a stopped VM whose ProviderConfig map is initialized,
// as buildQEMUConfig does at creation time.
func persistConfigVM(t *testing.T, p *QEMUProvider, name string) {
	t.Helper()
	cfg := &vmConfig{
		Name: name,
		Args: []string{"-name", name, "-m", "1024"},
		Spec: provider.InstanceSpec{
			Name: name,
			// A non-empty map: an empty one is dropped by omitempty on save and
			// reloads as nil, which real configs never do (creation stores keys).
			ProviderConfig: map[string]interface{}{"machine": "q35"},
		},
	}
	if err := p.saveVMConfig(cfg, filepath.Join(p.stateDir, name+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestInsertAndListMediaStoppedVM(t *testing.T) {
	// Arrange: an ISO within an allowed directory and a stopped VM.
	p := fakeProvider(t, &execx.Fake{})
	persistConfigVM(t, p, "web")
	iso := filepath.Join(p.imageDir, "install.iso")
	if err := os.WriteFile(iso, []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Act
	err := p.InsertMedia(context.Background(), provider.InstanceHandle{ID: "web"}, provider.MediaSpec{
		Type: provider.MediaTypeCDROM,
		Path: iso,
	})
	if err != nil {
		t.Fatalf("InsertMedia: %v", err)
	}

	// Assert: config records the ISO and ListMedia reflects it.
	media, err := p.ListMedia(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("ListMedia: %v", err)
	}
	if len(media) != 1 || media[0].Path != iso || !media[0].Inserted {
		t.Fatalf("media = %+v, want one inserted ISO", media)
	}
	// The rebuilt args must include a CD-ROM drive pointing at the ISO.
	cfg, err := p.loadVMConfig(filepath.Join(p.stateDir, "web.json"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cfg.Args, " ")
	if !strings.Contains(joined, "media=cdrom") || !strings.Contains(joined, iso) {
		t.Errorf("rebuilt args missing CD-ROM: %v", cfg.Args)
	}
}

func TestInsertMediaRejectsMissingFile(t *testing.T) {
	p := fakeProvider(t, &execx.Fake{})
	persistConfigVM(t, p, "web")
	err := p.InsertMedia(context.Background(), provider.InstanceHandle{ID: "web"}, provider.MediaSpec{
		Type: provider.MediaTypeCDROM,
		Path: filepath.Join(p.imageDir, "missing.iso"),
	})
	if err == nil {
		t.Error("expected error for a missing media file")
	}
}

func TestEjectMediaStoppedVM(t *testing.T) {
	// Arrange: insert then eject.
	p := fakeProvider(t, &execx.Fake{})
	persistConfigVM(t, p, "web")
	iso := filepath.Join(p.imageDir, "install.iso")
	if err := os.WriteFile(iso, []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.InsertMedia(context.Background(), provider.InstanceHandle{ID: "web"}, provider.MediaSpec{Type: provider.MediaTypeCDROM, Path: iso}); err != nil {
		t.Fatal(err)
	}

	// Act
	if err := p.EjectMedia(context.Background(), provider.InstanceHandle{ID: "web"}, "ide0-cd0"); err != nil {
		t.Fatalf("EjectMedia: %v", err)
	}

	// Assert: install_iso is gone from config.
	media, err := p.ListMedia(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 0 {
		t.Errorf("expected no media after eject, got %+v", media)
	}
}

func TestSetAndGetBootOrderStoppedVM(t *testing.T) {
	// Arrange
	p := fakeProvider(t, &execx.Fake{})
	persistConfigVM(t, p, "web")

	// Act
	err := p.SetBootOrder(context.Background(), provider.InstanceHandle{ID: "web"}, provider.BootOrder{
		Devices: []provider.BootDevice{provider.BootDeviceCDROM, provider.BootDeviceHardDisk},
	})
	if err != nil {
		t.Fatalf("SetBootOrder: %v", err)
	}

	// Assert
	order, err := p.GetBootOrder(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetBootOrder: %v", err)
	}
	if len(order.Devices) != 2 || order.Devices[0] != provider.BootDeviceCDROM {
		t.Errorf("boot order = %v, want [cdrom disk]", order.Devices)
	}
}

func TestRebuildArgsWithMediaInsertAndEject(t *testing.T) {
	// Insert into args with no existing CD-ROM: appends a drive and -boot d.
	p := &QEMUProvider{}
	base := []string{"-name", "web", "-m", "1024"}
	inserted := p.rebuildArgsWithMedia(base, "/img/a.iso", true)
	if !slices.Contains(inserted, "-boot") {
		t.Errorf("insert should append -boot: %v", inserted)
	}
	joined := strings.Join(inserted, " ")
	if !strings.Contains(joined, "media=cdrom") || !strings.Contains(joined, "/img/a.iso") {
		t.Errorf("insert did not add the CD-ROM drive: %v", inserted)
	}

	// Ejecting removes the CD-ROM drive and the -boot d entry.
	ejected := p.rebuildArgsWithMedia(inserted, "/img/a.iso", false)
	if strings.Contains(strings.Join(ejected, " "), "media=cdrom") {
		t.Errorf("eject should remove the CD-ROM drive: %v", ejected)
	}
}

func TestBuildCloneConfig(t *testing.T) {
	// Arrange
	p := fakeProvider(t, &execx.Fake{})
	source := &vmConfig{
		Name: "src",
		Spec: provider.InstanceSpec{
			Name:     "src",
			CPUs:     2,
			MemoryMB: 2048,
			Disks:    []provider.DiskSpec{{Path: "/data/src.qcow2"}},
		},
	}
	cloneDisk := "/data/clone.qcow2"

	// Act: clone with resource overrides.
	clone, err := p.buildCloneConfig(source, "clone", filepath.Join(p.dataDir, "clone"), cloneDisk, provider.CloneOptions{
		CPUs:     4,
		MemoryMB: 4096,
		Labels:   map[string]string{"env": "test"},
	})
	if err != nil {
		t.Fatalf("buildCloneConfig: %v", err)
	}

	// Assert
	if clone.Name != "clone" || clone.Spec.Name != "clone" {
		t.Errorf("clone name not applied: %+v", clone.Spec.Name)
	}
	if clone.Spec.CPUs != 4 || clone.Spec.MemoryMB != 4096 {
		t.Errorf("overrides not applied: cpus=%d mem=%d", clone.Spec.CPUs, clone.Spec.MemoryMB)
	}
	if len(clone.Spec.Disks) != 1 || clone.Spec.Disks[0].Path != cloneDisk {
		t.Errorf("clone disk path not updated: %+v", clone.Spec.Disks)
	}
	if clone.Spec.Labels["env"] != "test" {
		t.Errorf("label override dropped: %+v", clone.Spec.Labels)
	}
	// The deep copy must not alias the source disk slice.
	if source.Spec.Disks[0].Path != "/data/src.qcow2" {
		t.Error("buildCloneConfig mutated the source spec")
	}
}
