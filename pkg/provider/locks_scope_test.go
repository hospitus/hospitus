package provider

import (
	"context"
	"testing"
)

// TestLocksAreScopedToTheirOwner covers two providers holding their own
// InstanceLocks for the same instance name: the second must still serialize.
func TestLocksAreScopedToTheirOwner(t *testing.T) {
	first, second := &InstanceLocks{}, &InstanceLocks{}

	ctx, release, err := first.Acquire(context.Background(), "web")
	if err != nil {
		t.Fatalf("first.Acquire: %v", err)
	}
	defer release()

	// The context now carries first's marker. second must not read it as its own.
	held := make(chan struct{})
	go func() {
		_, rel, err := second.Acquire(ctx, "web")
		if err == nil {
			close(held)
			rel()
		}
	}()
	<-held

	blocked, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := second.Acquire(blocked, "web"); err == nil {
		t.Error("second.Acquire treated first's marker as its own and skipped the lock")
	}
}
