package bhyve

import "testing"

// TestParseTimeToSeconds covers the ps(1) TIME field as FreeBSD actually
// writes it, checked against a real host: bin/ps/print.c formats
// "%3d-%02d:%02d:%02d" past a day, and the fraction takes the locale's decimal
// separator — an fr_FR host prints "159384:28,69".
func TestParseTimeToSeconds(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
	}{
		{"0:02.65", 2.65},
		{"0:02,65", 2.65}, // the separator a French-locale host writes
		{"36:18.06", 2178.06},
		{"01:02:03", 3723},
		{"1-02:03:04", 93784},
		{"  4-16:21:37", 404497},
	} {
		got, err := parseTimeToSeconds(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q = %v, want %v", tc.in, got, tc.want)
		}
	}

	// A field it cannot read is an error, not a silent zero: reporting no CPU
	// time for a busy VM reads as an idle VM.
	for _, bad := range []string{"", "abc", "1:2:3:4", "x-01:02:03", "01:xx:03"} {
		if got, err := parseTimeToSeconds(bad); err == nil {
			t.Errorf("%q was accepted as %v", bad, got)
		}
	}
}
