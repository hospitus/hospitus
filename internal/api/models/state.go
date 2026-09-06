package models

import (
	"github.com/hospitus/hospitus/pkg/provider"
)

// HospitusStatus represents the unified, user-facing state of any instance or node.
type HospitusStatus string

const (
	StatusOnline  HospitusStatus = "online"  // Node is reachable / Instance is running
	StatusOffline HospitusStatus = "offline" // Node is unreachable / Instance is stopped
	StatusPending HospitusStatus = "pending" // Operation in progress (Creating, Starting)
	StatusError   HospitusStatus = "error"   // System failure or configuration issue
	StatusUnknown HospitusStatus = "unknown" // Initial state before heartbeat
)

// MapInstanceState converts provider-level states into unified Hospitus statuses.
func MapInstanceState(state provider.InstanceState) HospitusStatus {
	switch state {
	case provider.StateRunning:
		return StatusOnline
	case provider.StateStopped:
		return StatusOffline
	case provider.StateCreating, provider.StateStarting, provider.StateStopping, provider.StateDeleting:
		return StatusPending
	case provider.StateError:
		return StatusError
	default:
		return StatusUnknown
	}
}

// MapNodeStatus standardizes the string representation of node health.
func MapNodeStatus(status string) HospitusStatus {
	switch status {
	case "online", "active":
		return StatusOnline
	case "offline", "unreachable":
		return StatusOffline
	// An empty string is a node nothing has reported on yet, which is the
	// definition of unknown. It fell to the default and was shown as an
	// error, so a cluster that had merely not been polled looked broken.
	case "unknown", "":
		return StatusUnknown
	default:
		return StatusError
	}
}
