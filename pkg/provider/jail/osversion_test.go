package jail

import (
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestFreeBSDUserlandVersion(t *testing.T) {
	tests := []struct {
		name        string
		image       string
		osVersion   string
		wantRelease string
		wantReldate int
	}{
		{"image name", "freebsd-14.3-RELEASE-amd64", "", "14.3-RELEASE", 1403000},
		{"other minor", "freebsd-13.4-RELEASE-amd64", "", "13.4-RELEASE", 1304000},
		{"host version for comparison", "freebsd-15.1-RELEASE-amd64", "", "15.1-RELEASE", 1501000},
		{"arm64 image", "freebsd-14.2-RELEASE-arm64", "", "14.2-RELEASE", 1402000},
		{"stable branch", "freebsd-14-STABLE-amd64", "14.0-STABLE", "14.0-STABLE", 1400000},
		{"explicit version wins", "freebsd-14.3-RELEASE-amd64", "13.4-RELEASE", "13.4-RELEASE", 1304000},
		{"linux rootfs", "ubuntu-24.04-rootfs-amd64", "", "", 0},
		{"nothing to go on", "", "", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release, reldate := freebsdUserlandVersion(tt.image, tt.osVersion)
			if release != tt.wantRelease {
				t.Errorf("release = %q, want %q", release, tt.wantRelease)
			}
			if reldate != tt.wantReldate {
				t.Errorf("reldate = %d, want %d", reldate, tt.wantReldate)
			}
		})
	}
}

// TestBuildJailConfigSetsUserlandVersion is the point of the exercise: a jail
// built from a 14.3 base must report 14.3, whatever the host kernel is, or pkg
// inside it resolves the wrong ABI and refuses its own repository.
func TestBuildJailConfigSetsUserlandVersion(t *testing.T) {
	p := &JailProvider{}

	config := p.buildJailConfig(provider.InstanceSpec{
		Name:  "web",
		Image: "freebsd-14.3-RELEASE-amd64",
	}, "/zroot/hospitus/jails/web")

	if config.JailParameters.Osrelease != "14.3-RELEASE" {
		t.Errorf("osrelease = %q, want 14.3-RELEASE", config.JailParameters.Osrelease)
	}
	if config.JailParameters.Osreldate != 1403000 {
		t.Errorf("osreldate = %d, want 1403000", config.JailParameters.Osreldate)
	}
}

// Provider config parsing rebuilds the parameter set, which used to drop the
// version set above it.
func TestBuildJailConfigKeepsUserlandVersionWithProviderConfig(t *testing.T) {
	p := &JailProvider{}

	config := p.buildJailConfig(provider.InstanceSpec{
		Name:           "web",
		Image:          "freebsd-14.3-RELEASE-amd64",
		ProviderConfig: map[string]interface{}{"allow.raw_sockets": true},
	}, "/zroot/hospitus/jails/web")

	if config.JailParameters.Osrelease != "14.3-RELEASE" {
		t.Errorf("osrelease = %q, want it preserved through provider config parsing", config.JailParameters.Osrelease)
	}
}

// An operator who names a version means it.
func TestBuildJailConfigHonoursExplicitOsrelease(t *testing.T) {
	p := &JailProvider{}

	config := p.buildJailConfig(provider.InstanceSpec{
		Name:  "web",
		Image: "freebsd-14.3-RELEASE-amd64",
		ProviderConfig: map[string]interface{}{
			"osrelease": "13.4-RELEASE",
			"osreldate": 1304000,
		},
	}, "/zroot/hospitus/jails/web")

	if config.JailParameters.Osrelease != "13.4-RELEASE" {
		t.Errorf("osrelease = %q, want the declared 13.4-RELEASE", config.JailParameters.Osrelease)
	}
	// The pair has to move together: keeping the image-derived osreldate beside
	// a declared osrelease is exactly the mismatch this test is named for.
	if config.JailParameters.Osreldate != 1304000 {
		t.Errorf("osreldate = %d, want the declared 1304000", config.JailParameters.Osreldate)
	}
}

// A Linux jail has no FreeBSD release to report.
func TestBuildJailConfigLeavesLinuxJailsAlone(t *testing.T) {
	p := &JailProvider{}

	config := p.buildJailConfig(provider.InstanceSpec{
		Name:  "ubuntu",
		Image: "ubuntu-24.04-rootfs-amd64",
	}, "/zroot/hospitus/jails/ubuntu")

	if config.JailParameters.Osrelease != "" {
		t.Errorf("osrelease = %q, want it left unset for a Linux userland", config.JailParameters.Osrelease)
	}
}
