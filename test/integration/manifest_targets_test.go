package integration

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hospitus/hospitus/pkg/manifest"
)

// manifestTarget is one instance a manifest will create, and the question to
// ask it once it runs.
type manifestTarget struct {
	name string
	// role is the name the manifest gives this instance — "db", "web" — as
	// opposed to the name it is created under. A seeding script addresses
	// instances by role, since the created name carries a run-specific prefix.
	role     string
	provider string
	// check is the health_check command the manifest declares, empty when the
	// provider cannot run commands inside the instance.
	check []string
	// startPeriod is the grace the manifest asks for before a failing check
	// means anything.
	startPeriod time.Duration
	// timeout is how long the manifest allows one attempt to take. A probe
	// with no deadline of its own does not fail when the service is silent —
	// it hangs, and takes the whole run with it.
	timeout time.Duration
	// needsDevice lists device files the manifest hands straight to the guest,
	// such as a physical root disk.
	needsDevice []string
	// needsEmulation is the architecture the instance's binaries are built
	// for, when that is not the host's. Running them needs an interpreter
	// registered with binmiscctl(8), which Hospitus does not set up.
	needsEmulation string
	// needsPCI lists PCI functions the manifest passes through, written the way
	// bhyve writes them: "bus/device/function". They are not device files —
	// ppt(4) claims the function and exposes nothing under /dev — so they are
	// checked against pciconf instead.
	needsPCI []string
}

// manifestTargets renders a manifest the way `hospitus apply --var name=<base>`
// will, then reports what it is about to create. Reading the rendered manifest
// rather than a hardcoded list is what keeps the test in step with the example:
// rename an instance and the test follows.
func manifestTargets(path, base string) ([]manifestTarget, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	vars := map[string]any{"name": base}
	rendered, err := manifest.RenderTemplate(raw, vars, throwawaySecrets{}, "integration-test")
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}

	parsed, err := manifest.ParseString(string(rendered))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	switch {
	case parsed.Workload != nil:
		w := parsed.Workload
		target := newTarget(w.Workload.Name, w.Provider.Type, w.Lifecycle)
		target.role = "instance"
		target.needsEmulation = foreignArch(w.Image.Arch)
		target.needsDevice, target.needsPCI = hostHardware(w.Storage, w.ProviderOverrides)
		return []manifestTarget{target}, nil

	case parsed.Stack != nil:
		targets := make([]manifestTarget, 0, len(parsed.Stack.Instances))
		for _, inst := range parsed.Stack.Instances {
			// A stack instance is created as "<stack>_<instance>", so the name
			// the manifest declares is not the name to look for afterwards
			// (cmd/hospitus-cli/cmd/manifest/apply.go).
			created := parsed.Stack.Stack.Name + "_" + inst.Name
			target := newTarget(created, inst.Provider, inst.Lifecycle)
			target.role = inst.Name
			target.needsDevice, target.needsPCI = hostHardware(inst.Storage, inst.ProviderOverrides)
			target.needsEmulation = foreignArch(inst.Image.Arch)
			targets = append(targets, target)
		}
		return targets, nil
	}

	return nil, fmt.Errorf("%s is neither a workload nor a stack", path)
}

func newTarget(name, provider string, lifecycle manifest.LifecycleSpec) manifestTarget {
	target := manifestTarget{name: name, provider: provider}

	hc := lifecycle.HealthCheck
	if hc == nil || len(hc.Command) == 0 || !runsCommandsInside(provider) {
		return target
	}

	target.check = hc.Command
	if d, err := time.ParseDuration(hc.StartPeriod); err == nil {
		target.startPeriod = d
	}
	target.timeout = 30 * time.Second
	if d, err := time.ParseDuration(hc.Timeout); err == nil && d > 0 {
		target.timeout = d
	}
	return target
}

// hostHardware lists what a manifest hands straight to its guest: device files
// on one side, PCI functions on the other. Reading them from the manifest
// rather than naming the examples that need them today means the next such
// example is skipped correctly without anyone remembering to add it here.
func hostHardware(storage manifest.StorageSpec, overrides map[string]manifest.ProviderConfig) (devices, pci []string) {
	if rd := storage.RootDisk; rd != nil && rd.Type == manifest.DiskTypePhysical && rd.Path != "" {
		devices = append(devices, rd.Path)
	}

	for _, override := range overrides {
		pci = append(pci, override.Passthrough...)
	}
	return devices, pci
}

// foreignArch returns the architecture an instance's binaries need emulating
// for, or "" when they run natively. An empty or "native" arch is the host's.
func foreignArch(arch string) string {
	switch arch {
	case "", "native", runtime.GOARCH:
		return ""
	// The two spellings of the same machine.
	case "aarch64":
		if runtime.GOARCH == "arm64" {
			return ""
		}
	}
	return arch
}

// emulationIsRegistered reports whether the host can run binaries of that
// architecture, which takes an interpreter registered with binmiscctl(8).
// Hospitus copies the emulator into the jail but never registers it, so a host
// that has not been prepared creates the jail and cannot start it.
func emulationIsRegistered(arch string) bool {
	out, err := exec.Command("binmiscctl", "list").Output()
	if err != nil {
		return false
	}

	// binmiscctl prints one "name: <activator>" line per registered entry, and
	// the conventional name is the architecture.
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "name:"))
		if name == arch || (arch == "arm64" && name == "aarch64") {
			return true
		}
	}
	return false
}

// pciFunctionIsBound reports whether the host has handed a PCI function to
// ppt(4), which is what bhyve needs before it can pass the function to a guest.
//
// ppt creates no node under /dev — checking for /dev/ppt0 finds nothing on a
// host where the passthrough is working perfectly. pciconf is where the binding
// shows, as "ppt0@pci0:1:0:0:".
func pciFunctionIsBound(bdf string) bool {
	parts := strings.Split(bdf, "/")
	if len(parts) != 3 {
		return false
	}

	out, err := exec.Command("pciconf", "-l").Output()
	if err != nil {
		return false
	}

	want := fmt.Sprintf("@pci0:%s:%s:%s:", parts[0], parts[1], parts[2])
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "ppt") && strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// runsCommandsInside reports whether a provider can execute a command inside a
// running instance. Only jail and podman expose `hospitus <provider> exec`; a VM
// answers over its console or its network, not over an exec API. The same rule
// gates the health-check lint test in pkg/manifest.
func runsCommandsInside(provider string) bool {
	return provider == "jail" || provider == "podman"
}

// throwawaySecrets satisfies the template layer's secret lookups without
// touching the host's secret store. The values are never used: the test reads
// the rendered manifest for names and checks, and `hospitus apply` generates the
// real secrets itself.
type throwawaySecrets struct{}

func (throwawaySecrets) Get(_, _ string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s throwawaySecrets) Rotate(scope, name string) (string, error) { return s.Get(scope, name) }
func (throwawaySecrets) Remove(_, _ string) error                    { return nil }
func (throwawaySecrets) List(_ string) ([]string, error)             { return nil, nil }

// TestManifestTargetsReadEveryExample checks the reader before the deployments
// depend on it. It needs neither root nor a daemon, so a mistake here surfaces
// on any host rather than only on the FreeBSD box that runs the real thing.
func TestManifestTargetsReadEveryExample(t *testing.T) {
	dirs := exampleManifestDirs(t)
	if len(dirs) == 0 {
		t.Fatal("no example manifests found")
	}

	for _, dir := range dirs {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			targets, err := manifestTargets(filepath.Join(dir, "template.toml"), "ex-"+name)
			if err != nil {
				t.Fatalf("read manifest: %v", err)
			}
			if len(targets) == 0 {
				t.Fatal("manifest declares no instances")
			}

			for _, target := range targets {
				if target.name == "" || target.provider == "" {
					t.Errorf("instance read with name %q and provider %q", target.name, target.provider)
				}
				if runsCommandsInside(target.provider) && len(target.check) == 0 {
					t.Errorf("%s runs on %s, which has an exec, but no health check was read",
						target.name, target.provider)
				}
				t.Logf("%-28s %-7s check=%v", target.name, target.provider, target.check)
			}
		})
	}
}
