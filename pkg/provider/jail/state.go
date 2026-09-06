package jail

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// GetInstanceState returns the current state of a jail
func (p *JailProvider) GetInstanceState(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceState, error) {
	jailName := handle.ID

	// Check if jail configuration exists
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	// A permission denied, or any other stat failure, is not "not found": the
	// call would otherwise carry on and report Running or Stopped for a jail
	// whose configuration was never read.
	if _, err := os.Stat(configPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return provider.StateUnknown, provider.ErrInstanceNotFound
		}
		return provider.StateUnknown, fmt.Errorf("cannot read the jail configuration: %w", err)
	}

	// Check if running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return provider.StateUnknown, err
	}

	if running {
		return provider.StateRunning, nil
	}

	return provider.StateStopped, nil
}

// GetInstanceInfo returns detailed information about a jail
func (p *JailProvider) GetInstanceInfo(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceInfo, error) {
	jailName := handle.ID

	// Load configuration
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	jailConfig, err := p.loadJailConfig(configPath)
	if err != nil {
		return provider.InstanceInfo{}, provider.ErrInstanceNotFound
	}

	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return provider.InstanceInfo{}, err
	}

	info := provider.InstanceInfo{
		Handle: handle,
		State:  state,
		Spec:   jailConfig.Spec,
	}

	// Get runtime info if running
	if state == provider.StateRunning {
		jid, err := p.getJailID(ctx, jailName)
		if err == nil {
			info.PID = jid
		}

		// Get IP addresses
		ips, err := p.getJailIPs(ctx, jailName)
		if err == nil {
			info.IPAddresses = ips
		}
	}

	return info, nil
}

// ListInstances lists all jails
func (p *JailProvider) ListInstances(ctx context.Context, filter provider.InstanceFilter) ([]provider.InstanceHandle, error) {
	// List all configuration files
	pattern := filepath.Join(p.stateDir, "*.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to list configuration files: %w", err)
	}

	handles := make([]provider.InstanceHandle, 0, len(files))
	for _, file := range files {
		// Extract jail name from filename
		jailName := strings.TrimSuffix(filepath.Base(file), ".json")

		handle := provider.InstanceHandle{
			ID:       jailName,
			Provider: "jail",
			Metadata: map[string]interface{}{
				"zfs_dataset": fmt.Sprintf("%s/%s", p.zfsParent, jailName),
			},
		}

		// Apply filter if specified
		if len(filter.States) > 0 {
			state, err := p.GetInstanceState(ctx, handle)
			if err != nil {
				continue
			}

			match := false
			for _, filterState := range filter.States {
				if state == filterState {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}

		handles = append(handles, handle)
	}

	return handles, nil
}

// SetInstanceResources updates resource limits for a jail
func (p *JailProvider) SetInstanceResources(ctx context.Context, handle provider.InstanceHandle, resources provider.ResourceSpec) error {
	jailName := handle.ID
	// The name becomes a path under stateDir and, further down, an rctl rule.
	if err := validation.ValidateInstanceName(jailName); err != nil {
		return fmt.Errorf("invalid jail name %q: %w", jailName, err)
	}

	// Load configuration
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	jailConfig, err := p.loadJailConfig(configPath)
	if err != nil {
		return err
	}

	// Update resources
	jailConfig.Resources = resources

	// Save configuration
	if err := p.saveJailConfig(jailConfig, configPath); err != nil {
		return err
	}

	// Apply RCTL limits if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return err
	}

	if running {
		if err := p.applyRCTLLimits(ctx, jailName, resources); err != nil {
			return fmt.Errorf("failed to apply RCTL limits: %w", err)
		}
	}

	return nil
}

// GetInstanceMetrics returns resource usage metrics for a jail
// Uses FreeBSD rctl(8) for resource accounting and netstat for network stats
func (p *JailProvider) GetInstanceMetrics(ctx context.Context, handle provider.InstanceHandle) (provider.Metrics, error) {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return provider.Metrics{}, err
	}
	if !running {
		return provider.Metrics{
			Timestamp: time.Now(),
		}, nil
	}

	metrics := provider.Metrics{
		Timestamp: time.Now(),
	}

	// Get rctl usage stats
	rctlStats, err := p.getRCTLUsage(ctx, jailName)
	if err == nil {
		// Calculate CPU percentage from cputime/wallclock
		if wallclock, ok := rctlStats["wallclock"]; ok && wallclock > 0 {
			if cputime, ok := rctlStats["cputime"]; ok {
				// cputime is in seconds, wallclock is in seconds
				// Percentage = (cputime / wallclock) * 100
				metrics.CPUUsagePercent = (float64(cputime) / float64(wallclock)) * 100
				if metrics.CPUUsagePercent > 100 {
					metrics.CPUUsagePercent = 100 // Cap at 100% for multi-core
				}
			}
		}

		// Memory usage (memoryuse is resident memory in bytes)
		if memuse, ok := rctlStats["memoryuse"]; ok {
			metrics.MemoryUsedMB = memuse / (1024 * 1024)
		}
		// Add virtual memory as total if no limit set
		if vmemuse, ok := rctlStats["vmemoryuse"]; ok && vmemuse > 0 {
			metrics.MemoryTotalMB = vmemuse / (1024 * 1024)
		}

		// Disk I/O from rctl. readbps/writebps are rates (bytes per second), not
		// cumulative counters, so these fields carry a rate sample.
		// readbps and writebps are rates, not cumulative counters. Assigning
		// them to DiskReadBytes/DiskWriteBytes made a consumer's delta between
		// two samples meaningless, so they go to the rate fields instead.
		if readbps, ok := rctlStats["readbps"]; ok {
			metrics.DiskReadBytesPerSec = readbps
		}
		if writebps, ok := rctlStats["writebps"]; ok {
			metrics.DiskWriteBytesPerSec = writebps
		}
	}

	// Get network stats from epair interface (host side)
	epair := ""
	if ep, ok := handle.Metadata["vnet_epair"].(string); ok && ep != "" {
		epair = ep
	} else {
		// Try to load from config file
		configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
		if jailConfig, err := p.loadJailConfig(configPath); err == nil && jailConfig.VnetEpair != "" {
			epair = jailConfig.VnetEpair
		}
	}
	if epair != "" {
		rxBytes, txBytes := p.getInterfaceStats(ctx, epair)
		metrics.NetRxBytes = rxBytes
		metrics.NetTxBytes = txBytes
	}

	return metrics, nil
}

// getRCTLUsage retrieves resource usage from rctl for a jail
func (p *JailProvider) getRCTLUsage(ctx context.Context, jailName string) (map[string]int64, error) {
	output, err := p.cmd().Output(ctx, "rctl", "-u", fmt.Sprintf("jail:%s", jailName))
	if err != nil {
		return nil, err
	}

	stats := make(map[string]int64)
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valueStr := strings.TrimSpace(parts[1])
		value, err := strconv.ParseInt(valueStr, 10, 64)
		if err != nil {
			continue
		}
		stats[key] = value
	}
	return stats, nil
}

// getInterfaceStats retrieves network bytes from an interface using netstat
func (p *JailProvider) getInterfaceStats(ctx context.Context, ifname string) (rxBytes, txBytes int64) {
	// Use netstat -bin to get interface stats
	output, err := p.cmd().Output(ctx, "netstat", "-bin")
	if err != nil {
		return 0, 0
	}

	// Columns are located from the header, not counted. netstat leaves the
	// Address column empty for an interface that holds no address, which shifts
	// every later field left — fixed indexes then read Opkts as Ibytes.
	lines := strings.Split(string(output), "\n")
	if len(lines) == 0 {
		return 0, 0
	}
	header := strings.Fields(lines[0])
	ibytesCol, obytesCol := -1, -1
	for i, name := range header {
		switch name {
		case "Ibytes":
			ibytesCol = i
		case "Obytes":
			obytesCol = i
		}
	}
	if ibytesCol < 0 || obytesCol < 0 {
		return 0, 0
	}

	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) <= obytesCol || fields[0] != ifname {
			continue
		}
		// Only the link line carries the counters; the address lines repeat
		// them, and taking one of those would read a shifted row.
		if len(fields) > 2 && !strings.HasPrefix(fields[2], "<Link") {
			continue
		}
		if ibytes, err := strconv.ParseInt(fields[ibytesCol], 10, 64); err == nil {
			rxBytes = ibytes
		}
		if obytes, err := strconv.ParseInt(fields[obytesCol], 10, 64); err == nil {
			txBytes = obytes
		}
		break
	}
	return rxBytes, txBytes
}

// CheckInstanceHealth performs a health check on a jail instance
func (p *JailProvider) CheckInstanceHealth(ctx context.Context, handle provider.InstanceHandle) (*provider.InstanceHealth, error) {
	jailName := handle.ID
	// The name reaches filepath.Join and a zfs list below.
	if err := validation.ValidateInstanceName(jailName); err != nil {
		return nil, fmt.Errorf("invalid jail name %q: %w", jailName, err)
	}
	health := &provider.InstanceHealth{
		Timestamp: time.Now(),
		Checks:    make([]provider.HealthCheck, 0),
	}

	// Check 1: Jail exists and is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		health.Status = provider.HealthStatusUnknown
		health.Message = fmt.Sprintf("failed to check jail status: %v", err)
		health.Checks = append(health.Checks, provider.HealthCheck{
			Name:    "jail_running",
			Status:  provider.HealthStatusUnknown,
			Message: err.Error(),
		})
		return health, nil
	}

	if !running {
		health.Status = provider.HealthStatusUnhealthy
		health.Message = "jail is not running"
		health.Checks = append(health.Checks, provider.HealthCheck{
			Name:    "jail_running",
			Status:  provider.HealthStatusUnhealthy,
			Message: "jail is not running",
		})
		return health, nil
	}

	health.Checks = append(health.Checks, provider.HealthCheck{
		Name:    "jail_running",
		Status:  provider.HealthStatusHealthy,
		Message: "jail is running",
	})

	// Check 2: ZFS dataset exists
	// A dataset name is not a path: filepath.Join cleans it, so an empty parent
	// would yield a bare jail name. The rest of the provider builds it this way.
	zfsDataset := fmt.Sprintf("%s/%s", p.zfsParent, jailName)
	zfsCheck := provider.HealthCheck{Name: "zfs_dataset"}
	if err := p.cmd().Run(ctx, "zfs", "list", "-H", zfsDataset); err != nil {
		zfsCheck.Status = provider.HealthStatusUnhealthy
		zfsCheck.Message = "ZFS dataset not found"
	} else {
		zfsCheck.Status = provider.HealthStatusHealthy
		zfsCheck.Message = "ZFS dataset exists"
	}
	health.Checks = append(health.Checks, zfsCheck)

	// Check 3: Network interface (for VNET jails)
	netCheck := provider.HealthCheck{Name: "network"}
	epair := ""
	if ep, ok := handle.Metadata["vnet_epair"].(string); ok && ep != "" {
		epair = ep
	} else {
		configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
		if jailConfig, err := p.loadJailConfig(configPath); err == nil && jailConfig.VnetEpair != "" {
			epair = jailConfig.VnetEpair
		}
	}

	if epair != "" {
		// Check if epair interface exists
		if err := p.cmd().Run(ctx, "ifconfig", epair); err != nil {
			netCheck.Status = provider.HealthStatusDegraded
			netCheck.Message = fmt.Sprintf("VNET interface %s not found", epair)
		} else {
			netCheck.Status = provider.HealthStatusHealthy
			netCheck.Message = fmt.Sprintf("VNET interface %s is up", epair)
		}
	} else {
		netCheck.Status = provider.HealthStatusHealthy
		netCheck.Message = "non-VNET jail (shared network)"
	}
	health.Checks = append(health.Checks, netCheck)

	// Check 4: Process check - can we run a simple command inside the jail?
	procCheck := provider.HealthCheck{Name: "process_exec"}
	if err := p.cmd().Run(ctx, "jexec", jailName, "echo", "healthcheck"); err != nil {
		procCheck.Status = provider.HealthStatusDegraded
		procCheck.Message = "failed to execute command inside jail"
	} else {
		procCheck.Status = provider.HealthStatusHealthy
		procCheck.Message = "jail is responsive"
	}
	health.Checks = append(health.Checks, procCheck)

	// Determine overall status
	unhealthyCount := 0
	degradedCount := 0
	for _, check := range health.Checks {
		switch check.Status {
		case provider.HealthStatusUnhealthy:
			unhealthyCount++
		case provider.HealthStatusDegraded:
			degradedCount++
		}
	}

	switch {
	case unhealthyCount > 0:
		health.Status = provider.HealthStatusUnhealthy
		health.Message = fmt.Sprintf("%d unhealthy checks", unhealthyCount)
	case degradedCount > 0:
		health.Status = provider.HealthStatusDegraded
		health.Message = fmt.Sprintf("%d degraded checks", degradedCount)
	default:
		health.Status = provider.HealthStatusHealthy
		health.Message = "all checks passed"
	}

	return health, nil
}

var _ provider.InstanceAddressProvider = (*JailProvider)(nil)

// InstanceAddresses reports the addresses the jail's interfaces hold.
func (p *JailProvider) InstanceAddresses(ctx context.Context, handle provider.InstanceHandle) ([]net.IP, error) {
	return p.getJailIPs(ctx, handle.ID)
}
