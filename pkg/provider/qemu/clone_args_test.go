package qemu

import (
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestRewriteCloneArgs verifies clone arg rewriting only rewrites source-dir
// path references and sets -name explicitly, without a blind textual replace of
// the VM name that could corrupt unrelated args (audit HIGH clone.go:250).
func TestRewriteCloneArgs(t *testing.T) {
	args := []string{
		"-name", "vm",
		"-drive", "file=/vms/vm/disk.qcow2,format=qcow2",
		"-device", "virtio-net,mac=52:54:00:vm:00:01",
		"-cpu", "host",
	}
	out := rewriteCloneArgs(args, "/vms/vm", "/vms/clone", "clone")

	// -name is set to the clone name.
	var nameVal string
	for i, a := range out {
		if a == "-name" && i+1 < len(out) {
			nameVal = out[i+1]
		}
	}
	if nameVal != "clone" {
		t.Errorf("-name = %q, want clone", nameVal)
	}

	// The disk path (which references the source VM dir) is rewritten.
	if !strings.Contains(strings.Join(out, " "), "/vms/clone/disk.qcow2") {
		t.Errorf("disk path not rewritten: %v", out)
	}

	// The MAC arg, which coincidentally contains the name substring "vm", is
	// NOT corrupted into "clone".
	for _, a := range out {
		if strings.HasPrefix(a, "virtio-net") && strings.Contains(a, "clone") {
			t.Errorf("unrelated arg corrupted by name replace: %q", a)
		}
	}
}

// TestBuildCloneConfigGivesTheCloneItsOwnPorts covers the ports a clone used to
// inherit: its state file claimed the source's, and at the source's next start
// the allocator saw its own port taken and moved the *source* off the port the
// operator had been told about.
func TestBuildCloneConfigGivesTheCloneItsOwnPorts(t *testing.T) {
	stateDir := t.TempDir()
	p := &QEMUProvider{dataDir: t.TempDir(), stateDir: stateDir}
	writeVMState(t, stateDir, "web", 2222)
	writeVMPort(t, stateDir, "web", "ssh_port", 2222)

	source := &vmConfig{
		Name: "web",
		Args: []string{
			"-name", "web",
			"-netdev", "user,id=net0,hostfwd=tcp::2222-:22",
			"-vnc", ":0",
		},
		Spec: provider.InstanceSpec{
			Name: "web",
			ProviderConfig: map[string]interface{}{
				"ssh_port": 2222,
				"vnc_port": vncBasePort,
			},
		},
	}

	clone, err := p.buildCloneConfig(source, "web-copy", "/vms/web-copy", "/vms/web-copy/disk0.qcow2", provider.CloneOptions{})
	if err != nil {
		t.Fatalf("buildCloneConfig: %v", err)
	}

	sshPort, ok := storedPort(clone, "ssh_port")
	if !ok {
		t.Fatal("the clone recorded no ssh_port")
	}
	if sshPort == 2222 {
		t.Error("the clone kept the source's SSH port")
	}
	joined := strings.Join(clone.Args, " ")
	if strings.Contains(joined, "tcp::2222-:22") {
		t.Errorf("the clone's arguments still forward the source's port: %q", joined)
	}
	if strings.Contains(joined, "-vnc :0") {
		t.Errorf("the clone kept the source's VNC display: %q", joined)
	}
}
