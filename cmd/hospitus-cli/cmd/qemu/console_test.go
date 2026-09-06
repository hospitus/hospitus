package qemu

import "testing"

// TestVNCPort covers the JSON round-trip forms the provider config can take and
// the rejection of values that would print a misleading address.
func TestVNCPort(t *testing.T) {
	tests := []struct {
		name      string
		config    map[string]interface{}
		wantPort  int
		wantFound bool
	}{
		{"absent", map[string]interface{}{}, 0, false},
		{"nil config", nil, 0, false},
		{"int", map[string]interface{}{"vnc_port": 5901}, 5901, true},
		{"int64", map[string]interface{}{"vnc_port": int64(5902)}, 5902, true},
		{"float64 from JSON", map[string]interface{}{"vnc_port": float64(5903)}, 5903, true},
		{"wrong type", map[string]interface{}{"vnc_port": "5901"}, 0, false},
		{"below base port", map[string]interface{}{"vnc_port": 80}, 0, false},
		{"above port range", map[string]interface{}{"vnc_port": 70000}, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port, found := vncPort(tt.config)
			if found != tt.wantFound {
				t.Fatalf("vncPort found = %v, want %v", found, tt.wantFound)
			}
			if port != tt.wantPort {
				t.Errorf("vncPort = %d, want %d", port, tt.wantPort)
			}
		})
	}
}
