package validation

import (
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestValidateInstanceName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		shouldErr bool
	}{
		{"valid simple name", "test-vm", false},
		{"valid with numbers", "vm-123", false},
		{"valid with underscore", "test_vm_1", false},
		{"valid max length", "a12345678901234567890123456789012345678901234567890123456789012", false},
		{"empty name", "", true},
		{"too long", "a1234567890123456789012345678901234567890123456789012345678901234", true},
		{"path traversal", "../etc/passwd", true},
		{"command injection semicolon", "test;rm -rf /", true},
		{"command injection pipe", "test|cat /etc/passwd", true},
		{"command injection backtick", "test`whoami`", true},
		{"command injection dollar", "test$(whoami)", true},
		{"forward slash", "test/vm", true},
		{"backslash", "test\\vm", true},
		{"starts with dash", "-test", true},
		{"special chars", "test@vm", true},
		{"spaces", "test vm", true},
		{"newline", "test\nvm", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInstanceName(tt.input)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error for input %q but got none", tt.input)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error for input %q but got: %v", tt.input, err)
			}
		})
	}
}

func TestValidateProviderName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		shouldErr bool
	}{
		{"valid", "qemu", false},
		{"valid with dash", "my-provider", false},
		{"empty", "", true},
		{"uppercase", "QEMU", true},
		{"too long", "averylongprovidernamethatiswaymorethan32characters", true},
		{"starts with dash", "-provider", true},
		{"special chars", "provider@123", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProviderName(tt.input)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error for input %q but got none", tt.input)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error for input %q but got: %v", tt.input, err)
			}
		})
	}
}

func TestValidateResourceLimits(t *testing.T) {
	tests := []struct {
		name      string
		cpus      int
		memoryMB  int64
		shouldErr bool
	}{
		{"valid minimal", 1, 128, false},
		{"valid normal", 4, 4096, false},
		{"valid large", 64, 65536, false},
		{"zero cpus", 0, 1024, true},
		{"negative cpus", -1, 1024, true},
		{"too many cpus", 2000, 1024, true},
		{"too little memory", 1, 64, true},
		{"too much memory", 1, 5 * 1024 * 1024, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateResourceLimits(tt.cpus, tt.memoryMB)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error but got none")
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}
		})
	}
}

func TestValidateLabel(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		value     string
		shouldErr bool
	}{
		{"valid simple", "env", "production", false},
		{"valid with dash", "app-name", "myapp", false},
		{"empty key", "", "value", true},
		{"key too long", strings.Repeat("a", 254), "value", true},
		{"value too long", "key", strings.Repeat("a", 254), true},
		{"sql injection in value", "key", "'; DROP TABLE instances; --", true},
		{"backslash in value", "key", "test\\value", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateLabel(tt.key, tt.value)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error but got none")
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}
		})
	}
}

func TestValidateIPAddress(t *testing.T) {
	tests := []struct {
		name      string
		ip        string
		shouldErr bool
	}{
		// Valid IPv4 addresses
		{"valid ipv4", "192.168.1.1", false},
		{"valid ipv4 zero", "0.0.0.0", false},
		{"valid ipv4 localhost", "127.0.0.1", false},
		{"valid ipv4 broadcast", "255.255.255.255", false},
		{"valid ipv4 private", "10.0.0.1", false},
		{"valid ipv4 private b", "172.16.0.1", false},

		// Valid IPv6 addresses
		{"valid ipv6 abbreviated", "2001:db8::1", false},
		{"valid ipv6 loopback", "::1", false},
		{"valid ipv6 full", "2001:0db8:0000:0000:0000:0000:0000:0001", false},
		{"valid ipv6 link-local", "fe80::1", false},
		{"valid ipv6 all zeros", "::", false},
		{"valid ipv6 all ones", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"valid ipv6 mixed case", "2001:DB8::1", false},
		{"valid ipv6 embedded ipv4", "::ffff:192.168.1.1", false},

		// Invalid addresses
		{"empty", "", true},
		{"invalid ipv4 octet", "192.168.1.256", true},
		{"invalid format", "not-an-ip", true},
		{"invalid ipv4 extra octet", "1.2.3.4.5", true},
		{"invalid ipv4 missing octet", "1.2.3", true},
		// Note: Leading zeros are rejected by net.ParseIP (Go 1.17+, RFC 6943)
		{"invalid ipv4 leading zeros", "192.168.001.001", true},
		{"invalid ipv4 negative", "-1.2.3.4", true},
		{"invalid ipv6 double colon twice", "2001::db8::1", true},
		{"invalid ipv6 too many segments", "1:2:3:4:5:6:7:8:9", true},
		{"invalid ipv6 segment too large", "fffff::1", true},
		{"invalid ipv4 with port", "192.168.1.1:8080", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateIPAddress(tt.ip)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error for IP %q but got none", tt.ip)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error for IP %q but got: %v", tt.ip, err)
			}
		})
	}
}

func TestValidateJailParameter(t *testing.T) {
	tests := []struct {
		name      string
		param     string
		shouldErr bool
	}{
		{"allowed parameter", "host.hostname", false},
		{"allowed ip4", "ip4.addr", false},
		{"disallowed parameter", "exec.clean", true},
		{"malicious parameter", "exec.prestart", true},
		// A database needs System V shared memory, and the safe way to give it
		// is a namespace of the jail's own rather than the host's.
		{"per-jail sysv shm", "sysvshm", false},
		{"per-jail sysv sem", "sysvsem", false},
		{"per-jail sysv msg", "sysvmsg", false},
		// Permission, yes; a mount jail(8) performs, no — it has none for
		// linprocfs and refuses to start when handed one.
		{"linprocfs permission", "allow.mount.linprocfs", false},
		{"linprocfs mount", "mount.linprocfs", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateJailParameter(tt.param)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error for param %q but got none", tt.param)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error for param %q but got: %v", tt.param, err)
			}
		})
	}
}

func TestValidateJailParameterValue(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		shouldErr bool
	}{
		{"valid hostname", "myjail", false},
		{"valid path", "/usr/local/jails/myjail", false},
		{"command injection semicolon", "test; rm -rf /", true},
		{"command injection pipe", "test | cat /etc/passwd", true},
		{"shell expansion", "$(whoami)", true},
		{"backtick expansion", "`whoami`", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateJailParameterValue(tt.value)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error for value %q but got none", tt.value)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error for value %q but got: %v", tt.value, err)
			}
		})
	}
}

func TestValidateSnapshotName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		shouldErr bool
	}{
		// Valid names
		{"simple", "snapshot1", false},
		{"with dot", "backup.1", false},
		{"version style", "v1.0.0", false},
		{"date style", "2024-01-01", false},
		{"mixed", "backup_2024-01-01.v1", false},

		// Invalid names
		{"empty", "", true},
		{"too long", strings.Repeat("a", 64), true},
		{"path traversal", "../../etc", true},
		{"starts with slash", "/snapshot", true},
		{"starts with backslash", "\\snapshot", true},
		{"command injection semicolon", "snap;rm", true},
		{"command injection pipe", "snap|cat", true},
		{"starts with dot", ".hidden", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSnapshotName(tt.input)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error for input %q but got none", tt.input)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error for input %q but got: %v", tt.input, err)
			}
		})
	}
}

func TestValidateFilePath(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		allowAbsolute bool
		shouldErr     bool
	}{
		// Valid paths
		{"relative simple", "file.txt", false, false},
		{"relative nested", "config/app.conf", false, false},
		{"absolute allowed", "/etc/config", true, false},
		{"absolute nested allowed", "/var/lib/hospitus/data", true, false},

		// Invalid paths
		{"empty", "", false, true},
		{"path traversal simple", "../passwd", false, true},
		{"path traversal hidden", "config/../../../etc", false, true},
		{"path traversal in middle", "a/b/../../../c", false, true},
		{"absolute not allowed", "/etc/passwd", false, true},
		{"null byte attack", "file\x00.txt", false, true},
		{"backslash absolute", "\\etc\\passwd", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFilePath(tt.path, tt.allowAbsolute)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error for path %q (allowAbsolute=%v) but got none", tt.path, tt.allowAbsolute)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error for path %q (allowAbsolute=%v) but got: %v", tt.path, tt.allowAbsolute, err)
			}
		})
	}
}

func TestValidateEnvVar(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		value     string
		shouldErr bool
	}{
		{"valid simple", "MY_VAR", "value", false},
		{"valid with numbers", "VAR_1", "hello", false},
		{"valid empty value", "MY_VAR", "", false},
		{"empty key", "", "val", true},
		{"key starts with digit", "1VAR", "val", true},
		{"key with dash", "MY-VAR", "val", true},
		{"dangerous PATH", "PATH", "/usr/bin", true},
		{"dangerous LD_PRELOAD", "LD_PRELOAD", "evil.so", true},
		{"dangerous LD_LIBRARY_PATH", "LD_LIBRARY_PATH", "/tmp", true},
		{"dangerous DYLD_INSERT_LIBRARIES", "DYLD_INSERT_LIBRARIES", "x", true},
		{"dangerous DYLD_LIBRARY_PATH", "DYLD_LIBRARY_PATH", "x", true},
		{"dangerous IFS", "IFS", "x", true},
		{"dangerous SHELL", "SHELL", "/bin/sh", true},
		{"dangerous ENV", "ENV", "x", true},
		{"dangerous BASH_ENV", "BASH_ENV", "x", true},
		{"value with null byte", "MY_VAR", "val\x00ue", true},
		{"value with control char", "MY_VAR", "val\x01ue", true},
		{"value with tab allowed", "MY_VAR", "val\tue", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEnvVar(tt.key, tt.value)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error for key=%q value=%q but got none", tt.key, tt.value)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error for key=%q value=%q but got: %v", tt.key, tt.value, err)
			}
		})
	}
}

func TestValidateInterfaceName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		shouldErr bool
	}{
		{"valid em0", "em0", false},
		{"valid vtnet0", "vtnet0", false},
		{"valid bridge0", "bridge0", false},
		{"valid hospitus0", "hospitus0", false},
		{"valid underscore", "vlan_0", false},
		{"empty", "", true},
		{"too long 16", "abcdefghijklmnop", true},
		{"starts with digit", "0eth", true},
		{"contains space", "em 0", true},
		{"contains semicolon", "em0;rm", true},
		{"contains pipe", "em0|cat", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInterfaceName(tt.input)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error for %q but got none", tt.input)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error for %q but got: %v", tt.input, err)
			}
		})
	}
}

func TestValidateBridgeName(t *testing.T) {
	if err := ValidateBridgeName("hospitus0"); err != nil {
		t.Errorf("Valid bridge name failed: %v", err)
	}
	if err := ValidateBridgeName(""); err == nil {
		t.Error("Empty bridge name should fail")
	}
	if err := ValidateBridgeName("bad name!"); err == nil {
		t.Error("Invalid bridge name should fail")
	}
}

func TestIsShellInterpreter(t *testing.T) {
	tests := []struct {
		cmd    string
		expect bool
	}{
		{"sh", true},
		{"bash", true},
		{"dash", true},
		{"zsh", true},
		{"ksh", true},
		{"csh", true},
		{"tcsh", true},
		{"/bin/sh", true},
		{"/usr/bin/bash", true},
		{"/usr/local/bin/zsh", true},
		{"python", false},
		{"ls", false},
		{"nginx", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := IsShellInterpreter(tt.cmd)
			if got != tt.expect {
				t.Errorf("IsShellInterpreter(%q) = %v, want %v", tt.cmd, got, tt.expect)
			}
		})
	}
}

func TestValidateExecCommand(t *testing.T) {
	tests := []struct {
		name      string
		cmd       string
		shouldErr bool
	}{
		{"valid absolute path", "/usr/bin/ls", false},
		{"valid relative", "ls", false},
		{"valid with slash", "/bin/sh", false},
		{"empty", "", true},
		{"contains semicolon", "ls;rm", true},
		{"contains pipe", "ls|grep", true},
		{"contains backtick", "ls`whoami`", true},
		{"contains dollar paren", "ls$(whoami)", true},
		{"contains newline", "ls\nrm", true},
		{"path traversal", "/etc/../passwd", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExecCommand(tt.cmd)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error for %q but got none", tt.cmd)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error for %q but got: %v", tt.cmd, err)
			}
		})
	}
}

// TestInterfaceNameAcceptsADash covers the names hospitus and FreeBSD actually
// use.
//
// hospitus creates hospitus-nat for bhyve NAT and podman creates cni-podman0,
// so a manifest has to be able to name a bridge carrying a dash.
func TestInterfaceNameAcceptsADash(t *testing.T) {
	tests := []struct {
		name    string
		iface   string
		wantErr bool
	}{
		{"plain", "hospitus0", false},
		{"the daemon's own NAT bridge", "hospitus-nat", false},
		{"podman's bridge", "cni-podman0", false},
		{"underscore", "my_bridge", false},
		{"leading dash", "-bridge", true},
		{"too long", "abcdefghijklmnopq", true},
		{"a space", "my bridge", true},
		{"a semicolon", "br;id", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInterfaceName(tt.iface)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateInterfaceName(%q) error = %v, want error: %v", tt.iface, err, tt.wantErr)
			}
		})
	}
}

// TestNetworkSpecRequiresPrefixLength covers an address written without one.
//
// ifconfig falls back to the classful mask, so 10.0.0.10 configures a /8: the
// instance treats all of 10.0.0.0/8 as on-link and cannot reach the rest of it
// through its gateway. The address looks right and nothing reports the mask.
func TestNetworkSpecRequiresPrefixLength(t *testing.T) {
	tests := []struct {
		name    string
		spec    provider.NetworkSpec
		wantErr bool
	}{
		{"ipv4 with prefix", provider.NetworkSpec{IPv4: "10.0.0.10/24"}, false},
		{"ipv4 without prefix", provider.NetworkSpec{IPv4: "10.0.0.10"}, true},
		{"dhcp", provider.NetworkSpec{IPv4: "dhcp"}, false},
		{"no address at all", provider.NetworkSpec{}, false},
		{"ipv6 with prefix", provider.NetworkSpec{IPv6: "fd00::42/64"}, false},
		{"ipv6 without prefix", provider.NetworkSpec{IPv6: "fd00::42"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNetworkSpec(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateNetworkSpec(%+v) error = %v, want error: %v", tt.spec, err, tt.wantErr)
			}
		})
	}
}

func TestValidateExecArgument(t *testing.T) {
	tests := []struct {
		name      string
		arg       string
		shouldErr bool
	}{
		{"valid simple", "hello", false},
		{"valid flag", "--help", false},
		{"valid with equals", "--name=test", false},

		// An argument goes to execve as one element of argv, or reaches ssh
		// shell-quoted. Neither interprets these, and refusing them made the
		// documented way to reconfigure nginx in a jail impossible to run.
		{"sed script", "s/listen       80;/listen       8080;/", false},
		{"awk program", "{print $1}", false},
		{"relative path", "../etc/rc.conf", false},
		{"quoted string", `say "hello"`, false},

		{"null byte", "arg\x00bad", true},
		{"carriage return", "arg\rbad", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExecArgument(tt.arg)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error for %q but got none", tt.arg)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error for %q but got: %v", tt.arg, err)
			}
		})
	}
}

func TestValidateUsername(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		shouldErr bool
	}{
		{"valid root", "root", false},
		{"valid www", "www", false},
		{"valid with dash", "john-doe", false},
		{"valid with underscore", "john_doe", false},
		{"empty", "", true},
		{"starts with digit", "1user", true},
		{"starts with upper", "Root", true},
		{"valid single char", "a", false},
		{"contains space", "john doe", true},
		{"contains at sign", "user@host", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUsername(tt.input)
			if tt.shouldErr && err == nil {
				t.Errorf("Expected error for %q but got none", tt.input)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("Expected no error for %q but got: %v", tt.input, err)
			}
		})
	}
}

func TestValidateExecOptions(t *testing.T) {
	t.Run("valid basic command", func(t *testing.T) {
		opts := provider.ExecOptions{
			Command: "/bin/ls",
			Args:    []string{"-la"},
		}
		if err := ValidateExecOptions(opts); err != nil {
			t.Errorf("Expected no error: %v", err)
		}
	})
	t.Run("shell mode allows metacharacters in -c arg", func(t *testing.T) {
		opts := provider.ExecOptions{
			Command: "/bin/sh",
			Args:    []string{"-c", "echo hello | cat"},
		}
		if err := ValidateExecOptions(opts); err != nil {
			t.Errorf("Expected no error for shell -c: %v", err)
		}
	})
	t.Run("invalid command injection", func(t *testing.T) {
		opts := provider.ExecOptions{Command: "ls;rm"}
		if err := ValidateExecOptions(opts); err == nil {
			t.Error("Expected error for injection in command")
		}
	})
	t.Run("valid with user", func(t *testing.T) {
		opts := provider.ExecOptions{
			Command: "/bin/ls",
			User:    "www",
		}
		if err := ValidateExecOptions(opts); err != nil {
			t.Errorf("Expected no error: %v", err)
		}
	})
	t.Run("dangerous env var LD_PRELOAD", func(t *testing.T) {
		opts := provider.ExecOptions{
			Command: "/bin/ls",
			Env:     map[string]string{"LD_PRELOAD": "evil.so"},
		}
		if err := ValidateExecOptions(opts); err == nil {
			t.Error("Expected error for LD_PRELOAD")
		}
	})
}

func TestValidateDiskSpec(t *testing.T) {
	t.Run("valid empty", func(t *testing.T) {
		if err := ValidateDiskSpec(provider.DiskSpec{}); err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
	})
	t.Run("valid with fields", func(t *testing.T) {
		err := ValidateDiskSpec(provider.DiskSpec{ID: "disk0", Path: "/dev/da0", DeviceName: "da0"})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
	})
	t.Run("disk ID injection", func(t *testing.T) {
		if err := ValidateDiskSpec(provider.DiskSpec{ID: "disk;rm"}); err == nil {
			t.Error("Expected error for injection in disk ID")
		}
	})
	t.Run("disk path traversal", func(t *testing.T) {
		if err := ValidateDiskSpec(provider.DiskSpec{Path: "/../etc/passwd"}); err == nil {
			t.Error("Expected error for path traversal in disk path")
		}
	})
	t.Run("device name injection", func(t *testing.T) {
		if err := ValidateDiskSpec(provider.DiskSpec{DeviceName: "dev|bad"}); err == nil {
			t.Error("Expected error for injection in device name")
		}
	})
}

func TestValidateNetworkSpec(t *testing.T) {
	t.Run("valid empty", func(t *testing.T) {
		if err := ValidateNetworkSpec(provider.NetworkSpec{}); err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
	})
	t.Run("network ID injection", func(t *testing.T) {
		if err := ValidateNetworkSpec(provider.NetworkSpec{ID: "net;bad"}); err == nil {
			t.Error("Expected error for injection in network ID")
		}
	})
}

func TestValidateInstanceSpec(t *testing.T) {
	t.Run("valid minimal", func(t *testing.T) {
		if err := ValidateInstanceSpec(provider.InstanceSpec{Name: "myvm"}); err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
	})
	t.Run("invalid name", func(t *testing.T) {
		if err := ValidateInstanceSpec(provider.InstanceSpec{Name: "bad;name"}); err == nil {
			t.Error("Expected error for bad name")
		}
	})
	t.Run("description too long", func(t *testing.T) {
		err := ValidateInstanceSpec(provider.InstanceSpec{Name: "vm", Description: strings.Repeat("a", 1001)})
		if err == nil {
			t.Error("Expected error for too-long description")
		}
	})
	t.Run("image with path traversal", func(t *testing.T) {
		if err := ValidateInstanceSpec(provider.InstanceSpec{Name: "vm", Image: "../etc/evil"}); err == nil {
			t.Error("Expected error for path traversal in image")
		}
	})
	t.Run("invalid disk in spec", func(t *testing.T) {
		err := ValidateInstanceSpec(provider.InstanceSpec{
			Name:  "vm",
			Disks: []provider.DiskSpec{{ID: "bad;disk"}},
		})
		if err == nil {
			t.Error("Expected error for bad disk in spec")
		}
	})
}

func TestValidateSnapshotName_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"too long", strings.Repeat("a", 64), true},
		{"path traversal", "a..b", true},
		{"starts with slash", "/snap", true},
		{"injection semicolon", "snap;rm", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSnapshotName(tc.input)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for %q", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for %q: %v", tc.input, err)
			}
		})
	}
}

func TestValidateExecOptions_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		opts    provider.ExecOptions
		wantErr bool
	}{
		{
			name:    "empty command",
			opts:    provider.ExecOptions{Command: ""},
			wantErr: true,
		},
		{
			name:    "invalid user",
			opts:    provider.ExecOptions{Command: "/bin/echo", User: "bad;user"},
			wantErr: true,
		},
		{
			name:    "invalid working dir",
			opts:    provider.ExecOptions{Command: "/bin/echo", WorkingDir: "../escape"},
			wantErr: true,
		},
		{
			name: "invalid env var",
			opts: provider.ExecOptions{
				Command: "/bin/echo",
				Env:     map[string]string{"bad=key": "value"},
			},
			wantErr: true,
		},
		{
			name:    "valid",
			opts:    provider.ExecOptions{Command: "/bin/echo", Args: []string{"hello"}},
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateExecOptions(tc.opts)
			if tc.wantErr && err == nil {
				t.Errorf("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateInstanceSpec_ExtraFields(t *testing.T) {
	t.Run("ostype injection", func(t *testing.T) {
		if err := ValidateInstanceSpec(provider.InstanceSpec{Name: "vm", OSType: "bad;os"}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("osversion injection", func(t *testing.T) {
		if err := ValidateInstanceSpec(provider.InstanceSpec{Name: "vm", OSVersion: "bad|version"}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("arch injection", func(t *testing.T) {
		if err := ValidateInstanceSpec(provider.InstanceSpec{Name: "vm", Arch: "bad&arch"}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("bootloader injection", func(t *testing.T) {
		if err := ValidateInstanceSpec(provider.InstanceSpec{Name: "vm", Bootloader: "bad`boot"}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("provider config injection", func(t *testing.T) {
		if err := ValidateInstanceSpec(provider.InstanceSpec{
			Name:           "vm",
			ProviderConfig: map[string]interface{}{"key": "bad;value"},
		}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("annotation key too long", func(t *testing.T) {
		if err := ValidateInstanceSpec(provider.InstanceSpec{
			Name:        "vm",
			Annotations: map[string]string{strings.Repeat("a", 254): "val"},
		}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("annotation value too long", func(t *testing.T) {
		if err := ValidateInstanceSpec(provider.InstanceSpec{
			Name:        "vm",
			Annotations: map[string]string{"key": strings.Repeat("a", 1001)},
		}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("invalid network in spec", func(t *testing.T) {
		if err := ValidateInstanceSpec(provider.InstanceSpec{
			Name:     "vm",
			Networks: []provider.NetworkSpec{{ID: "bad;net"}},
		}); err == nil {
			t.Error("expected error")
		}
	})
}

// ---------------------------------------------------------------------------
// Additional tests for uncovered branches in validation functions
// ---------------------------------------------------------------------------

func TestValidateNetworkSpec_AllPaths(t *testing.T) {
	t.Run("valid bridge", func(t *testing.T) {
		if err := ValidateNetworkSpec(provider.NetworkSpec{Bridge: "hospitus0"}); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("invalid bridge name", func(t *testing.T) {
		if err := ValidateNetworkSpec(provider.NetworkSpec{Bridge: "bad;bridge"}); err == nil {
			t.Error("expected error for bad bridge name")
		}
	})
	t.Run("valid MAC", func(t *testing.T) {
		if err := ValidateNetworkSpec(provider.NetworkSpec{MAC: "52:54:00:11:22:33"}); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("invalid MAC format", func(t *testing.T) {
		if err := ValidateNetworkSpec(provider.NetworkSpec{MAC: "not-a-mac"}); err == nil {
			t.Error("expected error for invalid MAC")
		}
	})
	t.Run("valid IPv4 CIDR", func(t *testing.T) {
		if err := ValidateNetworkSpec(provider.NetworkSpec{IPv4: "192.168.1.10/24"}); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("dhcp IPv4 special value", func(t *testing.T) {
		if err := ValidateNetworkSpec(provider.NetworkSpec{IPv4: "dhcp"}); err != nil {
			t.Errorf("dhcp should be valid: %v", err)
		}
	})
	t.Run("invalid IPv4", func(t *testing.T) {
		if err := ValidateNetworkSpec(provider.NetworkSpec{IPv4: "999.999.999.999"}); err == nil {
			t.Error("expected error for invalid IPv4")
		}
	})
	t.Run("valid IPv6", func(t *testing.T) {
		if err := ValidateNetworkSpec(provider.NetworkSpec{IPv6: "fd00::1/48"}); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("invalid IPv6", func(t *testing.T) {
		if err := ValidateNetworkSpec(provider.NetworkSpec{IPv6: "not-ipv6"}); err == nil {
			t.Error("expected error for invalid IPv6")
		}
	})
}

func TestValidateInstanceName_AllPaths(t *testing.T) {
	t.Run("too long", func(t *testing.T) {
		if err := ValidateInstanceName(strings.Repeat("a", 64)); err == nil {
			t.Error("expected error for too-long name")
		}
	})
	t.Run("starts with dash", func(t *testing.T) {
		if err := ValidateInstanceName("-badname"); err == nil {
			t.Error("expected error for name starting with dash")
		}
	})
	t.Run("empty string", func(t *testing.T) {
		if err := ValidateInstanceName(""); err == nil {
			t.Error("expected error for empty name")
		}
	})
	t.Run("valid underscore", func(t *testing.T) {
		if err := ValidateInstanceName("my_vm"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestValidateLabel_AllPaths(t *testing.T) {
	t.Run("empty key", func(t *testing.T) {
		if err := ValidateLabel("", "value"); err == nil {
			t.Error("expected error for empty key")
		}
	})
	t.Run("key too long", func(t *testing.T) {
		if err := ValidateLabel(strings.Repeat("a", 254), "v"); err == nil {
			t.Error("expected error for too-long key")
		}
	})
	t.Run("value too long", func(t *testing.T) {
		if err := ValidateLabel("key", strings.Repeat("a", 1001)); err == nil {
			t.Error("expected error for too-long value")
		}
	})
	t.Run("injection in key", func(t *testing.T) {
		if err := ValidateLabel("bad;key", "v"); err == nil {
			t.Error("expected error for injection in key")
		}
	})
}

func TestValidateDiskSpec_DeviceNameInjection(t *testing.T) {
	if err := ValidateDiskSpec(provider.DiskSpec{DeviceName: "da0;rm"}); err == nil {
		t.Error("expected error for injection in device name")
	}
}

func TestValidateExecArgument_InvalidUTF8(t *testing.T) {
	if err := ValidateExecArgument("arg\xff\xfe"); err == nil {
		t.Error("expected error for invalid UTF-8 in argument")
	}
}

func TestValidateUsername_AllPaths(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if err := ValidateUsername(""); err == nil {
			t.Error("expected error for empty username")
		}
	})
	t.Run("too long", func(t *testing.T) {
		if err := ValidateUsername(strings.Repeat("a", 65)); err == nil {
			t.Error("expected error for too-long username")
		}
	})
}

func TestValidateInterfaceName_AllPaths(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if err := ValidateInterfaceName(""); err == nil {
			t.Error("expected error for empty interface name")
		}
	})
	t.Run("too long", func(t *testing.T) {
		if err := ValidateInterfaceName(strings.Repeat("a", 16)); err == nil {
			t.Error("expected error for too-long interface name")
		}
	})
}

func TestValidateProviderConfigEntry(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		value     interface{}
		shouldErr bool
	}{
		{"valid string", "cpu_profile", "host", false},
		{"valid int value", "vcpus", 4, false},
		{"valid bool value", "acpi", true, false},
		{"empty key", "", "x", true},
		{"key with slash", "a/b", "x", true},
		{"key with shell metachar", "a;b", "x", true},
		{"value with injection", "key", "x; rm -rf /", true},
		{"value with shell expansion", "key", "$(id)", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProviderConfigEntry(tt.key, tt.value)
			if tt.shouldErr && err == nil {
				t.Errorf("expected error for key=%q value=%v but got none", tt.key, tt.value)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("expected no error for key=%q value=%v but got: %v", tt.key, tt.value, err)
			}
		})
	}
}

func TestValidateSSHDestination(t *testing.T) {
	tests := []struct {
		name      string
		dest      string
		wantHost  string
		wantPath  string
		shouldErr bool
	}{
		{"user and host", "root@10.0.0.1:tank/backups", "root@10.0.0.1", "tank/backups", false},
		{"host only", "backup.example.com:pool/ds", "backup.example.com", "pool/ds", false},
		{"missing colon", "host-without-colon", "", "", true},
		{"empty host", ":pool/ds", "", "", true},
		{"empty path", "host:", "", "", true},
		{"injection in host", "root@host;rm:pool/ds", "", "", true},
		{"injection in path", "host:pool/ds;rm", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, path, err := ValidateSSHDestination(tt.dest)
			if tt.shouldErr {
				if err == nil {
					t.Errorf("expected error for %q but got none", tt.dest)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.dest, err)
			}
			if host != tt.wantHost || path != tt.wantPath {
				t.Errorf("got host=%q path=%q, want host=%q path=%q", host, path, tt.wantHost, tt.wantPath)
			}
		})
	}
}

func TestValidateInstanceSpec_CloudInit(t *testing.T) {
	spec := provider.InstanceSpec{
		Name: "vm",
		CloudInit: &provider.CloudInitConfig{
			UserData: "#cloud-config\nhostname: myvm",
			MetaData: `{"instance-id":"i-001"}`,
			Network:  "version: 2",
		},
	}
	if err := ValidateInstanceSpec(spec); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateInstanceSpec_Labels(t *testing.T) {
	t.Run("valid labels", func(t *testing.T) {
		spec := provider.InstanceSpec{
			Name:   "vm",
			Labels: map[string]string{"env": "prod", "team": "platform"},
		}
		if err := ValidateInstanceSpec(spec); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("invalid label key", func(t *testing.T) {
		spec := provider.InstanceSpec{
			Name:   "vm",
			Labels: map[string]string{"bad;key": "val"},
		}
		if err := ValidateInstanceSpec(spec); err == nil {
			t.Error("expected error for bad label key")
		}
	})
}

func TestValidateInstanceSpec_ProviderConfigValid(t *testing.T) {
	spec := provider.InstanceSpec{
		Name:           "vm",
		ProviderConfig: map[string]interface{}{"vnet": true, "jailname": "myjail"},
	}
	if err := ValidateInstanceSpec(spec); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Targeted tests for specific uncovered branches
// ---------------------------------------------------------------------------

func TestValidateLabel_KeyTooLong(t *testing.T) {
	// Use 254 lowercase 'a's: passes regex but exceeds 253-char limit
	longKey := strings.Repeat("a", 254)
	if err := ValidateLabel(longKey, "val"); err == nil {
		t.Error("expected error for key exceeding 253 chars")
	}
}

func TestValidateLabel_ValueSQLInjection(t *testing.T) {
	// Valid key, but value with SQL injection chars
	if err := ValidateLabel("valid-key", "it's bad"); err == nil {
		t.Error("expected error for SQL injection in label value")
	}
}

func TestValidateLabel_ValueUTF8(t *testing.T) {
	if err := ValidateLabel("valid-key", "\xff\xfe"); err == nil {
		t.Error("expected error for invalid UTF-8 in label value")
	}
}

func TestValidateJailParameterValue_ShellExpansion(t *testing.T) {
	t.Run("dollar-paren", func(t *testing.T) {
		if err := ValidateJailParameterValue("val$(cmd)"); err == nil {
			t.Error("expected error for $() shell expansion")
		}
	})
	t.Run("dollar-brace", func(t *testing.T) {
		if err := ValidateJailParameterValue("${VAR}"); err == nil {
			t.Error("expected error for ${} shell expansion")
		}
	})
}

func TestValidateExecOptions_InvalidArg(t *testing.T) {
	opts := provider.ExecOptions{
		Command: "/bin/ls",
		Args:    []string{"bad\x00arg"},
	}
	if err := ValidateExecOptions(opts); err == nil {
		t.Error("expected error for bad argument")
	}
}

func TestValidateExecOptions_ShellModeInvalidUTF8(t *testing.T) {
	opts := provider.ExecOptions{
		Command: "/bin/sh",
		Args:    []string{"-c", "\xff\xfe"},
	}
	if err := ValidateExecOptions(opts); err == nil {
		t.Error("expected error for invalid UTF-8 in shell command")
	}
}

func TestValidateExecCommand_InvalidUTF8(t *testing.T) {
	if err := ValidateExecCommand("\xff\xfe"); err == nil {
		t.Error("expected error for invalid UTF-8 in command")
	}
}

func TestValidateSnapshotName_CommandInjection(t *testing.T) {
	if err := ValidateSnapshotName("snap;bad"); err == nil {
		t.Error("expected error for command injection in snapshot name")
	}
}

// TestProviderConfigAcceptsNestedShellText covers the values a manifest nests
// inside a provider config.
//
// A cloud-init runcmd is shell text by definition — pipes, quotes,
// redirections — and cloud-init runs it inside the guest. Holding nested values
// to the jail(8) parameter character set rejected every example manifest
// carrying one.
func TestProviderConfigAcceptsNestedShellText(t *testing.T) {
	runcmd := []interface{}{
		"curl -fsSL https://example.invalid/gpg | gpg --dearmor -o /usr/share/keyrings/x.gpg",
		"echo 'deb [arch=amd64] https://example.invalid noble stable' > /etc/apt/sources.list.d/x.list",
	}

	nested := []struct {
		name  string
		key   string
		value interface{}
	}{
		{"a cloud-init runcmd", "cloud_init", map[string]interface{}{"runcmd": runcmd}},
		{"lifecycle hooks", "hooks", map[string]interface{}{"post_create": runcmd}},
		{"a mount", "mounts", []interface{}{
			map[string]interface{}{"host_path": "/tank/data", "mount_path": "/data", "read_only": true},
		}},
	}

	for _, tt := range nested {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateProviderConfigEntry(tt.key, tt.value); err != nil {
				t.Errorf("ValidateProviderConfigEntry(%q, ...) = %v, want it accepted", tt.key, err)
			}
		})
	}
}

// TestProviderConfigStillRejectsWhatNoValueCanCarry keeps the nested check on
// the two things that are unsafe wherever they appear.
func TestProviderConfigStillRejectsWhatNoValueCanCarry(t *testing.T) {
	withNull := map[string]interface{}{"runcmd": []interface{}{"echo hi\x00rm -rf /"}}
	if err := ValidateProviderConfigEntry("cloud_init", withNull); err == nil {
		t.Error("a nested value carrying a null byte was accepted")
	}

	// A top-level string is a jail parameter and keeps the stricter set.
	if err := ValidateProviderConfigEntry("host.hostname", "web; rm -rf /"); err == nil {
		t.Error("a jail parameter value with shell metacharacters was accepted")
	}
}

// TestProviderConfigWalksTypedCollections covers what a decoder actually hands
// over: a manifest's mounts arrive as []map[string]interface{} and a command
// list as []string, neither of which the interface{} cases match. Those used to
// pass through with nothing looked at inside them.
func TestProviderConfigWalksTypedCollections(t *testing.T) {
	withNull := "safe\x00rm -rf /"

	cases := []struct {
		name  string
		value interface{}
	}{
		{"[]string", []string{"ok", withNull}},
		{"[]map[string]interface{}", []map[string]interface{}{{"host_path": withNull}}},
		{"map[string]string", map[string]string{"cmd": withNull}},
		{"nested twice", [][]string{{withNull}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProviderConfigEntry("mounts", tc.value)
			if err == nil {
				t.Errorf("a null byte inside %s was accepted", tc.name)
			}
		})
	}
}

// TestSSHDestinationRejectsOptionLookalikes covers the host reaching ssh(1) as
// a bare argument: a leading dash turns a destination into an option.
func TestSSHDestinationRejectsOptionLookalikes(t *testing.T) {
	for _, dest := range []string{
		"-oProxyCommand=touch /tmp/pwned:tank/backup",
		"-l:tank/backup",
		"user@-oProxyCommand=id:tank/backup",
		"-user@host:tank/backup",
	} {
		if _, _, err := ValidateSSHDestination(dest); err == nil {
			t.Errorf("%q was accepted as an SSH destination", dest)
		}
	}

	for _, dest := range []string{
		"host:tank/backup",
		"user@host.example.com:tank/backup",
		"root@192.168.1.1:tank/backup",
	} {
		if _, _, err := ValidateSSHDestination(dest); err != nil {
			t.Errorf("%q was refused: %v", dest, err)
		}
	}
}
