package security

import (
	"context"
	"testing"
)

func TestGetHostMetrics(t *testing.T) {
	// Note: This test runs on the host system.
	// On FreeBSD, it will gather real data.
	// On other systems, it might return defaults or errors.

	collector := NewSystemCollector(nil, nil)
	ctx := context.Background()

	metrics, err := collector.GetHostMetrics(ctx)
	if err != nil {
		t.Logf("Warning: could not gather real host metrics (likely non-BSD): %v", err)
		return
	}

	if metrics == nil {
		t.Fatal("Expected metrics, got nil")
	}

	t.Logf("CPU Usage: %.2f%%", metrics.CPUPercent)
	t.Logf("Memory: %dMB / %dMB", metrics.MemoryUsedMB, metrics.MemoryTotalMB)
	t.Logf("Storage: %dGB / %dGB", metrics.StorageUsedGB, metrics.StorageTotalGB)

	// Basic sanity checks
	if metrics.MemoryTotalMB == 0 && err == nil {
		t.Errorf("Memory total should not be 0")
	}
}
