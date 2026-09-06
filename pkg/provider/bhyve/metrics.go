package bhyve

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
)

// Metrics are collected from various FreeBSD sources:
//   - CPU usage: From the bhyve process via ps(1)
//   - Memory: Configured memory (bhyve doesn't support ballooning)
//   - Disk I/O: Not collected (reported as zero)
//   - Network I/O: From netstat(1) for tap devices

// GetInstanceMetrics returns resource usage metrics for a bhyve VM.
//
//   - CPU usage: Calculated from process CPU time via ps(1)
//   - Memory: Reports configured memory (bhyve doesn't balloon)
//   - Disk I/O: Not collected (reported as zero)
//   - Network I/O: From netstat(1) for tap interfaces
func (p *BhyveProvider) GetInstanceMetrics(ctx context.Context, handle provider.InstanceHandle) (provider.Metrics, error) {
	vmName := handle.ID
	metrics := provider.Metrics{
		Timestamp: time.Now(),
	}

	// Load VM configuration to get tap devices and other info
	vmDir := filepath.Join(p.dataDir, vmName)
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return metrics, fmt.Errorf("failed to load VM config: %w", err)
	}

	// Get memory from configuration (bhyve doesn't support ballooning)
	metrics.MemoryTotalMB = config.MemoryMB
	metrics.MemoryUsedMB = config.MemoryMB // bhyve allocates full memory

	// Get CPU usage from bhyve process
	cpuPercent, err := p.getCPUUsage(ctx, vmName)
	if err == nil {
		metrics.CPUUsagePercent = cpuPercent
	}

	// Get network I/O from tap devices
	rxBytes, txBytes, err := p.getNetworkIO(ctx, config.TapDevs)
	if err == nil {
		metrics.NetRxBytes = rxBytes
		metrics.NetTxBytes = txBytes
	}

	// Get disk I/O from disk devices
	readBytes, writeBytes, err := p.getDiskIO(ctx, config.DiskPaths)
	if err == nil {
		metrics.DiskReadBytes = readBytes
		metrics.DiskWriteBytes = writeBytes
	}

	return metrics, nil
}

// getCPUUsage returns the CPU usage percentage for a bhyve VM.
// Uses ps(1) to get CPU percentage from the bhyve process, matched by the exact
// PID recorded in vm.state (a substring match on the VM name would attribute the
// usage of a different VM whose name merely contains vmName).
func (p *BhyveProvider) getCPUUsage(ctx context.Context, vmName string) (float64, error) {
	state, err := p.loadVMState(filepath.Join(p.dataDir, vmName))
	if err != nil {
		return 0, fmt.Errorf("failed to load VM state: %w", err)
	}
	if state.PID <= 0 {
		return 0, fmt.Errorf("no PID recorded for VM %s", vmName)
	}
	pidStr := strconv.Itoa(state.PID)

	output, err := p.cmd().Output(ctx, "ps", "-axo", "pid,pcpu,command")
	if err != nil {
		return 0, fmt.Errorf("failed to run ps: %w", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != pidStr {
			continue
		}
		cpuPercent, err := strconv.ParseFloat(fields[1], 64)
		if err == nil {
			return cpuPercent, nil
		}
	}

	return 0, fmt.Errorf("bhyve process not found for VM %s (pid %s)", vmName, pidStr)
}

// getNetworkIO returns the total bytes received and transmitted on tap devices.
// Uses netstat(1) to get interface statistics.
func (p *BhyveProvider) getNetworkIO(ctx context.Context, tapDevs []string) (rxBytes, txBytes int64, err error) {
	if len(tapDevs) == 0 {
		return 0, 0, nil
	}

	for _, tap := range tapDevs {
		// netstat -ibI tap0 - get interface bytes
		output, err := p.cmd().Output(ctx, "netstat", "-ibI", tap)
		if err != nil {
			continue // Skip interfaces that don't exist
		}

		// Parse netstat output
		// Format: Name    Mtu Network       Address            Ipkts Ierrs Idrop    Ibytes    Opkts Oerrs    Obytes  Coll
		scanner := bufio.NewScanner(bytes.NewReader(output))
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			if lineNum == 1 {
				continue // Skip header
			}
			line := scanner.Text()
			fields := strings.Fields(line)
			if len(fields) >= 11 {
				// Ibytes is field 7 (0-indexed), Obytes is field 10
				if rx, err := strconv.ParseInt(fields[7], 10, 64); err == nil {
					rxBytes += rx
				}
				if tx, err := strconv.ParseInt(fields[10], 10, 64); err == nil {
					txBytes += tx
				}
				break // Only need the first data line
			}
		}
	}

	return rxBytes, txBytes, nil
}

// getDiskIO returns the total bytes read and written from disk devices.
//
// Per-disk I/O accounting is not yet implemented for bhyve: ZFS does not expose
// per-dataset byte counters without dtrace, and gstat(8)/iostat(8) run as
// continuous samplers (gstat without -c never exits) rather than one-shot
// probes, so they cannot be scraped synchronously without blocking the metrics
// path. Until a proper source is wired in this returns zero.
func (p *BhyveProvider) getDiskIO(ctx context.Context, diskPaths []string) (readBytes, writeBytes int64, err error) {
	return 0, 0, nil
}

// MetricsInfo provides detailed metrics information for a VM.
// This extends the basic Metrics with additional bhyve-specific data.
type MetricsInfo struct {
	provider.Metrics

	// Process information
	PID          int     `json:"pid"`
	ProcessState string  `json:"process_state"` // R=running, S=sleeping, etc.
	CPUTime      float64 `json:"cpu_time"`      // Total CPU time in seconds

	// Per-interface network stats
	Interfaces []InterfaceMetrics `json:"interfaces,omitempty"`

	// Per-disk stats
	Disks []DiskMetrics `json:"disks,omitempty"`
}

// InterfaceMetrics provides per-interface network statistics.
type InterfaceMetrics struct {
	Name      string `json:"name"`
	RxBytes   int64  `json:"rx_bytes"`
	TxBytes   int64  `json:"tx_bytes"`
	RxPackets int64  `json:"rx_packets"`
	TxPackets int64  `json:"tx_packets"`
	RxErrors  int64  `json:"rx_errors"`
	TxErrors  int64  `json:"tx_errors"`
}

// DiskMetrics provides per-disk I/O statistics.
type DiskMetrics struct {
	Path       string  `json:"path"`
	ReadBytes  int64   `json:"read_bytes"`
	WriteBytes int64   `json:"write_bytes"`
	ReadOps    int64   `json:"read_ops"`
	WriteOps   int64   `json:"write_ops"`
	BusyPct    float64 `json:"busy_pct"`
}

// GetDetailedMetrics returns extended metrics including per-device statistics.
// This is useful for monitoring and debugging.
func (p *BhyveProvider) GetDetailedMetrics(ctx context.Context, handle provider.InstanceHandle) (*MetricsInfo, error) {
	vmName := handle.ID

	// Get basic metrics first
	basicMetrics, err := p.GetInstanceMetrics(ctx, handle)
	if err != nil {
		return nil, err
	}

	info := &MetricsInfo{
		Metrics: basicMetrics,
	}

	// Load VM config
	vmDir := filepath.Join(p.dataDir, vmName)
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return info, nil // Return basic metrics if config fails
	}

	// Get detailed process info
	pid, state, cpuTime, err := p.getProcessInfo(ctx, vmName)
	if err == nil {
		info.PID = pid
		info.ProcessState = state
		info.CPUTime = cpuTime
	}

	// Get per-interface metrics
	for _, tap := range config.TapDevs {
		ifMetrics, err := p.getInterfaceMetrics(ctx, tap)
		if err == nil {
			info.Interfaces = append(info.Interfaces, ifMetrics)
		}
	}

	// Get per-disk metrics
	for _, disk := range config.DiskPaths {
		diskMetrics, err := p.getDiskMetrics(ctx, disk)
		if err == nil {
			info.Disks = append(info.Disks, diskMetrics)
		}
	}

	return info, nil
}

// getProcessInfo returns detailed process information for a bhyve VM, matched by
// the exact PID recorded in vm.state rather than a substring of the VM name.
func (p *BhyveProvider) getProcessInfo(ctx context.Context, vmName string) (pid int, state string, cpuTime float64, err error) {
	vmState, err := p.loadVMState(filepath.Join(p.dataDir, vmName))
	if err != nil {
		return 0, "", 0, fmt.Errorf("failed to load VM state: %w", err)
	}
	if vmState.PID <= 0 {
		return 0, "", 0, fmt.Errorf("no PID recorded for VM %s", vmName)
	}
	pidStr := strconv.Itoa(vmState.PID)

	output, err := p.cmd().Output(ctx, "ps", "-axo", "pid,state,time,command")
	if err != nil {
		return 0, "", 0, fmt.Errorf("failed to run ps: %w", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || fields[0] != pidStr {
			continue
		}
		pid, _ = strconv.Atoi(fields[0])
		state = fields[1]
		// Parse time format: HH:MM:SS or MM:SS
		cpuTime = parseTimeToSeconds(fields[2])
		return pid, state, cpuTime, nil
	}

	return 0, "", 0, fmt.Errorf("process not found for VM %s (pid %s)", vmName, pidStr)
}

// parseTimeToSeconds converts ps time format to seconds.
func parseTimeToSeconds(timeStr string) float64 {
	parts := strings.Split(timeStr, ":")
	if len(parts) == 2 {
		// MM:SS
		minVal, _ := strconv.ParseFloat(parts[0], 64)
		sec, _ := strconv.ParseFloat(parts[1], 64)
		return minVal*60 + sec
	} else if len(parts) == 3 {
		// HH:MM:SS
		hour, _ := strconv.ParseFloat(parts[0], 64)
		minVal, _ := strconv.ParseFloat(parts[1], 64)
		sec, _ := strconv.ParseFloat(parts[2], 64)
		return hour*3600 + minVal*60 + sec
	}
	return 0
}

// getInterfaceMetrics returns detailed metrics for a network interface.
func (p *BhyveProvider) getInterfaceMetrics(ctx context.Context, ifName string) (InterfaceMetrics, error) {
	metrics := InterfaceMetrics{Name: ifName}

	output, err := p.cmd().Output(ctx, "netstat", "-ibI", ifName)
	if err != nil {
		return metrics, err
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum == 1 {
			continue // Skip header
		}
		line := scanner.Text()
		fields := strings.Fields(line)
		// Name    Mtu Network       Address            Ipkts Ierrs Idrop    Ibytes    Opkts Oerrs    Obytes  Coll
		if len(fields) >= 11 {
			metrics.RxPackets, _ = strconv.ParseInt(fields[4], 10, 64)
			metrics.RxErrors, _ = strconv.ParseInt(fields[5], 10, 64)
			metrics.RxBytes, _ = strconv.ParseInt(fields[7], 10, 64)
			metrics.TxPackets, _ = strconv.ParseInt(fields[8], 10, 64)
			metrics.TxErrors, _ = strconv.ParseInt(fields[9], 10, 64)
			metrics.TxBytes, _ = strconv.ParseInt(fields[10], 10, 64)
			break
		}
	}

	return metrics, nil
}

// getDiskMetrics returns detailed metrics for a disk device.
//
// Like getDiskIO, per-disk byte/op counters are not yet implemented for bhyve,
// so only the disk path is populated.
func (p *BhyveProvider) getDiskMetrics(ctx context.Context, diskPath string) (DiskMetrics, error) {
	return DiskMetrics{Path: diskPath}, nil
}
