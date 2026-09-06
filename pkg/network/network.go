// Package network lists the host's network bridges.
//
// Creating bridges, epairs, taps and NAT rules is the business of the provider
// that owns the instance — pkg/provider/jail for jails, pkg/provider/bhyve for
// VMs — and of pkg/firewall for the PF anchor. This package only reports what
// exists, for the API's bridge listing.
package network

import (
	"context"
	"strings"
)

// BridgeInfo describes one bridge interface on the host.
type BridgeInfo struct {
	Name       string
	Members    []string
	IPAddress  string
	MTU        int
	State      string // "up" or "down"
	MacAddress string
}

// Lister reports the bridges the host currently has.
type Lister interface {
	ListBridges(ctx context.Context) ([]BridgeInfo, error)
}

// parseIfconfig reads one interface out of ifconfig(8)'s output.
//
// The bridge's own flags carry the state and the MTU, and each member appears
// on its own "member:" line; the continuation line that follows a member
// carries the port cost and is not a member itself.
func parseIfconfig(name string, out []byte) BridgeInfo {
	info := BridgeInfo{Name: name, State: "down"}

	for i, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		if i == 0 {
			for j, f := range fields {
				switch {
				case strings.HasPrefix(f, "flags=") && strings.Contains(flagNames(f), "UP"):
					info.State = "up"
				case f == "mtu" && j+1 < len(fields):
					info.MTU = atoi(fields[j+1])
				}
			}
			continue
		}

		switch fields[0] {
		case "ether":
			if len(fields) >= 2 {
				info.MacAddress = fields[1]
			}
		case "inet":
			if len(fields) >= 2 && info.IPAddress == "" {
				info.IPAddress = fields[1]
			}
		case "member:":
			if len(fields) >= 2 {
				info.Members = append(info.Members, fields[1])
			}
		}
	}

	return info
}

// flagNames returns the symbolic part of a "flags=1008843<UP,BROADCAST,...>"
// field, so a match cannot be satisfied by the hexadecimal value.
func flagNames(field string) string {
	open := strings.Index(field, "<")
	end := strings.LastIndex(field, ">")
	if open < 0 || end < open {
		return ""
	}
	return field[open+1 : end]
}

// atoi parses a decimal field, yielding 0 when the field is not a number.
func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// parseIPLink reads "ip -oneline link show type bridge", one bridge per line.
func parseIPLink(out string) []BridgeInfo {
	var bridges []BridgeInfo
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		info := BridgeInfo{Name: strings.TrimSuffix(fields[1], ":"), State: "down"}
		if idx := strings.Index(info.Name, "@"); idx > 0 {
			info.Name = info.Name[:idx]
		}
		for j, f := range fields {
			switch {
			case strings.HasPrefix(f, "<") && strings.Contains(f, "UP"):
				info.State = "up"
			case f == "mtu" && j+1 < len(fields):
				info.MTU = atoi(fields[j+1])
			case f == "link/ether" && j+1 < len(fields):
				info.MacAddress = fields[j+1]
			}
		}
		bridges = append(bridges, info)
	}
	return bridges
}
