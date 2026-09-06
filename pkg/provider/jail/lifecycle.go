package jail

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// restartSettleDelay is a short pause between stopping and starting a jail on
// restart, giving the kernel time to fully tear down the previous instance.
const restartSettleDelay = 1 * time.Second

// RestartInstance restarts a jail
func (p *JailProvider) RestartInstance(ctx context.Context, handle provider.InstanceHandle) (err error) {
	// Attach provider context so the API layer can tell a failed restart from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("jail", "restart", handle.ID, err) }()

	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()

	// Stop then start
	if err := p.StopInstance(ctx, handle, provider.StopOptions{}); err != nil {
		return err
	}

	// Small, cancellable delay to ensure clean shutdown before restart.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(restartSettleDelay):
	}

	return p.StartInstance(ctx, handle)
}

// umountJail unmounts every filesystem mounted under the jail root.
func (p *JailProvider) umountJail(ctx context.Context, jailName string) {
	// Defense-in-depth: validate jail name before constructing paths
	if err := validation.ValidateInstanceName(jailName); err != nil {
		p.logWarn(ctx, "Skipping umount for invalid jail name", "jail", jailName, "error", err)
		return
	}

	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	cfg, err := p.loadJailConfig(configPath)
	if err != nil || cfg.Path == "" {
		return
	}
	jailPath := cfg.Path

	for _, target := range p.jailMounts(ctx, jailPath) {
		if err := p.cmd().Run(ctx, "umount", "-f", target); err != nil {
			p.logWarn(ctx, "Failed to unmount during jail cleanup", "jail", jailName, "target", target, "error", err)
		}
	}
}

// jailMounts returns every mount point under root, deepest first.
//
// The list is read from the mount table rather than guessed. A jail carries
// whatever its configuration asked for — devfs, fdescfs, procfs, linprocfs,
// the nullfs volumes it declared — plus anything an operator mounted by hand,
// and a fixed list unmounts neither the ones it does not know about nor the
// ones nested inside them. Sorting by descending path length puts a child
// before its parent, which is the only order umount(8) accepts.
func (p *JailProvider) jailMounts(ctx context.Context, root string) []string {
	out, err := p.cmd().Output(ctx, "mount", "-p")
	if err != nil {
		p.logWarn(ctx, "Failed to read the mount table; leaving the jail's mounts in place", "root", root, "error", err)
		return nil
	}

	prefix := strings.TrimSuffix(root, "/") + "/"
	var points []string
	for _, line := range strings.Split(string(out), "\n") {
		// mount -p prints fstab fields: device, mount point, type, options.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.HasPrefix(fields[1], prefix) {
			points = append(points, fields[1])
		}
	}

	sort.Slice(points, func(i, j int) bool { return len(points[i]) > len(points[j]) })
	return points
}
