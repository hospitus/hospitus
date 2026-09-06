package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestStopStackReportsInstancesItCouldNotStop covers a stack marked stopped over
// instances that are still running.
//
// Every stop failure was a warning, the status became "stopped" regardless, and
// the API reported success for work that did not happen.
func TestStopStackReportsInstancesItCouldNotStop(t *testing.T) {
	sm := NewStackManager(provider.NewRegistry(), nil)
	sm.stacks["app"] = &Stack{
		Name:   "app",
		Status: StackStatusRunning,
		Instances: []*StackInstance{
			// No provider is registered, so the stack cannot be stopped.
			{Name: "web", InstanceID: "app_web", Provider: "absent"},
		},
	}

	err := sm.StopStack(context.Background(), "app")
	if err == nil {
		t.Fatal("stopping a stack whose instances could not be stopped reported success")
	}
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("error = %v, want it to name the instance", err)
	}
	if got := sm.stacks["app"].Status; got == StackStatusStopped {
		t.Error("the stack was marked stopped although nothing was stopped")
	}
}

// TestDestroyStackKeepsItsRecordWhenInstancesRemain covers a stack record
// deleted over instances that are still there: they keep running with nothing
// left to manage them by.
func TestDestroyStackKeepsItsRecordWhenInstancesRemain(t *testing.T) {
	sm := NewStackManager(provider.NewRegistry(), nil)
	sm.stacks["app"] = &Stack{
		Name:   "app",
		Status: StackStatusRunning,
		Instances: []*StackInstance{
			{Name: "web", InstanceID: "app_web", Provider: "absent"},
		},
	}

	err := sm.DestroyStack(context.Background(), "app")
	if err == nil {
		t.Fatal("destroying a stack whose instances remain reported success")
	}
	if _, ok := sm.stacks["app"]; !ok {
		t.Error("the stack record was removed although its instances are still there")
	}
}
