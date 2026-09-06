package jail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxReleasesMap(t *testing.T) {
	// Verify all expected distributions are present
	expectedDistros := []struct {
		key     string
		osType  string
		version string
		arch    string
	}{
		{"debian-11-amd64", "linux", "debian-11", "amd64"},
		{"debian-12-amd64", "linux", "debian-12", "amd64"},
		{"ubuntu-20.04-amd64", "linux", "ubuntu-20.04", "amd64"},
		{"ubuntu-22.04-amd64", "linux", "ubuntu-22.04", "amd64"},
		{"centos-9-amd64", "linux", "centos-9", "amd64"},
		{"rocky-8-amd64", "linux", "rocky-8", "amd64"},
		{"rocky-9-amd64", "linux", "rocky-9", "amd64"},
		{"alma-8-amd64", "linux", "alma-8", "amd64"},
		{"alma-9-amd64", "linux", "alma-9", "amd64"},
		{"alpine-3.18-amd64", "linux", "alpine-3.18", "amd64"},
		{"alpine-3.20-amd64", "linux", "alpine-3.20", "amd64"},
		{"alpine-3.21-amd64", "linux", "alpine-3.21", "amd64"},
	}

	for _, tt := range expectedDistros {
		t.Run(tt.key, func(t *testing.T) {
			release, ok := linuxReleases[tt.key]
			if !ok {
				t.Fatalf("linuxReleases missing key %q", tt.key)
			}
			if release.Type != tt.osType {
				t.Errorf("Type = %q, want %q", release.Type, tt.osType)
			}
			if release.Version != tt.version {
				t.Errorf("Version = %q, want %q", release.Version, tt.version)
			}
			if release.Arch != tt.arch {
				t.Errorf("Arch = %q, want %q", release.Arch, tt.arch)
			}
			if release.BaseURL == "" {
				t.Error("BaseURL should not be empty")
			}
		})
	}
}

func TestLinuxReleasesAllLinuxType(t *testing.T) {
	for key, release := range linuxReleases {
		if release.Type != "linux" {
			t.Errorf("linuxReleases[%q].Type = %q, want \"linux\"", key, release.Type)
		}
	}
}

func TestLinuxReleasesBaseURLsReachable(t *testing.T) {
	// Verify URLs are well-formed (not actually fetching them)
	for key, release := range linuxReleases {
		if !strings.HasPrefix(release.BaseURL, "http://") && !strings.HasPrefix(release.BaseURL, "https://") {
			t.Errorf("linuxReleases[%q].BaseURL = %q, must start with http:// or https://", key, release.BaseURL)
		}
	}
}

func TestConfigureLinuxMounts(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	fstabDir := filepath.Join(stateDir, "fstab")
	if err := os.MkdirAll(fstabDir, 0o755); err != nil {
		t.Fatalf("failed to create fstab dir: %v", err)
	}

	p := &JailProvider{stateDir: stateDir}

	config := &jailConfig{
		Name:           "linux-test",
		Path:           "/zroot/hospitus/jails/linux-test/root",
		JailParameters: DefaultJailParameters(),
	}

	if err := p.configureLinuxMounts(config); err != nil {
		t.Fatalf("configureLinuxMounts failed: %v", err)
	}

	// Verify jail parameters were set correctly
	if !config.JailParameters.MountDevfs {
		t.Error("MountDevfs should be true")
	}
	if config.JailParameters.DevfsRuleset != 4 {
		t.Errorf("DevfsRuleset = %d, want 4", config.JailParameters.DevfsRuleset)
	}
	if !config.JailParameters.AllowMount {
		t.Error("AllowMount should be true")
	}
	if !config.JailParameters.AllowMountDevfs {
		t.Error("AllowMountDevfs should be true")
	}
	if !config.JailParameters.AllowMountLinprocfs {
		t.Error("AllowMountLinprocfs should be true")
	}
	if !config.JailParameters.AllowMountLinsysfs {
		t.Error("AllowMountLinsysfs should be true")
	}
	if !config.JailParameters.AllowMountTmpfs {
		t.Error("AllowMountTmpfs should be true")
	}
	if !config.JailParameters.AllowMountFdescfs {
		t.Error("AllowMountFdescfs should be true")
	}
	if !config.JailParameters.AllowMountNullfs {
		t.Error("AllowMountNullfs should be true")
	}
	if config.JailParameters.EnforceStatfs != 1 {
		t.Errorf("EnforceStatfs = %d, want 1", config.JailParameters.EnforceStatfs)
	}

	// Verify fstab was created
	fstabPath := filepath.Join(stateDir, "fstab", "linux-test")
	if config.JailParameters.MountFstab != fstabPath {
		t.Errorf("MountFstab = %q, want %q", config.JailParameters.MountFstab, fstabPath)
	}

	data, err := os.ReadFile(fstabPath)
	if err != nil {
		t.Fatalf("failed to read fstab: %v", err)
	}

	fstab := string(data)
	jailPath := config.Path

	// Verify fstab contains required mount entries
	requiredEntries := []struct {
		fstype string
		mount  string
	}{
		{"devfs", jailPath + "/dev"},
		{"tmpfs", jailPath + "/dev/shm"},
		{"fdescfs", jailPath + "/dev/fd"},
		{"linprocfs", jailPath + "/proc"},
		{"linsysfs", jailPath + "/sys"},
		{"tmpfs", jailPath + "/run"},
		{"tmpfs", jailPath + "/tmp"},
	}

	for _, entry := range requiredEntries {
		if !strings.Contains(fstab, entry.mount) {
			t.Errorf("fstab missing mount point %q", entry.mount)
		}
		if !strings.Contains(fstab, entry.fstype) {
			t.Errorf("fstab missing filesystem type %q", entry.fstype)
		}
	}

	// Verify fdescfs has linrdlnk option
	if !strings.Contains(fstab, "linrdlnk") {
		t.Error("fstab missing linrdlnk option for fdescfs")
	}
}

func TestCopyResolvConf(t *testing.T) {
	// Skip if /etc/resolv.conf doesn't exist (e.g., in some CI)
	if _, err := os.Stat("/etc/resolv.conf"); err != nil {
		t.Skip("Skipping: /etc/resolv.conf not available")
	}

	tmpDir := t.TempDir()
	jailPath := filepath.Join(tmpDir, "root")

	// Create etc directory
	if err := os.MkdirAll(filepath.Join(jailPath, "etc"), 0o755); err != nil {
		t.Fatalf("failed to create etc dir: %v", err)
	}

	p := &JailProvider{}
	if err := p.copyResolvConf(jailPath); err != nil {
		t.Fatalf("copyResolvConf failed: %v", err)
	}

	// Verify file was copied
	dst := filepath.Join(jailPath, "etc", "resolv.conf")
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("resolv.conf not copied: %v", err)
	}

	// Verify content matches host
	hostData, _ := os.ReadFile("/etc/resolv.conf")
	jailData, _ := os.ReadFile(dst)
	if string(hostData) != string(jailData) {
		t.Error("copied resolv.conf does not match host")
	}
}

// TestCopyResolvConfReplacesSymlink verifies that a pre-existing resolv.conf
// symlink (common in Linux rootfs images) is replaced by a real file rather
// than written through.
func TestCopyResolvConfReplacesSymlink(t *testing.T) {
	if _, err := os.Stat("/etc/resolv.conf"); err != nil {
		t.Skip("Skipping: /etc/resolv.conf not available")
	}

	tmpDir := t.TempDir()
	jailPath := filepath.Join(tmpDir, "root")
	etcDir := filepath.Join(jailPath, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		t.Fatalf("failed to create etc dir: %v", err)
	}

	// Simulate a Linux rootfs that ships resolv.conf as a dangling symlink.
	dst := filepath.Join(etcDir, "resolv.conf")
	if err := os.Symlink("../run/systemd/resolve/stub-resolv.conf", dst); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	p := &JailProvider{}
	if err := p.copyResolvConf(jailPath); err != nil {
		t.Fatalf("copyResolvConf failed: %v", err)
	}

	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("resolv.conf not present: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("resolv.conf is still a symlink; expected a real file")
	}
	hostData, _ := os.ReadFile("/etc/resolv.conf")
	jailData, _ := os.ReadFile(dst)
	if string(hostData) != string(jailData) {
		t.Error("copied resolv.conf does not match host")
	}
}

func TestFixupDebianFamily(t *testing.T) {
	tmpDir := t.TempDir()
	jailPath := filepath.Join(tmpDir, "root")

	// Create required directories
	aptDir := filepath.Join(jailPath, "etc", "apt", "apt.conf.d")
	if err := os.MkdirAll(aptDir, 0o755); err != nil {
		t.Fatalf("failed to create apt dir: %v", err)
	}
	etcDir := filepath.Join(jailPath, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		t.Fatalf("failed to create etc dir: %v", err)
	}

	p := &JailProvider{}
	ctx := t.Context()

	if err := p.fixupDebianFamily(ctx, jailPath); err != nil {
		t.Fatalf("fixupDebianFamily failed: %v", err)
	}

	// Check APT cache config was created
	aptConfig := filepath.Join(aptDir, "00freebsd")
	data, err := os.ReadFile(aptConfig)
	if err != nil {
		t.Fatalf("APT config not created: %v", err)
	}
	if !strings.Contains(string(data), "APT::Cache-Start") {
		t.Error("APT config missing Cache-Start directive")
	}
}

func TestEnsureSysrcKeyValidation(t *testing.T) {
	p := &JailProvider{}
	ctx := t.Context()

	// Keys with = or space should be rejected
	if err := p.ensureSysrc(ctx, "bad=key", "val"); err == nil {
		t.Error("Expected error for key containing '='")
	}
	if err := p.ensureSysrc(ctx, "bad key", "val"); err == nil {
		t.Error("Expected error for key containing space")
	}
}
