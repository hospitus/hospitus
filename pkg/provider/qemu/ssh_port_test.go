package qemu

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// writeVMState records a VM the way the provider does, so sshPortFor sees it.
func writeVMState(t *testing.T, stateDir, name string, sshPort int) {
	t.Helper()
	writeVMPort(t, stateDir, name, "ssh_port", sshPort)
}

// writeVMPort records a VM holding one port, the way the provider does.
func writeVMPort(t *testing.T, stateDir, name, key string, port int) {
	t.Helper()
	config := vmConfig{}
	config.Spec = provider.InstanceSpec{
		Name:           name,
		ProviderConfig: map[string]interface{}{key: port},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, name+".json"), data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestSSHPortForKeepsTheVMsOwnPort(t *testing.T) {
	stateDir := t.TempDir()
	p := &QEMUProvider{stateDir: stateDir}

	// A port this test holds open for the duration, then releases: allocatePort
	// returns a VM's own recorded port only when portIsFree agrees, so a fixed
	// number would make the test fail whenever something on the host happened
	// to listen on it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot reserve a port: %v", err)
	}
	free := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("releasing the probe port: %v", err)
	}

	writeVMState(t, stateDir, "ubuntu-server", free)

	if got := p.sshPortFor("ubuntu-server"); got != free {
		t.Errorf("sshPortFor = %d, want the recorded %d", got, free)
	}
}

func TestSSHPortForSkipsAnotherVMsPort(t *testing.T) {
	stateDir := t.TempDir()
	p := &QEMUProvider{stateDir: stateDir}
	// A VM that is not running still holds its port: probing the host alone
	// handed 2222 to both, and the second could not start.
	writeVMState(t, stateDir, "arm-test", 2222)

	got := p.sshPortFor("ubuntu-server")
	if got == 2222 {
		t.Errorf("sshPortFor = 2222, the port arm-test already holds")
	}
	if got < 2222 || got >= 2322 {
		t.Errorf("sshPortFor = %d, outside the range it searches", got)
	}
}

func TestSSHPortForMovesOffAPortAnotherVMAlsoHolds(t *testing.T) {
	stateDir := t.TempDir()
	p := &QEMUProvider{stateDir: stateDir}
	// Both were given 2222 before the ports were made unique.
	writeVMState(t, stateDir, "arm-test", 2222)
	writeVMState(t, stateDir, "ubuntu-server", 2222)

	if got := p.sshPortFor("ubuntu-server"); got == 2222 {
		t.Errorf("sshPortFor = 2222, which arm-test also holds")
	}
}

func TestRefreshSSHForwardRewritesAPortAnotherVMHolds(t *testing.T) {
	stateDir := t.TempDir()
	p := &QEMUProvider{stateDir: stateDir}
	writeVMState(t, stateDir, "arm-test", 2222)

	config := &vmConfig{Args: []string{
		"-netdev", "user,id=net0,hostfwd=tcp::2222-:22",
		"-device", "virtio-net-pci,netdev=net0",
	}}
	// The recorded ssh_port is what marks this forward as one Hospitus allocated
	// and may therefore move.
	config.Spec.ProviderConfig = map[string]interface{}{"ssh_port": 2222}

	if !p.refreshSSHForward(config, "ubuntu-server") {
		t.Fatal("refreshSSHForward reported no change, but 2222 is arm-test's")
	}
	if strings.Contains(config.Args[1], "tcp::2222-") {
		t.Errorf("argument still forwards 2222: %q", config.Args[1])
	}
	// The recorded port and the rewritten argument have to be the same one:
	// checking that some port was recorded, and separately that 2222 is gone,
	// left a rewrite to one port and a record of another indistinguishable from
	// a correct result — and the CLI reads the record to reach the VM.
	recorded, ok := storedPort(config, "ssh_port")
	if !ok {
		t.Fatal("the new port was not recorded in the spec")
	}
	if want := fmt.Sprintf("tcp::%d-", recorded); !strings.Contains(config.Args[1], want) {
		t.Errorf("argument %q does not forward the recorded port %d", config.Args[1], recorded)
	}
}

func TestRefreshSSHForwardLeavesAGoodPortAlone(t *testing.T) {
	stateDir := t.TempDir()
	p := &QEMUProvider{stateDir: stateDir}

	// The allocator probes the port, so it has to be one that is genuinely
	// free here. Hard-coding 2222 failed on any host already using it.
	port := freeLocalPort(t)
	writeVMState(t, stateDir, "ubuntu-server", port)

	forward := fmt.Sprintf("user,id=net0,hostfwd=tcp::%d-:22", port)
	config := &vmConfig{Args: []string{"-netdev", forward}}
	config.Spec.ProviderConfig = map[string]interface{}{"ssh_port": port}

	if p.refreshSSHForward(config, "ubuntu-server") {
		t.Errorf("the VM's own free port was rewritten: %q", config.Args[1])
	}
}

// freeLocalPort returns a port nothing is listening on right now.
func freeLocalPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot find a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("closing the probe listener: %v", err)
	}
	return port
}

// TestRefreshSSHForwardIgnoresAUserForward covers the two ways the SSH rule was
// confused with the operator's own forwards: an unanchored guest port matched
// "-:2222" inside a user rule, and a deliberate forward to guest 22 was treated
// as the pool-allocated one. Neither config records an ssh_port.
func TestRefreshSSHForwardIgnoresAUserForward(t *testing.T) {
	stateDir := t.TempDir()
	p := &QEMUProvider{stateDir: stateDir}
	writeVMState(t, stateDir, "arm-test", 2222)

	// A user forward 8080 → guest 2222: the guest port is not 22.
	userForward := &vmConfig{Args: []string{"-netdev", "user,id=net0,hostfwd=tcp::8080-:2222"}}
	if p.refreshSSHForward(userForward, "ubuntu-server") {
		t.Errorf("a forward to guest 2222 was rewritten as if it were SSH: %q", userForward.Args[1])
	}
	if !strings.Contains(userForward.Args[1], "tcp::8080-:2222") {
		t.Errorf("the user's host port was replaced: %q", userForward.Args[1])
	}

	// A deliberate forward to guest 22 that Hospitus did not allocate.
	unrecorded := &vmConfig{Args: []string{"-netdev", "user,id=net0,hostfwd=tcp::2222-:22"}}
	if p.refreshSSHForward(unrecorded, "ubuntu-server") {
		t.Errorf("a forward with no recorded ssh_port was reallocated: %q", unrecorded.Args[1])
	}
}

// TestSSHHostForwardMatchesOnlyGuest22 pins the anchoring of the regex itself.
func TestSSHHostForwardMatchesOnlyGuest22(t *testing.T) {
	tests := []struct {
		arg  string
		want bool
	}{
		{"user,id=net0,hostfwd=tcp::2222-:22", true},
		{"user,id=net0,hostfwd=tcp::2222-:22,hostfwd=tcp::8080-:80", true},
		{"user,id=net0,hostfwd=tcp::8080-:2222", false},
		{"user,id=net0,hostfwd=tcp::8080-:220", false},
		{"user,id=net0,hostfwd=tcp::8080-:220,restrict=no", false},
	}
	for _, tt := range tests {
		if got := sshHostForward.MatchString(tt.arg); got != tt.want {
			t.Errorf("sshHostForward.MatchString(%q) = %v, want %v", tt.arg, got, tt.want)
		}
	}
}

// TestRefreshUserForwardsRebuildsStoredRules covers the promise AddPortForward
// makes on a stopped VM: the stored rules have to reach the command line QEMU
// is started with, since the arguments are built once and replayed.
func TestRefreshUserForwardsRebuildsStoredRules(t *testing.T) {
	p := &QEMUProvider{stateDir: t.TempDir()}

	config := &vmConfig{Args: []string{
		"-netdev", "user,id=net0,hostfwd=tcp::2222-:22",
		"-device", "virtio-net-pci,netdev=net0",
	}}
	config.Spec.ProviderConfig = map[string]interface{}{
		"ssh_port": 2222,
		"port_forwards": portForwardsToRaw([]provider.PortForward{
			{Protocol: "tcp", HostPort: 8080, GuestPort: 80},
			{Protocol: "udp", HostPort: 5353, GuestPort: 53},
		}),
	}

	if !p.refreshUserForwards(config) {
		t.Fatal("refreshUserForwards reported no change, but two rules are stored")
	}
	netdev := config.Args[1]
	for _, want := range []string{"hostfwd=tcp::2222-:22", "hostfwd=tcp::8080-:80", "hostfwd=udp::5353-:53"} {
		if !strings.Contains(netdev, want) {
			t.Errorf("netdev %q is missing %q", netdev, want)
		}
	}

	// Idempotent: a second pass has nothing left to change.
	if p.refreshUserForwards(config) {
		t.Errorf("refreshUserForwards changed an already-current netdev: %q", config.Args[1])
	}
}

// TestRefreshUserForwardsDropsRemovedRules verifies a rule removed from the
// config also leaves the arguments, rather than surviving the restart.
func TestRefreshUserForwardsDropsRemovedRules(t *testing.T) {
	p := &QEMUProvider{stateDir: t.TempDir()}

	config := &vmConfig{Args: []string{
		"-netdev", "user,id=net0,hostfwd=tcp::2222-:22,hostfwd=tcp::8080-:80",
	}}
	config.Spec.ProviderConfig = map[string]interface{}{"ssh_port": 2222}

	if !p.refreshUserForwards(config) {
		t.Fatal("refreshUserForwards kept a rule the config no longer has")
	}
	if strings.Contains(config.Args[1], "8080") {
		t.Errorf("removed rule still on the command line: %q", config.Args[1])
	}
	if !strings.Contains(config.Args[1], "hostfwd=tcp::2222-:22") {
		t.Errorf("the SSH forward was dropped: %q", config.Args[1])
	}
}

func TestRefreshVNCDisplayMovesOffATakenDisplay(t *testing.T) {
	stateDir := t.TempDir()
	p := &QEMUProvider{stateDir: stateDir}
	writeVMPort(t, stateDir, "arm-test", "vnc_port", vncBasePort)

	config := &vmConfig{Args: []string{"-vnc", ":0", "-display", "none"}}

	if !p.refreshVNCDisplay(config, "ubuntu-server") {
		t.Fatal("refreshVNCDisplay reported no change, but display 0 is arm-test's")
	}
	if config.Args[1] == ":0" {
		t.Error("the display was not moved")
	}
	if port, ok := storedPort(config, "vnc_port"); !ok || port == vncBasePort {
		t.Errorf("vnc_port = %d (recorded: %v), want a different port", port, ok)
	}
}
