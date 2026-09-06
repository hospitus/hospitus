package jail

import "testing"

// TestEpairBSide verifies the jail-side interface name is derived by changing
// only the trailing "a", not the first "a" in "epair" (audit HIGH vnet.go:103,
// multinic.go:126: strings.Replace(name,"a","b",1) corrupted "epair" -> "epbir").
func TestEpairBSide(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"epair0a", "epair0b"},
		{"epair10a", "epair10b"},
		{"epair123a", "epair123b"},
	}
	for _, tt := range tests {
		if got := epairBSide(tt.in); got != tt.want {
			t.Errorf("epairBSide(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
