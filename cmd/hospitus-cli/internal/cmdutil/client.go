package cmdutil

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/job"
	"github.com/hospitus/hospitus/pkg/provider"
)

// APIClientInterface defines the interface for API client operations
// This allows for mocking in tests
type APIClientInterface interface {
	CreateInstance(ctx context.Context, req client.CreateInstanceRequest) (*datastore.Instance, error)
	ListInstances(ctx context.Context, filter client.ListInstancesFilter) ([]*datastore.Instance, error)
	GetInstance(ctx context.Context, id string) (*datastore.Instance, error)
	UpdateInstance(ctx context.Context, id string, req client.UpdateInstanceRequest) (*datastore.Instance, error)
	StartInstance(ctx context.Context, id string, w io.Writer) error
	StopInstance(ctx context.Context, id string, force bool) error
	RestartInstance(ctx context.Context, id string) error
	DeleteInstance(ctx context.Context, id string, force bool) error
	FetchImage(ctx context.Context, version string, w io.Writer) error
	ListImages(ctx context.Context) ([]map[string]interface{}, error)
	DeleteImage(ctx context.Context, imageName string) error
	ExportInstance(ctx context.Context, instanceID string, opts client.ExportOptions) (*client.ExportResult, error)
	ImportInstance(ctx context.Context, opts client.ImportOptions) (*client.ImportResult, error)
	// Firewall/Port forwarding operations
	ExposePort(ctx context.Context, instanceName string, req client.ExposePortRequest) (*client.ExposePortResult, error)
	UnexposePort(ctx context.Context, instanceName string, hostPort int, protocol string) error
	ListExposedPorts(ctx context.Context, instanceName string) ([]client.PortMapping, error)
	// Exec operations
	ExecCommand(ctx context.Context, instanceID string, req client.ExecRequest) (*client.ExecResult, error)
	ExecCommandStream(ctx context.Context, instanceID string, req client.ExecRequest, stdout, stderr io.Writer) (int, error)
	// Snapshot operations
	ListSnapshots(ctx context.Context, instanceID string) ([]client.SnapshotInfo, error)
	CreateSnapshot(ctx context.Context, instanceID, snapshotName string) error
	DeleteSnapshot(ctx context.Context, instanceID, snapshotName string) error
	RestoreSnapshot(ctx context.Context, instanceID, snapshotName string) error
	// Clone operations
	CloneInstance(ctx context.Context, instanceID string, opts client.CloneOptions) (*client.CloneResult, error)
	CloneFromSnapshot(ctx context.Context, instanceID, snapshotName string, opts client.CloneOptions) (*client.CloneResult, error)
	// Metrics operations
	GetInstanceMetrics(ctx context.Context, instanceID string) (*client.InstanceMetrics, error)
	// Health check operations
	GetInstanceHealth(ctx context.Context, instanceID string) (*client.InstanceHealth, error)
	// Media operations (for VM providers)
	InsertMedia(ctx context.Context, instanceID string, spec provider.MediaSpec) error
	EjectMedia(ctx context.Context, instanceID string, deviceID string) error
	ListMedia(ctx context.Context, instanceID string) ([]provider.MediaInfo, error)
	SetBootOrder(ctx context.Context, instanceID string, order provider.BootOrder) error
	GetBootOrder(ctx context.Context, instanceID string) (*provider.BootOrder, error)
	// Service management operations
	ServiceAction(ctx context.Context, instanceID string, req client.ServiceActionRequest) (*client.ServiceActionResult, error)
	GetServiceStatus(ctx context.Context, instanceID, serviceName string) (*client.ServiceStatus, error)
	ListServices(ctx context.Context, instanceID, filter string) ([]client.ServiceInfo, error)
	// Volume operations
	CreateVolume(ctx context.Context, req client.CreateVolumeRequest) (*client.VolumeInfo, error)
	DeleteVolume(ctx context.Context, volumeName string) error
	ListVolumes(ctx context.Context) ([]client.VolumeInfo, error)
	GetVolume(ctx context.Context, volumeName string) (*client.VolumeInfo, error)
	AttachVolume(ctx context.Context, instanceID, volumeName, mountPoint string) error
	DetachVolume(ctx context.Context, instanceID, volumeName string) error
	// Network interface operations
	AddNetworkInterface(ctx context.Context, instanceID string, req client.NetworkInterfaceRequest) (*client.NetworkInterfaceInfo, error)
	RemoveNetworkInterface(ctx context.Context, instanceID, interfaceName string) error
	ListNetworkInterfaces(ctx context.Context, instanceID string) ([]client.NetworkInterfaceInfo, error)
	// Lifecycle management
	RenameInstance(ctx context.Context, id, newName string) (map[string]string, error)
	UpgradeInstance(ctx context.Context, id, targetRelease string) (map[string]string, error)
	PauseInstance(ctx context.Context, id string) error
	ResumeInstance(ctx context.Context, id string) error
	// Checkpoint operations (bhyve)
	ListCheckpoints(ctx context.Context, instanceID string) ([]client.CheckpointInfo, error)
	CreateCheckpoint(ctx context.Context, instanceID, name string) error
	RestoreCheckpoint(ctx context.Context, instanceID, name string) error
	DeleteCheckpoint(ctx context.Context, instanceID, name string) error
	// Auto-start operations
	ListAutoStart(ctx context.Context) ([]client.AutoStartInfo, error)
	GetAutoStart(ctx context.Context, providerName, instanceID string) (*client.AutoStartInfo, error)
	SetAutoStart(ctx context.Context, providerName, instanceID string, cfg provider.AutoStartConfig) (*client.AutoStartInfo, error)
	DisableAutoStart(ctx context.Context, providerName, instanceID string) error
	// Backup operations
	ListBackups(ctx context.Context, instanceID string) ([]client.BackupInfo, error)
	GetBackup(ctx context.Context, backupID string) (*client.BackupInfo, error)
	CreateBackup(ctx context.Context, instanceID, backupType string) (*client.BackupInfo, error)
	DeleteBackup(ctx context.Context, backupID string) error
	RestoreBackup(ctx context.Context, backupID, targetInstanceID string) error
	VerifyBackup(ctx context.Context, backupID string) error
	SetBackupConfig(ctx context.Context, instanceID string, req client.BackupConfigRequest) error
	GetBackupConfig(ctx context.Context, instanceID string) (map[string]interface{}, error)
	// Job management operations
	ListJobs(ctx context.Context, status string) (*client.JobListResponse, error)
	GetJob(ctx context.Context, id string) (*job.Job, error)
	CancelJob(ctx context.Context, id string) error
	DeleteJob(ctx context.Context, id string) error
	JobStats(ctx context.Context) (map[string]int, error)
	// Port forwarding (QEMU VM provider)
	ListPortForwardsVM(ctx context.Context, instanceID string) ([]provider.PortForward, error)
	AddPortForwardVM(ctx context.Context, instanceID string, pf provider.PortForward) error
	RemovePortForwardVM(ctx context.Context, instanceID string, protocol string, hostPort int) error
}

// Global API client instance
var APIClient APIClientInterface

// InitClientFromConfig initializes the global API client from context settings.
// Precedence (highest to lowest):
//  1. flagURL / flagAPIKey (non-empty CLI flags)
//  2. Active context from config file
//  3. HOSPITUS_API_URL / HOSPITUS_API_KEY environment variables
//  4. The local daemon's key file, when talking to a loopback address
//  5. Hard-coded default (localhost:8080, no auth)
func InitClientFromConfig(flagURL, flagAPIKey, flagContext, configPath string, tlsSkipVerify bool, tlsCA string) error {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}

	// Determine active context
	var ctx *Context
	if flagContext != "" && cfg != nil {
		ctx = cfg.GetContext(flagContext)
	} else if cfg != nil {
		ctx = cfg.ActiveContext()
	}

	// Build effective settings with precedence
	apiURL := flagURL
	apiKey := flagAPIKey
	skipVerify := tlsSkipVerify
	caFile := tlsCA

	if apiURL == "" {
		if ctx != nil && ctx.URL != "" {
			apiURL = ctx.URL
		} else if v := os.Getenv("HOSPITUS_API_URL"); v != "" {
			apiURL = v
		} else {
			apiURL = "localhost:8080"
		}
	}
	if apiKey == "" {
		if ctx != nil && ctx.APIKey != "" {
			apiKey = ctx.APIKey
		} else if v := os.Getenv("HOSPITUS_API_KEY"); v != "" {
			apiKey = v
		} else {
			apiKey = localDaemonKey(apiURL)
		}
	}
	if !skipVerify && ctx != nil {
		skipVerify = ctx.TLSSkipVerify
	}
	if caFile == "" && ctx != nil {
		caFile = ctx.TLSCACert
	}

	APIClient = client.NewClientWithOptions(apiURL, client.ClientOptions{
		APIKey:        apiKey,
		TLSSkipVerify: skipVerify,
		TLSCACert:     caFile,
	})
	return nil
}

// localAPIKeyFiles are where the service scripts put the key they generate.
var localAPIKeyFiles = []string{
	"/usr/local/etc/hospitus/api.key",                                  // FreeBSD rc.d service
	os.ExpandEnv("$HOME/Library/Application Support/hospitus/api.key"), // macOS launchd agent
}

// localDaemonKey reads the key of a daemon running on this machine.
//
// Root on the host can already read that file, restart the daemon and reach its
// data, so using it grants nothing new. Without it `doas hospitus ...` fails with
// "Missing API key" on the machine running the daemon: a context belongs to a
// user's own config, and doas clears the environment.
//
// It applies only to a loopback address: a key meant for the local daemon must
// never be sent to a remote host the caller happens to be pointed at.
func localDaemonKey(apiURL string) string {
	if !isLoopbackTarget(apiURL) {
		return ""
	}

	for _, path := range localAPIKeyFiles {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if key := strings.TrimSpace(string(data)); key != "" {
			return key
		}
	}
	return ""
}

// isLoopbackTarget reports whether the URL names this machine.
func isLoopbackTarget(apiURL string) bool {
	target := apiURL
	if i := strings.Index(target, "://"); i >= 0 {
		target = target[i+3:]
	}
	if i := strings.IndexAny(target, "/"); i >= 0 {
		target = target[:i]
	}

	host := target
	if h, _, err := net.SplitHostPort(target); err == nil {
		host = h
	}

	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]", "":
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// GetClient returns the global API client
func GetClient() APIClientInterface {
	return APIClient
}

// ResolveInstanceName tries exact match first, then fuzzy-matches against running instances.
// It also tries normalized matching (ignoring dashes/underscores) and label-based matching.
// Returns the resolved name (which may differ from the requested name) and a nil error on success.
// Returns the original name and the original error if no match is found.
// checkProvider refuses a name that exists under a different provider, so a
// provider-scoped command never reaches another provider's instance.
func providerOf(instance *datastore.Instance) string {
	if instance == nil {
		return ""
	}
	return instance.Provider
}

func checkProvider(name, actual, want string) error {
	if want == "" || actual == "" || actual == want {
		return nil
	}
	return fmt.Errorf("%s belongs to provider %s, not %s", name, actual, want)
}

// ResolveInstanceNameStrict resolves an instance name using EXACT matching
// only. Unlike ResolveInstanceName it never falls back to fuzzy/substring,
// normalized, or label matching. Destructive operations (destroy) must use
// this: an approximate match could target — and permanently destroy — the
// wrong instance (e.g. "jail1" matching "jail10").
func ResolveInstanceNameStrict(ctx context.Context, apiClient APIClientInterface, name, providerName string) (string, error) {
	if instance, err := apiClient.GetInstance(ctx, name); err == nil {
		if err := checkProvider(name, providerOf(instance), providerName); err != nil {
			return "", err
		}
		return name, nil
	}
	instances, err := apiClient.ListInstances(ctx, client.ListInstancesFilter{Provider: providerName})
	if err != nil {
		return "", fmt.Errorf("instance %q not found: %w", name, err)
	}
	for _, inst := range instances {
		if inst.ID == name {
			return name, nil
		}
	}
	return "", fmt.Errorf("instance %q not found", name)
}

func ResolveInstanceName(ctx context.Context, apiClient APIClientInterface, name, providerName string) (string, error) {
	// Try exact match first via GetInstance
	instance, err := apiClient.GetInstance(ctx, name)
	if err == nil {
		if provErr := checkProvider(name, providerOf(instance), providerName); provErr != nil {
			return "", provErr
		}
		return name, nil
	}

	// Exact match failed; try listing all instances of this provider
	instances, listErr := apiClient.ListInstances(ctx, client.ListInstancesFilter{
		Provider: providerName,
	})
	if listErr != nil {
		return name, err // return original error
	}

	// Look for exact match in list
	for _, inst := range instances {
		if inst.ID == name {
			return name, nil
		}
	}

	// Fuzzy match: instance ID contains the requested name, or vice versa
	// Prefer shorter (more precise) matches.
	var bestMatch string
	for _, inst := range instances {
		if strings.Contains(inst.ID, name) || strings.Contains(name, inst.ID) {
			if bestMatch == "" || len(inst.ID) < len(bestMatch) {
				bestMatch = inst.ID
			}
		}
	}

	// Normalized match: ignore dashes and underscores
	// This handles cases like "node-red" vs "nodered"
	normalizedName := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(name), "-", ""), "_", "")
	if bestMatch == "" && normalizedName != "" {
		for _, inst := range instances {
			normalizedID := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(inst.ID), "-", ""), "_", "")
			if normalizedID == normalizedName {
				if bestMatch == "" || len(inst.ID) < len(bestMatch) {
					bestMatch = inst.ID
				}
			}
		}
	}

	// Label-based match: check if any label value matches the requested name
	if bestMatch == "" {
		for _, inst := range instances {
			for _, value := range inst.Labels {
				if value == name {
					if bestMatch == "" || len(inst.ID) < len(bestMatch) {
						bestMatch = inst.ID
					}
				}
			}
		}
	}

	if bestMatch != "" {
		return bestMatch, nil
	}

	return name, err
}

// RequireInstanceOf fails unless the named instance belongs to providerName.
//
// A command under "hospitus qemu" must not act on a bhyve VM. Commands that
// resolve a name get this from the resolver; the ones that pass the name
// straight to the API call this instead, so that the check does not also
// bring fuzzy matching where there was none.
func RequireInstanceOf(ctx context.Context, apiClient APIClientInterface, name, providerName string) error {
	instance, err := apiClient.GetInstance(ctx, name)
	if err != nil {
		// Let the operation itself report a name that does not exist: it knows
		// the noun to use, and this must not turn a missing instance into a
		// different error.
		return nil
	}
	return checkProvider(name, providerOf(instance), providerName)
}
