package applecontainer

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// CreateInstance creates a container without starting it.
func (p *Provider) CreateInstance(ctx context.Context, spec provider.InstanceSpec) (_ provider.InstanceHandle, err error) {
	defer func() { err = provider.WrapError("container", "create", spec.Name, err) }()

	if err := validation.ValidateInstanceName(spec.Name); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid container name: %w", err)
	}
	if spec.Image == "" {
		return provider.InstanceHandle{}, fmt.Errorf("an image is required")
	}
	// The image goes on the command line right after the options, so a value
	// starting with a dash would be parsed as one more option (--volume=/:/host).
	if strings.HasPrefix(spec.Image, "-") {
		return provider.InstanceHandle{}, fmt.Errorf("invalid image %q: an image reference cannot start with '-'", spec.Image)
	}

	if _, findErr := p.find(ctx, spec.Name); findErr == nil {
		return provider.InstanceHandle{}, provider.ErrInstanceExists
	}

	args := []string{"create", "--name", spec.Name}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(spec.CPUs))
	}
	if spec.MemoryMB > 0 {
		// The tool takes a size with a unit rather than a byte count.
		args = append(args, "--memory", strconv.FormatInt(spec.MemoryMB, 10)+"M")
	}
	for key, value := range spec.Labels {
		args = append(args, "--label", key+"="+value)
	}
	args = append(args, spec.Image)

	// The command follows the image, as with podman. Arguments become argv in
	// the container and are never interpreted by a host shell, so only their
	// encoding needs checking.
	command, err := commandArgs(spec.ProviderConfig)
	if err != nil {
		return provider.InstanceHandle{}, err
	}
	args = append(args, command...)

	if output, err := p.cmd().CombinedOutput(ctx, p.containerBin, args...); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create container: %w (output: %s)", err, string(output))
	}

	return provider.InstanceHandle{
		ID:       spec.Name,
		Provider: "container",
	}, nil
}

// commandArgs reads the optional command to run in the container.
//
// InstanceSpec has no field for it — a container command is provider-specific —
// so it travels in ProviderConfig, the same way the podman provider takes it.
func commandArgs(providerConfig map[string]interface{}) ([]string, error) {
	raw, ok := providerConfig["command"].([]interface{})
	if !ok {
		return nil, nil
	}

	args := make([]string, 0, len(raw))
	for _, value := range raw {
		arg, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("command arguments must be strings")
		}
		if !utf8.ValidString(arg) {
			return nil, fmt.Errorf("command argument is not valid UTF-8")
		}
		args = append(args, arg)
	}
	return args, nil
}

// StartInstance starts a created container.
func (p *Provider) StartInstance(ctx context.Context, handle provider.InstanceHandle) (err error) {
	defer func() { err = provider.WrapError("container", "start", handle.ID, err) }()

	if output, err := p.cmd().CombinedOutput(ctx, p.containerBin, "start", handle.ID); err != nil {
		return fmt.Errorf("failed to start container: %w (output: %s)", err, string(output))
	}
	return nil
}

// StopInstance stops a running container.
func (p *Provider) StopInstance(ctx context.Context, handle provider.InstanceHandle, opts provider.StopOptions) (err error) {
	defer func() { err = provider.WrapError("container", "stop", handle.ID, err) }()

	args := []string{"stop"}
	if opts.Timeout > 0 {
		args = append(args, "--time", strconv.Itoa(int(opts.Timeout/time.Second)))
	}
	args = append(args, handle.ID)

	if output, err := p.cmd().CombinedOutput(ctx, p.containerBin, args...); err != nil {
		// A container that is already stopped is the state the caller wanted.
		if strings.Contains(strings.ToLower(string(output)), "not running") {
			return nil
		}
		return fmt.Errorf("failed to stop container: %w (output: %s)", err, string(output))
	}
	return nil
}

// RestartInstance stops the container and starts it again.
//
// The tool has no restart verb, so this is the two steps it would run.
func (p *Provider) RestartInstance(ctx context.Context, handle provider.InstanceHandle) (err error) {
	defer func() { err = provider.WrapError("container", "restart", handle.ID, err) }()

	if err := p.StopInstance(ctx, handle, provider.StopOptions{}); err != nil {
		return err
	}
	return p.StartInstance(ctx, handle)
}

// DeleteInstance removes a container.
func (p *Provider) DeleteInstance(ctx context.Context, handle provider.InstanceHandle, force bool) (err error) {
	defer func() { err = provider.WrapError("container", "delete", handle.ID, err) }()

	args := []string{"delete"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, handle.ID)

	if output, err := p.cmd().CombinedOutput(ctx, p.containerBin, args...); err != nil {
		// Deleting something that is already gone is the state the caller wanted.
		if strings.Contains(strings.ToLower(string(output)), "not found") {
			return nil
		}
		return fmt.Errorf("failed to delete container: %w (output: %s)", err, string(output))
	}
	return nil
}

// GetInstanceState reports what the tool says about a container.
func (p *Provider) GetInstanceState(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceState, error) {
	record, err := p.find(ctx, handle.ID)
	if err != nil {
		return provider.StateUnknown, err
	}
	return mapState(record.Status.State), nil
}

// mapState translates the tool's vocabulary into the provider's.
func mapState(state string) provider.InstanceState {
	switch strings.ToLower(state) {
	case "running":
		return provider.StateRunning
	case "stopped", "exited":
		return provider.StateStopped
	case "stopping":
		return provider.StateStopping
	case "starting":
		return provider.StateStarting
	default:
		return provider.StateUnknown
	}
}

// GetInstanceInfo returns what is known about a container.
func (p *Provider) GetInstanceInfo(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceInfo, error) {
	record, err := p.find(ctx, handle.ID)
	if err != nil {
		return provider.InstanceInfo{}, err
	}

	info := provider.InstanceInfo{
		Handle: handle,
		State:  mapState(record.Status.State),
		Spec: provider.InstanceSpec{
			Name:     record.ID,
			Image:    record.Configuration.Image.Reference,
			CPUs:     record.Configuration.Resources.CPUs,
			MemoryMB: record.Configuration.Resources.MemoryInBytes / (1024 * 1024),
		},
	}

	for _, network := range record.Status.Networks {
		if network.MACAddress != "" {
			info.MACAddresses = append(info.MACAddresses, network.MACAddress)
		}
		if ip := parseIP(network.IPv4Address); ip != nil {
			info.IPAddresses = append(info.IPAddresses, ip)
		}
	}
	return info, nil
}

// ListInstances returns every container the tool knows about.
func (p *Provider) ListInstances(ctx context.Context, _ provider.InstanceFilter) ([]provider.InstanceHandle, error) {
	records, err := p.list(ctx)
	if err != nil {
		return nil, err
	}

	handles := make([]provider.InstanceHandle, 0, len(records))
	for i := range records {
		handles = append(handles, provider.InstanceHandle{
			ID:       records[i].ID,
			Provider: "container",
		})
	}
	return handles, nil
}

var _ provider.InstanceAddressProvider = (*Provider)(nil)

// InstanceAddresses reports the addresses the runtime assigned to the
// container's networks.
func (p *Provider) InstanceAddresses(ctx context.Context, handle provider.InstanceHandle) ([]net.IP, error) {
	record, err := p.find(ctx, handle.ID)
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	for _, network := range record.Status.Networks {
		if ip := parseIP(network.IPv4Address); ip != nil {
			ips = append(ips, ip)
		}
	}
	return ips, nil
}
