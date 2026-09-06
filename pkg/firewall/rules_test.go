package firewall

import (
	"strings"
	"testing"
)

func TestPortMappingValidate(t *testing.T) {
	tests := []struct {
		name    string
		mapping PortMapping
		wantErr bool
	}{
		{
			name: "valid TCP mapping",
			mapping: PortMapping{
				Instance:   "test",
				Protocol:   ProtocolTCP,
				HostPort:   80,
				TargetPort: 8080,
				TargetIP:   "10.0.0.2",
			},
			wantErr: false,
		},
		{
			name: "valid UDP mapping",
			mapping: PortMapping{
				Instance:   "test",
				Protocol:   ProtocolUDP,
				HostPort:   53,
				TargetPort: 53,
				TargetIP:   "10.0.0.2",
			},
			wantErr: false,
		},
		{
			name: "missing instance",
			mapping: PortMapping{
				Protocol:   ProtocolTCP,
				HostPort:   80,
				TargetPort: 8080,
				TargetIP:   "10.0.0.2",
			},
			wantErr: true,
		},
		{
			name: "invalid protocol",
			mapping: PortMapping{
				Instance:   "test",
				Protocol:   "invalid",
				HostPort:   80,
				TargetPort: 8080,
				TargetIP:   "10.0.0.2",
			},
			wantErr: true,
		},
		{
			name: "invalid host port (0)",
			mapping: PortMapping{
				Instance:   "test",
				Protocol:   ProtocolTCP,
				HostPort:   0,
				TargetPort: 8080,
				TargetIP:   "10.0.0.2",
			},
			wantErr: true,
		},
		{
			name: "invalid host port (65536)",
			mapping: PortMapping{
				Instance:   "test",
				Protocol:   ProtocolTCP,
				HostPort:   65536,
				TargetPort: 8080,
				TargetIP:   "10.0.0.2",
			},
			wantErr: true,
		},
		{
			name: "invalid target IP",
			mapping: PortMapping{
				Instance:   "test",
				Protocol:   ProtocolTCP,
				HostPort:   80,
				TargetPort: 8080,
				TargetIP:   "invalid",
			},
			wantErr: true,
		},
		{
			name: "missing target IP",
			mapping: PortMapping{
				Instance:   "test",
				Protocol:   ProtocolTCP,
				HostPort:   80,
				TargetPort: 8080,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.mapping.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNATRuleValidate(t *testing.T) {
	tests := []struct {
		name    string
		rule    NATRule
		wantErr bool
	}{
		{
			name: "valid NAT rule",
			rule: NATRule{
				Instance:      "test",
				SourceNetwork: "10.0.0.0/24",
				OutInterface:  "eth0",
			},
			wantErr: false,
		},
		{
			name: "missing instance",
			rule: NATRule{
				SourceNetwork: "10.0.0.0/24",
				OutInterface:  "eth0",
			},
			wantErr: true,
		},
		{
			name: "missing source network",
			rule: NATRule{
				Instance:     "test",
				OutInterface: "eth0",
			},
			wantErr: true,
		},
		{
			name: "invalid CIDR",
			rule: NATRule{
				Instance:      "test",
				SourceNetwork: "10.0.0.0",
				OutInterface:  "eth0",
			},
			wantErr: true,
		},
		{
			name: "missing out interface",
			rule: NATRule{
				Instance:      "test",
				SourceNetwork: "10.0.0.0/24",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rule.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParsePortSpec(t *testing.T) {
	tests := []struct {
		name       string
		spec       string
		wantProto  Protocol
		wantHost   int
		wantTarget int
		wantErr    bool
	}{
		{
			name:       "simple port",
			spec:       "80",
			wantProto:  ProtocolTCP,
			wantHost:   80,
			wantTarget: 80,
			wantErr:    false,
		},
		{
			name:       "port mapping",
			spec:       "80:8080",
			wantProto:  ProtocolTCP,
			wantHost:   80,
			wantTarget: 8080,
			wantErr:    false,
		},
		{
			name:       "TCP with port mapping",
			spec:       "tcp/80:8080",
			wantProto:  ProtocolTCP,
			wantHost:   80,
			wantTarget: 8080,
			wantErr:    false,
		},
		{
			name:       "UDP with port mapping",
			spec:       "udp/53:5353",
			wantProto:  ProtocolUDP,
			wantHost:   53,
			wantTarget: 5353,
			wantErr:    false,
		},
		{
			name:       "UDP simple",
			spec:       "udp/53",
			wantProto:  ProtocolUDP,
			wantHost:   53,
			wantTarget: 53,
			wantErr:    false,
		},
		{
			name:    "invalid protocol",
			spec:    "icmp/80",
			wantErr: true,
		},
		{
			name:    "invalid port",
			spec:    "abc",
			wantErr: true,
		},
		{
			name:    "invalid host port in mapping",
			spec:    "abc:8080",
			wantErr: true,
		},
		{
			name:    "invalid target port in mapping",
			spec:    "80:abc",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, hostPort, targetPort, err := ParsePortSpec(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParsePortSpec() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if proto != tt.wantProto {
					t.Errorf("ParsePortSpec() proto = %v, want %v", proto, tt.wantProto)
				}
				if hostPort != tt.wantHost {
					t.Errorf("ParsePortSpec() hostPort = %v, want %v", hostPort, tt.wantHost)
				}
				if targetPort != tt.wantTarget {
					t.Errorf("ParsePortSpec() targetPort = %v, want %v", targetPort, tt.wantTarget)
				}
			}
		})
	}
}

func TestPortMappingGenerateID(t *testing.T) {
	mapping := PortMapping{
		Instance:   "web",
		Provider:   "jail",
		Protocol:   ProtocolTCP,
		HostPort:   80,
		TargetPort: 8080,
	}

	id := mapping.GenerateID()
	expected := "web-jail-tcp-80-8080"
	if id != expected {
		t.Errorf("GenerateID() = %v, want %v", id, expected)
	}
}

func TestPortMappingString(t *testing.T) {
	mapping := PortMapping{
		HostInterface: "eth0",
		HostPort:      80,
		TargetIP:      "10.0.0.2",
		TargetPort:    8080,
		Protocol:      ProtocolTCP,
	}

	str := mapping.String()
	expected := "eth0:80 -> 10.0.0.2:8080 (tcp)"
	if str != expected {
		t.Errorf("String() = %v, want %v", str, expected)
	}
}

// TestPortMappingValidateRejectsUnsafeInstance ensures instance names that
// could inject into backend rule text are rejected (audit nftables.go:74).
func TestPortMappingValidateRejectsUnsafeInstance(t *testing.T) {
	bad := []string{"web\"; drop", "../evil", "a b", "foo\nbar", "with;semicolon"}
	for _, name := range bad {
		pm := PortMapping{
			Instance:   name,
			Protocol:   ProtocolTCP,
			HostPort:   80,
			TargetPort: 8080,
			TargetIP:   "10.0.0.2",
		}
		if err := pm.Validate(); err == nil {
			t.Errorf("Validate() accepted unsafe instance name %q", name)
		}
	}
}

// TestParsePortSpecRejectsOutOfRange ensures ports outside 1-65535 are rejected
// (audit rules.go:153).
func TestParsePortSpecRejectsOutOfRange(t *testing.T) {
	for _, spec := range []string{"0", "70000", "80:0", "80:70000"} {
		if _, _, _, err := ParsePortSpec(spec); err == nil {
			t.Errorf("ParsePortSpec(%q) accepted out-of-range port", spec)
		}
	}
}

func TestPortMappingArgs(t *testing.T) {
	base := PortMapping{
		Instance:   "web",
		Protocol:   ProtocolTCP,
		HostPort:   80,
		TargetIP:   "10.0.0.2",
		TargetPort: 8080,
		ID:         "web-jail-tcp-80-8080",
	}

	// Without a host interface: no -i token.
	args := portMappingArgs("-A", base)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-i ") {
		t.Errorf("unexpected -i in args without HostInterface: %v", args)
	}
	if args[2] != "-A" {
		t.Errorf("op token = %q, want -A", args[2])
	}

	// With a host interface: Add and Delete must be identical except the op,
	// and both must carry the -i variant (audit iptables.go:110).
	withIf := base
	withIf.HostInterface = "em0"
	addArgs := portMappingArgs("-A", withIf)
	delArgs := portMappingArgs("-D", withIf)
	if !strings.Contains(strings.Join(addArgs, " "), "-i em0") {
		t.Errorf("Add args missing -i em0: %v", addArgs)
	}
	if len(addArgs) != len(delArgs) {
		t.Fatalf("Add/Delete arg length mismatch: %d vs %d", len(addArgs), len(delArgs))
	}
	for k := range addArgs {
		if k == 2 {
			continue // the op token differs (-A vs -D)
		}
		if addArgs[k] != delArgs[k] {
			t.Errorf("Add/Delete differ at %d: %q vs %q", k, addArgs[k], delArgs[k])
		}
	}
}
