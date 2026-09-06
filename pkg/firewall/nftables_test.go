package firewall

import "testing"

// TestParseNFTMappingReturnsUsableValues covers what ListRules hands back: a
// value carrying only Instance and ID cannot be passed to Validate or
// AddPortMapping, because protocol, ports and target are empty.
func TestParseNFTMappingReturnsUsableValues(t *testing.T) {
	line := `		tcp dport 80 dnat to 10.0.0.2:8080 comment "hospitus:web:web/jail/tcp/any/80/10.0.0.2/8080"`
	got, ok := parseNFTMapping(line)
	if !ok {
		t.Fatal("the rule nftables writes was not recognized")
	}
	if got.Protocol != ProtocolTCP || got.HostPort != 80 || got.TargetIP != "10.0.0.2" || got.TargetPort != 8080 {
		t.Errorf("fields not parsed: %+v", got)
	}
	if got.Instance != "web" {
		t.Errorf("instance = %q, want web", got.Instance)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("the parsed mapping does not validate: %v", err)
	}

	withIface := `		iifname "em0" udp dport 53 dnat to 10.0.0.3:5353 comment "hospitus:dns:dns/jail/udp/em0/53/10.0.0.3/5353"`
	got, ok = parseNFTMapping(withIface)
	if !ok {
		t.Fatal("the interface-scoped form was not recognized")
	}
	if got.HostInterface != "em0" || got.Protocol != ProtocolUDP {
		t.Errorf("interface form not parsed: %+v", got)
	}

	if _, ok := parseNFTMapping(`		tcp dport 80 accept`); ok {
		t.Error("a rule that is not one of ours was parsed as a mapping")
	}
}
