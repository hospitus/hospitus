package manifest

import (
	"testing"
)

// The same content must convert the same way whether it is written as a
// workload or as a stack instance. instanceConfigToSpec reimplemented the
// workload conversion instead of sharing it, and two branches went missing.
func TestStackInstanceMatchesWorkloadConversion(t *testing.T) {
	c := NewConverter()

	t.Run("a Linux jail needs no image source", func(t *testing.T) {
		inst := &InstanceConfig{
			Name:     "ubuntu",
			Provider: "jail",
			ProviderOverrides: map[string]ProviderConfig{
				"jail": {OSType: "linux"},
			},
		}
		// The workload form of the same thing is accepted; this used to fail
		// on ParseImageSource("").
		if _, err := c.instanceConfigToSpec(inst); err != nil {
			t.Errorf("a Linux jail instance was refused: %v", err)
		}
	})

	t.Run("a cloud image gets seed data without a cloud_init block", func(t *testing.T) {
		inst := &InstanceConfig{
			Name:     "web",
			Provider: "qemu",
			Image:    ImageSpec{Source: "cloud:ubuntu-24.04", Arch: "amd64"},
			Networks: []NetworkSpec{{Name: "default", Type: NetworkTypeBridge}},
		}
		spec, err := c.instanceConfigToSpec(inst)
		if err != nil {
			t.Fatalf("instanceConfigToSpec: %v", err)
		}
		if spec.CloudInit == nil || spec.CloudInit.MetaData == "" {
			t.Error("a cloud image produced no seed data; the VM boots with no user and no keys")
		}
	})
}

// Every path that converts a stack must validate what it produces.
// ToInstanceSpecs calls the private method directly, so a check placed only in
// the exported InstanceConfigToSpec wrapper left a whole stack manifest
// unvalidated — and a bridge name is spliced into QEMU's -netdev argument.
func TestStackConversionValidatesEverySpec(t *testing.T) {
	m := &StackManifest{
		Instances: []InstanceConfig{{
			Name:     "web",
			Provider: "qemu",
			Image:    ImageSpec{Source: "cloud:ubuntu-24.04", Arch: "amd64"},
			Networks: []NetworkSpec{{
				Name:   "default",
				Type:   NetworkTypeBridge,
				Bridge: "br0,helper=/tmp/x",
			}},
		}},
	}

	if _, err := NewConverter().ToInstanceSpecs(m); err == nil {
		t.Fatal("a bridge carrying an extra QEMU option was accepted")
	}
}
