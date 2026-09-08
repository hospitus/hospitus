package jail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

var _ provider.ExportImportProvider = (*JailProvider)(nil)

// runCommand runs cmd, capturing stderr for diagnostics. Unlike
// (*exec.Cmd).CombinedOutput it does not require Stdout/Stderr to be unset, so
// it is safe for commands that stream stdout to a file or pipe (zfs
// send/receive). CombinedOutput returns "Stdout already set" the moment either
// stream is preset, which made every ZFS export/import fail before running.
func runCommand(cmd *exec.Cmd) error {
	var stderr bytes.Buffer
	if cmd.Stderr == nil || cmd.Stderr == os.Stderr {
		cmd.Stderr = &stderr
	}
	if err := cmd.Run(); err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return fmt.Errorf("%w (output: %s)", err, s)
		}
		return err
	}
	return nil
}

// ExportManifest contains metadata about an exported jail
type ExportManifest struct {
	Version      string                 `json:"version"`
	ExportDate   time.Time              `json:"export_date"`
	JailName     string                 `json:"jail_name"`
	OSType       string                 `json:"os_type"`
	OSVersion    string                 `json:"os_version"`
	Arch         string                 `json:"arch"`
	ZFSDataset   string                 `json:"zfs_dataset,omitempty"`
	UseZFSSend   bool                   `json:"use_zfs_send"`
	Compressed   bool                   `json:"compressed"`
	HasSnapshots bool                   `json:"has_snapshots"`
	Snapshots    []string               `json:"snapshots,omitempty"`
	Networks     []provider.NetworkSpec `json:"networks,omitempty"`
	Checksums    map[string]string      `json:"checksums,omitempty"`
}

// ExportInstance exports a jail to a tarball or ZFS stream for migration.
//
// This implements enhanced export functionality:
//   - Stops the jail if requested
//   - Uses ZFS send/receive for ZFS-based jails (more efficient)
//   - Falls back to tarball for non-ZFS jails
//   - Creates a manifest with metadata
//   - Optionally includes snapshots
//
// Export format:
//   - For ZFS: jail-name.zfs.gz (or .zfs) - ZFS stream
//   - For tar: jail-name.tar.gz (or .tar) - Tarball
//
// Contents:
//   - manifest.json    - Export metadata
//   - config.json      - Jail configuration
//   - rootfs/          - Jail root filesystem (tar only)
//   - zfs.stream       - ZFS stream (ZFS only)
func (p *JailProvider) ExportInstance(ctx context.Context, handle provider.InstanceHandle, exportPath string, opts provider.ExportOptions) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	jailName := handle.ID
	// The name becomes a path under stateDir and a dataset reaching
	// "zfs snapshot -r", "zfs destroy -r" and "zfs send -R".
	if err := validation.ValidateInstanceName(jailName); err != nil {
		return fmt.Errorf("invalid jail name %q: %w", jailName, err)
	}

	// Load jail configuration
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	jailConfig, err := p.loadJailConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load jail configuration: %w", err)
	}

	// Get ZFS dataset info
	zfsDataset := fmt.Sprintf("%s/%s", p.zfsParent, jailName)
	useZFS := p.zfsDatasetExists(ctx, zfsDataset)

	// Stop the instance if requested or required
	if opts.StopInstance {
		state, err := p.GetInstanceState(ctx, handle)
		if err != nil {
			return fmt.Errorf("failed to get instance state: %w", err)
		}
		if state == provider.StateRunning {
			p.logInfo(ctx, "stopping jail before export", "jail", jailName, "export_path", exportPath)
			if err := p.StopInstance(ctx, handle, provider.StopOptions{Timeout: 30 * time.Second}); err != nil {
				return fmt.Errorf("failed to stop instance before export: %w", err)
			}
		}
	}

	// Create temporary directory for export staging
	tempDir, err := os.MkdirTemp("", "hospitus-export-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	exportDir := filepath.Join(tempDir, jailName)
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		return fmt.Errorf("failed to create export directory: %w", err)
	}

	// Create manifest
	manifest := ExportManifest{
		Version:    "1.0",
		ExportDate: time.Now(),
		JailName:   jailName,
		OSType:     jailConfig.Spec.OSType,
		OSVersion:  jailConfig.Spec.OSVersion,
		Arch:       jailConfig.Spec.Arch,
		ZFSDataset: zfsDataset,
		UseZFSSend: useZFS,
		Compressed: opts.Compress,
		Networks:   jailConfig.Networks,
	}

	// Copy configuration
	configDest := filepath.Join(exportDir, "config.json")
	configData, err := json.MarshalIndent(jailConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := os.WriteFile(configDest, configData, 0o600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Export jail data
	if useZFS {
		// Use ZFS send for efficient export
		p.logInfo(ctx, "exporting jail using ZFS send", "jail", jailName, "zfs_dataset", zfsDataset, "export_path", exportPath)

		// Get list of snapshots if requested
		if opts.IncludeSnapshots {
			snapshots, err := p.listZFSSnapshots(ctx, zfsDataset)
			if err == nil && len(snapshots) > 0 {
				manifest.HasSnapshots = true
				manifest.Snapshots = snapshots
			}
		}

		// Create snapshot for export
		exportSnap := fmt.Sprintf("%s@hospitus-export-%d", zfsDataset, time.Now().Unix())
		if output, err := p.cmd().CombinedOutput(ctx, "zfs", "snapshot", "-r", exportSnap); err != nil {
			return fmt.Errorf("failed to create export snapshot: %w (output: %s)", err, string(output))
		}
		defer func() {
			// Detached and bounded: this runs on the way out, including when the
			// caller's context is already canceled — which is precisely when
			// the snapshot would otherwise be left on the pool for ever.
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			defer cancel()
			if out, err := p.cmd().CombinedOutput(cleanupCtx, "zfs", "destroy", "-r", exportSnap); err != nil {
				p.logWarn(ctx, "could not remove the export snapshot", "snapshot", exportSnap,
					"output", strings.TrimSpace(string(out)), logging.FieldError, err)
			}
		}()

		// ZFS send to file
		zfsStreamPath := filepath.Join(exportDir, "zfs.stream")
		var sendCmd *exec.Cmd
		if opts.Compress {
			// Pipe zfs send through gzip using Go pipes (no shell -c)
			zfsStreamPath += ".gz"
			zfsCmd := exec.CommandContext(ctx, "zfs", "send", "-R", exportSnap)
			gzipCmd := exec.CommandContext(ctx, "gzip", "-c")
			outFile, err := os.Create(zfsStreamPath)
			if err != nil {
				return fmt.Errorf("failed to create ZFS stream file: %w", err)
			}
			defer outFile.Close()
			gzipCmd.Stdin, err = zfsCmd.StdoutPipe()
			if err != nil {
				return fmt.Errorf("failed to create pipe: %w", err)
			}
			gzipCmd.Stdout = outFile
			if err := zfsCmd.Start(); err != nil {
				return fmt.Errorf("failed to start ZFS send: %w", err)
			}
			if err := runCommand(gzipCmd); err != nil {
				// zfsCmd is running and writing into a pipe nobody reads any
				// more. Returning here left it unreaped for the daemon's life.
				_ = zfsCmd.Process.Kill()
				_ = zfsCmd.Wait()
				return fmt.Errorf("failed to export ZFS stream: %w", err)
			}
			if err := zfsCmd.Wait(); err != nil {
				return fmt.Errorf("ZFS send failed: %w", err)
			}
		} else {
			sendCmd = exec.CommandContext(ctx, "zfs", "send", "-R", exportSnap)
			outFile, err := os.Create(zfsStreamPath)
			if err != nil {
				return fmt.Errorf("failed to create ZFS stream file: %w", err)
			}
			defer outFile.Close()
			sendCmd.Stdout = outFile
			if err := runCommand(sendCmd); err != nil {
				return fmt.Errorf("failed to export ZFS stream: %w", err)
			}
		}
	} else {
		// Fall back to tarball for non-ZFS
		p.logInfo(ctx, "exporting jail using tar", "jail", jailName, "source_path", jailConfig.Path, "export_path", exportPath)

		jailRoot := jailConfig.Path
		// The rootfs tarball stays uncompressed: the final archive below
		// compresses the whole export directory, and gzipping an already
		// gzipped stream costs a second full pass over the filesystem to
		// produce a slightly larger file. Import still accepts a ".gz" inner
		// tarball, so archives written by earlier versions keep working.
		rootfsDest := filepath.Join(exportDir, "rootfs.tar")
		tarArgs := []string{"tar", "-cf", rootfsDest, "-C", jailRoot, "."}
		if output, err := p.cmd().CombinedOutput(ctx, tarArgs[0], tarArgs[1:]...); err != nil {
			return fmt.Errorf("failed to create rootfs tarball: %w (output: %s)", err, string(output))
		}
	}

	// Write manifest
	manifestPath := filepath.Join(exportDir, "manifest.json")
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}

	// Create final archive
	p.logInfo(ctx, "creating export archive", "jail", jailName, "export_path", exportPath, "compress", opts.Compress, "use_zfs", useZFS)
	var finalArgs []string
	if opts.Compress && !useZFS {
		finalArgs = []string{"tar", "-czf", exportPath, "-C", tempDir, jailName}
	} else {
		finalArgs = []string{"tar", "-cf", exportPath, "-C", tempDir, jailName}
	}
	if output, err := p.cmd().CombinedOutput(ctx, finalArgs[0], finalArgs[1:]...); err != nil {
		return fmt.Errorf("failed to create export archive: %w (output: %s)", err, string(output))
	}

	p.logInfo(ctx, "jail exported successfully", "jail", jailName, "export_path", exportPath)
	return nil
}

// zfsDatasetExists checks if a ZFS dataset exists
func (p *JailProvider) zfsDatasetExists(ctx context.Context, dataset string) bool {
	return p.cmd().Run(ctx, "zfs", "list", "-H", dataset) == nil
}

// listZFSSnapshots lists all snapshots for a dataset
func (p *JailProvider) listZFSSnapshots(ctx context.Context, dataset string) ([]string, error) {
	output, err := p.cmd().Output(ctx, "zfs", "list", "-H", "-t", "snapshot", "-o", "name", "-r", dataset)
	if err != nil {
		return nil, err
	}

	var snapshots []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line != "" {
			snapshots = append(snapshots, line)
		}
	}
	return snapshots, nil
}

// assertSafeTarEntries lists the entries of a tar archive and rejects any that
// are absolute or contain a ".." component, which would let a malicious archive
// write outside the extraction directory when extracted as root.
func (p *JailProvider) assertSafeTarEntries(ctx context.Context, archive string) error {
	out, err := p.cmd().CombinedOutput(ctx, "tar", "-tf", archive)
	if err != nil {
		return fmt.Errorf("failed to list archive entries: %w (output: %s)", err, string(out))
	}
	for _, line := range strings.Split(string(out), "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" {
			continue
		}
		if strings.HasPrefix(entry, "/") || entry == ".." ||
			strings.HasPrefix(entry, "../") || strings.Contains(entry, "/../") ||
			strings.HasSuffix(entry, "/..") {
			return fmt.Errorf("refusing to import archive: unsafe path entry %q", entry)
		}
	}
	return nil
}

// ImportInstance imports a jail from a tarball or ZFS stream.
//
// This implements enhanced import functionality:
//   - Reads manifest to determine import method
//   - Uses ZFS receive for ZFS-exported jails
//   - Falls back to tarball extraction for non-ZFS
//   - Optionally renames the jail
//   - Optionally resets network configuration
//   - Registers the jail with HOSPITUS
//   - Optionally starts the jail after import
func (p *JailProvider) ImportInstance(ctx context.Context, importPath string, opts provider.ImportOptions) (provider.InstanceHandle, error) {
	// SECURITY: Validate import path exists
	if _, err := os.Stat(importPath); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("import file not found: %w", err)
	}

	// Create temporary directory for extraction
	tempDir, err := os.MkdirTemp("", "hospitus-import-*")
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// A dataset created by "zfs receive" below is undone unless the import runs
	// to completion. Both branches register here: the tar one destroys its own
	// dataset on the failures it handles, but the steps after them — the config
	// load and the config write — simply returned, exactly as the receive
	// branch did.
	//
	// zfsDestroyCleanup detaches the context itself, so a caller who canceled
	// still gets the dataset removed rather than left holding the name.
	var createdDataset string
	imported := false

	p.logInfo(ctx, "extracting import archive", "import_path", importPath, "temp_dir", tempDir)

	// SECURITY: extraction runs as root, so reject any archive that contains
	// absolute paths or ".." traversal before extracting (CWE-22). This check
	// is what confines the extraction; both tar implementations strip a
	// leading "/" on their own, and neither stops "../" escapes.
	if err := p.assertSafeTarEntries(ctx, importPath); err != nil {
		return provider.InstanceHandle{}, err
	}

	// Extract tarball to temp directory.
	//
	// The flags stay within what bsdtar and GNU tar both accept. FreeBSD is
	// the primary platform and its tar is bsdtar, which has no
	// --no-absolute-names: passing it aborted every import with "Option
	// --no-absolute-names is not supported" before a single file was read.
	output, err := p.cmd().CombinedOutput(ctx, "tar", "-xf", importPath, "-C", tempDir)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to extract tarball: %w (output: %s)", err, string(output))
	}

	// Find the jail directory in the extracted content
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to read temp directory: %w", err)
	}

	var jailDirName string
	for _, entry := range entries {
		if entry.IsDir() {
			jailDirName = entry.Name()
			break
		}
	}

	if jailDirName == "" {
		return provider.InstanceHandle{}, fmt.Errorf("no jail directory found in tarball")
	}

	extractedDir := filepath.Join(tempDir, jailDirName)

	// Try to load manifest (new format)
	var manifest ExportManifest
	manifestPath := filepath.Join(extractedDir, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	hasManifest := err == nil
	if hasManifest {
		if err := json.Unmarshal(manifestData, &manifest); err != nil {
			return provider.InstanceHandle{}, fmt.Errorf("failed to parse manifest: %w", err)
		}
	}

	// Determine target jail name
	targetJailName := jailDirName
	if opts.NewName != "" {
		targetJailName = opts.NewName
	} else if hasManifest && manifest.JailName != "" {
		targetJailName = manifest.JailName
	}

	// SECURITY: Validate target name
	if err := validation.ValidateInstanceName(targetJailName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid jail name: %w", err)
	}

	// Lock to prevent TOCTOU race between existence check and ZFS dataset creation.
	// This serializes concurrent CreateInstance/ImportInstance calls for the same provider.
	p.createMu.Lock()
	defer p.createMu.Unlock()

	// Registered after the unlock so it runs before it: defers unwind in
	// reverse, and destroying a failed import's dataset outside the lock that
	// serializes creation lets another create start on the same name while the
	// destroy is still in flight.
	defer func() {
		if !imported && createdDataset != "" {
			p.zfsDestroyCleanup(ctx, createdDataset)
		}
	}()

	// Check if target jail already exists (now protected by mutex)
	targetZFSDataset := fmt.Sprintf("%s/%s", p.zfsParent, targetJailName)
	if p.zfsDatasetExists(ctx, targetZFSDataset) {
		return provider.InstanceHandle{}, fmt.Errorf("jail %s already exists (ZFS dataset)", targetJailName)
	}

	configDest := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", targetJailName))
	if _, err := os.Stat(configDest); err == nil {
		return provider.InstanceHandle{}, fmt.Errorf("jail %s already exists (config file)", targetJailName)
	}

	// Import based on export type
	var jailPath string
	if hasManifest && manifest.UseZFSSend {
		p.logInfo(ctx, "importing jail using ZFS receive", "jail", targetJailName, "zfs_dataset", targetZFSDataset, "import_path", importPath)

		// Find ZFS stream file
		zfsStreamPath := filepath.Join(extractedDir, "zfs.stream")
		if _, err := os.Stat(zfsStreamPath + ".gz"); err == nil {
			zfsStreamPath += ".gz"
		}

		// ZFS receive
		var recvCmd *exec.Cmd
		if strings.HasSuffix(zfsStreamPath, ".gz") {
			// Pipe gunzip through zfs receive using Go pipes (no shell -c)
			gunzipCmd := exec.CommandContext(ctx, "gunzip", "-c", zfsStreamPath)
			recvCmd = exec.CommandContext(ctx, "zfs", "receive", "-F", targetZFSDataset)
			var err error
			recvCmd.Stdin, err = gunzipCmd.StdoutPipe()
			if err != nil {
				return provider.InstanceHandle{}, fmt.Errorf("failed to create pipe: %w", err)
			}
			if err := gunzipCmd.Start(); err != nil {
				return provider.InstanceHandle{}, fmt.Errorf("failed to start gunzip: %w", err)
			}
			if err := runCommand(recvCmd); err != nil {
				// gunzip is still running with nobody reading its pipe.
				_ = gunzipCmd.Process.Kill()
				_ = gunzipCmd.Wait()
				return provider.InstanceHandle{}, fmt.Errorf("failed to receive ZFS stream: %w", err)
			}
			// Reap gunzip; receive already succeeded so its exit status is not actionable
			_ = gunzipCmd.Wait()
		} else {
			recvCmd = exec.CommandContext(ctx, "zfs", "receive", "-F", targetZFSDataset)
			inFile, err := os.Open(zfsStreamPath)
			if err != nil {
				return provider.InstanceHandle{}, fmt.Errorf("failed to open ZFS stream: %w", err)
			}
			defer inFile.Close()
			recvCmd.Stdin = inFile
			if output, err := recvCmd.CombinedOutput(); err != nil {
				return provider.InstanceHandle{}, fmt.Errorf("failed to receive ZFS stream: %w (output: %s)", err, string(output))
			}
		}

		// From here the dataset exists. The tar branch below destroys it on every
		// failure; this one returned and left it behind, holding the space and
		// the name against a later import.
		createdDataset = targetZFSDataset

		// Get mountpoint
		jailPath, err = p.getZFSMountpoint(ctx, targetZFSDataset)
		if err != nil {
			return provider.InstanceHandle{}, fmt.Errorf("failed to get ZFS mountpoint: %w", err)
		}
	} else {
		p.logInfo(ctx, "importing jail using tar extraction", "jail", targetJailName, "zfs_dataset", targetZFSDataset, "import_path", importPath)

		// Create ZFS dataset for jail
		if err := p.createZFSDataset(ctx, targetZFSDataset); err != nil {
			return provider.InstanceHandle{}, fmt.Errorf("failed to create ZFS dataset: %w", err)
		}
		createdDataset = targetZFSDataset

		jailPath, err = p.getZFSMountpoint(ctx, targetZFSDataset)
		if err != nil {
			// Rollback cleanup is best-effort
			_ = p.destroyZFSDataset(ctx, targetZFSDataset, true)
			return provider.InstanceHandle{}, fmt.Errorf("failed to get ZFS mountpoint: %w", err)
		}

		// Find rootfs tarball
		rootfsTar := filepath.Join(extractedDir, "rootfs.tar")
		if _, err := os.Stat(rootfsTar + ".gz"); err == nil {
			rootfsTar += ".gz"
		}

		// Extract rootfs if present (new format)
		if _, err := os.Stat(rootfsTar); err == nil {
			var tarArgs []string
			if strings.HasSuffix(rootfsTar, ".gz") {
				tarArgs = []string{"tar", "-xzf", rootfsTar, "-C", jailPath}
			} else {
				tarArgs = []string{"tar", "-xf", rootfsTar, "-C", jailPath}
			}
			if output, err := p.cmd().CombinedOutput(ctx, tarArgs[0], tarArgs[1:]...); err != nil {
				// Rollback cleanup is best-effort
				_ = p.destroyZFSDataset(ctx, targetZFSDataset, true)
				return provider.InstanceHandle{}, fmt.Errorf("failed to extract rootfs: %w (output: %s)", err, string(output))
			}
		} else {
			// Old format - rootfs is a directory
			oldRootfs := filepath.Join(extractedDir, "rootfs")
			if _, err := os.Stat(oldRootfs); err == nil {
				// Copy files
				if output, err := p.cmd().CombinedOutput(ctx, "cp", "-a", oldRootfs+"/.", jailPath+"/"); err != nil {
					// Rollback cleanup is best-effort
					_ = p.destroyZFSDataset(ctx, targetZFSDataset, true)
					return provider.InstanceHandle{}, fmt.Errorf("failed to copy rootfs: %w (output: %s)", err, string(output))
				}
			}
		}
	}

	// Load or create jail configuration
	var cfg *jailConfig
	configSrc := filepath.Join(extractedDir, "config.json")
	if _, err := os.Stat(configSrc); err == nil {
		// New format - config.json
		cfg, err = p.loadJailConfig(configSrc)
		if err != nil {
			return provider.InstanceHandle{}, fmt.Errorf("failed to load config: %w", err)
		}
	} else {
		// Old format - jail.conf in jail directory
		oldConfigPath := filepath.Join(extractedDir, "jail.conf")
		if _, err := os.Stat(oldConfigPath); err == nil {
			cfg, err = p.loadJailConfig(oldConfigPath)
			if err != nil {
				return provider.InstanceHandle{}, fmt.Errorf("failed to load old config: %w", err)
			}
		} else {
			// Create default config
			cfg = &jailConfig{
				Name:           targetJailName,
				Path:           jailPath,
				JailParameters: DefaultJailParameters(),
			}
		}
	}

	// Update configuration for new location
	cfg.Name = targetJailName
	cfg.Path = jailPath

	// Handle network configuration options
	if opts.NewIP != "" {
		// Set specific IP for the first network interface
		if len(cfg.Networks) > 0 {
			cfg.Networks[0].IPv4 = opts.NewIP
		}
		// Reset MAC if also requested
		if opts.ResetMAC {
			for i := range cfg.Networks {
				cfg.Networks[i].MAC = "" // Will be regenerated
			}
		}
	} else if opts.ResetMAC {
		// Clear network configuration to get new IPs via DHCP
		for i := range cfg.Networks {
			cfg.Networks[i].IPv4 = "dhcp"
			cfg.Networks[i].MAC = "" // Will be regenerated
		}
	}

	// Save configuration
	if err := p.saveJailConfig(cfg, configDest); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to save config: %w", err)
	}

	// Create instance handle
	handle := provider.InstanceHandle{
		ID:       targetJailName,
		Provider: "jail",
		Metadata: map[string]interface{}{
			"zfs_dataset": targetZFSDataset,
			"mountpoint":  jailPath,
			"imported":    true,
		},
	}

	// The dataset is now recorded and reachable: past this point it is the
	// caller's, not something to undo.
	imported = true

	p.logInfo(ctx, "jail imported successfully", "jail", targetJailName, "zfs_dataset", targetZFSDataset, "mountpoint", jailPath)

	// Start the instance if requested
	if opts.StartAfterImport {
		p.logInfo(ctx, "starting imported jail", "jail", targetJailName)
		if err := p.StartInstance(ctx, handle); err != nil {
			return handle, fmt.Errorf("instance imported but failed to start: %w", err)
		}
		p.logInfo(ctx, "imported jail started successfully", "jail", targetJailName)
	}

	return handle, nil
}
