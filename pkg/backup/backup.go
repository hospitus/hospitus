// Package backup provides automated backup and restore capabilities for instances.
// It supports scheduled ZFS snapshots, remote backup via ZFS send/receive,
// retention policies, and verified restore operations.
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/validation"
)

// BackupType defines the type of backup
type BackupType string

const (
	BackupTypeSnapshot BackupType = "snapshot"    // Local ZFS snapshot
	BackupTypeFull     BackupType = "full"        // Full backup (ZFS send)
	BackupTypeIncr     BackupType = "incremental" // Incremental backup
)

// BackupStatus represents the status of a backup
type BackupStatus string

const (
	BackupStatusPending   BackupStatus = "pending"
	BackupStatusRunning   BackupStatus = "running"
	BackupStatusCompleted BackupStatus = "completed"
	BackupStatusFailed    BackupStatus = "failed"
)

// BackupConfig defines backup configuration for an instance
type BackupConfig struct {
	// Enabled indicates if automatic backups are enabled
	Enabled bool

	// Schedule is when automatic backups run: "hourly", "daily", "weekly",
	// "monthly", or a Go duration such as "6h". Cron expressions are not
	// supported.
	Schedule string

	// Retention defines how many backups to keep
	Retention RetentionPolicy

	// Destination for remote backups (optional)
	// Format: user@host:pool/dataset or local path
	Destination string

	// Compression for backup streams
	Compression string // none, gzip, lz4, zstd

	// Encryption key file path. Encryption is not implemented: the API refuses
	// a configuration that sets this rather than record a plaintext transfer
	// as encrypted.
	EncryptionKeyFile string

	// PreBackupHook runs before backup
	PreBackupHook string

	// PostBackupHook runs after backup
	PostBackupHook string
}

// RetentionPolicy defines backup retention
type RetentionPolicy struct {
	// KeepLast keeps the last N backups
	KeepLast int

	// KeepHourly keeps the last N hourly backups
	KeepHourly int

	// KeepDaily keeps the last N daily backups
	KeepDaily int

	// KeepWeekly keeps the last N weekly backups
	KeepWeekly int

	// KeepMonthly keeps the last N monthly backups
	KeepMonthly int

	// MinAge is the minimum age before a backup can be pruned
	MinAge time.Duration
}

// BackupInfo represents information about a backup
type BackupInfo struct {
	ID           string       // Unique backup ID
	InstanceID   string       // Instance ID
	Type         BackupType   // Type of backup
	Status       BackupStatus // Current status
	CreatedAt    time.Time    // When backup was created
	CompletedAt  time.Time    // When backup completed
	Size         int64        // Size in bytes
	SnapshotName string       // ZFS snapshot name
	Dataset      string       // dataset the snapshot was taken of, resolved at creation
	Destination  string       // Where the config said to put the backup
	Location     string       // Where the stream actually landed: a file, or host:path
	Compressed   bool         // Whether backup is compressed
	Encrypted    bool         // Whether backup is encrypted
	Error        string       // Error message if failed
	Verified     bool         // Whether backup has been verified

	// BaseSnapshot is the dataset@snapshot an incremental stream was sent
	// against. Empty for full and snapshot backups — and for records made
	// before it was tracked, whose chain is unknown and can only be warned
	// about at verification.
	BaseSnapshot string `json:"BaseSnapshot,omitempty"`
}

// BackupManager manages automated backups
type BackupManager struct {
	mu         sync.RWMutex
	configs    map[string]*BackupConfig // instanceID -> config
	backups    map[string][]*BackupInfo // instanceID -> backups
	schedules  map[string]chan struct{} // instanceID -> stop channel
	dataDir    string                   // Base directory for backup data
	resolve    DatasetResolver          // reports the dataset holding an instance
	ds         *datastore.Datastore     // Reference to datastore for persistence
	logger     *slog.Logger             // Structured logger
	wg         sync.WaitGroup           // tracks running schedule goroutines
	rootCtx    context.Context          // parent context for scheduled backups
	rootCancel context.CancelFunc       // cancels all in-flight scheduled backups
}

// DatasetResolver reports the ZFS dataset that holds an instance's data.
//
// The manager does not derive the name. A jail's dataset is recorded by the jail
// provider in its handle, a bhyve VM's is the parent of its ZVOL disks, and a
// podman container has none at all — deriving <pool>/jails/<id> for every
// provider names a dataset that does not exist.
type DatasetResolver func(ctx context.Context, instanceID string) (string, error)

// NewBackupManager creates a new backup manager
func NewBackupManager(dataDir string, resolve DatasetResolver, ds *datastore.Datastore) *BackupManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &BackupManager{
		configs:    make(map[string]*BackupConfig),
		backups:    make(map[string][]*BackupInfo),
		schedules:  make(map[string]chan struct{}),
		dataDir:    dataDir,
		resolve:    resolve,
		ds:         ds,
		logger:     logging.WithComponent("backup"),
		rootCtx:    ctx,
		rootCancel: cancel,
	}
}

// Configure sets the backup configuration for an instance
func (bm *BackupManager) Configure(instanceID string, config *BackupConfig) error {
	if config == nil {
		return fmt.Errorf("backup configuration is required")
	}
	if err := validation.ValidateInstanceName(instanceID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", instanceID, err)
	}

	// Everything that can refuse the request runs first. Stopping the running
	// schedule before validating left the instance with no schedule, no stored
	// configuration and an error — backups silently off after a bad request.
	//
	// A schedule we cannot parse is refused rather than handed to a goroutine
	// that logs and exits.
	if config.Enabled && config.Schedule != "" && parseSchedule(config.Schedule) == 0 {
		return fmt.Errorf("invalid backup schedule %q: use hourly/daily/weekly/monthly or a Go duration like 6h", config.Schedule)
	}
	// ValidateCompression carries both rules — the algorithm must be known, and
	// compression cannot travel to a remote destination. Without this call the
	// value reaches compressorFor, which quietly answers "no compressor" and
	// writes the stream plain while the record says Compressed: false.
	if err := ValidateCompression(config.Compression, config.Destination); err != nil {
		return err
	}

	bm.mu.Lock()
	defer bm.mu.Unlock()

	// Stop existing schedule if any
	if stop, ok := bm.schedules[instanceID]; ok {
		close(stop)
		delete(bm.schedules, instanceID)
	}

	// A copy: the caller keeps its pointer, and editing it afterwards would
	// change what the manager runs on without going through Configure.
	stored := *config
	bm.configs[instanceID] = &stored

	// A configuration held only in memory is gone at the next restart, and the
	// schedule with it: backups stop silently and nothing says so.
	if err := bm.persistConfig(instanceID, &stored); err != nil {
		return err
	}

	// Start scheduled backups if enabled
	if config.Enabled && config.Schedule != "" {
		stop := make(chan struct{})
		bm.schedules[instanceID] = stop
		bm.wg.Add(1)
		go bm.runSchedule(instanceID, config.Schedule, stop)
	}

	return nil
}

// persistConfig writes a configuration to the datastore. A manager without one
// keeps its configurations in memory only, which is what tests construct.
func (bm *BackupManager) persistConfig(instanceID string, config *BackupConfig) error {
	if bm.ds == nil {
		return nil
	}
	stored := storedFromConfig(config)
	if err := bm.ds.UpdateBackupConfig(context.Background(), instanceID, stored); err != nil {
		return fmt.Errorf("failed to persist backup configuration for %s: %w", instanceID, err)
	}
	return nil
}

// storedFromConfig is the write half of configFromStored; keeping the pair
// adjacent is what makes a dropped field visible.
func storedFromConfig(config *BackupConfig) *datastore.InstanceBackupConfig {
	stored := &datastore.InstanceBackupConfig{
		Enabled:        config.Enabled,
		Schedule:       config.Schedule,
		Destination:    config.Destination,
		Compression:    config.Compression,
		PreBackupHook:  config.PreBackupHook,
		PostBackupHook: config.PostBackupHook,
		KeepLast:       config.Retention.KeepLast,
		KeepHourly:     config.Retention.KeepHourly,
		KeepDaily:      config.Retention.KeepDaily,
		KeepWeekly:     config.Retention.KeepWeekly,
		KeepMonthly:    config.Retention.KeepMonthly,
	}
	if config.Retention.MinAge > 0 {
		stored.MinAge = config.Retention.MinAge.String()
	}
	return stored
}

// configFromStored rebuilds a configuration from what was written.
func configFromStored(stored *datastore.InstanceBackupConfig) *BackupConfig {
	var minAge time.Duration
	if stored.MinAge != "" {
		if d, err := time.ParseDuration(stored.MinAge); err == nil && d > 0 {
			minAge = d
		}
	}
	return &BackupConfig{
		Enabled:        stored.Enabled,
		Schedule:       stored.Schedule,
		Destination:    stored.Destination,
		Compression:    stored.Compression,
		PreBackupHook:  stored.PreBackupHook,
		PostBackupHook: stored.PostBackupHook,
		Retention: RetentionPolicy{
			KeepLast:    stored.KeepLast,
			KeepHourly:  stored.KeepHourly,
			KeepDaily:   stored.KeepDaily,
			KeepWeekly:  stored.KeepWeekly,
			KeepMonthly: stored.KeepMonthly,
			MinAge:      minAge,
		},
	}
}

// GetConfig returns the backup configuration for an instance
func (bm *BackupManager) GetConfig(instanceID string) *BackupConfig {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	c, ok := bm.configs[instanceID]
	if !ok || c == nil {
		return nil
	}
	cp := *c
	return &cp
}

// Start starts the backup manager and initializes all scheduled backups.
// This is called during daemon startup.
func (bm *BackupManager) Start(ctx context.Context) error {
	// Without this the manager starts empty: configured schedules never resume
	// after a restart, and every existing backup ID becomes unknown, so it can
	// no longer be listed, verified, restored or deleted.
	if err := bm.reload(ctx); err != nil {
		return err
	}
	bm.mu.RLock()
	configured := len(bm.configs)
	bm.mu.RUnlock()
	bm.logger.Info("Backup manager started", logging.FieldAction, "start",
		"configurations", configured, "backups", len(bm.ListAllBackups()))

	// Tie the scheduled-backup lifetime to the caller's context: when it is
	// canceled (daemon shutdown), stop all schedules and abort in-flight runs.
	if ctx != nil {
		go func() {
			<-ctx.Done()
			bm.Stop()
		}()
	}
	return nil
}

// reload restores configurations and backup records from the datastore, and
// restarts the schedules the configurations ask for.
func (bm *BackupManager) reload(ctx context.Context) error {
	if bm.ds == nil {
		return nil
	}

	instances, err := bm.ds.ListInstances(ctx, datastore.InstanceFilter{})
	if err != nil {
		return fmt.Errorf("failed to read backup configurations: %w", err)
	}
	for _, instance := range instances {
		if instance.Backup == nil {
			continue
		}
		// Configure restarts the schedule and writes the configuration back,
		// which is harmless: it is the same configuration.
		if err := bm.Configure(instance.ID, configFromStored(instance.Backup)); err != nil {
			bm.logger.Warn("Stored backup configuration cannot be used",
				logging.FieldInstance, instance.ID, logging.FieldError, err)
		}
	}

	records, err := bm.ds.ListBackupRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to read backup records: %w", err)
	}
	bm.mu.Lock()
	defer bm.mu.Unlock()
	for _, record := range records {
		var info BackupInfo
		if err := json.Unmarshal(record.Record, &info); err != nil {
			bm.logger.Warn("Stored backup record cannot be read",
				"backup", record.ID, logging.FieldError, err)
			continue
		}
		bm.backups[record.InstanceID] = append(bm.backups[record.InstanceID], &info)
	}
	return nil
}

// Stop halts all scheduled backups, cancels any in-flight scheduled run, and
// waits for the schedule goroutines to exit. It is safe to call more than once.
func (bm *BackupManager) Stop() {
	bm.mu.Lock()
	for id, stop := range bm.schedules {
		close(stop)
		delete(bm.schedules, id)
	}
	bm.mu.Unlock()

	// Abort any backup currently running inside a schedule goroutine.
	bm.rootCancel()

	bm.wg.Wait()
}

// RemoveConfig removes backup configuration for an instance
func (bm *BackupManager) RemoveConfig(instanceID string) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if stop, ok := bm.schedules[instanceID]; ok {
		close(stop)
		delete(bm.schedules, instanceID)
	}
	delete(bm.configs, instanceID)

	if bm.ds != nil {
		if err := bm.ds.UpdateBackupConfig(context.Background(), instanceID, nil); err != nil {
			bm.logger.Warn("Backup configuration removed in memory only",
				logging.FieldInstance, instanceID, logging.FieldError, err)
		}
	}
}

// datasetFor asks the resolver for the dataset holding an instance.
func (bm *BackupManager) datasetFor(ctx context.Context, instanceID string) (string, error) {
	if bm.resolve == nil {
		return "", fmt.Errorf("no dataset resolver configured; cannot locate the storage of instance %s", instanceID)
	}
	dataset, err := bm.resolve(ctx, instanceID)
	if err != nil {
		return "", err
	}
	if dataset == "" {
		return "", fmt.Errorf("instance %s has no dataset to back up", instanceID)
	}
	return dataset, nil
}

// CreateSnapshot creates a local ZFS snapshot
func (bm *BackupManager) CreateSnapshot(ctx context.Context, instanceID string) (*BackupInfo, error) {
	timestamp := time.Now().Format("20060102-150405")
	snapshotName := fmt.Sprintf("hospitus-backup-%s", timestamp)

	dataset, err := bm.datasetFor(ctx, instanceID)
	if err != nil {
		return nil, err
	}

	info := &BackupInfo{
		ID:           fmt.Sprintf("%s-%s", instanceID, timestamp),
		InstanceID:   instanceID,
		Type:         BackupTypeSnapshot,
		Status:       BackupStatusRunning,
		CreatedAt:    time.Now(),
		SnapshotName: snapshotName,
		Dataset:      dataset,
	}

	// Execute pre-backup hook
	config := bm.GetConfig(instanceID)
	if config != nil && config.PreBackupHook != "" {
		if err := bm.runHook(ctx, config.PreBackupHook, instanceID); err != nil {
			info.Status = BackupStatusFailed
			info.Error = fmt.Sprintf("pre-backup hook failed: %v", err)
			bm.addBackup(instanceID, info)
			return info, err
		}
	}

	// Create ZFS snapshot
	cmd := exec.CommandContext(ctx, "zfs", "snapshot", "-r", fmt.Sprintf("%s@%s", dataset, snapshotName))
	if output, err := cmd.CombinedOutput(); err != nil {
		info.Status = BackupStatusFailed
		info.Error = fmt.Sprintf("zfs snapshot failed: %v (%s)", err, strings.TrimSpace(string(output)))
		bm.addBackup(instanceID, info)
		return info, fmt.Errorf("failed to create snapshot: %w", err)
	}

	size, _ := bm.getSnapshotSize(ctx, dataset, snapshotName)
	info.Size = size

	// Execute post-backup hook
	if config != nil && config.PostBackupHook != "" {
		if err := bm.runHook(ctx, config.PostBackupHook, instanceID); err != nil {
			bm.logger.Warn("Post-backup hook failed", logging.FieldAction, "snapshot", logging.FieldInstance, instanceID, logging.FieldError, err)
		}
	}

	info.Status = BackupStatusCompleted
	info.CompletedAt = time.Now()
	bm.addBackup(instanceID, info)

	// Apply retention policy
	if config != nil {
		bm.applyRetention(ctx, instanceID, config.Retention)
	}

	return info, nil
}

// CreateBackup creates a full or incremental backup
func (bm *BackupManager) CreateBackup(ctx context.Context, instanceID string, backupType BackupType) (*BackupInfo, error) {
	// The id becomes part of the backup file name through filepath.Join, so a
	// "/" or a ".." in it would place the stream outside the destination the
	// operator configured.
	if err := validation.ValidateInstanceName(instanceID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", instanceID, err)
	}

	config := bm.GetConfig(instanceID)
	if config == nil {
		return nil, fmt.Errorf("no backup configuration for instance %s", instanceID)
	}

	timestamp := time.Now().Format("20060102-150405")
	snapshotName := fmt.Sprintf("hospitus-backup-%s", timestamp)
	dataset, err := bm.datasetFor(ctx, instanceID)
	if err != nil {
		return nil, err
	}

	info := &BackupInfo{
		ID:           fmt.Sprintf("%s-%s", instanceID, timestamp),
		InstanceID:   instanceID,
		Type:         backupType,
		Status:       BackupStatusRunning,
		CreatedAt:    time.Now(),
		SnapshotName: snapshotName,
		Dataset:      dataset,
		Destination:  config.Destination,
		// Set from what the transfer actually did, once it has run. Encryption
		// is not implemented: the API refuses a key file rather than record a
		// plaintext transfer as encrypted.
		Compressed: false,
		Encrypted:  false,
	}

	// Execute pre-backup hook
	if config.PreBackupHook != "" {
		if err := bm.runHook(ctx, config.PreBackupHook, instanceID); err != nil {
			info.Status = BackupStatusFailed
			info.Error = fmt.Sprintf("pre-backup hook failed: %v", err)
			bm.addBackup(instanceID, info)
			return info, err
		}
	}

	// Create a new snapshot for the backup
	cmd := exec.CommandContext(ctx, "zfs", "snapshot", "-r", fmt.Sprintf("%s@%s", dataset, snapshotName))
	if output, err := cmd.CombinedOutput(); err != nil {
		info.Status = BackupStatusFailed
		info.Error = fmt.Sprintf("zfs snapshot failed: %v (%s)", err, strings.TrimSpace(string(output)))
		bm.addBackup(instanceID, info)
		return info, fmt.Errorf("failed to create snapshot: %w", err)
	}

	// Build the backup command
	fullSnapshot := fmt.Sprintf("%s@%s", dataset, snapshotName)
	sendArgs := []string{"send", "-R", fullSnapshot}

	if backupType == BackupTypeIncr {
		// Find the previous snapshot for incremental
		prevSnapshot := bm.findPreviousSnapshot(instanceID)
		if prevSnapshot == "" {
			// Fall back to full backup
			info.Type = BackupTypeFull
		} else {
			sendArgs = []string{"send", "-R", "-i", prevSnapshot, fullSnapshot}
			// Record what the stream was sent against: an incremental restores
			// nothing without its base, and deletion/retention must know the
			// base is still needed.
			info.BaseSnapshot = prevSnapshot
		}
	}
	sendCmd := exec.CommandContext(ctx, "zfs", sendArgs...)

	// Execute the backup pipeline
	location, output, err := bm.runPipeline(ctx, sendCmd, config, info.ID)
	if err != nil {
		info.Status = BackupStatusFailed
		info.Error = fmt.Sprintf("backup failed: %v (%s)", err, strings.TrimSpace(string(output)))
		// The snapshot exists only to be streamed. Leaving it means a schedule
		// pointing at an unreachable destination adds one recursive snapshot per
		// run, for ever. streamToFile removes its partial file for the same
		// reason; the record is kept so the failure is still reported.
		bm.destroySnapshot(ctx, dataset, snapshotName)
		bm.addBackup(instanceID, info)
		return info, fmt.Errorf("backup failed: %w", err)
	}
	info.Location = location
	info.Compressed = decompressorFor(location) != nil
	info.Size = bm.measureBackup(ctx, sendArgs, location, dataset, snapshotName)

	// Execute post-backup hook
	if config.PostBackupHook != "" {
		if err := bm.runHook(ctx, config.PostBackupHook, instanceID); err != nil {
			bm.logger.Warn("Post-backup hook failed", logging.FieldAction, "backup", logging.FieldInstance, instanceID, logging.FieldError, err)
		}
	}

	info.Status = BackupStatusCompleted
	info.CompletedAt = time.Now()
	bm.addBackup(instanceID, info)

	// Apply retention policy
	bm.applyRetention(ctx, instanceID, config.Retention)

	return info, nil
}

// findBackup returns the backup with the given ID, searching all instances
// under the read lock. Shared by Restore and VerifyBackup.
func (bm *BackupManager) findBackup(backupID string) *BackupInfo {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	for _, backups := range bm.backups {
		for _, b := range backups {
			if b.ID == backupID {
				return b
			}
		}
	}
	return nil
}

// Restore restores from a backup
func (bm *BackupManager) Restore(ctx context.Context, backupID, targetInstanceID string) error {
	backup := bm.findBackup(backupID)
	if backup == nil {
		return fmt.Errorf("backup not found: %s", backupID)
	}

	dataset := backup.Dataset
	if dataset == "" {
		return fmt.Errorf("backup %s records no dataset; it predates dataset tracking and cannot be restored", backupID)
	}
	targetDataset, err := bm.datasetFor(ctx, targetInstanceID)
	if err != nil {
		return err
	}

	switch backup.Type {
	case BackupTypeSnapshot:
		// For snapshot, use zfs clone or rollback
		if backup.InstanceID == targetInstanceID {
			cmd := exec.CommandContext(ctx, "zfs", "rollback", "-r", fmt.Sprintf("%s@%s", dataset, backup.SnapshotName))
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("rollback failed: %w (%s)", err, strings.TrimSpace(string(output)))
			}
		} else {
			// Clone to new dataset
			cmd := exec.CommandContext(ctx, "zfs", "clone", fmt.Sprintf("%s@%s", dataset, backup.SnapshotName), targetDataset)
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("clone failed: %w (%s)", err, strings.TrimSpace(string(output)))
			}
		}

	case BackupTypeFull, BackupTypeIncr:
		// For full/incremental backup, use zfs receive. The record holds
		// everything a restore needs; requiring a live configuration made good
		// stored backups unrestorable once the config (or instance) was gone.
		source := backup.Location
		if source == "" {
			return fmt.Errorf("backup %s records no location; nothing was written for it to restore from", backupID)
		}

		// An incremental stream only applies on top of the snapshot it was sent
		// against. Without the base present on the target, zfs receive fails
		// with a low-level message that says nothing about which backup is
		// missing.
		if backup.Type == BackupTypeIncr {
			if backup.BaseSnapshot == "" {
				return fmt.Errorf("backup %s is incremental but records no base snapshot; it cannot be restored", backupID)
			}
			base := backup.BaseSnapshot
			if !strings.Contains(base, "@") {
				return fmt.Errorf("backup %s records an unusable base %q", backupID, base)
			}
			baseOnTarget := targetDataset + "@" + base[strings.Index(base, "@")+1:]
			if err := exec.CommandContext(ctx, "zfs", "list", "-H", baseOnTarget).Run(); err != nil {
				return fmt.Errorf("backup %s is incremental on %s, which %s does not have; restore its base first",
					backupID, base, targetDataset)
			}
		}

		recvCmd := exec.CommandContext(ctx, "zfs", "receive", "-F", targetDataset)

		if output, err := bm.runRestorePipeline(ctx, recvCmd, source, backup.SnapshotName); err != nil {
			return fmt.Errorf("restore failed: %w (%s)", err, strings.TrimSpace(string(output)))
		}

	default:
		// Records are rebuilt by json.Unmarshal on reload and inserted by Seed,
		// so an empty or unknown Type reaches here. Falling through returned nil
		// and the caller read a restore that never happened as a success.
		return fmt.Errorf("backup %s has an unknown type %q; nothing was restored", backupID, backup.Type)
	}

	return nil
}

// verifyStream checks that a stored full or incremental backup is intact.
//
// The stream cannot be replayed against the live dataset to check it: "zfs
// receive -n" refuses a destination that exists or that holds snapshots, and
// refuses a missing one as incremental. What can be checked is the file itself,
// which is where truncation and corruption show: a compressed stream through
// its compressor's integrity test, an uncompressed one through zstream dump.
//
// A stream received on another host is left unverifiable rather than passed
// silently.
func (bm *BackupManager) verifyStream(ctx context.Context, backup *BackupInfo) error {
	if backup.Location == "" {
		return fmt.Errorf("backup %s records no location and cannot be verified", backup.ID)
	}
	if strings.Contains(backup.Location, ":") {
		return fmt.Errorf("backup %s was sent to %s; verify it there", backup.ID, backup.Location)
	}

	info, err := os.Stat(backup.Location)
	if err != nil {
		return fmt.Errorf("backup %s: %w", backup.ID, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("backup %s is empty", backup.ID)
	}

	test := integrityTestFor(backup.Location)
	if test == nil {
		return bm.verifyRawStream(ctx, backup)
	}

	args := append(append([]string{}, test[1:]...), backup.Location)
	if output, err := exec.CommandContext(ctx, test[0], args...).CombinedOutput(); err != nil {
		return fmt.Errorf("backup %s is damaged: %w (%s)", backup.ID, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// verifyRawStream checks an uncompressed send stream with `zstream dump`,
// which walks the records and checksums the stream itself carries. zstream
// ships in the FreeBSD base system; where it is missing the check degrades to
// the existence and size already established, and says so.
func (bm *BackupManager) verifyRawStream(ctx context.Context, backup *BackupInfo) error {
	if _, err := exec.LookPath("zstream"); err != nil {
		if bm.logger != nil {
			bm.logger.Warn("zstream is not installed; backup checked by existence and size only",
				"backup", backup.ID)
		}
		return nil
	}

	cmd := exec.CommandContext(ctx, "zstream", "dump", backup.Location)
	// The dump itself is not wanted, only the verdict; the diagnosis goes to
	// stderr.
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("backup %s is damaged: %w (%s)", backup.ID, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// VerifyBackup verifies a backup's integrity
func (bm *BackupManager) VerifyBackup(ctx context.Context, backupID string) error {
	backup := bm.findBackup(backupID)
	if backup == nil {
		return fmt.Errorf("backup not found: %s", backupID)
	}

	switch backup.Type {
	case BackupTypeSnapshot:
		// Verify snapshot exists
		if backup.Dataset == "" {
			return fmt.Errorf("backup %s records no dataset and cannot be verified", backupID)
		}
		dataset := fmt.Sprintf("%s@%s", backup.Dataset, backup.SnapshotName)
		cmd := exec.CommandContext(ctx, "zfs", "list", "-H", dataset)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("snapshot verification failed: snapshot not found")
		}
	default:
		if err := bm.verifyStream(ctx, backup); err != nil {
			return err
		}
		if backup.Type == BackupTypeIncr {
			if err := bm.verifyBase(ctx, backup); err != nil {
				return err
			}
		}
	}

	// Mark as verified and write the mark back: set in memory only, it
	// silently reverted to false at the next restart.
	bm.mu.Lock()
	backup.Verified = true
	verified := *backup
	bm.mu.Unlock()
	bm.persistBackup(&verified)

	return nil
}

// verifyBase checks that the snapshot an incremental stream was sent against
// still exists: without it the stream cannot be received anywhere. A record
// from before base tracking carries no base to check, which is warned about
// rather than failed.
func (bm *BackupManager) verifyBase(ctx context.Context, backup *BackupInfo) error {
	if backup.BaseSnapshot == "" {
		if bm.logger != nil {
			bm.logger.Warn("Backup records no base snapshot; its chain cannot be verified",
				"backup", backup.ID)
		}
		return nil
	}
	cmd := exec.CommandContext(ctx, "zfs", "list", "-H", backup.BaseSnapshot)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("incremental backup %s was sent against %s, which no longer exists",
			backup.ID, backup.BaseSnapshot)
	}
	return nil
}

// ListBackups lists all backups for an instance
func (bm *BackupManager) ListBackups(instanceID string) []*BackupInfo {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	// Copies, not the stored pointers: bm.mu guards the maps, not the records.
	// VerifyBackup writes Verified through the same pointer a caller may still
	// be reading, with no lock in common.
	backups := bm.backups[instanceID]
	result := make([]*BackupInfo, 0, len(backups))
	for _, b := range backups {
		cp := *b
		result = append(result, &cp)
	}
	return result
}

// Seed records a backup without performing one. It exists for tests that need a
// manager holding known records.
func (bm *BackupManager) Seed(instanceID string, info *BackupInfo) {
	bm.addBackup(instanceID, info)
}

// ListAllBackups returns every backup the manager knows of, across instances.
func (bm *BackupManager) ListAllBackups() []*BackupInfo {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	var all []*BackupInfo
	for _, backups := range bm.backups {
		all = append(all, backups...)
	}
	return all
}

// GetBackup returns the backup with the given ID, or nil.
func (bm *BackupManager) GetBackup(backupID string) *BackupInfo {
	// A copy, for the same reason as ListBackups: the caller reads it without
	// the manager lock while VerifyBackup may be writing the stored record.
	b := bm.findBackup(backupID)
	if b == nil {
		return nil
	}
	cp := *b
	return &cp
}

// DeleteBackup deletes a backup. A backup a newer incremental was sent against
// is refused: deleting the base leaves that incremental unrestorable.
//
// The zfs destroy and file removal run outside the manager lock — they can
// take a while, and holding the write lock across them stalls every other
// backup operation.
func (bm *BackupManager) DeleteBackup(ctx context.Context, backupID string) error {
	bm.mu.Lock()
	var target *BackupInfo
	var instanceID string
search:
	for id, backups := range bm.backups {
		for i, b := range backups {
			if b.ID != backupID {
				continue
			}
			if ref := bm.baseReferenceLocked(b); ref != nil {
				bm.mu.Unlock()
				return fmt.Errorf("backup %s is the base of incremental backup %s; delete that one first",
					backupID, ref.ID)
			}
			target = b
			instanceID = id
			// Claim the record before unlocking, so a concurrent delete of the
			// same backup does not destroy the same snapshot twice.
			bm.backups[id] = append(backups[:i], backups[i+1:]...)
			break search
		}
	}
	bm.mu.Unlock()

	if target == nil {
		return fmt.Errorf("backup not found: %s", backupID)
	}

	if err := bm.removeBackupData(ctx, target); err != nil {
		// What the backup left behind is still there; put the record back.
		bm.mu.Lock()
		bm.backups[instanceID] = append(bm.backups[instanceID], target)
		bm.mu.Unlock()
		return err
	}

	if bm.ds != nil {
		if err := bm.ds.DeleteBackupRecord(ctx, backupID); err != nil {
			return fmt.Errorf("backup deleted but its record remains: %w", err)
		}
	}
	return nil
}

// removeBackupData destroys what a backup left on the host: its snapshot, and
// the stream file of a local full or incremental.
func (bm *BackupManager) removeBackupData(ctx context.Context, b *BackupInfo) error {
	// Every type leaves a snapshot behind: a full or incremental backup sends
	// from one, and destroying it only for the snapshot type left those on the
	// pool for ever, where the next incremental would also chain onto them.
	if b.Dataset != "" && b.SnapshotName != "" {
		if err := bm.destroySnapshotErr(ctx, b.Dataset, b.SnapshotName); err != nil {
			return err
		}
	}

	// A local backup is a file. A remote one belongs to the host that holds
	// it, and is not reached from here.
	if b.Location != "" && !strings.Contains(b.Location, ":") {
		if err := os.Remove(b.Location); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to delete backup stream %s: %w", b.Location, err)
		}
	}
	return nil
}

// destroySnapshotErr removes a backup's snapshot. A snapshot that is already
// gone is the state the caller wanted, not a failure: reporting one leaves the
// record undeletable, exactly as os.IsNotExist is tolerated for the stream file
// just below.
func (bm *BackupManager) destroySnapshotErr(ctx context.Context, dataset, snapshot string) error {
	name := fmt.Sprintf("%s@%s", dataset, snapshot)
	out, err := exec.CommandContext(ctx, "zfs", "destroy", "-r", name).CombinedOutput()
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(string(out)), "does not exist") {
		return nil
	}
	return fmt.Errorf("failed to delete snapshot %s: %w (%s)", name, err, strings.TrimSpace(string(out)))
}

// destroySnapshot removes a snapshot on an error path, where nothing can be
// done about a second failure but log it.
func (bm *BackupManager) destroySnapshot(ctx context.Context, dataset, snapshot string) {
	if err := bm.destroySnapshotErr(context.WithoutCancel(ctx), dataset, snapshot); err != nil {
		bm.logger.Warn("Could not remove the snapshot of a failed backup",
			logging.FieldAction, "snapshot-cleanup", "snapshot", dataset+"@"+snapshot, "error", err)
	}
}

// baseReferenceLocked returns a backup whose incremental stream was sent
// against b's snapshot, or nil. The caller holds bm.mu.
func (bm *BackupManager) baseReferenceLocked(b *BackupInfo) *BackupInfo {
	if b.Dataset == "" || b.SnapshotName == "" {
		return nil
	}
	base := fmt.Sprintf("%s@%s", b.Dataset, b.SnapshotName)
	for _, backups := range bm.backups {
		for _, other := range backups {
			if other.ID != b.ID && other.BaseSnapshot == base {
				return other
			}
		}
	}
	return nil
}

// isIncrementalBase reports whether another backup chains onto b.
func (bm *BackupManager) isIncrementalBase(b *BackupInfo) bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.baseReferenceLocked(b) != nil
}

// runSchedule runs scheduled backups for an instance
func (bm *BackupManager) runSchedule(instanceID, schedule string, stop chan struct{}) {
	defer bm.wg.Done()

	interval := parseSchedule(schedule)
	if interval == 0 {
		bm.logger.Error("Invalid backup schedule", logging.FieldAction, "schedule", logging.FieldInstance, instanceID, "schedule", schedule)
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Derive from rootCtx so Stop() aborts an in-flight backup.
			ctx, cancel := context.WithTimeout(bm.rootCtx, 30*time.Minute)
			if _, err := bm.CreateSnapshot(ctx, instanceID); err != nil {
				bm.logger.Error("Scheduled backup failed", logging.FieldAction, "scheduled_backup", logging.FieldInstance, instanceID, logging.FieldError, err)
			}
			cancel()
		case <-stop:
			return
		}
	}
}

// parseSchedule parses a simple schedule string and returns interval
// For simplicity, supports: "hourly", "daily", "weekly", "monthly"
// or intervals like "1h", "6h", "12h", "24h"
func parseSchedule(schedule string) time.Duration {
	switch strings.ToLower(schedule) {
	case "hourly":
		return time.Hour
	case "daily":
		return 24 * time.Hour
	case "weekly":
		return 7 * 24 * time.Hour
	case "monthly":
		return 30 * 24 * time.Hour
	default:
		d, err := time.ParseDuration(schedule)
		if err != nil {
			return 0
		}
		// ParseDuration accepts a sign, so "-1h" parses. Callers only guard
		// against 0, and time.NewTicker panics on anything non-positive — from
		// a goroutine, which takes the daemon with it.
		if d <= 0 {
			return 0
		}
		return d
	}
}

// addBackup adds a backup to the list
func (bm *BackupManager) addBackup(instanceID string, info *BackupInfo) {
	bm.mu.Lock()
	bm.backups[instanceID] = append(bm.backups[instanceID], info)
	bm.mu.Unlock()

	bm.persistBackup(info)
}

// persistBackup writes a backup's record to the datastore, replacing any
// earlier version. Changes made after creation (Verified) must be written
// again or they last only until the next restart.
func (bm *BackupManager) persistBackup(info *BackupInfo) {
	if bm.ds == nil {
		return
	}
	record, err := json.Marshal(info)
	if err != nil {
		bm.logger.Warn("Cannot record backup", logging.FieldInstance, info.InstanceID, logging.FieldError, err)
		return
	}
	if err := bm.ds.SaveBackupRecord(context.Background(), info.ID, info.InstanceID, record); err != nil {
		bm.logger.Warn("Cannot record backup", logging.FieldInstance, info.InstanceID, logging.FieldError, err)
	}
}

// applyRetention applies retention policy to backups
func (bm *BackupManager) applyRetention(ctx context.Context, instanceID string, policy RetentionPolicy) {
	// No retention rule expressed means "keep everything". Without this guard a
	// zero-value policy (the default when a user enables backups without a
	// retention block) would delete every backup, including the one just made.
	if policy.KeepLast == 0 && policy.KeepHourly == 0 && policy.KeepDaily == 0 &&
		policy.KeepWeekly == 0 && policy.KeepMonthly == 0 {
		return
	}

	// Take a deep copy of the slice under lock: sort.Slice and the deletion loop
	// below must not mutate/read the shared backing array while addBackup,
	// DeleteBackup or ListBackups run concurrently from the scheduler goroutine.
	bm.mu.RLock()
	backups := append([]*BackupInfo(nil), bm.backups[instanceID]...)
	bm.mu.RUnlock()

	if len(backups) == 0 {
		return
	}

	// Sort by creation time (newest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt.After(backups[j].CreatedAt)
	})

	// Determine which backups to keep
	keep := make(map[string]bool)
	now := time.Now()

	for i := 0; i < policy.KeepLast && i < len(backups); i++ {
		keep[backups[i].ID] = true
	}

	// Keep hourly, daily, weekly, monthly
	bm.keepByInterval(backups, keep, policy.KeepHourly, time.Hour)
	bm.keepByInterval(backups, keep, policy.KeepDaily, 24*time.Hour)
	bm.keepByInterval(backups, keep, policy.KeepWeekly, 7*24*time.Hour)
	bm.keepByInterval(backups, keep, policy.KeepMonthly, 30*24*time.Hour)

	// Delete backups not in keep list
	for _, b := range backups {
		if !keep[b.ID] {
			// Check minimum age
			if policy.MinAge > 0 && now.Sub(b.CreatedAt) < policy.MinAge {
				continue
			}
			// A backup a newer incremental was sent against must outlive the
			// policy: deleting the base leaves that incremental unrestorable.
			if bm.isIncrementalBase(b) {
				continue
			}
			if err := bm.DeleteBackup(ctx, b.ID); err != nil {
				bm.logger.Warn("Failed to delete old backup", logging.FieldAction, "delete", "backup_id", b.ID, logging.FieldError, err)
			}
		}
	}
}

// keepByInterval keeps N backups per interval
func (bm *BackupManager) keepByInterval(backups []*BackupInfo, keep map[string]bool, count int, interval time.Duration) {
	if count == 0 {
		return
	}

	buckets := make(map[int64]*BackupInfo)
	bucketKeys := make([]int64, 0)
	for _, b := range backups {
		bucket := b.CreatedAt.Unix() / int64(interval.Seconds())
		if _, ok := buckets[bucket]; !ok {
			buckets[bucket] = b
			bucketKeys = append(bucketKeys, bucket)
		}
	}

	// Keep the newest `count` buckets. Iterating the map directly would pick
	// arbitrary buckets because Go randomizes map iteration order; sort the
	// bucket keys descending (newest first) to make selection deterministic.
	sort.Slice(bucketKeys, func(i, j int) bool { return bucketKeys[i] > bucketKeys[j] })
	for i, bucket := range bucketKeys {
		if i >= count {
			break
		}
		keep[buckets[bucket].ID] = true
	}
}

// measureBackup reports the size of the stream a backup just sent. A local
// backup is a file to stat. A remote stream leaves nothing here to measure, so
// a dry-run of the same send reports what went over; the snapshot figure
// stands in when that fails.
func (bm *BackupManager) measureBackup(ctx context.Context, sendArgs []string, location, dataset, snapshotName string) int64 {
	if location != "" && strings.Contains(location, ":") {
		if size, err := estimateSendSize(ctx, sendArgs); err == nil {
			return size
		}
	}
	snapshotSize, _ := bm.getSnapshotSize(ctx, dataset, snapshotName)
	return backupSize(location, snapshotSize)
}

// estimateSendSize asks zfs how large a send stream is without sending it:
// "zfs send -nvP" prints a parsable "size <bytes>" line to stdout.
func estimateSendSize(ctx context.Context, sendArgs []string) (int64, error) {
	args := append([]string{"send", "-nvP"}, sendArgs[1:]...)
	output, err := exec.CommandContext(ctx, "zfs", args...).Output()
	if err != nil {
		return 0, err
	}
	return parseSendSize(string(output))
}

// parseSendSize extracts the byte count from zfs send -nvP output.
func parseSendSize(output string) (int64, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "size" {
			return strconv.ParseInt(fields[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("zfs send -nvP reported no size")
}

// backupSize reports how large a backup actually is.
//
// A snapshot's referenced size is an estimate; a full backup writes a file,
// and that file is what an operator wants a size for. A stream received on
// another host cannot be measured from here, so the snapshot figure stands in.
func backupSize(location string, snapshotSize int64) int64 {
	if location == "" {
		return snapshotSize
	}
	if info, err := os.Stat(location); err == nil && !info.IsDir() {
		return info.Size()
	}
	return snapshotSize
}

// getSnapshotSize returns the referenced size of a ZFS snapshot — the data it
// names. Its "used" is only what it consumes on top of the live dataset, which
// for a fresh snapshot is nothing: it still shares every block.
func (bm *BackupManager) getSnapshotSize(ctx context.Context, dataset, snapshotName string) (int64, error) {
	cmd := exec.CommandContext(ctx, "zfs", "list", "-Hp", "-o", "referenced", fmt.Sprintf("%s@%s", dataset, snapshotName))
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var size int64
	// A parse failure leaves size at 0, which is an acceptable fallback here.
	_, _ = fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &size)
	return size, nil
}

// allowedHookDirs returns the directories a backup hook script may live in.
// Hooks running as the daemon (root) must be confined so a request body
// cannot point runHook at an arbitrary host executable.
func (bm *BackupManager) allowedHookDirs() []string {
	dirs := []string{"/usr/local/etc/hospitus/hooks"}
	if bm.dataDir != "" {
		dirs = append(dirs, filepath.Join(bm.dataDir, "hooks"))
	}
	return dirs
}

// validateHookPath confines a hook to an allowed hooks directory, rejecting
// shell commands, flags, traversal and symlinks pointing outside. Mirrors the
// jail provider's validateHookPath.
func (bm *BackupManager) validateHookPath(hook string) (string, error) {
	if strings.ContainsAny(hook, "|&;`$(){}<>!\\\"'") {
		return "", fmt.Errorf("hook contains shell metacharacters; only script paths are allowed")
	}
	if strings.HasPrefix(hook, "-") {
		return "", fmt.Errorf("hook must be an absolute path, not a flag")
	}
	if !filepath.IsAbs(hook) {
		return "", fmt.Errorf("hook must be an absolute path, got: %s", hook)
	}
	cleaned := filepath.Clean(hook)

	// Resolve the allowed directories too, so a resolved candidate is compared
	// against resolved roots. On macOS /var is itself a link to /private/var,
	// and comparing one against the other rejects every legitimate hook.
	dirs := bm.allowedHookDirs()
	roots := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			roots = append(roots, r)
			continue
		}
		roots = append(roots, dir)
	}
	within := func(p string) bool {
		for _, dir := range roots {
			if p == dir || strings.HasPrefix(p, dir+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	if _, err := os.Lstat(cleaned); err != nil {
		return "", fmt.Errorf("hook script not found: %s", cleaned)
	}

	// Resolve every component, not just the last one. A textual prefix test on
	// the unresolved path lets a symlinked *parent* — the hooks directory
	// itself, say — point anywhere while the final component is an ordinary
	// file, so the symlink branch never runs and confinement passes on a script
	// living outside every allowed directory.
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("hook path cannot be resolved: %s", cleaned)
	}
	if !within(resolved) {
		if resolved != cleaned {
			return "", fmt.Errorf("hook resolves outside allowed directories: %s → %s", cleaned, resolved)
		}
		return "", fmt.Errorf("hook script must be under an allowed hooks directory (got: %s)", resolved)
	}
	finfo, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("hook script not found: %s", resolved)
	}
	if !finfo.Mode().IsRegular() {
		return "", fmt.Errorf("hook must be a regular file, not a directory or special file: %s", cleaned)
	}
	if err := checkHookOwnership(resolved, finfo); err != nil {
		return "", err
	}
	return resolved, nil
}

// checkHookOwnership enforces that a hook script is safe to run as the daemon:
// it must be executable, not writable by group or others, and owned by root or
// by the user the daemon runs as, so an unprivileged user cannot tamper with
// it and gain code execution as the daemon.
func checkHookOwnership(path string, info os.FileInfo) error {
	perm := info.Mode().Perm()
	if perm&0o111 == 0 {
		return fmt.Errorf("hook script %s is not executable", path)
	}
	if perm&0o022 != 0 {
		return fmt.Errorf("hook script %s must not be group- or world-writable", path)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if st.Uid != 0 && int(st.Uid) != os.Geteuid() {
			return fmt.Errorf("hook script %s must be owned by root or the daemon user", path)
		}
	}
	return nil
}

// runHook executes a backup hook script.
// The hook must be an absolute path to a script under an allowed hooks
// directory — raw shell commands and arbitrary host executables are rejected
// to prevent command injection and privilege abuse (CWE-78).
func (bm *BackupManager) runHook(ctx context.Context, hook, instanceID string) error {
	cleaned, err := bm.validateHookPath(hook)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, cleaned)
	// Minimal, controlled environment — do not leak the daemon's process
	// environment (API keys, tokens) into hook scripts.
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		fmt.Sprintf("HOSPITUS_INSTANCE_ID=%s", instanceID),
		fmt.Sprintf("HOSPITUS_BACKUP_TIME=%s", time.Now().Format(time.RFC3339)),
	}
	return cmd.Run()
}

// findPreviousSnapshot finds the most recent snapshot for incremental backup
func (bm *BackupManager) findPreviousSnapshot(instanceID string) string {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	backups := bm.backups[instanceID]
	if len(backups) == 0 {
		return ""
	}

	// Find the most recent completed full or incremental backup — one with a
	// stored stream. A snapshot-type record leaves no stream anywhere, so an
	// incremental chained onto it would be unrestorable from the stored files
	// alone.
	// By CreatedAt, not by position: DeleteBackup re-appends a record when
	// removing its data fails, so the last element is not always the newest,
	// and an incremental would chain onto an older base than it should.
	var newest *BackupInfo
	for _, b := range backups {
		if b.Status != BackupStatusCompleted || b.Dataset == "" || b.Location == "" {
			continue
		}
		if b.Type != BackupTypeFull && b.Type != BackupTypeIncr {
			continue
		}
		// Not strictly After: on equal timestamps the later record wins, which
		// keeps the previous "last eligible" behavior for records that carry no
		// distinct creation time.
		if newest == nil || !b.CreatedAt.Before(newest.CreatedAt) {
			newest = b
		}
	}
	if newest != nil {
		return fmt.Sprintf("%s@%s", newest.Dataset, newest.SnapshotName)
	}

	return ""
}

// runPipeline runs the backup pipeline, piping zfs send to the destination.
// For remote destinations, pipes through SSH using exec argument arrays
// instead of shell strings to prevent command injection (CWE-78).
func (bm *BackupManager) runPipeline(ctx context.Context, sendCmd *exec.Cmd, config *BackupConfig, backupID string) (location string, output []byte, err error) {
	// A stream has to go somewhere. Running the send and reading its output into
	// memory writes nothing, buffers the whole dataset, and leaves a backup that
	// reports success and restores nothing.
	if config.Destination == "" {
		return "", nil, fmt.Errorf("a full or incremental backup needs a destination: a local directory, or host:path for a remote pool")
	}

	if !strings.Contains(config.Destination, ":") {
		file, err := bm.streamToFile(ctx, sendCmd, config.Destination, backupID, config.Compression)
		if err != nil {
			return "", nil, err
		}
		return file, nil, nil
	}

	sshHost, remotePath, err := validation.ValidateSSHDestination(config.Destination)
	if err != nil {
		return "", nil, fmt.Errorf("invalid backup destination: %w", err)
	}

	sshCmd := exec.CommandContext(ctx, "ssh", "--", sshHost, "zfs", "receive", "-F", remotePath)

	pipe, err := sendCmd.StdoutPipe()
	if err != nil {
		return "", nil, fmt.Errorf("failed to create pipe: %w", err)
	}
	sshCmd.Stdin = pipe

	var sshOut bytes.Buffer
	sshCmd.Stdout = &sshOut
	sshCmd.Stderr = &sshOut

	if err := sshCmd.Start(); err != nil {
		return "", nil, fmt.Errorf("failed to start ssh: %w", err)
	}
	if err := sendCmd.Start(); err != nil {
		killStarted(sshCmd)
		return "", sshOut.Bytes(), fmt.Errorf("failed to start zfs send: %w", err)
	}

	// Wait on both processes concurrently. The parent holds the read end of the
	// pipe open, so if ssh dies first zfs send would block writing forever;
	// killing the peer on failure guarantees both Waits return.
	sendErr, sshErr := waitPipeline(sendCmd, sshCmd)

	if sendErr != nil {
		return "", sshOut.Bytes(), fmt.Errorf("zfs send failed: %w", sendErr)
	}
	if sshErr != nil {
		return "", sshOut.Bytes(), fmt.Errorf("ssh zfs receive failed: %w", sshErr)
	}

	return config.Destination, sshOut.Bytes(), nil
}

// streamCompressors maps a configured algorithm to the commands that compress a
// send stream on the way out and expand it on the way back.
//
// gzip and zstd are in the FreeBSD base system; lz4 comes from a port, and its
// absence is reported by name rather than as a failed backup.
var streamCompressors = map[string]struct {
	extension  string
	compress   []string
	decompress []string
	// test reads the whole file back and reports whether it is intact, without
	// expanding it anywhere. It is what "backup verify" runs.
	test []string
}{
	"gzip": {".gz", []string{"gzip", "-c"}, []string{"gzip", "-dc"}, []string{"gzip", "-t"}},
	"zstd": {".zst", []string{"zstd", "-q", "-c"}, []string{"zstd", "-qdc"}, []string{"zstd", "-t"}},
	"lz4":  {".lz4", []string{"lz4", "-q", "-c"}, []string{"lz4", "-qdc"}, []string{"lz4", "-t"}},
}

// integrityTestFor returns the command that checks a stored stream, chosen by
// the extension the backup was written with.
func integrityTestFor(location string) []string {
	for _, c := range streamCompressors {
		if strings.HasSuffix(location, c.extension) {
			return c.test
		}
	}
	return nil
}

// ValidateCompression reports whether an algorithm can be applied to a backup
// stream bound for the given destination.
//
// Compression is applied where hospitus holds both ends: a local file it writes and
// reads back. Over ssh the far side runs "zfs receive" with an argument array
// and no shell, so there is nothing there to expand a compressed stream; asking
// for one is refused rather than silently ignored.
func ValidateCompression(algorithm, destination string) error {
	switch algorithm {
	case "", "none":
		return nil
	}
	if _, ok := streamCompressors[algorithm]; !ok {
		return fmt.Errorf("unknown compression %q: use gzip, zstd, lz4, or none", algorithm)
	}
	if strings.Contains(destination, ":") {
		return fmt.Errorf("compression %q cannot be applied to the remote destination %q: "+
			"set compression on the receiving pool instead", algorithm, destination)
	}
	return nil
}

// compressorFor returns the compressor named by a config, or ok=false when the
// stream is written as it comes.
func compressorFor(algorithm string) (extension string, command []string, ok bool) {
	c, found := streamCompressors[algorithm]
	if !found {
		return "", nil, false
	}
	return c.extension, c.compress, true
}

// decompressorFor returns the command that expands a stream, chosen by the file
// extension the backup was written with.
func decompressorFor(location string) []string {
	for _, c := range streamCompressors {
		if strings.HasSuffix(location, c.extension) {
			return c.decompress
		}
	}
	return nil
}

// runThroughCompressor pipes a send stream through a compressor into a file.
func runThroughCompressor(ctx context.Context, sendCmd *exec.Cmd, compressor []string, out *os.File, sendErrOut *bytes.Buffer) error {
	pipe, err := sendCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create pipe: %w", err)
	}

	compressCmd := exec.CommandContext(ctx, compressor[0], compressor[1:]...)
	compressCmd.Stdin = pipe
	compressCmd.Stdout = out
	// Its own buffer: os/exec gives each command a copying goroutine, so two
	// commands sharing one writer race on it.
	var compressErrOut bytes.Buffer
	compressCmd.Stderr = &compressErrOut

	if err := compressCmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", compressor[0], err)
	}
	if err := sendCmd.Start(); err != nil {
		killStarted(compressCmd)
		return fmt.Errorf("failed to start zfs send: %w", err)
	}

	sendErr, compressErr := waitPipeline(sendCmd, compressCmd)
	if sendErr != nil {
		return fmt.Errorf("zfs send failed: %w (%s)", sendErr, strings.TrimSpace(sendErrOut.String()))
	}
	if compressErr != nil {
		return fmt.Errorf("%s failed: %w (%s)", compressor[0], compressErr, strings.TrimSpace(compressErrOut.String()))
	}
	return nil
}

// streamToFile writes a zfs send stream to a file under dir, and returns the
// file's path.
//
// The alternative — reading the stream into memory and dropping it — buffers the
// whole dataset and produces a backup that reports success and restores nothing.
func (bm *BackupManager) streamToFile(ctx context.Context, sendCmd *exec.Cmd, dir, backupID, compression string) (string, error) {
	if err := validation.ValidateFilePath(dir, true); err != nil {
		return "", fmt.Errorf("invalid backup destination %q: %w", dir, err)
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("backup destination %q must be an absolute path or host:path", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create backup destination %q: %w", dir, err)
	}

	extension, compressor, compressed := compressorFor(compression)
	if compressed {
		if _, err := exec.LookPath(compressor[0]); err != nil {
			return "", fmt.Errorf("compression %q needs %s, which is not installed: %w", compression, compressor[0], err)
		}
	}

	path := filepath.Join(dir, backupID+".zfs"+extension)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("cannot create backup file %q: %w", path, err)
	}

	var stderr bytes.Buffer
	sendCmd.Stderr = &stderr

	var runErr error
	if compressed {
		runErr = runThroughCompressor(ctx, sendCmd, compressor, file, &stderr)
	} else {
		sendCmd.Stdout = file
		runErr = sendCmd.Run()
	}
	closeErr := file.Close()

	// A partial stream restores nothing and its presence would let the next
	// incremental chain onto it, so it does not survive a failure.
	if runErr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("zfs send failed: %w (%s)", runErr, strings.TrimSpace(stderr.String()))
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("cannot finish writing %q: %w", path, closeErr)
	}
	return path, nil
}

// killStarted tears down a pipeline command that was already started when its
// peer failed to start: left alone it lives on — a zombie once it exits — until
// the daemon does.
func killStarted(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}

// waitPipeline waits for a producer and consumer command connected by a pipe,
// tearing down the peer if either exits with an error so neither Wait blocks
// indefinitely. It returns the producer and consumer errors respectively.
func waitPipeline(producer, consumer *exec.Cmd) (producerErr, consumerErr error) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if consumerErr = consumer.Wait(); consumerErr != nil && producer.Process != nil {
			_ = producer.Process.Kill()
		}
	}()
	go func() {
		defer wg.Done()
		if producerErr = producer.Wait(); producerErr != nil && consumer.Process != nil {
			_ = consumer.Process.Kill()
		}
	}()
	wg.Wait()
	return producerErr, consumerErr
}

// runThroughDecompressor pipes a compressed backup file through its expander
// into zfs receive.
func runThroughDecompressor(ctx context.Context, decompressor []string, file *os.File, recvCmd *exec.Cmd) ([]byte, error) {
	if _, err := exec.LookPath(decompressor[0]); err != nil {
		return nil, fmt.Errorf("this backup needs %s to be read, which is not installed: %w", decompressor[0], err)
	}

	expandCmd := exec.CommandContext(ctx, decompressor[0], decompressor[1:]...)
	expandCmd.Stdin = file

	pipe, err := expandCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create pipe: %w", err)
	}
	recvCmd.Stdin = pipe

	// One buffer per command: os/exec gives each a copying goroutine, and two
	// commands sharing a writer race on it.
	var expandOut, recvOut bytes.Buffer
	expandCmd.Stderr = &expandOut
	recvCmd.Stdout = &recvOut
	recvCmd.Stderr = &recvOut

	if err := recvCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start zfs receive: %w", err)
	}
	if err := expandCmd.Start(); err != nil {
		killStarted(recvCmd)
		return nil, fmt.Errorf("failed to start %s: %w", decompressor[0], err)
	}

	expandErr, recvErr := waitPipeline(expandCmd, recvCmd)
	if expandErr != nil {
		return recvOut.Bytes(), fmt.Errorf("%s failed: %w (%s)", decompressor[0], expandErr, strings.TrimSpace(expandOut.String()))
	}
	if recvErr != nil {
		return recvOut.Bytes(), fmt.Errorf("zfs receive failed: %w", recvErr)
	}
	return recvOut.Bytes(), nil
}

// runRestorePipeline runs the restore pipeline, piping the source to zfs receive.
// For remote sources, pipes through SSH using exec argument arrays
// instead of shell strings to prevent command injection (CWE-78).
func (bm *BackupManager) runRestorePipeline(ctx context.Context, recvCmd *exec.Cmd, source, snapshot string) ([]byte, error) {
	if !strings.Contains(source, ":") {
		// A local backup is a file holding the send stream. Running receive with
		// no stdin feeds it nothing, and it reports success over an empty
		// restore.
		file, err := os.Open(source)
		if err != nil {
			return nil, fmt.Errorf("cannot read backup stream %q: %w", source, err)
		}
		defer file.Close()

		// The extension says how the stream was written; receive is fed the
		// expanded form either way.
		decompressor := decompressorFor(source)
		if decompressor == nil {
			recvCmd.Stdin = file
			return recvCmd.CombinedOutput()
		}
		return runThroughDecompressor(ctx, decompressor, file, recvCmd)
	}

	sshHost, remotePath, err := validation.ValidateSSHDestination(source)
	if err != nil {
		return nil, fmt.Errorf("invalid restore source: %w", err)
	}

	// ValidateSSHDestination yields only pool/dataset. Sending that bare name
	// streams the dataset's current head, not the snapshot this backup recorded,
	// so the restore would produce some other state — and an incremental has no
	// base to stand on at all.
	if snapshot == "" {
		return nil, fmt.Errorf("backup records no snapshot name; a remote restore cannot select what to send")
	}
	if err := validation.ValidateSnapshotName(snapshot); err != nil {
		return nil, fmt.Errorf("invalid snapshot name %q: %w", snapshot, err)
	}
	sshCmd := exec.CommandContext(ctx, "ssh", "--", sshHost, "zfs", "send", remotePath+"@"+snapshot)

	pipe, err := sshCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create pipe: %w", err)
	}
	recvCmd.Stdin = pipe

	var recvOut bytes.Buffer
	recvCmd.Stdout = &recvOut
	recvCmd.Stderr = &recvOut

	if err := recvCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start zfs receive: %w", err)
	}
	if err := sshCmd.Start(); err != nil {
		killStarted(recvCmd)
		return recvOut.Bytes(), fmt.Errorf("failed to start ssh: %w", err)
	}

	// ssh (producer) feeds zfs receive (consumer) through the pipe; kill the
	// peer on failure so neither Wait can block forever.
	sshErr, recvErr := waitPipeline(sshCmd, recvCmd)

	if sshErr != nil {
		return recvOut.Bytes(), fmt.Errorf("ssh zfs send failed: %w", sshErr)
	}
	if recvErr != nil {
		return recvOut.Bytes(), fmt.Errorf("zfs receive failed: %w", recvErr)
	}

	return recvOut.Bytes(), nil
}
