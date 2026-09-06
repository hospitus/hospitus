package qemu

import (
	"strings"
	"testing"
)

func TestQEMUArchName(t *testing.T) {
	cases := map[string]string{"amd64": "x86_64", "arm64": "aarch64", "x86_64": "x86_64", "riscv64": "riscv64"}
	for in, want := range cases {
		if got := qemuArchName(in); got != want {
			t.Errorf("qemuArchName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestUEFIVarsCandidates verifies the VARS templates are searched with the QEMU
// arch naming (not the Hospitus arch), derive from the resolved code path, and
// include the homebrew and AAVMF locations (audit HIGH qemu.go:564).
func TestUEFIVarsCandidates(t *testing.T) {
	got := uefiVarsCandidates("amd64", "/usr/local/share/qemu/edk2-x86_64-code.fd")
	joined := strings.Join(got, "\n")

	// Derived from the code path.
	if got[0] != "/usr/local/share/qemu/edk2-x86_64-vars.fd" {
		t.Errorf("first candidate = %q, want the vars-derived path", got[0])
	}
	// QEMU arch naming, never the literal "amd64".
	if strings.Contains(joined, "edk2-amd64-") {
		t.Errorf("candidates use Hospitus arch instead of QEMU arch:\n%s", joined)
	}
	for _, want := range []string{"/opt/homebrew/share/qemu/", "/usr/share/AAVMF/AAVMF_VARS.fd"} {
		if !strings.Contains(joined, want) {
			t.Errorf("candidates missing %q:\n%s", want, joined)
		}
	}

	// The vars file QEMU actually ships is named after a different architecture
	// than the code file: edk2-x86_64-code.fd sits next to edk2-i386-vars.fd,
	// and edk2-aarch64-code.fd next to edk2-arm-vars.fd.
	if !strings.Contains(joined, "edk2-i386-vars.fd") {
		t.Errorf("amd64 candidates missing QEMU's own vars name:\n%s", joined)
	}
	armCandidates := strings.Join(uefiVarsCandidates("arm64", "/opt/homebrew/share/qemu/edk2-aarch64-code.fd"), "\n")
	if !strings.Contains(armCandidates, "edk2-arm-vars.fd") {
		t.Errorf("arm64 candidates missing QEMU's own vars name:\n%s", armCandidates)
	}
}

func TestQEMUVarsArchName(t *testing.T) {
	cases := map[string]string{
		"amd64":   "i386",
		"x86_64":  "i386",
		"i386":    "i386",
		"arm64":   "arm",
		"aarch64": "arm",
		"riscv64": "riscv64",
	}
	for in, want := range cases {
		if got := qemuVarsArchName(in); got != want {
			t.Errorf("qemuVarsArchName(%q) = %q, want %q", in, got, want)
		}
	}
}
