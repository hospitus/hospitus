package manifest

import (
	"reflect"
	"testing"
)

// TestWorkloadManifestCarriesEveryInstanceField guards the mapping a stack apply
// makes from one of its instances to a workload manifest.
//
// The mapping is written field by field, so a field added to InstanceConfig and
// forgotten there is dropped in silence: cloud-init and environment were, and a
// stack deployed VMs that were never provisioned and containers with none of
// their variables.
//
// DependsOn is the exception: it orders the deployment and describes nothing
// about the instance itself.
func TestWorkloadManifestCarriesEveryInstanceField(t *testing.T) {
	workload := map[string]bool{}
	wm := reflect.TypeOf(WorkloadManifest{})
	for i := 0; i < wm.NumField(); i++ {
		workload[wm.Field(i).Name] = true
	}
	// Name and Provider travel under different names: an instance's Name becomes
	// the workload's Workload.Name, and its Provider the workload's
	// Provider.Type.
	workload["Name"] = true
	workload["Provider"] = true
	// Not part of the instance itself.
	workload["DependsOn"] = true

	ic := reflect.TypeOf(InstanceConfig{})
	for i := 0; i < ic.NumField(); i++ {
		field := ic.Field(i).Name
		if !workload[field] {
			t.Errorf("InstanceConfig.%s has nowhere to go in a WorkloadManifest; "+
				"a stack apply would drop it", field)
		}
	}
}
