package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Podman cloning uses podman commit to create an image from the source
// container, then podman create to instantiate a new container from that image.

// Ensure PodmanProvider implements CloneProvider
var _ provider.CloneProvider = (*PodmanProvider)(nil)

// CloneInstance creates a new container from an existing one.
//
// The source container is committed to a temporary image, then a new container
// is created from that image with the same settings. The source container can
// be running or stopped.
func (p *PodmanProvider) CloneInstance(ctx context.Context, source provider.InstanceHandle, cloneName string, opts provider.CloneOptions) (provider.InstanceHandle, error) {
	if err := validation.ValidateInstanceName(cloneName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid clone name: %w", err)
	}

	sourceName := source.ID

	// Check if source exists
	p.mu.RLock()
	_, exists := p.instances[sourceName]
	p.mu.RUnlock()

	if !exists {
		return provider.InstanceHandle{}, fmt.Errorf("source container %s not found", sourceName)
	}

	// The image the clone will run from. It is not temporary: the clone
	// container is created from it and keeps referencing it for its whole life.
	cloneImage := fmt.Sprintf("hospitus-clone-%s-%d", cloneName, time.Now().UnixNano())

	// Commit the source container to a temporary image
	commitArgs := []string{"commit", "--pause=false"}
	if opts.LinkedClone {
		// Marks the image as one Hospitus produced by cloning. It records no
		// parent: podman has no such reference, and the source is named in the
		// handle's clone_of metadata instead.
		commitArgs = append(commitArgs, "--change", "LABEL hospitus.clone=true")
	}
	commitArgs = append(commitArgs, sourceName, cloneImage)

	// The output is only used in the error message: the image is addressed by
	// the name assigned above, never by a parsed ID.
	if out, err := p.cmd().CombinedOutput(ctx, p.podmanBin, commitArgs...); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to commit source container: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// Remove the committed image only if no clone ends up using it. It used to
	// be removed unconditionally on the success path with "rmi --force", which
	// podman-rmi(1) documents as removing every container using the image —
	// that is, the clone that had just been created from it.
	cloneCreated := false
	defer func() {
		if cloneCreated {
			return
		}
		if out, err := p.cmd().CombinedOutput(context.Background(), p.podmanBin, "rmi", "--force", cloneImage); err != nil {
			p.logger.Warn("failed to remove the clone image after a failed clone",
				"image", cloneImage, logging.FieldError, err, "output", strings.TrimSpace(string(out)))
		}
	}()

	// Build create command for the clone.
	//
	// The CPU and memory limits are read from the source with podman inspect:
	// GetInstanceInfo does not populate them, so a clone silently ran without
	// the source's limits. Published ports, volumes, env and command are still
	// NOT replicated — they live in the source's create arguments, which podman
	// does not expose in a form this code reconstructs.
	createArgs := []string{"create", "--name", cloneName}

	sourceInfo, err := p.GetInstanceInfo(ctx, source)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to get source container info: %w", err)
	}
	cpus, memoryMB := p.containerResourceLimits(ctx, sourceName)

	if opts.CPUs > 0 {
		cpus = opts.CPUs
	}
	if opts.MemoryMB > 0 {
		memoryMB = opts.MemoryMB
	}

	if cpus > 0 {
		createArgs = append(createArgs, "--cpus", fmt.Sprintf("%d", cpus))
	}
	if memoryMB > 0 {
		createArgs = append(createArgs, "--memory", fmt.Sprintf("%dm", memoryMB))
	}

	// Copy labels from source
	if sourceInfo.Spec.Labels != nil {
		for k, v := range sourceInfo.Spec.Labels {
			createArgs = append(createArgs, "--label", fmt.Sprintf("%s=%s", k, v))
		}
	}

	// Add custom labels
	if opts.Labels != nil {
		for k, v := range opts.Labels {
			createArgs = append(createArgs, "--label", fmt.Sprintf("%s=%s", k, v))
		}
	}

	// Add hospitus label
	createArgs = append(createArgs, "--label", "hospitus.managed=true", cloneImage)

	createOutput, err := p.cmd().CombinedOutput(ctx, p.podmanBin, createArgs...)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create clone container: %w: %s", err, string(createOutput))
	}

	if id := extractContainerID(string(createOutput)); id == "" {
		return provider.InstanceHandle{}, fmt.Errorf("failed to get clone container ID from output: %s", string(createOutput))
	}

	// Refresh container info into our cache. On failure, roll back the newly
	// created container so we do not leave an orphaned clone behind.
	if err := p.refreshContainerInfo(ctx, cloneName); err != nil {
		if out, rmErr := p.cmd().CombinedOutput(context.Background(), p.podmanBin, "rm", "--force", cloneName); rmErr != nil {
			p.logger.Warn("failed to roll back clone container after refresh failure",
				"container", cloneName, logging.FieldError, rmErr, "output", strings.TrimSpace(string(out)))
		}
		return provider.InstanceHandle{}, fmt.Errorf("failed to refresh clone container info: %w", err)
	}

	cloneCreated = true

	handle := provider.InstanceHandle{
		ID:       cloneName,
		Provider: providerName,
		Metadata: map[string]interface{}{
			"name":  cloneName,
			"image": cloneImage,
		},
	}

	if opts.LinkedClone {
		handle.Metadata["linked_clone"] = true
		handle.Metadata["clone_of"] = sourceName
	}

	return handle, nil
}

// containerResourceLimits reads the CPU and memory limits podman records for a
// container. Both are zero when the container has none, or when the inspect
// fails — a clone without limits is better than no clone.
//
// podman stores the CPU limit as NanoCpus (1 CPU = 1e9) and the memory limit in
// bytes; hospitus expresses them as whole CPUs and MB.
func (p *PodmanProvider) containerResourceLimits(ctx context.Context, containerName string) (cpus int, memoryMB int64) {
	out, err := p.cmd().Output(ctx, p.podmanBin, "inspect", "--format", "json", containerName)
	if err != nil {
		return 0, 0
	}

	var inspectData []struct {
		HostConfig struct {
			NanoCpus int64 `json:"NanoCpus"`
			Memory   int64 `json:"Memory"`
		} `json:"HostConfig"`
	}
	if err := json.Unmarshal(out, &inspectData); err != nil || len(inspectData) == 0 {
		return 0, 0
	}

	host := inspectData[0].HostConfig
	if host.NanoCpus > 0 {
		cpus = int(host.NanoCpus / 1e9)
	}
	if host.Memory > 0 {
		memoryMB = host.Memory / (1024 * 1024)
	}
	return cpus, memoryMB
}

// CloneFromSnapshot creates a new container from a snapshot image.
func (p *PodmanProvider) CloneFromSnapshot(ctx context.Context, snapshot provider.SnapshotHandle, cloneName string, opts provider.CloneOptions) (provider.InstanceHandle, error) {
	if err := validation.ValidateInstanceName(cloneName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid clone name: %w", err)
	}

	containerName, ok := snapshot.Metadata["container"].(string)
	if !ok {
		return provider.InstanceHandle{}, fmt.Errorf("invalid snapshot metadata: missing container name")
	}

	imageName, ok := snapshot.Metadata["imageName"].(string)
	if !ok {
		imageName = snapshot.ID
	}

	// Check if the snapshot image exists
	if err := p.cmd().Run(ctx, p.podmanBin, "image", "exists", imageName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("snapshot image %s not found", imageName)
	}

	// Get source container info to replicate settings
	sourceHandle := provider.InstanceHandle{
		ID:       containerName,
		Provider: providerName,
	}
	sourceInfo, err := p.GetInstanceInfo(ctx, sourceHandle)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to get source container info: %w", err)
	}

	// Build create command for the clone. As in CloneInstance, the limits come
	// from podman inspect and ports/volumes/env are not reconstructed.
	createArgs := []string{"create", "--name", cloneName}

	cpus, memoryMB := p.containerResourceLimits(ctx, containerName)

	if opts.CPUs > 0 {
		cpus = opts.CPUs
	}
	if opts.MemoryMB > 0 {
		memoryMB = opts.MemoryMB
	}

	if cpus > 0 {
		createArgs = append(createArgs, "--cpus", fmt.Sprintf("%d", cpus))
	}
	if memoryMB > 0 {
		createArgs = append(createArgs, "--memory", fmt.Sprintf("%dm", memoryMB))
	}

	// Copy labels from source
	if sourceInfo.Spec.Labels != nil {
		for k, v := range sourceInfo.Spec.Labels {
			createArgs = append(createArgs, "--label", fmt.Sprintf("%s=%s", k, v))
		}
	}

	// Add custom labels
	if opts.Labels != nil {
		for k, v := range opts.Labels {
			createArgs = append(createArgs, "--label", fmt.Sprintf("%s=%s", k, v))
		}
	}

	// Add hospitus label
	createArgs = append(createArgs, "--label", "hospitus.managed=true", imageName)

	createOutput, err := p.cmd().CombinedOutput(ctx, p.podmanBin, createArgs...)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create clone from snapshot: %w: %s", err, string(createOutput))
	}

	if id := extractContainerID(string(createOutput)); id == "" {
		return provider.InstanceHandle{}, fmt.Errorf("failed to get clone container ID from output: %s", string(createOutput))
	}

	// Refresh container info into our cache. On failure, roll back the newly
	// created container so we do not leave an orphaned clone behind.
	if err := p.refreshContainerInfo(ctx, cloneName); err != nil {
		if out, rmErr := p.cmd().CombinedOutput(context.Background(), p.podmanBin, "rm", "--force", cloneName); rmErr != nil {
			p.logger.Warn("failed to roll back clone container after refresh failure",
				"container", cloneName, logging.FieldError, rmErr, "output", strings.TrimSpace(string(out)))
		}
		return provider.InstanceHandle{}, fmt.Errorf("failed to refresh clone container info: %w", err)
	}

	handle := provider.InstanceHandle{
		ID:       cloneName,
		Provider: providerName,
		Metadata: map[string]interface{}{
			"name":  cloneName,
			"image": imageName,
		},
	}

	return handle, nil
}
