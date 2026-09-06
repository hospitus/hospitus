package jail

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TrafficStats represents network traffic statistics for a jail.
type TrafficStats struct {
	InterfaceName string `json:"interface"`
	RxBytes       int64  `json:"rx_bytes"`
	TxBytes       int64  `json:"tx_bytes"`
	RxPackets     int64  `json:"rx_packets"`
	TxPackets     int64  `json:"tx_packets"`
}

// GetTrafficStats returns network traffic statistics for a jail.
//
// This implements CBSD's traffic accounting using netstat:
//   - Reads interface statistics for jail interfaces
//   - Returns RX/TX bytes and packets
//   - Can be called periodically for monitoring
//
// For more advanced accounting, CBSD uses ipfw with counters or RACCT.
func (p *JailProvider) GetTrafficStats(ctx context.Context, handle provider.InstanceHandle) ([]TrafficStats, error) {
	jailName := handle.ID

	// Check if jail is running
	jid, err := p.getJailID(ctx, jailName)
	if err != nil {
		return nil, fmt.Errorf("failed to get jail ID: %w", err)
	}

	if jid == 0 {
		return nil, fmt.Errorf("jail %s is not running", jailName)
	}

	// Get list of interfaces in the jail
	output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "ifconfig", "-l")
	if err != nil {
		return nil, fmt.Errorf("failed to list interfaces: %w", err)
	}

	interfaces := strings.Fields(string(output))
	stats := []TrafficStats{}

	// Get statistics for each interface
	for _, iface := range interfaces {
		// Skip loopback interface
		if iface == "lo0" {
			continue
		}

		// Use netstat to get interface statistics
		// netstat -I <interface> -b (byte counts)
		netstatOutput, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "netstat", "-I", iface, "-b", "-n")
		if err != nil {
			// Interface might not have statistics yet
			continue
		}

		// Parse netstat output
		// Format: Name Mtu Network Address Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll
		lines := strings.Split(string(netstatOutput), "\n")
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
				InterfaceName: iface,
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

// ResetTrafficStats resets traffic statistics for a jail.
//
// A real reset would persist the current kernel counters as a baseline and
// subtract them in GetTrafficStats. That baseline store is not implemented yet,
// so rather than report a misleading success we return ErrUnsupportedOperation.
func (p *JailProvider) ResetTrafficStats(_ context.Context, _ provider.InstanceHandle) error {
	return fmt.Errorf("reset traffic stats: %w", provider.ErrUnsupportedOperation)
}
