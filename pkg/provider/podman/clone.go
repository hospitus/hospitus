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
	// The source reaches "podman container exists" below, before anything else
	// looks at it — and a leading dash there is an option, not a name.
	if err := validation.ValidateInstanceName(source.ID); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid source name: %w", err)
	}

	sourceName := source.ID

	// Podman is the authority, not the cache: after a daemon restart the map is
	// empty, and a container podman has would be reported as missing before
	// GetInstanceInfo ever looked at it.
	if !p.containerExists(ctx, sourceName) {
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
		// Its own context so cleanup still runs when ctx is canceled, but
		// bounded: Background never expires, and a hung podman would block the
		// caller for as long as it hung.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if out, err := p.cmd().CombinedOutput(cleanupCtx, p.podmanBin, "rmi", "--force", cloneImage); err != nil {
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
	sourceInfo, err := p.GetInstanceInfo(ctx, source)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to get source container info: %w", err)
	}
	if err := p.createClone(ctx, cloneName, cloneImage, p.recreateSpec(ctx, sourceInfo.Spec), sourceName, opts); err != nil {
		return provider.InstanceHandle{}, err
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
func (p *PodmanProvider) containerResourceLimits(ctx context.Context, containerName string) (nanoCPUs, memoryMB int64) {
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
	// In nano-CPUs, not whole ones: dividing here truncated "--cpus 0.5" to
	// zero, which buildCreateArgs reads as "no limit" — the clone of a
	// half-CPU container ran unbounded — and "--cpus 1.5" to one.
	if host.NanoCpus > 0 {
		nanoCPUs = host.NanoCpus
	}
	if host.Memory > 0 {
		memoryMB = host.Memory / (1024 * 1024)
	}
	return nanoCPUs, memoryMB
}

// CloneFromSnapshot creates a new container from a snapshot image.
func (p *PodmanProvider) CloneFromSnapshot(ctx context.Context, snapshot provider.SnapshotHandle, cloneName string, opts provider.CloneOptions) (provider.InstanceHandle, error) {
	if err := validation.ValidateInstanceName(cloneName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid clone name: %w", err)
	}

	containerName, err := p.snapshotContainerFor(snapshot)
	if err != nil {
		return provider.InstanceHandle{}, err
	}

	imageName, err := p.snapshotImageFor(snapshot)
	if err != nil {
		return provider.InstanceHandle{}, err
	}
	if err := p.imageIsSnapshotOf(ctx, imageName, snapshot.Instance); err != nil {
		return provider.InstanceHandle{}, err
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
	// The source container may be gone — surviving it is what a snapshot is
	// for. Its settings are replicated when it is still there, and the clone
	// falls back to what the snapshot image itself carries when it is not.
	var sourceSpec provider.InstanceSpec
	if sourceInfo, err := p.GetInstanceInfo(ctx, sourceHandle); err == nil {
		sourceSpec = p.recreateSpec(ctx, sourceInfo.Spec)
	} else {
		p.logger.Info("source container is gone; cloning from the snapshot image alone",
			"container", containerName, "snapshot", imageName, logging.FieldError, err)
	}

	// Build create command for the clone. As in CloneInstance, the limits come
	// from podman inspect and ports/volumes/env are not reconstructed.
	if err := p.createClone(ctx, cloneName, imageName, sourceSpec, containerName, opts); err != nil {
		return provider.InstanceHandle{}, err
	}

	handle := provider.InstanceHandle{
		ID:       cloneName,
		Provider: providerName,
		Metadata: map[string]interface{}{
			"name":  cloneName,
			"image": imageName,
		},
	}

	// The same metadata CloneInstance records. Both methods take the option and
	// both produce a container sharing the source's image layers, so reporting
	// it in one and not the other described the same result two ways.
	if opts.LinkedClone {
		handle.Metadata["linked_clone"] = true
		handle.Metadata["clone_of"] = snapshot.Instance
	}

	return handle, nil
}

// createClone runs the "podman create" the two clone paths share: resolve the
// source's limits, apply the caller's overrides, build the arguments, create
// the container, and roll it back if the cache cannot be refreshed afterwards.
//
// Both paths carried these thirty lines, and had already drifted — one refused
// a missing container ID that the other accepted, and the resource limits were
// read two different ways.
func (p *PodmanProvider) createClone(ctx context.Context, cloneName, image string, spec provider.InstanceSpec, limitsFrom string, opts provider.CloneOptions) error {
	nanoCPUs, memoryMB := p.containerResourceLimits(ctx, limitsFrom)
	if opts.CPUs > 0 {
		nanoCPUs = int64(opts.CPUs) * nanoCPUsPerCPU
	}
	if opts.MemoryMB > 0 {
		memoryMB = opts.MemoryMB
	}

	createArgs, err := buildCreateArgs(cloneName, image, spec, opts.Labels, nanoCPUs, memoryMB)
	if err != nil {
		return err
	}

	createOutput, err := p.cmd().CombinedOutput(ctx, p.podmanBin, createArgs...)
	if err != nil {
		return fmt.Errorf("failed to create clone container: %w: %s", err, string(createOutput))
	}

	// From here the container exists, so every failure below removes it rather
	// than leaving an orphan. The unreadable-ID case used to return without
	// doing so, and that container then blocked the name on a retry.
	created := true
	defer func() {
		if !created {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if out, rmErr := p.cmd().CombinedOutput(rollbackCtx, p.podmanBin, "rm", "--force", cloneName); rmErr != nil {
			p.logger.Warn("failed to roll back clone container",
				"container", cloneName, logging.FieldError, rmErr, "output", strings.TrimSpace(string(out)))
		}
	}()

	if id := extractContainerID(string(createOutput)); id == "" {
		return fmt.Errorf("failed to get clone container ID from output: %s", string(createOutput))
	}
	if err := p.refreshContainerInfo(ctx, cloneName); err != nil {
		return fmt.Errorf("failed to refresh clone container info: %w", err)
	}

	created = false
	return nil
}
