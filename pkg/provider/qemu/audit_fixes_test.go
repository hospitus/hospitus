package qemu

import (
	"regexp"
	"strings"
	"testing"
)

func TestAllocateVNCPortStartsAtBase(t *testing.T) {
	// Arrange: an empty state dir, so no VM holds a display yet.
	p := &QEMUProvider{stateDir: t.TempDir()}

	// Act
	port := p.allocatePort("web", "vnc_port", vncBasePort)

	// Assert: previously the base was double-counted, yielding 11800.
	if port < vncBasePort || port >= vncBasePort+100 {
		t.Fatalf("expected first VNC port near %d, got %d", vncBasePort, port)
	}
}

func TestTapInterfacesFromArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "single tap netdev",
			args: []string{"-netdev", "tap,id=net0,ifname=tap0,script=no"},
			want: []string{"tap0"},
		},
		{
			name: "multiple tap netdevs",
			args: []string{
				"-netdev", "tap,id=net0,ifname=tap0",
				"-netdev", "tap,id=net1,ifname=tap1",
			},
			want: []string{"tap0", "tap1"},
		},
		{
			name: "user-mode networking has no tap",
			args: []string{"-netdev", "user,id=net0,hostfwd=tcp::2222-:22"},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tapInterfacesFromArgs(tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestGenerateMACFormat(t *testing.T) {
	mac, err := generateMAC()
	if err != nil {
		t.Fatalf("generateMAC returned error: %v", err)
	}
	re := regexp.MustCompile(`^52:54:00:[0-9a-f]{2}:[0-9a-f]{2}:[0-9a-f]{2}$`)
	if !re.MatchString(mac) {
		t.Fatalf("MAC %q does not match expected 52:54:00 locally-administered format", mac)
	}
}

func TestRewriteMACsInArgs(t *testing.T) {
	// Arrange
	args := []string{
		"-device", "virtio-net-pci,netdev=net0,mac=52:54:00:11:22:33",
		"-device", "virtio-net-pci,netdev=net1,mac=52:54:00:aa:bb:cc",
	}
	original := make([]string, len(args))
	copy(original, args)

	// Act
	if err := rewriteMACsInArgs(args); err != nil {
		t.Fatalf("rewriteMACsInArgs error: %v", err)
	}

	// Assert: MACs changed, structure preserved, and the two NICs differ.
	if args[1] == original[1] || args[3] == original[3] {
		t.Fatalf("expected MAC addresses to be regenerated, got %v", args)
	}
	if !strings.Contains(args[1], "netdev=net0,mac=") {
		t.Fatalf("expected netdev structure preserved, got %q", args[1])
	}
	mac1 := macFragment(args[1])
	mac2 := macFragment(args[3])
	if mac1 == "" || mac2 == "" || mac1 == mac2 {
		t.Fatalf("expected two distinct fresh MACs, got %q and %q", mac1, mac2)
	}
}

func macFragment(arg string) string {
	for _, field := range strings.Split(arg, ",") {
		if strings.HasPrefix(field, "mac=") {
			return strings.TrimPrefix(field, "mac=")
		}
	}
	return ""
}

func TestRebuildArgsWithMediaPreservesCloudInit(t *testing.T) {
	// Arrange: a cloud-init CD-ROM plus no user CD-ROM.
	p := NewQEMUProvider()
	args := []string{
		"-drive", "file=/data/vm/cloud-init.iso,format=raw,media=cdrom,readonly=on",
		"-boot", "c",
	}

	// Act: insert a user ISO.
	out := p.rebuildArgsWithMedia(args, "/images/iso/ubuntu.iso", true)

	// Assert: the cloud-init drive is still present and a new user CD-ROM added.
	joined := strings.Join(out, " ")
	if !strings.Contains(joined, "cloud-init.iso") {
		t.Fatalf("cloud-init ISO was dropped: %v", out)
	}
	if !strings.Contains(joined, "ubuntu.iso") {
		t.Fatalf("user ISO was not inserted: %v", out)
	}
}

func TestRebuildArgsWithMediaEjectKeepsCloudInit(t *testing.T) {
	// Arrange: cloud-init CD-ROM plus a user CD-ROM.
	p := NewQEMUProvider()
	args := []string{
		"-drive", "file=/data/vm/cloud-init.iso,format=raw,media=cdrom,readonly=on",
		"-drive", "file=/images/iso/ubuntu.iso,format=raw,media=cdrom,readonly=on",
	}

	// Act: eject user media.
	out := p.rebuildArgsWithMedia(args, "", false)

	// Assert: cloud-init survives, user ISO removed.
	joined := strings.Join(out, " ")
	if !strings.Contains(joined, "cloud-init.iso") {
		t.Fatalf("cloud-init ISO was dropped on eject: %v", out)
	}
	if strings.Contains(joined, "ubuntu.iso") {
		t.Fatalf("user ISO should have been ejected: %v", out)
	}
}
