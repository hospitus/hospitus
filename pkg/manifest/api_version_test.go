package manifest

import (
	"strings"
	"testing"
)

// TestAPIVersionIsChecked covers the one value api_version may hold.
//
// A manifest from a future release names fields this build ignores, so applying
// it as though it were current gives an instance the manifest did not describe.
func TestAPIVersionIsChecked(t *testing.T) {
	tests := []struct {
		name    string
		version string
		wantErr bool
	}{
		{"the current version", APIVersion, false},
		{"absent, which the parser fills in", "", false},
		{"a version that never existed", "hospitus.io/v99", true},
		{"another project's", "apps/v1", true},
		{"a near miss", "hospitus.io/V1", true},
		{"prose", "latest", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateAPIVersion("workload.api_version", tt.version)
			if (len(errs) > 0) != tt.wantErr {
				t.Fatalf("validateAPIVersion(%q) = %v, want error: %v", tt.version, errs, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(errs.Error(), tt.version) {
				t.Errorf("the error does not name the version it rejected: %s", errs.Error())
			}
		})
	}
}

// TestWorkloadRejectsAnUnknownAPIVersion checks it through the validator, which
// is the path a manifest actually takes.
func TestWorkloadRejectsAnUnknownAPIVersion(t *testing.T) {
	m := &WorkloadManifest{}
	m.Workload.APIVersion = "hospitus.io/v99"
	m.Workload.Name = "web"
	m.Provider.Type = "jail"

	errs := NewValidator().validateWorkload(m)
	if !strings.Contains(errs.Error(), "workload.api_version") {
		t.Errorf("a workload with an unknown api_version was accepted: %v", errs)
	}
}

// TestStackRejectsAnUnknownAPIVersion covers the other manifest kind.
func TestStackRejectsAnUnknownAPIVersion(t *testing.T) {
	m := &StackManifest{}
	m.Stack.APIVersion = "hospitus.io/v99"
	m.Stack.Name = "app"
	m.Instances = []InstanceConfig{{Name: "web", Provider: "jail"}}

	errs := NewValidator().validateStack(m)
	if !strings.Contains(errs.Error(), "stack.api_version") {
		t.Errorf("a stack with an unknown api_version was accepted: %v", errs)
	}
}
