package qemu

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
)

// Metrics can be collected via:
//   - Process stats (ps) for basic CPU/memory usage
//   - QMP (QEMU Machine Protocol) for detailed VM stats

// GetInstanceMetrics returns resource usage metrics for a QEMU VM.
//
// This implementation gathers metrics from process stats.
// For more detailed metrics, QMP integration would be needed.
func (p *QEMUProvider) GetInstanceMetrics(ctx context.Context, handle provider.InstanceHandle) (provider.Metrics, error) {
	vmName := handle.ID
	metrics := provider.Metrics{
		Timestamp: time.Now(),
	}

	// Load VM configuration for memory info
	configPath := filepath.Join(p.stateDir, vmName+".json")
	config, err := p.loadVMConfig(configPath)
	if err == nil && config != nil {
		// Get configured memory. QEMU args are stored as separate slice elements
		// ("-m", "2048"), so read the value that follows the "-m" flag rather than
		// splitting a single element (which never had two fields, leaving memory 0).
		for i, arg := range config.Args {
			if arg != "-m" || i+1 >= len(config.Args) {
				continue
			}
			mem := config.Args[i+1]
			switch {
			case strings.HasSuffix(mem, "G"):
				if val, err := strconv.ParseInt(strings.TrimSuffix(mem, "G"), 10, 64); err == nil {
					metrics.MemoryTotalMB = val * 1024
				}
			case strings.HasSuffix(mem, "M"):
				if val, err := strconv.ParseInt(strings.TrimSuffix(mem, "M"), 10, 64); err == nil {
					metrics.MemoryTotalMB = val
				}
			default:
				if val, err := strconv.ParseInt(mem, 10, 64); err == nil {
					metrics.MemoryTotalMB = val
				}
			}
			break
		}
	}

	// Get CPU usage from QEMU process
	cpuPercent, err := p.getProcessCPU(ctx, vmName)
	if err == nil {
		metrics.CPUUsagePercent = cpuPercent
	}

	// Get actual memory usage from process RSS
	memUsedMB, err := p.getProcessMemory(ctx, vmName)
	if err == nil && memUsedMB > 0 {
		metrics.MemoryUsedMB = memUsedMB
	} else {
		// Fallback to total memory if we can't get RSS
		metrics.MemoryUsedMB = metrics.MemoryTotalMB
	}

	// Get disk I/O metrics via QMP (best-effort)
	qmpSocket := filepath.Join(p.dataDir, vmName, "qmp.sock")
	if diskRead, diskWrite, err := p.getQMPDiskStats(qmpSocket); err == nil {
		metrics.DiskReadBytes = diskRead
		metrics.DiskWriteBytes = diskWrite
	}

	// Get network I/O metrics from process stats (best-effort)
	if netRx, netTx, err := p.getNetStats(ctx, vmName); err == nil {
		metrics.NetRxBytes = netRx
		metrics.NetTxBytes = netTx
	}

	return metrics, nil
}

// getQMPDiskStats queries QEMU via QMP for aggregate disk I/O bytes.
func (p *QEMUProvider) getQMPDiskStats(qmpSocket string) (readBytes, writeBytes int64, err error) {
	client, err := NewQMPClient(qmpSocket)
	if err != nil {
		return 0, 0, err
	}
	if err := client.Connect(); err != nil {
		return 0, 0, err
	}
	defer client.Close()

	stats, err := client.QueryBlockStats()
	if err != nil {
		return 0, 0, err
	}

	for _, s := range stats {
		readBytes += s.Stats.ReadBytes
		writeBytes += s.Stats.WriteBytes
	}

	return readBytes, writeBytes, nil
}

// getNetStats gets network I/O stats for the VM's tap interface(s).
//
// The previous implementation read /proc/<pid>/net/dev, but QEMU shares the host
// network namespace, so that summed every host interface — reporting host
// traffic, not the VM's. Instead we read the counters of the tap interface(s)
// actually attached to the VM (parsed from the "-netdev tap,...,ifname=" args)
// via /sys/class/net/<ifname>/statistics. Only available on Linux; VMs using
// user-mode NAT have no host-visible interface and report an error.
func (p *QEMUProvider) getNetStats(ctx context.Context, vmName string) (rxBytes, txBytes int64, err error) {
	if runtime.GOOS != "linux" {
		return 0, 0, fmt.Errorf("net stats not available on %s", runtime.GOOS)
	}

	configPath := filepath.Join(p.stateDir, vmName+".json")
	config, err := p.loadVMConfig(configPath)
	if err != nil || config == nil {
		return 0, 0, fmt.Errorf("failed to load VM config for net stats: %w", err)
	}

	ifaces := tapInterfacesFromArgs(config.Args)
	if len(ifaces) == 0 {
		return 0, 0, fmt.Errorf("no tap interface attached to VM %s (user-mode networking has no host-visible interface)", vmName)
	}

	for _, ifname := range ifaces {
		if rx, rerr := readIfaceStat(ifname, "rx_bytes"); rerr == nil {
			rxBytes += rx
		}
		if tx, terr := readIfaceStat(ifname, "tx_bytes"); terr == nil {
			txBytes += tx
		}
	}

	return rxBytes, txBytes, nil
}

// tapInterfacesFromArgs extracts the ifname= values of tap netdevs from QEMU
// arguments (e.g. "-netdev tap,id=net0,ifname=tap0,script=no").
func tapInterfacesFromArgs(args []string) []string {
	var ifaces []string
	for i, arg := range args {
		if arg != "-netdev" || i+1 >= len(args) {
			continue
		}
		value := args[i+1]
		if !strings.HasPrefix(value, "tap,") && value != "tap" {
			continue
		}
		for _, field := range strings.Split(value, ",") {
			if name, ok := strings.CutPrefix(field, "ifname="); ok && name != "" {
				ifaces = append(ifaces, name)
			}
		}
	}
	return ifaces
}

// sysClassNet is the sysfs root for per-interface network counters.
const sysClassNet = "/sys/class/net"

// readIfaceStat reads a single counter (e.g. "rx_bytes") for a network interface
// from /sys/class/net/<ifname>/statistics/<stat>.
func readIfaceStat(ifname, stat string) (int64, error) {
	path := filepath.Join(sysClassNet, ifname, "statistics", stat)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
}

// getProcessCPU returns CPU usage percentage for the QEMU process, matched by
// the exact PID from the VM's PID file. A substring match on the VM name would
// attribute CPU from a different VM whose name merely contains vmName.
func (p *QEMUProvider) getProcessCPU(ctx context.Context, vmName string) (float64, error) {
	pidFile := filepath.Join(p.stateDir, fmt.Sprintf("%s.pid", vmName))
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, fmt.Errorf("failed to read PID file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return 0, fmt.Errorf("invalid PID: %w", err)
	}

	output, err := p.cmd().Output(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "pcpu=")
	if err != nil {
		return 0, err
	}

	cpuPercent, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil {
		return 0, err
	}
	return cpuPercent, nil
}

// getProcessMemory returns memory usage in MB for the QEMU process.
// It reads the RSS (Resident Set Size) from the process stats.
func (p *QEMUProvider) getProcessMemory(ctx context.Context, vmName string) (int64, error) {
	// First, try to get PID from PID file
	pidFile := filepath.Join(p.stateDir, fmt.Sprintf("%s.pid", vmName))
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, fmt.Errorf("failed to read PID file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return 0, fmt.Errorf("invalid PID: %w", err)
	}

	// Get RSS based on platform
	switch runtime.GOOS {
	case "darwin":
		return p.getProcessMemoryDarwin(ctx, pid)
	case "linux":
		return p.getProcessMemoryLinux(ctx, pid)
	case "freebsd":
		return p.getProcessMemoryFreeBSD(ctx, pid)
	default:
		return p.getProcessMemoryGeneric(ctx, pid)
	}
}

// getProcessMemoryDarwin gets memory usage on macOS.
func (p *QEMUProvider) getProcessMemoryDarwin(ctx context.Context, pid int) (int64, error) {
	// macOS ps: RSS is in KB
	output, err := p.cmd().Output(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "rss=")
	if err != nil {
		return 0, err
	}

	rssKB, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		return 0, err
	}

	return rssKB / 1024, nil // Convert KB to MB
}

// getProcessMemoryLinux gets memory usage on Linux.
func (p *QEMUProvider) getProcessMemoryLinux(ctx context.Context, pid int) (int64, error) {
	// Linux: read from /proc/<pid>/status
	statusFile := fmt.Sprintf("/proc/%d/status", pid)
	data, err := os.ReadFile(statusFile)
	if err != nil {
		// Fallback to ps
		return p.getProcessMemoryGeneric(ctx, pid)
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "VmRSS:") {
			// Format: "VmRSS:    12345 kB"
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				rssKB, err := strconv.ParseInt(fields[1], 10, 64)
				if err != nil {
					return 0, err
				}
				return rssKB / 1024, nil // Convert KB to MB
			}
		}
	}

	return 0, fmt.Errorf("VmRSS not found in /proc/%d/status", pid)
}

// getProcessMemoryFreeBSD gets memory usage on FreeBSD.
func (p *QEMUProvider) getProcessMemoryFreeBSD(ctx context.Context, pid int) (int64, error) {
	// FreeBSD ps: RSS is in KB
	output, err := p.cmd().Output(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "rss=")
	if err != nil {
		return 0, err
	}

	rssKB, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		return 0, err
	}

	return rssKB / 1024, nil // Convert KB to MB
}

// getProcessMemoryGeneric is a fallback for other platforms.
func (p *QEMUProvider) getProcessMemoryGeneric(ctx context.Context, pid int) (int64, error) {
	// Generic fallback using ps
	output, err := p.cmd().Output(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "rss=")
	if err != nil {
		return 0, err
	}

	rssKB, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		return 0, err
	}

	return rssKB / 1024, nil // Convert KB to MB
}
