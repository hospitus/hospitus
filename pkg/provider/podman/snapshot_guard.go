package podman

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// snapshotImageFor resolves the image a snapshot handle names, bound to the
// instance the same handle declares.
//
// The handle comes from the caller. CreateSnapshot names its image
// "<snapshotPrefix><container>-<snapshot>", so an image belonging to this
// instance is recognizable; anything else is another container's snapshot, an
// unrelated image on the host, or — with a leading dash — an option for the
// podman command it is about to be spliced into.
func (p *PodmanProvider) snapshotImageFor(snapshot provider.SnapshotHandle) (string, error) {
	imageName, named := snapshot.Metadata["imageName"].(string)
	if !named || imageName == "" {
		// podman reports no Names for an untagged image, and ListSnapshots then
		// stores the id. The prefix check below cannot apply to an id, so the
		// caller checks the labels instead — which is the stronger test anyway.
		if snapshot.ID == "" {
			return "", fmt.Errorf("invalid snapshot metadata: missing imageName")
		}
		// The id becomes a podman argument, and this fallback is the one path
		// that does not go through the name prefix below. A podman image id is
		// a sha256 digest in hex, full or abbreviated, so the shape is checked
		// rather than only the characters that would make it an option: that
		// admits nothing but an id.
		if !imageIDPattern.MatchString(snapshot.ID) {
			return "", fmt.Errorf("refusing snapshot id %q: not an image id", snapshot.ID)
		}
		if err := validation.ValidateInstanceName(snapshot.Instance); err != nil {
			return "", fmt.Errorf("invalid snapshot instance %q: %w", snapshot.Instance, err)
		}
		return snapshot.ID, nil
	}
	if err := validation.ValidateInstanceName(snapshot.Instance); err != nil {
		return "", fmt.Errorf("invalid snapshot instance %q: %w", snapshot.Instance, err)
	}
	// podman reports an image as "localhost/hospitus-snapshot-web-s1:latest",
	// and ListSnapshots stores that form. Comparing the raw string against the
	// bare prefix rejected every handle the provider itself had produced.
	if want := snapshotPrefix + snapshot.Instance + "-"; !strings.HasPrefix(bareImageName(imageName), want) {
		return "", fmt.Errorf("image %s is not a snapshot of the instance %q it is presented for",
			imageName, snapshot.Instance)
	}
	return imageName, nil
}

// imageIDPattern matches a podman image id: a hex sha256 digest, abbreviated
// to at least the twelve characters "podman images" prints.
var imageIDPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

// bareImageName strips the registry and the tag podman adds around an image
// name, leaving the name this provider chose when it committed the snapshot.
func bareImageName(image string) string {
	if i := strings.LastIndex(image, "/"); i >= 0 {
		image = image[i+1:]
	}
	if i := strings.LastIndex(image, ":"); i > 0 {
		image = image[:i]
	}
	return image
}

// snapshotContainerFor resolves the container a snapshot handle names. It must
// be the instance the handle declares: the two are set from the same value at
// creation, and a disagreement means the handle was assembled elsewhere.
func (p *PodmanProvider) snapshotContainerFor(snapshot provider.SnapshotHandle) (string, error) {
	containerName, ok := snapshot.Metadata["container"].(string)
	if !ok || containerName == "" {
		return "", fmt.Errorf("invalid snapshot metadata: missing container name")
	}
	if err := validation.ValidateInstanceName(containerName); err != nil {
		return "", fmt.Errorf("invalid container name %q: %w", containerName, err)
	}
	if snapshot.Instance != "" && containerName != snapshot.Instance {
		return "", fmt.Errorf("snapshot names container %s but is presented for %q",
			containerName, snapshot.Instance)
	}
	return containerName, nil
}
