package provider

import (
	"errors"
	"fmt"
	"testing"
)

func TestWrapError(t *testing.T) {
	t.Run("nil stays nil", func(t *testing.T) {
		if err := WrapError("podman", "delete", "web", nil); err != nil {
			t.Errorf("WrapError(nil) = %v, want nil", err)
		}
	})

	t.Run("attaches provider context", func(t *testing.T) {
		cause := errors.New("container is running")
		err := WrapError("podman", "delete", "web", cause)

		var provErr *ProviderError
		if !errors.As(err, &provErr) {
			t.Fatalf("WrapError did not produce a *ProviderError: %T", err)
		}
		if provErr.Provider != "podman" || provErr.Operation != "delete" || provErr.InstanceID != "web" {
			t.Errorf("context lost: %+v", provErr)
		}
		if !errors.Is(err, cause) {
			t.Error("the cause must stay reachable through errors.Is")
		}
	})

	t.Run("already wrapped is untouched", func(t *testing.T) {
		inner := NewProviderError("qemu", "start", "vm1", errors.New("no such image"))
		wrapped := WrapError("qemu", "create", "vm1", inner)

		var got *ProviderError
		if !errors.As(wrapped, &got) {
			t.Fatalf("WrapError did not keep a *ProviderError: %T", wrapped)
		}
		if got.Operation != "start" {
			t.Errorf("re-wrapping replaced the original context: %+v", got)
		}
	})

	t.Run("sees through fmt.Errorf", func(t *testing.T) {
		inner := NewProviderError("jail", "stop", "web", errors.New("busy"))
		wrapped := WrapError("jail", "delete", "web", fmt.Errorf("cleanup: %w", inner))

		var provErr *ProviderError
		if !errors.As(wrapped, &provErr) || provErr.Operation != "stop" {
			t.Errorf("wrapping should preserve the innermost provider context, got %v", wrapped)
		}
	})
}
