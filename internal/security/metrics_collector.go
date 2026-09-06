package security

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/dataset"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
)

// HostMetrics represents the current resource usage of the physical host.
type HostMetrics struct {
	CPUPercent     float64
	MemoryUsedMB   uint64
	MemoryTotalMB  uint64
	StorageUsedGB  uint64
	StorageTotalGB uint64
	Timestamp      time.Time
}

// SystemCollector provides methods to gather real-time FreeBSD metrics.
type SystemCollector struct {
	datastore *datastore.Datastore
	registry  *provider.Registry
	logger    *slog.Logger
}

// NewSystemCollector creates a new metrics collector.
func NewSystemCollector(ds *datastore.Datastore, reg *provider.Registry) *SystemCollector {
	return &SystemCollector{
		datastore: ds,
		registry:  reg,
		logger:    logging.WithComponent("metrics-collector"),
	}
}

// GetHostMetrics gathers actual host metrics via sysctl and zfs.
func (c *SystemCollector) GetHostMetrics(ctx context.Context) (*HostMetrics, error) {
	metrics := &HostMetrics{
		Timestamp: time.Now(),
	}

	// 1. CPU Usage (load average as a proxy; could be more granular)
	if out, err := exec.CommandContext(ctx, "sysctl", "-n", "vm.loadavg").Output(); err != nil {
		c.logger.Debug("Failed to get CPU load average", logging.FieldError, err)
	} else {
		fields := strings.Fields(strings.Trim(string(out), "{ }"))
		if len(fields) > 0 {
			if load, err := strconv.ParseFloat(fields[0], 64); err == nil {
				metrics.CPUPercent = (load / float64(runtime.NumCPU())) * 100
				if metrics.CPUPercent > 100 {
					metrics.CPUPercent = 100
				}
			}
		}
	}

	// 2. Memory Usage
	var pageSize uint64 = 4096
	if out, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.pagesize").Output(); err != nil {
		c.logger.Debug("Failed to get hw.pagesize", logging.FieldError, err)
	} else {
		pageSize, _ = strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	}

	if out, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.physmem").Output(); err != nil {
		c.logger.Debug("Failed to get hw.physmem", logging.FieldError, err)
	} else {
		metrics.MemoryTotalMB, _ = strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
		metrics.MemoryTotalMB /= (1024 * 1024)
	}

	var active, wired uint64
	if out, err := exec.CommandContext(ctx, "sysctl", "-n", "vm.stats.vm.v_active_count").Output(); err != nil {
		c.logger.Debug("Failed to get active memory count", logging.FieldError, err)
	} else {
		active, _ = strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	}
	if out, err := exec.CommandContext(ctx, "sysctl", "-n", "vm.stats.vm.v_wire_count").Output(); err != nil {
		c.logger.Debug("Failed to get wired memory count", logging.FieldError, err)
	} else {
		wired, _ = strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	}
	metrics.MemoryUsedMB = (active + wired) * pageSize / (1024 * 1024)

	// 3. Storage Usage (zroot pool)
	if out, err := exec.CommandContext(ctx, "zfs", "list", "-Hp", "-o", "used,available", dataset.Pool()).Output(); err != nil {
		c.logger.Debug("Failed to get storage metrics from pool", logging.FieldError, err)
	} else {
		parts := strings.Fields(string(out))
		if len(parts) >= 2 {
			used, _ := strconv.ParseUint(parts[0], 10, 64)
			avail, _ := strconv.ParseUint(parts[1], 10, 64)
			metrics.StorageUsedGB = used / (1024 * 1024 * 1024)
			metrics.StorageTotalGB = (used + avail) / (1024 * 1024 * 1024)
		}
	}

	// The sysctls above are BSD's. Where they are absent every read fails, is
	// logged at debug level, and the caller is handed a report of zeros that
	// looks like a measurement. Say the host could not be measured instead.
	if metrics.MemoryTotalMB == 0 {
		return nil, fmt.Errorf("cannot read host memory on %s: the collector reads BSD sysctls", runtime.GOOS)
	}

	return metrics, nil
}
