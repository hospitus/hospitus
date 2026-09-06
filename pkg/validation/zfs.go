package validation

import (
	"strconv"
	"strings"
)

// ValidZFSCompression reports whether the value names a compression algorithm
// "zfs set compression=" accepts. zstd takes a level of 1 to 19, zstd-fast one
// of 1-10, 20, 30, ... 100, 500 or 1000 — a sparse set, not a range — and
// gzip one of 1 to 9.
//
// Shared rather than written twice: the API accepted the leveled zstd values
// and the jail provider's own allowlist did not, so the same value was valid
// on one entry point and refused on the other. The value ends up on a root
// command line, so an unknown one is refused rather than passed on.
func ValidZFSCompression(value string) bool {
	switch value {
	case "on", "off", "lz4", "zle", "lzjb", "gzip", "zstd", "zstd-fast":
		return true
	}
	if rest, ok := strings.CutPrefix(value, "gzip-"); ok {
		return len(rest) == 1 && rest[0] >= '1' && rest[0] <= '9'
	}
	// Before "zstd-", which is a prefix of it: "zstd-fast-100" reached the
	// generic branch, Atoi("fast-100") failed, and every leveled zstd-fast
	// value was refused.
	if rest, ok := strings.CutPrefix(value, "zstd-fast-"); ok {
		level, ok := compressionLevel(rest)
		return ok && validZstdFastLevel(level)
	}
	if rest, ok := strings.CutPrefix(value, "zstd-"); ok {
		level, ok := compressionLevel(rest)
		return ok && level >= 1 && level <= 19
	}
	return false
}

// compressionLevel reads the N of "zstd-N", and refuses anything zfs would not
// recognize as that name.
//
// Not strconv.Atoi alone: it takes a leading sign and leading zeros, so
// "zstd-+5" and "zstd-05" passed this validator and were then refused by zfs
// itself — which registers exact property names — turning a caller's typo into
// a 500.
func compressionLevel(rest string) (int, bool) {
	if rest == "" || (len(rest) > 1 && rest[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return 0, false
		}
	}
	level, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return level, true
}

// validZstdFastLevel reports whether N in "zstd-fast-N" is a level OpenZFS
// takes. zfsprops(7) spells the set out as [1-10, 20, 30, ..., 100, 500,
// 1000] — sparse, not a range: "zstd-fast-11" passed a 1..1000 check here and
// was then refused by zfs itself.
func validZstdFastLevel(level int) bool {
	switch {
	case level >= 1 && level <= 10:
		return true
	case level >= 20 && level <= 100 && level%10 == 0:
		return true
	case level == 500 || level == 1000:
		return true
	}
	return false
}
