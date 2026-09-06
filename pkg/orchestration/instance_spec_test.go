package orchestration

import (
	"testing"

	"github.com/hospitus/hospitus/pkg/manifest"
)

// TestStackInstanceReachesTheProviderWhole covers what a stack deploys.
//
// The daemon converted a stack instance itself, copying the name, image,
// architecture, CPUs, memory and networks and leaving the rest behind. Storage,
// environment and provider overrides never reached the provider, so a valid
// stack deployed workloads without their disks, their variables or their jail
// parameters — and said nothing.
func TestStackInstanceReachesTheProviderWhole(t *testing.T) {
	inst := manifest.InstanceConfig{
		Name:     "db",
		Provider: "jail",
		Image:    manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"},
		Resources: manifest.ResourceSpec{
			CPU:    4,
			Memory: "8Gi",
		},
		Storage: manifest.StorageSpec{
			Volumes: []manifest.VolumeSpec{
				{Name: "src", HostPath: "/usr/src", MountPath: "/usr/src", ReadOnly: true},
			},
		},
		Environment: map[string]string{"POSTGRES_DB": "app"},
		ProviderOverrides: map[string]manifest.ProviderConfig{
			"jail": {Parameters: map[string]interface{}{"allow.sysvipc": true}},
		},
	}

	sm := &StackManager{}
	spec, err := sm.instanceToSpec(inst, "app_db")
	if err != nil {
		t.Fatalf("instanceToSpec: %v", err)
	}

	if spec.Name != "app_db" {
		t.Errorf("name = %q, want the stack-qualified name", spec.Name)
	}
	if spec.CPUs != 4 || spec.MemoryMB != 8192 {
		t.Errorf("resources = %d CPUs / %d MB, want 4 / 8192", spec.CPUs, spec.MemoryMB)
	}

	mounts, ok := spec.ProviderConfig["mounts"].([]map[string]interface{})
	if !ok || len(mounts) != 1 {
		t.Errorf("the declared bind mount did not reach the provider: %v", spec.ProviderConfig["mounts"])
	}
	if _, ok := spec.ProviderConfig["environment"]; !ok {
		t.Error("the declared environment did not reach the provider")
	}
	if _, ok := spec.ProviderConfig["allow.sysvipc"]; !ok {
		t.Error("the declared jail parameter did not reach the provider")
	}
}
