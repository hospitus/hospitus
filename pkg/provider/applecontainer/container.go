// Package applecontainer runs Linux containers on macOS through Apple's
// container tool.
//
// container is a command-line tool that runs each OCI container inside its own
// lightweight virtual machine on Virtualization.framework. From this provider's
// point of view it is shaped exactly like Podman — a CLI that owns its own
// store — so it is driven the same way, through execx.Runner, and every command
// site is testable with execx.Fake.
//
// It requires macOS 26 or later on Apple silicon, and is pre-1.0: its
// maintainers only guarantee stability within a patch version, so a minor
// upgrade can change behavior this provider relies on.
package applecontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// Compile-time assertion: Provider implements the provider interface.
var _ provider.Provider = (*Provider)(nil)

// Provider implements the Provider interface using Apple's container tool.
type Provider struct {
	mu sync.RWMutex

	config       provider.ProviderConfig
	containerBin string
	logger       *slog.Logger
	runner       execx.Runner
}

// NewProvider creates a provider backed by Apple's container tool.
func NewProvider() *Provider {
	return &Provider{runner: execx.Default()}
}

// cmd returns the command runner, defaulting to the real os/exec backend when
// the provider was built as a bare struct literal (tests).
func (p *Provider) cmd() execx.Runner {
	if p.runner == nil {
		return execx.Default()
	}
	return p.runner
}

// Metadata returns information about the provider.
func (p *Provider) Metadata() provider.ProviderMetadata {
	return provider.ProviderMetadata{
		Name:        "container",
		Version:     "1.0.0",
		Type:        provider.ProviderTypeContainer,
		Author:      "HOSPITUS Project",
		Description: "Linux containers on macOS through Apple's container tool",
		Homepage:    "https://github.com/apple/container",
		License:     "Apache-2.0",
	}
}

// Capabilities declares what this provider supports.
//
// Each container runs in its own VM, which is what makes stop and start cheap
// and snapshots absent: there is no image layer to freeze and no live state to
// save. Claiming otherwise would let the API accept operations that cannot run.
func (p *Provider) Capabilities() provider.ProviderCapabilities {
	return provider.ProviderCapabilities{
		SupportsConsole: false, // no ConsoleProvider yet; exec and logs go through the tool

		NetworkTypes:         []provider.NetworkType{provider.NetworkTypeBridge},
		MaxNetworkInterfaces: 1,

		DiskTypes: []provider.DiskType{provider.DiskTypeRaw},
		MaxDisks:  8,

		// Apple silicon only, per the tool's own requirements — not the host
		// architecture. On an Intel Mac, reporting amd64 let a scheduler pick
		// this provider and fail later in Initialize.
		SupportedArchitectures: []string{"arm64"},
		SupportsCrossArch:      false,

		MaxCPUs:     16,
		MaxMemoryMB: 65536,

		PlatformFeatures: map[string]interface{}{
			"oci_images":               true,
			"virtualization_framework": true,
		},
	}
}

// Initialize locates the container binary and checks the service is up.
func (p *Provider) Initialize(ctx context.Context, config provider.ProviderConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if config.Logger != nil {
		p.logger = config.Logger.With(logging.FieldProvider, "container")
	} else if p.logger == nil {
		p.logger = logging.WithProvider("container")
	}

	if runtime.GOOS != "darwin" {
		return fmt.Errorf("the container tool from Apple requires macOS, not %s", runtime.GOOS)
	}
	// Refused before the binary lookup, so the message names the real reason
	// rather than a missing tool.
	if runtime.GOARCH != "arm64" {
		return fmt.Errorf("the container tool from Apple requires Apple silicon, not %s", runtime.GOARCH)
	}

	p.config = config

	bin, err := exec.LookPath("container")
	if err != nil {
		return fmt.Errorf("container not found: %w (install it with: brew install container)", err)
	}
	p.containerBin = bin

	// The tool talks to a background service; without it every command fails
	// with an error that says nothing about the cause.
	if err := p.cmd().Run(ctx, p.containerBin, "system", "status"); err != nil {
		return fmt.Errorf("the container service is not running: %w (start it with: container system start)", err)
	}

	p.logger.Info("container provider initialized", "binary", p.containerBin)
	return nil
}

// Shutdown releases provider resources. Running containers are left alone: the
// service owns them and they outlive the daemon.
func (p *Provider) Shutdown(_ context.Context) error {
	return nil
}

// HealthCheck reports whether containers can run here.
func (p *Provider) HealthCheck(ctx context.Context) error {
	if p.containerBin == "" {
		return fmt.Errorf("container binary not found")
	}
	if err := p.cmd().Run(ctx, p.containerBin, "system", "status"); err != nil {
		return fmt.Errorf("the container service is not running: %w", err)
	}
	return nil
}

// containerRecord is the part of `container list --format json` this provider
// reads. The tool reports much more; decoding only what is used keeps the
// provider from breaking every time an unrelated field moves.
type containerRecord struct {
	ID     string `json:"id"`
	Status struct {
		State    string `json:"state"`
		Networks []struct {
			IPv4Address string `json:"ipv4Address"`
			MACAddress  string `json:"macAddress"`
			Hostname    string `json:"hostname"`
		} `json:"networks"`
	} `json:"status"`
	Configuration struct {
		Image struct {
			Reference string `json:"reference"`
		} `json:"image"`
		Resources struct {
			CPUs          int   `json:"cpus"`
			MemoryInBytes int64 `json:"memoryInBytes"`
		} `json:"resources"`
	} `json:"configuration"`
}

// list returns every container the tool knows about, running or not.
func (p *Provider) list(ctx context.Context) ([]containerRecord, error) {
	output, err := p.cmd().Output(ctx, p.containerBin, "list", "--all", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	// An empty store prints nothing rather than an empty array.
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}

	var records []containerRecord
	if err := json.Unmarshal(output, &records); err != nil {
		return nil, fmt.Errorf("failed to decode the container list: %w", err)
	}
	return records, nil
}

// find returns one container by name, or ErrInstanceNotFound.
func (p *Provider) find(ctx context.Context, name string) (*containerRecord, error) {
	records, err := p.list(ctx)
	if err != nil {
		return nil, err
	}
	for i := range records {
		if records[i].ID == name {
			return &records[i], nil
		}
	}
	return nil, provider.ErrInstanceNotFound
}
