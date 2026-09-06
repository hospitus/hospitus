package bhyve

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

type TrafficStats struct {
	InterfaceName string `json:"interface"`
	RxBytes       int64  `json:"rx_bytes"`
	TxBytes       int64  `json:"tx_bytes"`
	RxPackets     int64  `json:"rx_packets"`
	TxPackets     int64  `json:"tx_packets"`
}

// GetTrafficStats returns network traffic statistics for a bhyve VM.
//
// This implements CBSD's traffic accounting using netstat:
//   - Reads interface statistics for tap devices associated with the VM
//   - Returns RX/TX bytes and packets
//   - Can be called periodically for monitoring
//
// For more advanced accounting, CBSD uses ipfw with counters or RACCT.
func (p *BhyveProvider) GetTrafficStats(ctx context.Context, handle provider.InstanceHandle) ([]TrafficStats, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID

	// Load VM configuration to get tap devices
	vmDir := filepath.Join(p.dataDir, vmName)
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load VM config: %w", err)
	}

	stats := []TrafficStats{}

	// Get statistics for each tap device
	for _, tapDev := range config.TapDevs {
		// Use netstat to get interface statistics
		// netstat -I <interface> -b (byte counts)
		output, err := p.cmd().CombinedOutput(ctx, "netstat", "-I", tapDev, "-b", "-n")
		if err != nil {
			// Interface might not exist or have statistics yet
			continue
		}

		// Parse netstat output
		// Format: Name Mtu Network Address Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll
		lines := strings.Split(string(output), "\n")
		if len(lines) < 2 {
			continue
		}

		// Parse the data line (skip header)
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) < 11 {
				continue
			}

			// Fields: [Name Mtu Network Address Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll]
			// Ibytes is at index 7, Obytes is at index 10
			rxBytes, _ := strconv.ParseInt(fields[7], 10, 64)
			txBytes, _ := strconv.ParseInt(fields[10], 10, 64)
			rxPackets, _ := strconv.ParseInt(fields[4], 10, 64)
			txPackets, _ := strconv.ParseInt(fields[8], 10, 64)

			stats = append(stats, TrafficStats{
				InterfaceName: tapDev,
				RxBytes:       rxBytes,
				TxBytes:       txBytes,
				RxPackets:     rxPackets,
				TxPackets:     txPackets,
			})
			break
		}
	}

	return stats, nil
}

// ResetTrafficStats is not implemented for bhyve.
//
// The kernel counters cannot be reset, so a real implementation has to persist
// a baseline and subtract it in GetTrafficStats. Until it does, this refuses:
// returning success left the caller treating the counters it read afterwards as
// post-reset values when they were still cumulative.
func (p *BhyveProvider) ResetTrafficStats(ctx context.Context, handle provider.InstanceHandle) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	return fmt.Errorf("resetting traffic statistics is not supported by the bhyve provider")
}
