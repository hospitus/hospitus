package jail

import (
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestManifestJailParametersReachTheJail guards the whole path a jail parameter
// travels: a manifest asks for one under provider_overrides.jail.parameters,
// the converter flattens it into ProviderConfig, and the provider has to put it
// on the jail(8) command line.
//
// A parameter lost along the way leaves the jail running with the default and
// says nothing. PostgreSQL asking for System V shared memory then meets a jail
// that denies it, and fails with a shmget error that names no manifest.
func TestManifestJailParametersReachTheJail(t *testing.T) {
	p := &JailProvider{}

	spec := provider.InstanceSpec{
		Name: "db",
		ProviderConfig: map[string]interface{}{
			// Asked for by the manifest.
			"sysvshm":           "new",
			"allow.sysvipc":     true,
			"allow.raw_sockets": false,
			// Not jail parameters: these belong to the provider and must not
			// be handed to jail(8).
			"os_type":    "freebsd",
			"bootloader": "uefi",
		},
	}

	config := p.buildJailConfig(spec, "/zroot/hospitus/jails/db")

	for _, key := range []string{"sysvshm", "allow.sysvipc", "allow.raw_sockets"} {
		if _, ok := config.Parameters[key]; !ok {
			t.Errorf("%q was asked for by the manifest and did not reach the jail", key)
		}
	}
	if got := config.Parameters["sysvshm"]; got != "new" {
		t.Errorf("sysvshm = %v, want \"new\"", got)
	}

	for _, key := range []string{"os_type", "bootloader"} {
		if _, ok := config.Parameters[key]; ok {
			t.Errorf("%q is not a jail parameter and must not be passed to jail(8)", key)
		}
	}
}
