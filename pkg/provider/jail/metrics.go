package jail

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
)

// Metrics provided:
//   - CPU usage (pcpu, cputime)
//   - Memory usage (memoryuse, vmemoryuse)
//   - Process counts (maxproc, nthr)
//   - I/O stats (readbps, writebps, readiops, writeiops)
//   - ZFS usage (used, available, quota)
//
// These metrics can be scraped by Prometheus from the /metrics/prometheus endpoint.

// JailMetrics contains resource metrics for a single jail
type JailMetrics struct {
	// Name is the jail name
	Name string `json:"name"`

	// State is the jail state (running, stopped, etc.)
	State string `json:"state"`

	// CPU metrics
	CPUPercent   float64 `json:"cpu_percent"`    // Current CPU usage percentage
	CPUTimeUsed  int64   `json:"cpu_time_used"`  // Total CPU time used in seconds
	CPUTimeLimit int64   `json:"cpu_time_limit"` // CPU time limit (0 = unlimited)

	// Memory metrics
	MemoryUsed   int64 `json:"memory_used"`   // Physical memory used in bytes
	MemoryLimit  int64 `json:"memory_limit"`  // Memory limit in bytes (0 = unlimited)
	VMemoryUsed  int64 `json:"vmemory_used"`  // Virtual memory used in bytes
	VMemoryLimit int64 `json:"vmemory_limit"` // Virtual memory limit (0 = unlimited)
	MemoryLocked int64 `json:"memory_locked"` // Locked memory in bytes
	SwapUsed     int64 `json:"swap_used"`     // Swap usage in bytes

	// Process metrics
	ProcessCount int `json:"process_count"` // Current number of processes
	ProcessLimit int `json:"process_limit"` // Maximum processes (0 = unlimited)
	ThreadCount  int `json:"thread_count"`  // Current number of threads
	ThreadLimit  int `json:"thread_limit"`  // Maximum threads (0 = unlimited)

	// I/O metrics
	ReadBPS    int64 `json:"read_bps"`    // Current read bytes per second
	WriteBPS   int64 `json:"write_bps"`   // Current write bytes per second
	ReadIOPS   int64 `json:"read_iops"`   // Current read operations per second
	WriteIOPS  int64 `json:"write_iops"`  // Current write operations per second
	OpenFiles  int   `json:"open_files"`  // Current open file count
	FilesLimit int   `json:"files_limit"` // Maximum open files (0 = unlimited)

	// Storage metrics (ZFS)
	DiskUsed      int64  `json:"disk_used"`      // Disk space used in bytes
	DiskAvailable int64  `json:"disk_available"` // Disk space available in bytes
	DiskQuota     int64  `json:"disk_quota"`     // Disk quota in bytes (0 = unlimited)
	ZFSDataset    string `json:"zfs_dataset"`    // ZFS dataset name

	// Network metrics
	NetworkRxBytes   int64 `json:"network_rx_bytes"`   // Bytes received
	NetworkTxBytes   int64 `json:"network_tx_bytes"`   // Bytes transmitted
	NetworkRxPackets int64 `json:"network_rx_packets"` // Packets received
	NetworkTxPackets int64 `json:"network_tx_packets"` // Packets transmitted

	// Timestamps
	CollectedAt time.Time `json:"collected_at"` // When metrics were collected
	Uptime      int64     `json:"uptime"`       // Jail uptime in seconds
}

// GetJailMetrics returns metrics for a specific jail
func (p *JailProvider) GetJailMetrics(ctx context.Context, name string) (*JailMetrics, error) {
	// Check if jail is running
	running, err := p.isJailRunning(ctx, name)
	if err != nil {
		return nil, err
	}

	metrics := &JailMetrics{
		Name:        name,
		CollectedAt: time.Now(),
	}

	if !running {
		metrics.State = "stopped"
		// Get storage metrics even for stopped jails
		p.collectStorageMetrics(ctx, name, metrics)
		return metrics, nil
	}

	metrics.State = "running"

	// Collect all metrics in parallel
	var wg sync.WaitGroup
	errChan := make(chan error, 4)

	wg.Add(4)
	go func() {
		defer wg.Done()
		if err := p.collectRCTLMetrics(ctx, name, metrics); err != nil {
			errChan <- err
		}
	}()
	go func() {
		defer wg.Done()
		if err := p.collectProcessMetrics(ctx, name, metrics); err != nil {
			errChan <- err
		}
	}()
	go func() {
		defer wg.Done()
		p.collectStorageMetrics(ctx, name, metrics)
	}()
	go func() {
		defer wg.Done()
		p.collectNetworkMetrics(ctx, name, metrics)
	}()

	wg.Wait()
	close(errChan)

	// Check for errors (log but don't fail)
	for err := range errChan {
		if err != nil {
			p.logWarn(ctx, "metrics collection error", "jail", name, logging.FieldError, err)
		}
	}

	metrics.Uptime = p.getJailUptime(ctx, name)

	return metrics, nil
}

// GetAllMetrics returns metrics for all jails
func (p *JailProvider) GetAllMetrics(ctx context.Context) ([]JailMetrics, error) {
	instances, err := p.ListInstances(ctx, provider.InstanceFilter{})
	if err != nil {
		return nil, fmt.Errorf("failed to list instances: %w", err)
	}

	var metrics []JailMetrics
	for _, inst := range instances {
		m, err := p.GetJailMetrics(ctx, inst.ID)
		if err != nil {
			// Log but continue with other jails
			p.logWarn(ctx, "failed to get metrics for jail", "jail", inst.ID, logging.FieldError, err)
			continue
		}
		metrics = append(metrics, *m)
	}

	return metrics, nil
}

// collectRCTLMetrics collects RCTL (resource control) metrics
func (p *JailProvider) collectRCTLMetrics(ctx context.Context, name string, metrics *JailMetrics) error {
	// Check if RCTL is enabled
	output, err := p.cmd().Output(ctx, "sysctl", "-n", "kern.racct.enable")
	if err != nil || strings.TrimSpace(string(output)) != "1" {
		return nil // RCTL not enabled, skip
	}

	// Get RCTL usage: rctl -u jail:<name>
	output, err = p.cmd().Output(ctx, "rctl", "-u", fmt.Sprintf("jail:%s", name))
	if err != nil {
		return nil // No rules or jail not found
	}

	// Parse output: resource=value
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		resource := parts[0]
		value, _ := strconv.ParseInt(parts[1], 10, 64)

		switch resource {
		case "pcpu":
			metrics.CPUPercent = float64(value)
		case "cputime":
			metrics.CPUTimeUsed = value
		case "memoryuse":
			metrics.MemoryUsed = value
		case "vmemoryuse":
			metrics.VMemoryUsed = value
		case "memorylocked":
			metrics.MemoryLocked = value
		case "swapuse":
			metrics.SwapUsed = value
		case "nthr":
			metrics.ThreadCount = int(value)
		case "openfiles":
			metrics.OpenFiles = int(value)
		case "readbps":
			metrics.ReadBPS = value
		case "writebps":
			metrics.WriteBPS = value
		case "readiops":
			metrics.ReadIOPS = value
		case "writeiops":
			metrics.WriteIOPS = value
		}
	}

	// Get limits from rules
	p.collectRCTLLimits(ctx, name, metrics)

	return nil
}

// collectRCTLLimits collects RCTL limits
func (p *JailProvider) collectRCTLLimits(ctx context.Context, name string, metrics *JailMetrics) {
	output, err := p.cmd().Output(ctx, "rctl", "-l", fmt.Sprintf("jail:%s", name))
	if err != nil {
		return
	}

	// Parse rules: jail:name:resource:action=amount
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Split(line, ":")
		if len(parts) < 4 {
			continue
		}

		resource := parts[2]
		actionAmount := strings.SplitN(parts[3], "=", 2)
		if len(actionAmount) != 2 {
			continue
		}

		limit, _ := strconv.ParseInt(actionAmount[1], 10, 64)

		switch resource {
		case "cputime":
			metrics.CPUTimeLimit = limit
		case "memoryuse":
			metrics.MemoryLimit = limit
		case "vmemoryuse":
			metrics.VMemoryLimit = limit
		case "maxproc":
			metrics.ProcessLimit = int(limit)
		case "nthr":
			metrics.ThreadLimit = int(limit)
		case "openfiles":
			metrics.FilesLimit = int(limit)
		}
	}
}

// collectProcessMetrics collects process-related metrics
func (p *JailProvider) collectProcessMetrics(ctx context.Context, name string, metrics *JailMetrics) error {
	// Count processes using jexec
	output, err := p.cmd().Output(ctx, "jexec", name, "ps", "-ax", "-o", "pid=")
	if err != nil {
		return nil
	}

	count := 0
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	metrics.ProcessCount = count

	return nil
}

// collectStorageMetrics collects ZFS storage metrics
func (p *JailProvider) collectStorageMetrics(ctx context.Context, name string, metrics *JailMetrics) {
	dataset := fmt.Sprintf("%s/%s", p.zfsParent, name)
	metrics.ZFSDataset = dataset

	if output, err := p.cmd().Output(ctx, "zfs", "get", "-Hp", "-o", "value", "used", dataset); err == nil {
		metrics.DiskUsed, _ = strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	}

	if output, err := p.cmd().Output(ctx, "zfs", "get", "-Hp", "-o", "value", "available", dataset); err == nil {
		metrics.DiskAvailable, _ = strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	}

	if output, err := p.cmd().Output(ctx, "zfs", "get", "-Hp", "-o", "value", "quota", dataset); err == nil {
		val := strings.TrimSpace(string(output))
		if val != "none" && val != "0" {
			metrics.DiskQuota, _ = strconv.ParseInt(val, 10, 64)
		}
	}
}

// collectNetworkMetrics collects network interface metrics
func (p *JailProvider) collectNetworkMetrics(ctx context.Context, name string, metrics *JailMetrics) {
	// Get network interface stats from netstat
	output, err := p.cmd().Output(ctx, "jexec", name, "netstat", "-I", "epair", "-b", "-n")
	if err != nil {
		// Try vnet0 interface
		output, err = p.cmd().Output(ctx, "jexec", name, "netstat", "-I", "vnet0", "-b", "-n")
		if err != nil {
			return
		}
	}

	// Parse netstat output (skip header)
	lines := strings.Split(string(output), "\n")
	for i, line := range lines {
		if i == 0 { // Skip header
			continue
		}
		fields := strings.Fields(line)
		// FreeBSD "netstat -b" columns:
		// Name Mtu Network Address Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll
		// Require the full set so we never index past the slice (the previous
		// >=7 guard could panic on fields[7]/fields[9]).
		if len(fields) < 11 {
			continue
		}
		if ipkts, err := strconv.ParseInt(fields[4], 10, 64); err == nil {
			metrics.NetworkRxPackets += ipkts
		}
		if ibytes, err := strconv.ParseInt(fields[7], 10, 64); err == nil {
			metrics.NetworkRxBytes += ibytes
		}
		if opkts, err := strconv.ParseInt(fields[8], 10, 64); err == nil {
			metrics.NetworkTxPackets += opkts
		}
		if obytes, err := strconv.ParseInt(fields[10], 10, 64); err == nil {
			metrics.NetworkTxBytes += obytes
		}
	}
}

// getJailUptime returns the jail uptime in seconds
func (p *JailProvider) getJailUptime(ctx context.Context, name string) int64 {
	// Use jls to get creation time
	output, err := p.cmd().Output(ctx, "jls", "-j", name, "-n", "created")
	if err != nil {
		return 0
	}

	// Parse created=<timestamp>
	line := strings.TrimSpace(string(output))
	if !strings.HasPrefix(line, "created=") {
		return 0
	}

	timestamp, err := strconv.ParseInt(strings.TrimPrefix(line, "created="), 10, 64)
	if err != nil {
		return 0
	}

	return time.Now().Unix() - timestamp
}

// FormatPrometheusMetrics formats jail metrics in Prometheus exposition format
func FormatPrometheusMetrics(allMetrics []JailMetrics) string {
	var sb strings.Builder

	// Instance info metric
	sb.WriteString("# HELP hospitus_jail_info Information about jails\n")
	sb.WriteString("# TYPE hospitus_jail_info gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		running := 0
		if m.State == "running" {
			running = 1
		}
		fmt.Fprintf(&sb, "hospitus_jail_info{name=%q,state=%q,dataset=%q} %d\n",
			m.Name, m.State, m.ZFSDataset, running)
	}

	// Uptime
	sb.WriteString("# HELP hospitus_jail_uptime_seconds Jail uptime in seconds\n")
	sb.WriteString("# TYPE hospitus_jail_uptime_seconds gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_uptime_seconds{name=%q} %d\n", m.Name, m.Uptime)
		}
	}

	// CPU metrics
	sb.WriteString("# HELP hospitus_jail_cpu_percent Current CPU usage percentage\n")
	sb.WriteString("# TYPE hospitus_jail_cpu_percent gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_cpu_percent{name=%q} %.2f\n", m.Name, m.CPUPercent)
		}
	}

	sb.WriteString("# HELP hospitus_jail_cpu_seconds_total Total CPU time used in seconds\n")
	sb.WriteString("# TYPE hospitus_jail_cpu_seconds_total counter\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_cpu_seconds_total{name=%q} %d\n", m.Name, m.CPUTimeUsed)
		}
	}

	// Memory metrics
	sb.WriteString("# HELP hospitus_jail_memory_bytes Current memory usage in bytes\n")
	sb.WriteString("# TYPE hospitus_jail_memory_bytes gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_memory_bytes{name=%q} %d\n", m.Name, m.MemoryUsed)
		}
	}

	sb.WriteString("# HELP hospitus_jail_memory_limit_bytes Memory limit in bytes\n")
	sb.WriteString("# TYPE hospitus_jail_memory_limit_bytes gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.MemoryLimit > 0 {
			fmt.Fprintf(&sb, "hospitus_jail_memory_limit_bytes{name=%q} %d\n", m.Name, m.MemoryLimit)
		}
	}

	sb.WriteString("# HELP hospitus_jail_vmemory_bytes Virtual memory usage in bytes\n")
	sb.WriteString("# TYPE hospitus_jail_vmemory_bytes gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_vmemory_bytes{name=%q} %d\n", m.Name, m.VMemoryUsed)
		}
	}

	sb.WriteString("# HELP hospitus_jail_swap_bytes Swap usage in bytes\n")
	sb.WriteString("# TYPE hospitus_jail_swap_bytes gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" && m.SwapUsed > 0 {
			fmt.Fprintf(&sb, "hospitus_jail_swap_bytes{name=%q} %d\n", m.Name, m.SwapUsed)
		}
	}

	// Process metrics
	sb.WriteString("# HELP hospitus_jail_processes Current number of processes\n")
	sb.WriteString("# TYPE hospitus_jail_processes gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_processes{name=%q} %d\n", m.Name, m.ProcessCount)
		}
	}

	sb.WriteString("# HELP hospitus_jail_processes_limit Maximum number of processes\n")
	sb.WriteString("# TYPE hospitus_jail_processes_limit gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.ProcessLimit > 0 {
			fmt.Fprintf(&sb, "hospitus_jail_processes_limit{name=%q} %d\n", m.Name, m.ProcessLimit)
		}
	}

	sb.WriteString("# HELP hospitus_jail_threads Current number of threads\n")
	sb.WriteString("# TYPE hospitus_jail_threads gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_threads{name=%q} %d\n", m.Name, m.ThreadCount)
		}
	}

	// I/O metrics
	sb.WriteString("# HELP hospitus_jail_io_read_bytes_per_second Read bytes per second\n")
	sb.WriteString("# TYPE hospitus_jail_io_read_bytes_per_second gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_io_read_bytes_per_second{name=%q} %d\n", m.Name, m.ReadBPS)
		}
	}

	sb.WriteString("# HELP hospitus_jail_io_write_bytes_per_second Write bytes per second\n")
	sb.WriteString("# TYPE hospitus_jail_io_write_bytes_per_second gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_io_write_bytes_per_second{name=%q} %d\n", m.Name, m.WriteBPS)
		}
	}

	sb.WriteString("# HELP hospitus_jail_io_read_ops_per_second Read operations per second\n")
	sb.WriteString("# TYPE hospitus_jail_io_read_ops_per_second gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_io_read_ops_per_second{name=%q} %d\n", m.Name, m.ReadIOPS)
		}
	}

	sb.WriteString("# HELP hospitus_jail_io_write_ops_per_second Write operations per second\n")
	sb.WriteString("# TYPE hospitus_jail_io_write_ops_per_second gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_io_write_ops_per_second{name=%q} %d\n", m.Name, m.WriteIOPS)
		}
	}

	sb.WriteString("# HELP hospitus_jail_open_files Current number of open files\n")
	sb.WriteString("# TYPE hospitus_jail_open_files gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_open_files{name=%q} %d\n", m.Name, m.OpenFiles)
		}
	}

	// Storage metrics
	sb.WriteString("# HELP hospitus_jail_disk_used_bytes Disk space used in bytes\n")
	sb.WriteString("# TYPE hospitus_jail_disk_used_bytes gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		fmt.Fprintf(&sb, "hospitus_jail_disk_used_bytes{name=%q} %d\n", m.Name, m.DiskUsed)
	}

	sb.WriteString("# HELP hospitus_jail_disk_available_bytes Disk space available in bytes\n")
	sb.WriteString("# TYPE hospitus_jail_disk_available_bytes gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		fmt.Fprintf(&sb, "hospitus_jail_disk_available_bytes{name=%q} %d\n", m.Name, m.DiskAvailable)
	}

	sb.WriteString("# HELP hospitus_jail_disk_quota_bytes Disk quota in bytes\n")
	sb.WriteString("# TYPE hospitus_jail_disk_quota_bytes gauge\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.DiskQuota > 0 {
			fmt.Fprintf(&sb, "hospitus_jail_disk_quota_bytes{name=%q} %d\n", m.Name, m.DiskQuota)
		}
	}

	// Network metrics
	sb.WriteString("# HELP hospitus_jail_network_receive_bytes_total Total bytes received\n")
	sb.WriteString("# TYPE hospitus_jail_network_receive_bytes_total counter\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_network_receive_bytes_total{name=%q} %d\n", m.Name, m.NetworkRxBytes)
		}
	}

	sb.WriteString("# HELP hospitus_jail_network_transmit_bytes_total Total bytes transmitted\n")
	sb.WriteString("# TYPE hospitus_jail_network_transmit_bytes_total counter\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_network_transmit_bytes_total{name=%q} %d\n", m.Name, m.NetworkTxBytes)
		}
	}

	sb.WriteString("# HELP hospitus_jail_network_receive_packets_total Total packets received\n")
	sb.WriteString("# TYPE hospitus_jail_network_receive_packets_total counter\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_network_receive_packets_total{name=%q} %d\n", m.Name, m.NetworkRxPackets)
		}
	}

	sb.WriteString("# HELP hospitus_jail_network_transmit_packets_total Total packets transmitted\n")
	sb.WriteString("# TYPE hospitus_jail_network_transmit_packets_total counter\n")
	for i := range allMetrics {
		m := &allMetrics[i]
		if m.State == "running" {
			fmt.Fprintf(&sb, "hospitus_jail_network_transmit_packets_total{name=%q} %d\n", m.Name, m.NetworkTxPackets)
		}
	}

	return sb.String()
}

// GetStats returns resource statistics for an instance.
func (p *JailProvider) GetStats(ctx context.Context, handle provider.InstanceHandle) (map[string]interface{}, error) {
	metrics, err := p.GetJailMetrics(ctx, handle.ID)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"name":             metrics.Name,
		"state":            metrics.State,
		"uptime":           metrics.Uptime,
		"cpu_percent":      metrics.CPUPercent,
		"cpu_time_used":    metrics.CPUTimeUsed,
		"memory_used":      metrics.MemoryUsed,
		"memory_limit":     metrics.MemoryLimit,
		"vmemory_used":     metrics.VMemoryUsed,
		"swap_used":        metrics.SwapUsed,
		"process_count":    metrics.ProcessCount,
		"thread_count":     metrics.ThreadCount,
		"open_files":       metrics.OpenFiles,
		"read_bps":         metrics.ReadBPS,
		"write_bps":        metrics.WriteBPS,
		"disk_used":        metrics.DiskUsed,
		"disk_available":   metrics.DiskAvailable,
		"disk_quota":       metrics.DiskQuota,
		"network_rx_bytes": metrics.NetworkRxBytes,
		"network_tx_bytes": metrics.NetworkTxBytes,
		"collected_at":     metrics.CollectedAt,
	}, nil
}
