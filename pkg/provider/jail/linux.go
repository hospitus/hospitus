package jail

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hospitus/hospitus/pkg/logging"
)

// Common Linux releases for jail
var linuxReleases = map[string]OSRelease{
	// Debian releases
	"debian-11-amd64": {
		Type:    "linux",
		Version: "debian-11",
		Arch:    "amd64",
		BaseURL: "https://deb.debian.org/debian",
	},
	"debian-12-amd64": {
		Type:    "linux",
		Version: "debian-12",
		Arch:    "amd64",
		BaseURL: "https://deb.debian.org/debian",
	},
	// Ubuntu releases
	"ubuntu-20.04-amd64": {
		Type:    "linux",
		Version: "ubuntu-20.04",
		Arch:    "amd64",
		BaseURL: "https://archive.ubuntu.com/ubuntu",
	},
	"ubuntu-22.04-amd64": {
		Type:    "linux",
		Version: "ubuntu-22.04",
		Arch:    "amd64",
		BaseURL: "https://archive.ubuntu.com/ubuntu",
	},
	// CentOS Stream releases
	"centos-9-amd64": {
		Type:    "linux",
		Version: "centos-9",
		Arch:    "amd64",
		BaseURL: "https://mirror.stream.centos.org/9-stream/BaseOS/x86_64/os/",
	},
	// Rocky Linux releases
	"rocky-8-amd64": {
		Type:    "linux",
		Version: "rocky-8",
		Arch:    "amd64",
		BaseURL: "https://download.rockylinux.org/pub/rocky/8/BaseOS/x86_64/os/",
	},
	"rocky-9-amd64": {
		Type:    "linux",
		Version: "rocky-9",
		Arch:    "amd64",
		BaseURL: "https://download.rockylinux.org/pub/rocky/9/BaseOS/x86_64/os/",
	},
	// Alma Linux releases
	"alma-8-amd64": {
		Type:    "linux",
		Version: "alma-8",
		Arch:    "amd64",
		BaseURL: "https://repo.almalinux.org/almalinux/8/BaseOS/x86_64/os/",
	},
	"alma-9-amd64": {
		Type:    "linux",
		Version: "alma-9",
		Arch:    "amd64",
		BaseURL: "https://repo.almalinux.org/almalinux/9/BaseOS/x86_64/os/",
	},
	// Alpine Linux releases
	"alpine-3.18-amd64": {
		Type:    "linux",
		Version: "alpine-3.18",
		Arch:    "amd64",
		BaseURL: "https://dl-cdn.alpinelinux.org/alpine/v3.18/main",
	},
	"alpine-3.20-amd64": {
		Type:    "linux",
		Version: "alpine-3.20",
		Arch:    "amd64",
		BaseURL: "https://dl-cdn.alpinelinux.org/alpine/v3.20/main",
	},
	"alpine-3.21-amd64": {
		Type:    "linux",
		Version: "alpine-3.21",
		Arch:    "amd64",
		BaseURL: "https://dl-cdn.alpinelinux.org/alpine/v3.21/main",
	},
	// Alpine on aarch64, which runs through qemu-aarch64-static like any other
	// foreign-architecture jail. The minirootfs download already picks the
	// aarch64 tarball when the release says so; only these entries were
	// missing, which left the whole Linux catalog amd64-only.
	//
	// Debian and Ubuntu have no arm64 entries because debootstrap needs the
	// matching archive keyring on the host, and FreeBSD packages Ubuntu's for
	// amd64 alone. Alpine needs no keyring: it ships a rootfs tarball.
	"alpine-3.20-arm64": {
		Type:    "linux",
		Version: "alpine-3.20",
		Arch:    "arm64",
		BaseURL: "https://dl-cdn.alpinelinux.org/alpine/v3.20/main",
	},
	"alpine-3.21-arm64": {
		Type:    "linux",
		Version: "alpine-3.21",
		Arch:    "arm64",
		BaseURL: "https://dl-cdn.alpinelinux.org/alpine/v3.21/main",
	},
}

// setupLinuxJail configures a Linux jail with necessary compatibility layers.
//
// This implements CBSD/Bastille Linux jail support:
//   - Loads linux_common, linux64, linprocfs, linsysfs, fdescfs, tmpfs
//   - Creates Linux-specific mount points (/proc, /sys, /dev/shm, etc.)
//   - Sets up linprocfs and linsysfs
//   - Configures Linux compatibility layer (sysctl, rc.conf)
//   - Applies post-create fixups (resolv.conf, apt cache, dpkg fixups)
func (p *JailProvider) setupLinuxJail(ctx context.Context, jailPath string, osRelease *OSRelease) error {
	// Check if Linux compatibility layer is loaded
	if err := p.ensureLinuxCompatibility(ctx); err != nil {
		return fmt.Errorf("failed to enable Linux compatibility: %w", err)
	}

	// Install Linux base system based on distribution
	if err := p.installLinuxBase(ctx, jailPath, osRelease); err != nil {
		return fmt.Errorf("failed to install Linux base system: %w", err)
	}

	// Create Linux-specific directories (AFTER install to ensure they exist)
	// Some may already exist in pre-built rootfs (e.g. dev/fd as symlink)
	linuxDirs := []string{
		"proc",
		"sys",
		"dev/shm",
		"dev/fd",
		"tmp",
		"run",
	}

	for _, dir := range linuxDirs {
		dirPath := filepath.Join(jailPath, dir)
		// Use Lstat (not Stat) to detect symlinks without following them.
		// A symlink like dev/fd -> /proc/self/fd would cause Stat to fail
		// with ENOENT when the target doesn't exist.
		info, err := os.Lstat(dirPath)
		if err == nil {
			// Path exists — skip if it's already a directory or symlink
			if info.IsDir() || (info.Mode()&os.ModeSymlink != 0) {
				continue
			}
			// Not a directory or symlink (e.g. device) — skip
			p.logWarn(ctx, "Linux dir path exists but is not a directory/symlink, skipping", "path", dirPath, "type", info.Mode())
			continue
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to lstat %s: %w", dir, err)
		}
		if err := os.MkdirAll(dirPath, 0o755); err != nil {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}

	// Post-create fixups
	if err := p.fixupLinuxJail(ctx, jailPath, osRelease); err != nil {
		return fmt.Errorf("failed to fixup Linux jail: %w", err)
	}

	return nil
}

// fixupLinuxJail applies post-creation fixes required for a working Linux jail.
func (p *JailProvider) fixupLinuxJail(ctx context.Context, jailPath string, osRelease *OSRelease) error {
	// 1. Copy host resolv.conf so DNS works immediately
	if err := p.copyResolvConf(jailPath); err != nil {
		p.logWarn(ctx, "failed to copy resolv.conf", logging.FieldError, err)
	}

	// 2. Distribution-specific fixups
	switch {
	case strings.Contains(osRelease.Version, "debian"),
		strings.Contains(osRelease.Version, "ubuntu"):
		if err := p.fixupDebianFamily(ctx, jailPath); err != nil {
			return err
		}
	}

	return nil
}

// copyResolvConf copies the host's resolv.conf into the jail. Linux rootfs
// images frequently ship /etc/resolv.conf as a symlink (e.g. to
// /run/systemd/resolve/...); the symlink is removed first so a real file is
// written inside the jail instead of dangling or escaping the root.
func (p *JailProvider) copyResolvConf(jailPath string) error {
	src := "/etc/resolv.conf"
	dst := filepath.Join(jailPath, "etc", "resolv.conf")
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("failed to read host resolv.conf: %w", err)
	}
	if info, err := os.Lstat(dst); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("failed to remove resolv.conf symlink: %w", err)
		}
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return fmt.Errorf("failed to write jail resolv.conf: %w", err)
	}
	return nil
}

// fixupDebianFamily applies Debian/Ubuntu-specific fixes.
func (p *JailProvider) fixupDebianFamily(ctx context.Context, jailPath string) error {
	// Increase APT cache start to avoid out-of-memory during package operations
	aptDir := filepath.Join(jailPath, "etc", "apt", "apt.conf.d")
	if err := os.MkdirAll(aptDir, 0o755); err == nil {
		_ = os.WriteFile(
			filepath.Join(aptDir, "00freebsd"),
			[]byte("APT::Cache-Start 251658240;\n"),
			0o600,
		)
	}

	return nil
}

// ensureLinuxCompatibility ensures the Linux compatibility layer is loaded.
//
// This follows CBSD/Bastille practices: load linux_common, linux64,
// linprocfs, linsysfs, fdescfs, tmpfs, and ensure linux_enable=YES
// and compat.linux.osrelease sysctl are set.
func (p *JailProvider) ensureLinuxCompatibility(ctx context.Context) error {
	modules := []string{"linux_common", "linux64", "linprocfs", "linsysfs", "fdescfs", "tmpfs"}
	for _, mod := range modules {
		// Match by kld file name, which is what kldload below takes: several of
		// these files provide no module of the same name, so -m alone sends
		// every start through a redundant kldload that reports "already loaded".
		if err := p.cmd().Run(ctx, "kldstat", "-q", "-n", mod); err == nil {
			continue
		}
		if err := p.cmd().Run(ctx, "kldstat", "-q", "-m", mod); err == nil {
			continue
		}
		output, err := p.cmd().CombinedOutput(ctx, "kldload", mod)
		if err != nil {
			if strings.Contains(strings.ToLower(string(output)), "already loaded") {
				continue
			}
			// fdescfs and tmpfs may already be compiled in; warn but continue
			if mod == "fdescfs" || mod == "tmpfs" {
				p.logWarn(ctx, "failed to load kernel module, may be compiled in", "module", mod, "output", string(output))
				continue
			}
			return fmt.Errorf("failed to load %s module: %w (output: %s)", mod, err, string(output))
		}
	}

	// Ensure linux_enable=YES in rc.conf for persistence across reboots
	if err := p.ensureSysrc(ctx, "linux_enable", "YES"); err != nil {
		p.logWarn(ctx, "failed to set linux_enable=YES", logging.FieldError, err)
	}

	// Set linux.osrelease sysctl so Linux apps see a modern kernel version
	// Brave/Chromium requires at least 3.x; use 4.4.0 as safe default
	if err := p.setSysctlIfNotSet(ctx, "compat.linux.osrelease", "4.4.0"); err != nil {
		p.logWarn(ctx, "failed to set compat.linux.osrelease", logging.FieldError, err)
	}

	// Ensure ELF fallback brand is set (required for Linux binaries on FreeBSD)
	if err := p.setSysctlIfNotSet(ctx, "kern.elf64.fallback_brand", "3"); err != nil {
		p.logWarn(ctx, "failed to set kern.elf64.fallback_brand", logging.FieldError, err)
	}

	return nil
}

// ensureSysrc ensures a rc.conf variable is set to a given value.
func (p *JailProvider) ensureSysrc(ctx context.Context, key, value string) error {
	// SECURITY: Prevent key injection
	if strings.Contains(key, "=") || strings.Contains(key, " ") {
		return fmt.Errorf("invalid sysrc key: %s", key)
	}
	output, err := p.cmd().CombinedOutput(ctx, "sysrc", key+"="+value)
	if err != nil {
		return fmt.Errorf("sysrc %s=%s failed: %w (output: %s)", key, value, err, string(output))
	}
	return nil
}

// setSysctlIfNotSet sets a sysctl to a value only if it is currently empty/unset.
func (p *JailProvider) setSysctlIfNotSet(ctx context.Context, key, value string) error {
	out, err := p.cmd().Output(ctx, "sysctl", "-n", key)
	if err == nil {
		current := strings.TrimSpace(string(out))
		if current != "" && current != "-1" {
			return nil // Already set
		}
	}
	output, err := p.cmd().CombinedOutput(ctx, "sysctl", key+"="+value)
	if err != nil {
		return fmt.Errorf("sysctl %s=%s failed: %w (output: %s)", key, value, err, string(output))
	}
	return nil
}

// installLinuxBase installs a Linux base system using debootstrap or other methods.
//
// Prefers pre-built rootfs images (fast extraction) over debootstrap.
func (p *JailProvider) installLinuxBase(ctx context.Context, jailPath string, osRelease *OSRelease) error {
	// Try pre-built rootfs first (much faster than debootstrap)
	if extracted, err := p.tryExtractPrebuiltRootfs(ctx, jailPath, osRelease); err != nil {
		return err
	} else if extracted {
		p.logInfo(ctx, "Linux base installed from pre-built rootfs", "version", osRelease.Version)
		return nil
	}

	switch {
	case strings.Contains(osRelease.Version, "debian"):
		return p.installDebianBase(ctx, jailPath, osRelease)
	case strings.Contains(osRelease.Version, "ubuntu"):
		return p.installUbuntuBase(ctx, jailPath, osRelease)
	case strings.Contains(osRelease.Version, "centos"),
		strings.Contains(osRelease.Version, "rocky"),
		strings.Contains(osRelease.Version, "alma"):
		return p.installRHELBase(ctx, jailPath, osRelease)
	case strings.Contains(osRelease.Version, "alpine"):
		return p.installAlpineBase(ctx, jailPath, osRelease)
	default:
		return fmt.Errorf("unsupported Linux distribution: %s", osRelease.Version)
	}
}

// tryExtractPrebuiltRootfs looks for a pre-built rootfs tarball in the image catalog
// and extracts it if found. Returns (true, nil) if extracted, (false, nil) if not found.
func (p *JailProvider) tryExtractPrebuiltRootfs(ctx context.Context, jailPath string, osRelease *OSRelease) (bool, error) {
	arch := osRelease.Arch
	if arch == "" {
		arch = "amd64"
	}

	// Parse version like "ubuntu-22.04" or "debian-12" into distro + version
	parts := strings.SplitN(osRelease.Version, "-", 2)
	if len(parts) != 2 {
		return false, nil
	}
	// Build filename: <distro>-<version>-rootfs-<arch>.tar.xz
	// e.g. ubuntu-24.04-rootfs-amd64.tar.xz, debian-12-rootfs-amd64.tar.xz
	filename := fmt.Sprintf("%s-%s-rootfs-%s.tar.xz", parts[0], parts[1], arch)

	imageDir := filepath.Join(p.config.DataDir, "images")
	if envDir := os.Getenv("HOSPITUS_IMAGE_DIR"); envDir != "" {
		imageDir = envDir
	}

	candidates := []string{
		filepath.Join(imageDir, "sets", filename),
		filepath.Join(imageDir, filename),
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}

		p.logInfo(ctx, "extracting pre-built Linux rootfs", "path", candidate, "destination", jailPath)
		output, err := p.cmd().CombinedOutput(ctx, "tar", "--no-xattrs", "-xJf", candidate, "-C", jailPath)
		if err != nil {
			return false, fmt.Errorf("failed to extract pre-built rootfs %s: %w (output: %s)", candidate, err, string(output))
		}
		return true, nil
	}

	return false, nil
}

// installDebianBase installs Debian base system using debootstrap.
func (p *JailProvider) installDebianBase(ctx context.Context, jailPath string, osRelease *OSRelease) error {
	return p.debootstrapBase(ctx, jailPath, osRelease, map[string]string{
		"debian-11": "bullseye",
		"debian-12": "bookworm",
	}, "https://deb.debian.org/debian", debianKeyrings)
}

// Archive keyrings used to verify debootstrap package signatures. FreeBSD's
// debian-archive-keyring / ubuntu-keyring packages install under
// /usr/local/share/keyrings.
var debianKeyrings = []string{
	"/usr/local/share/keyrings/debian-archive-keyring.gpg",
	"/usr/share/keyrings/debian-archive-keyring.gpg",
}

var ubuntuKeyrings = []string{
	"/usr/local/share/keyrings/ubuntu-archive-keyring.gpg",
	"/usr/share/keyrings/ubuntu-archive-keyring.gpg",
}

// validateMirrorScheme rejects non-HTTPS debootstrap mirrors so base packages
// are never fetched in cleartext.
func validateMirrorScheme(mirror string) error {
	if !strings.HasPrefix(mirror, "https://") {
		return fmt.Errorf("insecure mirror %q: only https:// mirrors are allowed", mirror)
	}
	return nil
}

// firstExistingFile returns the first path that exists, or "".
func firstExistingFile(paths []string) string {
	for _, p := range paths {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// debootstrapBase is a shared helper for Debian/Ubuntu bootstrapping.
func (p *JailProvider) debootstrapBase(ctx context.Context, jailPath string, osRelease *OSRelease, codenames map[string]string, defaultMirror string, keyrings []string) error {
	if _, err := exec.LookPath("debootstrap"); err != nil {
		return fmt.Errorf("debootstrap not found (install: pkg install debootstrap)")
	}

	codename, ok := codenames[osRelease.Version]
	if !ok {
		return fmt.Errorf("unknown version: %s", osRelease.Version)
	}

	mirror := osRelease.BaseURL
	if mirror == "" {
		mirror = defaultMirror
	}

	// SECURITY: refuse plaintext mirrors — base packages must be fetched over TLS.
	if err := validateMirrorScheme(mirror); err != nil {
		return err
	}

	// SECURITY: verify package signatures against the archive keyring instead of
	// the previous --no-check-gpg. Fail loudly if no keyring is installed rather
	// than silently bootstrapping unverified packages.
	keyring := firstExistingFile(keyrings)
	if keyring == "" {
		// Name the paths rather than a package: FreeBSD packages ubuntu-keyring,
		// which carries the Ubuntu archive keyring, but has no port for Debian's
		// — so telling the user to install debian-archive-keyring sends them
		// after something that does not exist.
		return fmt.Errorf("no archive keyring found at %s; refusing to bootstrap without GPG verification "+
			"(FreeBSD provides Ubuntu's as the ubuntu-keyring package; Debian's is not packaged and has to be "+
			"placed there by hand)", strings.Join(keyrings, " or "))
	}

	output, err := p.cmd().CombinedOutput(ctx, "debootstrap",
		"--arch=amd64",
		"--keyring="+keyring,
		"--variant=minbase",
		codename,
		jailPath,
		mirror,
	)
	if err != nil {
		return fmt.Errorf("debootstrap failed: %w (output: %s)", err, string(output))
	}

	return nil
}

// installUbuntuBase installs Ubuntu base system using debootstrap.
func (p *JailProvider) installUbuntuBase(ctx context.Context, jailPath string, osRelease *OSRelease) error {
	return p.debootstrapBase(ctx, jailPath, osRelease, map[string]string{
		"ubuntu-20.04": "focal",
		"ubuntu-22.04": "jammy",
		"ubuntu-24.04": "noble",
	}, "https://archive.ubuntu.com/ubuntu", ubuntuKeyrings)
}

// installRHELBase installs RHEL-based (CentOS/Rocky/Alma) base system using dnf.
//
// This uses dnf with --installroot to bootstrap a minimal system.
// Requires: dnf (from sysutils/dnf port)
func (p *JailProvider) installRHELBase(ctx context.Context, jailPath string, osRelease *OSRelease) error {
	// Check if dnf is available
	dnfPath, err := exec.LookPath("dnf")
	if err != nil {
		return fmt.Errorf("dnf not found (install: pkg install dnf)")
	}

	// Determine release version and distro name
	var repoName, releasePkg string
	version := "9" // default

	switch {
	case strings.Contains(osRelease.Version, "rocky-8"):
		repoName = "Rocky Linux 8"
		releasePkg = "rocky-release"
		version = "8"
	case strings.Contains(osRelease.Version, "rocky-9"):
		repoName = "Rocky Linux 9"
		releasePkg = "rocky-release"
		version = "9"
	case strings.Contains(osRelease.Version, "alma-8"):
		repoName = "AlmaLinux 8"
		releasePkg = "almalinux-release"
		version = "8"
	case strings.Contains(osRelease.Version, "alma-9"):
		repoName = "AlmaLinux 9"
		releasePkg = "almalinux-release"
		version = "9"
	case strings.Contains(osRelease.Version, "centos"):
		repoName = "CentOS Stream 9"
		releasePkg = "centos-stream-release"
		version = "9"
	}

	p.logInfo(ctx, "installing Linux base system", "distribution", repoName, "jail_path", jailPath)

	// Create required directories
	etcDirs := []string{
		filepath.Join(jailPath, "etc", "yum.repos.d"),
		filepath.Join(jailPath, "var", "lib", "rpm"),
		filepath.Join(jailPath, "var", "cache", "dnf"),
	}
	for _, dir := range etcDirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}

	// Create minimal repo file
	repoContent := fmt.Sprintf(`[baseos]
name=%s - BaseOS
baseurl=%s
enabled=1
gpgcheck=0
`, repoName, osRelease.BaseURL)

	repoFile := filepath.Join(jailPath, "etc", "yum.repos.d", "baseos.repo")
	if err := os.WriteFile(repoFile, []byte(repoContent), 0o600); err != nil {
		return fmt.Errorf("failed to create repo file: %w", err)
	}

	// Initialize RPM database
	if output, err := p.cmd().CombinedOutput(ctx, "rpm", "--root", jailPath, "--initdb"); err != nil {
		return fmt.Errorf("failed to initialize RPM database: %w (output: %s)", err, string(output))
	}

	// Install minimal system
	minimalPkgs := []string{
		releasePkg,
		"basesystem",
		"filesystem",
		"bash",
		"coreutils",
		"glibc-minimal-langpack",
	}

	args := []string{
		"-y",
		"--installroot=" + jailPath,
		"--releasever=" + version,
		"--setopt=install_weak_deps=False",
		"--nodocs",
		"install",
	}
	args = append(args, minimalPkgs...)

	output, err := p.cmd().CombinedOutput(ctx, dnfPath, args...)
	if err != nil {
		return fmt.Errorf("dnf install failed: %w (output: %s)", err, string(output))
	}

	p.logInfo(ctx, "Linux base system installed successfully", "distribution", repoName, "jail_path", jailPath)
	return nil
}

// installAlpineBase installs Alpine Linux base system.
//
// This downloads and extracts the Alpine minirootfs tarball.
// Alpine is particularly lightweight and well-suited for containers.
func (p *JailProvider) installAlpineBase(ctx context.Context, jailPath string, osRelease *OSRelease) error {
	// Extract Alpine version (e.g., "alpine-3.19" -> "3.19")
	version := strings.TrimPrefix(osRelease.Version, "alpine-")
	if version == osRelease.Version {
		return fmt.Errorf("invalid Alpine version format: %s", osRelease.Version)
	}

	// Construct minirootfs URL
	// Format: https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/alpine-minirootfs-3.19.0-x86_64.tar.gz
	arch := "x86_64"
	if osRelease.Arch == "arm64" || osRelease.Arch == "aarch64" {
		arch = "aarch64"
	}

	// Probe the two common minirootfs patch versions.
	// In production, this should query the Alpine release API
	minorVersions := []string{".1", ".0"}

	var downloadURL string
	var tarballPath string

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "hospitus-alpine-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Try to download minirootfs
	for _, minor := range minorVersions {
		fullVersion := version + minor
		downloadURL = fmt.Sprintf("https://dl-cdn.alpinelinux.org/alpine/v%s/releases/%s/alpine-minirootfs-%s-%s.tar.gz",
			version, arch, fullVersion, arch)

		tarballPath = filepath.Join(tmpDir, "alpine-minirootfs.tar.gz")

		p.logInfo(ctx, "downloading Alpine Linux minirootfs", "version", fullVersion, "arch", arch, "url", downloadURL)
		if _, err := p.cmd().CombinedOutput(ctx, "fetch", "-o", tarballPath, downloadURL); err == nil {
			break // Success
		}
		tarballPath = "" // Reset for next try
	}

	if tarballPath == "" {
		return fmt.Errorf("failed to download Alpine minirootfs from %s", downloadURL)
	}

	// Extract minirootfs
	p.logInfo(ctx, "extracting Alpine base system", "jail_path", jailPath, "tarball_path", tarballPath)
	if output, err := p.cmd().CombinedOutput(ctx, "tar", "-xzf", tarballPath, "-C", jailPath); err != nil {
		return fmt.Errorf("failed to extract Alpine minirootfs: %w (output: %s)", err, string(output))
	}

	// Configure Alpine repositories
	repoFile := filepath.Join(jailPath, "etc", "apk", "repositories")
	repoContent := fmt.Sprintf(`https://dl-cdn.alpinelinux.org/alpine/v%s/main
https://dl-cdn.alpinelinux.org/alpine/v%s/community
`, version, version)

	if err := os.WriteFile(repoFile, []byte(repoContent), 0o600); err != nil {
		return fmt.Errorf("failed to configure Alpine repositories: %w", err)
	}

	p.logInfo(ctx, "Alpine Linux base system installed successfully", "version", version, "jail_path", jailPath)
	return nil
}

// configureLinuxMounts sets jail parameters and fstab required for Linux jails.
//
// This follows Bastille/CBSD practices:
//   - allow.mount + allow.mount.* flags (required for linprocfs, linsysfs, tmpfs, fdescfs, devfs)
//   - enforce_statfs = 1 (less restrictive, needed for Linux userland)
//   - devfs_ruleset = 4
//   - devfs, linprocfs, linsysfs, fdescfs (with linrdlnk), tmpfs (including /tmp)
func (p *JailProvider) configureLinuxMounts(config *jailConfig) error {
	fstabPath := filepath.Join(p.stateDir, "fstab", config.Name)
	config.JailParameters.MountFstab = fstabPath

	// jail(8) only supports mount.devfs, mount.fdescfs, mount.procfs,
	// and mount.fstab. linprocfs, linsysfs, and tmpfs are mounted via fstab.
	config.JailParameters.MountDevfs = true
	config.JailParameters.DevfsRuleset = 4

	// Allow mounting filesystems inside the jail (required for Linux functionality)
	config.JailParameters.AllowMount = true
	config.JailParameters.AllowMountDevfs = true
	config.JailParameters.AllowMountLinprocfs = true
	config.JailParameters.AllowMountLinsysfs = true
	config.JailParameters.AllowMountTmpfs = true
	config.JailParameters.AllowMountFdescfs = true
	config.JailParameters.AllowMountNullfs = true

	// Linux userland needs less restrictive statfs (df, mount, etc.)
	config.JailParameters.EnforceStatfs = 1

	// Ensure fstab directory exists
	if err := os.MkdirAll(filepath.Dir(fstabPath), 0o755); err != nil {
		return fmt.Errorf("failed to create fstab directory: %w", err)
	}

	// Create fstab for Linux mounts
	// Bastille mounts: devfs, tmpfs (shm), fdescfs (linrdlnk), linprocfs, linsysfs, tmpfs (/run, /tmp)
	fstabContent := fmt.Sprintf(`# Linux jail mounts
devfs		%s/dev		devfs		rw				0	0
tmpfs		%s/dev/shm	tmpfs		rw,mode=1777,size=1g		0	0
fdescfs		%s/dev/fd	fdescfs		rw,linrdlnk			0	0
linprocfs	%s/proc		linprocfs	rw				0	0
linsysfs	%s/sys		linsysfs	rw				0	0
tmpfs		%s/run		tmpfs		rw,mode=0755,size=100m		0	0
tmpfs		%s/tmp		tmpfs		rw,mode=1777,size=1g		0	0
`, config.Path, config.Path, config.Path, config.Path, config.Path, config.Path, config.Path)

	if err := os.WriteFile(fstabPath, []byte(fstabContent), 0o600); err != nil {
		return fmt.Errorf("failed to create fstab: %w", err)
	}

	return nil
}
