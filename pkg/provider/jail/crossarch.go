package jail

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/hospitus/hospitus/pkg/logging"
)

// qemuArchMap maps target architectures to QEMU binary names
var qemuArchMap = map[string]string{
	"arm64":   "qemu-aarch64-static",
	"aarch64": "qemu-aarch64-static",
	"riscv64": "qemu-riscv64-static",
	"riscv":   "qemu-riscv64-static",
	"i386":    "qemu-i386-static",
	"armv7":   "qemu-arm-static",
	"arm":     "qemu-arm-static",
}

// detectImageOS reports the operating system an image holds, read from its
// name, or "" when the name says nothing.
//
// The catalog names every Linux userland a jail can run "<distro>-<version>-
// rootfs-<arch>" — ubuntu, alpine, fedora and debian at present — and gives no
// FreeBSD image a "rootfs" in its name. Both halves are required so that a
// cloud image such as debian-12-amd64, which is a disk for bhyve rather than a
// tree for a jail, is not mistaken for one.
func detectImageOS(image string) string {
	imageLower := strings.ToLower(image)
	if !strings.Contains(imageLower, "-rootfs-") {
		return ""
	}

	for _, distro := range []string{"ubuntu", "alpine", "fedora", "debian", "rocky", "centos", "arch"} {
		if strings.HasPrefix(imageLower, distro+"-") {
			return "linux"
		}
	}

	return ""
}

// detectImageArch detects the target architecture from an image name.
// Returns empty string if architecture cannot be detected or is native.
func detectImageArch(image string) string {
	imageLower := strings.ToLower(image)

	// Check for architecture suffixes in image name
	archPatterns := []struct {
		pattern string
		arch    string
	}{
		{"-arm64", "arm64"},
		{"-aarch64", "arm64"},
		{".arm64", "arm64"},
		{".aarch64", "arm64"},
		{"-riscv64", "riscv64"},
		{"-riscv", "riscv64"},
		{".riscv64", "riscv64"},
		{"-i386", "i386"},
		{".i386", "i386"},
		{"-armv7", "armv7"},
		{".armv7", "armv7"},
		{"-amd64", "amd64"},
		{".amd64", "amd64"},
	}

	for _, p := range archPatterns {
		if strings.Contains(imageLower, p.pattern) {
			return p.arch
		}
	}

	return ""
}

// normalizeArch normalizes architecture names to a canonical form
func normalizeArch(arch string) string {
	switch strings.ToLower(arch) {
	case "arm64", "aarch64":
		return "arm64"
	case "riscv64", "riscv":
		return "riscv64"
	case "i386", "i686", "x86":
		return "i386"
	case "amd64", "x86_64", "x64":
		return "amd64"
	case "armv7", "arm":
		return "armv7"
	case "native", "":
		return runtime.GOARCH
	default:
		return arch
	}
}

// isCrossArch returns true if the target architecture requires emulation
func isCrossArch(targetArch string) bool {
	hostArch := runtime.GOARCH
	target := normalizeArch(targetArch)

	// Same architecture - no emulation needed
	if target == hostArch {
		return false
	}

	// amd64 host can run i386 natively (32-bit compatibility)
	if hostArch == "amd64" && target == "i386" {
		return false
	}

	return true
}

// getQEMUBinaryPath returns the path to the QEMU static binary for the target architecture
func getQEMUBinaryPath(targetArch string) (string, error) {
	target := normalizeArch(targetArch)
	binaryName, ok := qemuArchMap[target]
	if !ok {
		return "", fmt.Errorf("no QEMU binary mapping for architecture: %s", targetArch)
	}

	// Common locations for QEMU static binaries
	searchPaths := []string{
		"/usr/local/bin/" + binaryName,
		"/usr/bin/" + binaryName,
		"/usr/local/libexec/qemu/" + binaryName,
	}

	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("QEMU binary %s not found. Install qemu-user-static package: pkg install qemu-user-static", binaryName)
}

// setupCrossArchEmulation copies the appropriate QEMU static binary into the jail
// for cross-architecture emulation support.
func (p *JailProvider) setupCrossArchEmulation(ctx context.Context, jailPath, targetArch string) error {
	if !isCrossArch(targetArch) {
		return nil // No emulation needed for native architecture
	}

	target := normalizeArch(targetArch)
	qemuSrc, err := getQEMUBinaryPath(target)
	if err != nil {
		return err
	}

	// Destination inside the jail (same path as source, relative to jail root)
	// This is important because binmiscctl uses absolute paths
	qemuDst := filepath.Join(jailPath, qemuSrc)
	qemuDir := filepath.Dir(qemuDst)

	// Create directory structure inside jail
	if err := os.MkdirAll(qemuDir, 0o755); err != nil {
		return fmt.Errorf("failed to create QEMU directory in jail: %w", err)
	}

	// Copy QEMU binary
	p.logInfo(ctx, "setting up cross-architecture emulation", "target_arch", target, "host_arch", runtime.GOARCH, "jail_path", jailPath)
	p.logInfo(ctx, "copying QEMU static binary into jail", "source", qemuSrc, "destination", qemuDst)

	// Use cp command for simplicity and to preserve permissions
	output, err := p.cmd().CombinedOutput(ctx, "cp", qemuSrc, qemuDst)
	if err != nil {
		return fmt.Errorf("failed to copy QEMU binary: %w (output: %s)", err, string(output))
	}

	// Ensure it's executable
	if err := os.Chmod(qemuDst, 0o755); err != nil {
		return fmt.Errorf("failed to set QEMU binary permissions: %w", err)
	}

	// Copy native rescue binaries for network configuration
	// QEMU doesn't support netlink sockets, so we need native binaries for route/ping
	rescueSrc := "/rescue"
	rescueDst := filepath.Join(jailPath, "rescue")

	if _, err := os.Stat(rescueSrc); err == nil {
		// Create rescue directory in jail
		if err := os.MkdirAll(rescueDst, 0o755); err != nil {
			p.logWarn(ctx, "failed to create rescue directory in jail", "path", rescueDst, logging.FieldError, err)
		} else {
			p.logInfo(ctx, "copying native rescue binaries for cross-arch networking", "source_dir", rescueSrc, "destination_dir", rescueDst)

			// Copy essential rescue binaries for networking
			rescueBinaries := []string{"route", "ping", "ping6", "ifconfig", "netstat"}
			for _, bin := range rescueBinaries {
				src := filepath.Join(rescueSrc, bin)
				dst := filepath.Join(rescueDst, bin)
				if _, err := os.Stat(src); err == nil {
					if err := p.cmd().Run(ctx, "cp", src, dst); err != nil {
						p.logWarn(ctx, "failed to copy rescue binary into jail", "binary", bin, "source", src, "destination", dst, logging.FieldError, err)
					}
				}
			}
		}
	}

	return nil
}
