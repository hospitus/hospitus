package qemu

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// argsOf renders a built command line so a test can look for a device.
func argsOf(args []string) string { return strings.Join(args, " ") }

// TestQEMUOffersAGuestAgentChannel is what makes exec and address discovery
// possible at all.
//
// ExecCommand and InstanceAddresses go through QMP, and QEMU relays both to the
// agent over a virtio-serial port named org.qemu.guest_agent.0. Without the
// port the calls fail whatever the guest runs.
func TestQEMUOffersAGuestAgentChannel(t *testing.T) {
	vmDir := t.TempDir()
	p := &QEMUProvider{dataDir: vmDir, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	spec := provider.InstanceSpec{Name: "web", CPUs: 2, MemoryMB: 2048, ProviderConfig: map[string]interface{}{}}

	config := p.buildQEMUConfig(spec, "qemu-system-x86_64", filepath.Join(vmDir, "disk0.qcow2"), "", vmDir, "amd64")
	line := argsOf(config.Args)

	for _, want := range []string{
		"virtio-serial",
		"virtserialport,chardev=qga0,name=org.qemu.guest_agent.0",
		filepath.Join(vmDir, "qga.sock"),
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the command line does not carry %q\ngot: %s", want, line)
		}
	}

	if got, _ := config.Spec.ProviderConfig["qga_socket"].(string); got != filepath.Join(vmDir, "qga.sock") {
		t.Errorf("qga_socket = %q, want the socket in the VM directory", got)
	}
}

// TestQEMUSocketsUseTheCurrentChardevSyntax pins the option form.
//
// QEMU deprecated the bare "server" and "nowait" flags; on QEMU 11 each one
// prints a warning per VM start, and they are slated to stop working.
func TestQEMUSocketsUseTheCurrentChardevSyntax(t *testing.T) {
	vmDir := t.TempDir()
	p := &QEMUProvider{dataDir: vmDir, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	spec := provider.InstanceSpec{Name: "web", CPUs: 1, MemoryMB: 512, ProviderConfig: map[string]interface{}{}}

	line := argsOf(p.buildQEMUConfig(spec, "qemu-system-x86_64", filepath.Join(vmDir, "disk0.qcow2"), "", vmDir, "amd64").Args)

	for _, deprecated := range []string{",server,", ",nowait", ",server ", "server,nowait"} {
		if strings.Contains(line, deprecated) {
			t.Errorf("the command line still uses the deprecated form %q\ngot: %s", deprecated, line)
		}
	}
	if !strings.Contains(line, "server=on,wait=off") {
		t.Errorf("expected server=on,wait=off\ngot: %s", line)
	}
}
