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

// TestInsertMediaRefusesAPathOutsideTheImageDirectory covers the second of
// InsertMedia's two guards. The first — the file must exist — is covered above;
// this one is the containment, and nothing exercised it.
func TestInsertMediaRefusesAPathOutsideTheImageDirectory(t *testing.T) {
	p := fakeProvider(t, &execx.Fake{})
	persistConfigVM(t, p, "web")

	// A real file the VM has no business reading.
	outside := filepath.Join(t.TempDir(), "host.iso")
	if err := os.WriteFile(outside, []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Type set, like the other cases: without it a rejection on an unset media
	// type would satisfy this test and leave the containment guard uncovered.
	err := p.InsertMedia(context.Background(), provider.InstanceHandle{ID: "web"},
		provider.MediaSpec{Type: provider.MediaTypeCDROM, Path: outside})
	if err == nil {
		t.Fatal("InsertMedia accepted a path outside the managed directories")
	}
	if strings.Contains(err.Error(), "not found") {
		t.Errorf("refused for the wrong reason: %v", err)
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
	// The shared helper, like every other test here: a bare literal leaves the
	// directories and the runner unset.
	p := fakeProvider(t, &execx.Fake{})
	base := []string{"-name", "web", "-m", "1024"}
	inserted := p.rebuildArgsWithMedia(base, "/img/a.iso", true)
	if !slices.Contains(inserted, "-boot") {
		t.Errorf("insert should append -boot: %v", inserted)
	}
	joined := strings.Join(inserted, " ")
	if !strings.Contains(joined, "media=cdrom") || !strings.Contains(joined, "/img/a.iso") {
		t.Errorf("insert did not add the CD-ROM drive: %v", inserted)
	}

	// Ejecting removes the CD-ROM drive and the boot entry that pointed at it.
	// The insert half above checks -boot; without the same check here a
	// leftover order would go unnoticed, and the VM would keep trying to boot
	// from a drive that is gone.
	ejected := p.rebuildArgsWithMedia(inserted, "/img/a.iso", false)
	if strings.Contains(strings.Join(ejected, " "), "media=cdrom") {
		t.Errorf("eject should remove the CD-ROM drive: %v", ejected)
	}
	// The cdrom letter goes; the rest of the order stays, which is what keeps a
	// VM booting from its disk instead of losing its boot order entirely.
	//
	// Against the literal value, not against withoutCDROM of itself: the eject
	// path applies that very function, so the old comparison tested one of its
	// fixed points and held for anything it could return.
	bootValue := ""
	for i, a := range ejected {
		if a == "-boot" && i+1 < len(ejected) {
			bootValue = ejected[i+1]
		}
	}
	if bootValue == "" {
		t.Fatalf("eject dropped -boot entirely: %v", ejected)
	}
	// The order= sub-option's letters alone: -boot is a comma-separated list,
	// so anything after the first "=" also swallows "menu=on" and its own "d".
	letters := ""
	for _, part := range strings.Split(bootValue, ",") {
		if rest, ok := strings.CutPrefix(part, "order="); ok {
			letters = rest
		}
	}
	if letters == "" {
		t.Fatalf("eject left no boot order: %q", bootValue)
	}
	if strings.Contains(letters, "d") {
		t.Errorf("eject left the CD-ROM in the boot order: %q", bootValue)
	}
	if !strings.Contains(letters, "c") {
		t.Errorf("eject lost the disk from the boot order: %q", bootValue)
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
	clone, err := p.buildCloneConfig(source, filepath.Join(p.dataDir, source.Name), "clone", filepath.Join(p.dataDir, "clone"), cloneDisk, provider.CloneOptions{
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

// TestBootValueHandlesSubOptions covers the parts of a -boot value that are not
// the order.
//
// qemu takes a comma-separated list — "order=dc,menu=on,splash=logo.jpg" — and
// treating it as one blob deleted "-boot menu=on" on an eject, and stripped the
// "d" out of "splash=logo.jpg" along the way.
func TestBootValueHandlesSubOptions(t *testing.T) {
	for _, tc := range []struct{ in, ejected, inserted string }{
		{"order=dc", "order=c", "order=dc"},
		{"order=c", "order=c", "order=dc"},
		{"order=d", "", "order=d"},
		{"d", "", "d"},
		{"nc", "nc", "dnc"},
		// qemu accepts "-boot menu=on" with no order at all, so a sub-option
		// that names no device survives an eject on its own.
		{"menu=on", "menu=on", "menu=on,order=dc"},
		{"order=dc,menu=on", "order=c,menu=on", "order=dc,menu=on"},
		{"order=c,splash=addendum.jpg", "order=c,splash=addendum.jpg", "order=dc,splash=addendum.jpg"},
		// once=d is a one-shot; inserting media also puts the CD-ROM at the head
		// of the standing order, which is what the request means.
		{"once=d,order=c", "order=c", "once=d,order=dc"},
	} {
		if got := withoutCDROM(tc.in); got != tc.ejected {
			t.Errorf("withoutCDROM(%q) = %q, want %q", tc.in, got, tc.ejected)
		}
		if got := withCDROMFirst(tc.in); got != tc.inserted {
			t.Errorf("withCDROMFirst(%q) = %q, want %q", tc.in, got, tc.inserted)
		}
	}
}

// TestInsertMediaLeavesOneBootOption covers the duplicate the insert path used
// to produce: it preserved an existing -boot and appended another.
func TestInsertMediaLeavesOneBootOption(t *testing.T) {
	p := fakeProvider(t, &execx.Fake{})
	args := []string{"-m", "1024", "-boot", "order=c"}

	got := p.rebuildArgsWithMedia(args, "/img/a.iso", true)

	boots := 0
	for i, a := range got {
		if a == "-boot" {
			boots++
			if i+1 < len(got) && !strings.Contains(got[i+1], "d") {
				t.Errorf("the boot order does not name the CD-ROM: %q", got[i+1])
			}
		}
	}
	if boots != 1 {
		t.Errorf("found %d -boot options, want exactly one: %v", boots, got)
	}
}
