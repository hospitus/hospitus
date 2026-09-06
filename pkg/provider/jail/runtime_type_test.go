package jail

import (
	"runtime"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestRuntimeTypeFollowsTheImage covers how a jail's runtime is decided.
//
// The runtime decides whether start runs "/bin/sh /etc/rc". A Linux rootfs has
// no /etc/rc, so getting this wrong does not degrade the jail — it makes it
// impossible to start:
//
//	/bin/sh: can't open '/etc/rc': No such file or directory
//
// which is what "hospitus jail create --image alpine-3.20-rootfs-amd64" produced,
// exactly as the images guide told the reader to write it. Only an explicit
// --os-type was read; the image name, which names the distribution outright,
// was not.
func TestRuntimeTypeFollowsTheImage(t *testing.T) {
	tests := []struct {
		name string
		spec provider.InstanceSpec
		want string
	}{
		{
			name: "linux rootfs image",
			spec: provider.InstanceSpec{Image: "alpine-3.20-rootfs-amd64"},
			want: "linux",
		},
		{
			name: "linux rootfs image, another distribution",
			spec: provider.InstanceSpec{Image: "ubuntu-24.04-rootfs-amd64"},
			want: "linux",
		},
		{
			name: "explicit os type still wins",
			spec: provider.InstanceSpec{OSType: "linux", Image: "freebsd-14.3-RELEASE-amd64"},
			want: "linux",
		},
		{
			// Named for the host's own architecture: any other suffix makes
			// this crossarch, and the answer would then depend on the machine
			// running the test rather than on the rule being checked.
			name: "freebsd base set",
			spec: provider.InstanceSpec{Image: "freebsd-14.3-RELEASE-" + runtime.GOARCH},
			want: "freebsd",
		},
		{
			// A cloud image is a disk for bhyve, not a tree a jail can run, and
			// must not be read as a Linux userland on the strength of its name.
			name: "linux cloud image is not a jail rootfs",
			spec: provider.InstanceSpec{Image: "debian-12-" + runtime.GOARCH},
			want: "freebsd",
		},
		{
			// Cross-architecture detection still has to win when nothing else
			// claims the image.
			name: "foreign architecture base set",
			spec: provider.InstanceSpec{Image: "freebsd-14.3-RELEASE-riscv64"},
			want: "crossarch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := determineRuntimeType(tt.spec); got != tt.want {
				t.Errorf("determineRuntimeType(%+v) = %q, want %q", tt.spec, got, tt.want)
			}
		})
	}
}
