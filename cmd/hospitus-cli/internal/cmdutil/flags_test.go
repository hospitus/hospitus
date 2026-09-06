package cmdutil

import (
	"math"
	"testing"
)

func TestParseSize(t *testing.T) {
	t.Run("valid sizes", func(t *testing.T) {
		cases := map[string]int64{
			"":     0,
			"0":    0,
			"none": 0,
			"512":  512,
			"1K":   1024,
			"1KB":  1024,
			"10M":  10 * 1024 * 1024,
			"2G":   2 * 1024 * 1024 * 1024,
			"1T":   1024 * 1024 * 1024 * 1024,
		}
		for in, want := range cases {
			got, err := ParseSize(in)
			if err != nil {
				t.Errorf("%q: unexpected error: %v", in, err)
				continue
			}
			if got != want {
				t.Errorf("%q: want %d, got %d", in, want, got)
			}
		}
	})

	t.Run("negative rejected", func(t *testing.T) {
		if _, err := ParseSize("-5G"); err == nil {
			t.Error("expected error for negative size, got nil")
		}
	})

	t.Run("overflow rejected", func(t *testing.T) {
		// A value that overflows int64 once multiplied by the GB multiplier.
		big := int64(math.MaxInt64/(1024*1024*1024)) + 1
		if _, err := ParseSize(itoaGB(big)); err == nil {
			t.Error("expected overflow error, got nil")
		}
	})

	t.Run("invalid format", func(t *testing.T) {
		if _, err := ParseSize("abc"); err == nil {
			t.Error("expected error for invalid format, got nil")
		}
	})
}

// itoaGB formats n as a "<n>G" size string.
func itoaGB(n int64) string {
	digits := ""
	if n == 0 {
		return "0G"
	}
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits + "G"
}
