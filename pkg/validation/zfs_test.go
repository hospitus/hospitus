package validation

import (
	"testing"
)

// TestValidZFSCompression covers the compression values a volume request may
// carry.
//
// "zstd-" was tested before "zstd-fast-", of which it is a prefix, so every
// leveled zstd-fast value reached the generic branch, failed Atoi("fast-N")
// and was refused with 400.
func TestValidZFSCompression(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  bool
	}{
		{"on", true},
		{"off", true},
		{"lz4", true},
		{"zstd", true},
		{"zstd-fast", true},
		{"zstd-1", true},
		{"zstd-19", true},
		{"zstd-20", false},
		{"zstd-0", false},
		{"zstd-fast-1", true},
		{"zstd-fast-10", true},
		// The two that tell the sparse set from a 1..1000 range.
		{"zstd-fast-11", false},
		{"zstd-fast-20", true},
		{"zstd-fast-25", false},
		{"zstd-fast-100", true},
		{"zstd-fast-1000", true},
		{"zstd-fast-1001", false},

		// Atoi takes these; zfs registers exact names and does not.
		{"zstd-+5", false},
		{"zstd-05", false},
		{"zstd-fast-+5", false},
		{"zstd-fast-05", false},
		{"zstd- 5", false},
		{"zstd-5 ", false},
		{"gzip-+5", false},
		{"zstd-fast-0", false},
		{"gzip", true},
		{"gzip-9", true},
		{"gzip-10", false},
		{"lzo", false},
		{"", false},
	} {
		if got := ValidZFSCompression(tt.value); got != tt.want {
			t.Errorf("ValidZFSCompression(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}
