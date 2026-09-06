package jail

import (
	"runtime"
	"testing"
)

func TestNormalizeArch(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"arm64", "arm64"},
		{"aarch64", "arm64"},
		{"AARCH64", "arm64"},
		{"riscv64", "riscv64"},
		{"riscv", "riscv64"},
		{"i386", "i386"},
		{"i686", "i386"},
		{"x86", "i386"},
		{"amd64", "amd64"},
		{"x86_64", "amd64"},
		{"x64", "amd64"},
		{"armv7", "armv7"},
		{"arm", "armv7"},
		{"native", runtime.GOARCH},
		{"", runtime.GOARCH},
		{"unknown_arch", "unknown_arch"},
	}
	for _, tc := range tests {
		got := normalizeArch(tc.input)
		if got != tc.want {
			t.Errorf("normalizeArch(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestDetectImageArch(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{"FreeBSD-14.3-RELEASE-arm64.tar.xz", "arm64"},
		{"freebsd-14.3-aarch64", "arm64"},
		{"image.arm64.tar.gz", "arm64"},
		{"image-riscv64.img", "riscv64"},
		{"ubuntu-22.04-amd64", "amd64"},
		{"freebsd-14.3-RELEASE-amd64", "amd64"},
		{"image-i386.tar", "i386"},
		{"image.armv7.tar", "armv7"},
		{"no-arch-in-name", ""},
		{"", ""},
	}
	for _, tc := range tests {
		got := detectImageArch(tc.image)
		if got != tc.want {
			t.Errorf("detectImageArch(%q) = %q, want %q", tc.image, got, tc.want)
		}
	}
}

func TestIsCrossArch(t *testing.T) {
	host := runtime.GOARCH

	// Native arch → not cross
	if isCrossArch(host) {
		t.Errorf("isCrossArch(%q) should be false on matching host", host)
	}
	// "native" alias → not cross
	if isCrossArch("native") {
		t.Error("isCrossArch('native') should be false")
	}
	// On amd64 host, i386 is natively supported (32-bit compat)
	if host == "amd64" {
		if isCrossArch("i386") {
			t.Error("isCrossArch('i386') should be false on amd64 host")
		}
		// arm64 requires emulation on amd64
		if !isCrossArch("arm64") {
			t.Error("isCrossArch('arm64') should be true on amd64 host")
		}
	}
}

func TestGetQEMUBinaryPath_UnknownArch(t *testing.T) {
	_, err := getQEMUBinaryPath("mips64")
	if err == nil {
		t.Error("expected error for unknown architecture 'mips64', got nil")
	}
}
