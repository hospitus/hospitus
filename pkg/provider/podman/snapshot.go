package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
)

// Ensure PodmanProvider implements SnapshotProvider
var _ provider.SnapshotProvider = (*PodmanProvider)(nil)

const snapshotPrefix = "hospitus-snapshot-"

// Labels stamped on every snapshot image. The image name alone is ambiguous —
// "hospitus-snapshot-web-2-s1" is container "web-2" snapshot "s1" and equally
// container "web" snapshot "2-s1" — so a container's list could claim another
// container's snapshot and deleting it would remove the real one. The container
// and snapshot names are therefore recorded as labels, and both filtering and
// decoding go through them.
const (
	snapshotContainerLabel = "hospitus.snapshot.container"
	snapshotNameLabel      = "hospitus.snapshot.name"
)

// commitArgs builds the podman commit invocation that takes a snapshot.
//
// No --message: podman accepts one only with the docker image format and
// rejects the whole commit otherwise. The provenance is carried by the labels
// instead, which is also what identifies the snapshot unambiguously.
//
// --pause=false because ocijail, the OCI runtime podman uses on FreeBSD, has no
// pause: "ocijail pause <id> failed: exit status 106" fails the whole commit.
// The image is therefore taken from a running container, so a busy one can be
// captured mid-write. Clone takes the same trade-off for the same reason.
func commitArgs(container, image, snapshot string) []string {
	return []string{
		"commit", "--pause=false", "--format", "oci",
		"--change", fmt.Sprintf("LABEL %s=%s", snapshotContainerLabel, container),
		"--change", fmt.Sprintf("LABEL %s=%s", snapshotNameLabel, snapshot),
		container, image,
	}
}

// CreateSnapshot creates a snapshot of a container using podman commit.
func (p *PodmanProvider) CreateSnapshot(ctx context.Context, handle provider.InstanceHandle, name string) (provider.SnapshotHandle, error) {
	containerName := handle.ID

	// Podman owns the container store: asking it is the only answer that holds
	// after a daemon restart while podman was down, when the cache is cold and
	// every snapshot of a live container failed as "not found".
	if !p.containerExists(ctx, containerName) {
		return provider.SnapshotHandle{}, fmt.Errorf("container %s not found", containerName)
	}

	// Generate image name for the snapshot. It is a human-readable handle only;
	// the labels are what identify the snapshot.
	imageName := fmt.Sprintf("%s%s-%s", snapshotPrefix, containerName, name)

	// Output (stdout) alone, so podman's warnings on stderr are not mixed into
	// the image ID.
	out, err := p.cmd().Output(ctx, p.podmanBin, commitArgs(containerName, imageName, name)...)
	if err != nil {
		return provider.SnapshotHandle{}, fmt.Errorf("failed to create snapshot: %w", err)
	}

	imageID := strings.TrimSpace(string(out))

	instanceID := containerName
	p.mu.RLock()
	if info, ok := p.instances[containerName]; ok && info.ID != "" {
		instanceID = info.ID
	}
	p.mu.RUnlock()

	return provider.SnapshotHandle{
		ID:       imageID,
		Instance: instanceID,
		Metadata: map[string]interface{}{
			"name":      name,
			"imageName": imageName,
			"container": containerName,
		},
	}, nil
}

// DeleteSnapshot deletes a snapshot image.
func (p *PodmanProvider) DeleteSnapshot(ctx context.Context, snapshot provider.SnapshotHandle) error {
	imageID := snapshot.ID

	output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "rmi", "--force", imageID)
	if err != nil {
		if strings.Contains(string(output), "image not known") {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete snapshot: %w: %s", err, string(output))
	}

	return nil
}

// RestoreSnapshot restores a container from a snapshot.
// This creates a new container from the snapshot image and replaces the original.
func (p *PodmanProvider) RestoreSnapshot(ctx context.Context, handle provider.InstanceHandle, snapshot provider.SnapshotHandle) error {
	containerName := handle.ID
	imageName, ok := snapshot.Metadata["imageName"].(string)
	if !ok {
		imageName = snapshot.ID
	}

	// Podman is the authority on both existence and state. The cache could be
	// cold (nothing found for a container podman has) or stale — a "stopped"
	// record left the old container running under the .restore-bak name below,
	// still holding its ports.
	if !p.containerExists(ctx, containerName) {
		return fmt.Errorf("container %s not found", containerName)
	}

	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get container state: %w", err)
	}

	// Stop the container if running
	if state == provider.StateRunning || state == provider.StatePaused {
		if output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "stop", containerName); err != nil {
			return fmt.Errorf("failed to stop container: %w: %s", err, string(output))
		}
	}

	// Get the original container configuration
	inspectOutput, err := p.cmd().Output(ctx, p.podmanBin, "inspect", "--format", "json", containerName)
	if err != nil {
		return fmt.Errorf("failed to inspect container: %w", err)
	}

	var inspectData []map[string]interface{}
	if err := json.Unmarshal(inspectOutput, &inspectData); err != nil {
		return fmt.Errorf("failed to parse container info: %w", err)
	}
	if len(inspectData) == 0 {
		return fmt.Errorf("no container info found")
	}

	// Set the old container aside instead of destroying it, so a failed
	// restore can be rolled back. Destroying first (as before) meant a create
	// failure — missing image, full disk, name conflict — lost the container
	// permanently.
	// NOTE: only labels are restored here; published ports, volumes, env, cmd
	// and networks from the original container are NOT reconstructed.
	backupName := containerName + ".restore-bak"
	_ = p.cmd().Run(ctx, p.podmanBin, "rm", "--force", backupName) // clear any stale backup
	if output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "rename", containerName, backupName); err != nil {
		return fmt.Errorf("failed to set aside old container: %w: %s", err, string(output))
	}

	// Create new container from snapshot with the same name
	createArgs := []string{"create", "--name", containerName}

	// Preserve labels, read from the container itself rather than the cache.
	for k, v := range containerLabels(inspectData[0]) {
		createArgs = append(createArgs, "--label", fmt.Sprintf("%s=%s", k, v))
	}

	createArgs = append(createArgs, imageName)
	if output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, createArgs...); err != nil {
		// Roll back: restore the original container by its name.
		_ = p.cmd().Run(ctx, p.podmanBin, "rename", backupName, containerName)
		return fmt.Errorf("failed to create container from snapshot (rolled back): %w: %s", err, string(output))
	}

	// Restore succeeded — remove the set-aside original.
	_ = p.cmd().Run(ctx, p.podmanBin, "rm", "--force", backupName)

	if err := p.refreshContainerInfo(ctx, containerName); err != nil {
		return fmt.Errorf("failed to refresh container info: %w", err)
	}

	return nil
}

// ListSnapshots returns all snapshots for a container.
func (p *PodmanProvider) ListSnapshots(ctx context.Context, handle provider.InstanceHandle) ([]provider.SnapshotInfo, error) {
	containerName := handle.ID

	// List the images labeled as snapshots of this container. Filtering by
	// name would also match another container's images: "web" matches
	// "hospitus-snapshot-web-2-s1", which belongs to "web-2".
	filter := fmt.Sprintf("label=%s=%s", snapshotContainerLabel, containerName)
	output, err := p.cmd().Output(ctx, p.podmanBin, "images", "--format", "json", "--filter", filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}

	if len(output) == 0 || string(output) == "null\n" || string(output) == "[]\n" {
		return []provider.SnapshotInfo{}, nil
	}

	var images []struct {
		ID      string            `json:"Id"`
		Names   []string          `json:"Names"`
		Created int64             `json:"Created"`
		Size    int64             `json:"Size"`
		Labels  map[string]string `json:"Labels"`
	}

	if err := json.Unmarshal(output, &images); err != nil {
		return nil, fmt.Errorf("failed to parse images: %w", err)
	}

	var snapshots []provider.SnapshotInfo
	for _, img := range images {
		// The labels are the snapshot's identity: they say which container it
		// belongs to and what it is called, whatever the image name looks like.
		name, ok := snapshotNameFromLabels(img.Labels, containerName)
		if !ok {
			continue
		}

		// podman `images --format json` emits Created as a Unix timestamp
		// (integer seconds), not an RFC3339 string.
		createdAt := time.Unix(img.Created, 0)

		imageName := ""
		if len(img.Names) > 0 {
			imageName = img.Names[0]
		}

		snapshots = append(snapshots, provider.SnapshotInfo{
			Handle: provider.SnapshotHandle{
				ID:       img.ID,
				Instance: containerName,
				Metadata: map[string]interface{}{
					"name":      name,
					"imageName": imageName,
					"container": containerName,
				},
			},
			Name:      name,
			CreatedAt: createdAt,
			SizeMB:    img.Size / (1024 * 1024),
		})
	}

	return snapshots, nil
}

// containerLabels reads the labels out of one podman inspect record.
func containerLabels(data map[string]interface{}) map[string]string {
	config, ok := data["Config"].(map[string]interface{})
	if !ok {
		return nil
	}
	raw, ok := config["Labels"].(map[string]interface{})
	if !ok {
		return nil
	}
	labels := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			labels[k] = s
		}
	}
	return labels
}

// snapshotNameFromLabels recovers a snapshot's name from the labels its image
// carries, and reports whether the image is a snapshot of containerName.
//
// This replaces decoding the image name. The name is ambiguous whenever a
// container name contains a dash: "hospitus-snapshot-web-2-s1" reads equally as
// container "web-2" snapshot "s1" and container "web" snapshot "2-s1", so a
// list for "web" claimed "web-2"'s snapshot and deleting the phantom removed
// the real image. The labels say which is which.
//
// An image committed before snapshots were labeled has neither label and is
// not listed: it cannot be attributed to a container with any certainty. It
// still exists in podman and can be removed with podman(1).
func snapshotNameFromLabels(labels map[string]string, containerName string) (string, bool) {
	if labels[snapshotContainerLabel] != containerName {
		return "", false
	}
	name := labels[snapshotNameLabel]
	if name == "" {
		return "", false
	}
	return name, true
}
