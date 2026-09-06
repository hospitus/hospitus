package cmdutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
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
	if flagContext != "" {
		if cfg != nil {
			ctx = cfg.GetContext(flagContext)
		}
		// Named and absent is a typo, not a reason to fall back: silently
		// using the environment or localhost:8080 sent a destructive command
		// to whichever daemon happened to answer there.
		if ctx == nil {
			return fmt.Errorf("no context named %q; run \"hospitus context list\" to see the ones you have", flagContext)
		}
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
	// Everything the context carries describes the daemon at ctx.URL. Point
	// --url somewhere else and none of it applies any more: the key would have
	// gone to the new host, and its TLS settings would have governed a
	// connection to a daemon that never asked for them.
	urlOverridden := flagURL != "" && ctx != nil && flagURL != ctx.URL
	ctxApplies := ctx != nil && !urlOverridden

	if apiKey == "" {
		switch {
		case ctxApplies && ctx.APIKey != "":
			apiKey = ctx.APIKey
		default:
			if v := os.Getenv("HOSPITUS_API_KEY"); v != "" {
				apiKey = v
			} else {
				apiKey = localDaemonKey(apiURL)
			}
		}
	}
	// TLSSkipVerify especially: inherited across an override, a context that
	// legitimately skips verification for a daemon with a self-signed
	// certificate silently disabled it for the host named on the command line
	// (CWE-295).
	if !skipVerify && ctxApplies {
		skipVerify = ctx.TLSSkipVerify
	}
	if caFile == "" && ctxApplies {
		caFile = ctx.TLSCACert
	}

	if err := checkPlaintextKey(apiURL, apiKey, skipVerify, caFile); err != nil {
		return err
	}

	daemonURL = redactUserinfo(apiURL)
	daemonIsLocal = isLoopbackTarget(apiURL)

	APIClient = client.NewClientWithOptions(apiURL, client.ClientOptions{
		APIKey:        apiKey,
		TLSSkipVerify: skipVerify,
		TLSCACert:     caFile,
	})
	return nil
}

// daemonURL and daemonIsLocal record what InitClientFromConfig settled on, for
// the commands that hand the daemon a filesystem path.
//
// daemonIsLocal defaults to true: a command that runs without the client being
// initialized (a unit test, mainly) keeps the old behavior.
var (
	daemonURL     = "localhost:8080"
	daemonIsLocal = true
)

// redactUserinfo strips any "user:password@" from a URL before it is shown.
//
// DaemonPath names the daemon in the error it returns for a relative path, and
// a URL configured as https://user:password@host would have printed the
// password to the terminal and into whatever log caught it.
func redactUserinfo(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil {
		// Not parseable as a URL, or nothing to hide. A bare "host:port" —
		// what a context usually holds — lands here and is returned as is.
		if err != nil && strings.Contains(rawURL, "@") {
			// Unparseable *and* it looks like it carries credentials: say
			// nothing rather than risk printing them.
			return "the configured daemon"
		}
		return rawURL
	}
	u.User = url.User("redacted")
	return u.String()
}

// CheckInstanceList rejects a response carrying a JSON null element.
//
// It decodes to a nil *datastore.Instance, and every caller that reads a field
// off it panics. Skipping it silently is no better: the command then acts on a
// list it knows to be short, and says nothing.
func CheckInstanceList(instances []*datastore.Instance) error {
	for i, inst := range instances {
		if inst == nil {
			return fmt.Errorf("the daemon's instance list holds a null at position %d: the response is malformed", i)
		}
	}
	return nil
}

// DaemonIsLocal reports whether the daemon runs on this machine.
//
// For the few checks that can only speak for the host they run on — a CPU
// architecture, say. Against a remote daemon such a check describes the wrong
// machine and has to be left to the daemon itself.
func DaemonIsLocal() bool {
	return daemonIsLocal
}

// DaemonPath prepares a filesystem path for an API call that opens it on the
// daemon's host.
//
// Export and import name a file the *daemon* reads and writes. Against a local
// daemon, resolving a relative path here is right and convenient. Against a
// remote one it is a trap: filepath.Abs would silently prefix the CLI's own
// working directory onto a path that will be opened on another machine. So a
// relative path is refused there rather than guessed at.
func DaemonPath(kind, path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	if !daemonIsLocal {
		return "", fmt.Errorf("%s path %q is relative, and the daemon at %s would open it on its own host, not here\n"+
			"  → give an absolute path as it exists on the daemon's machine", kind, path, daemonURL)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid %s path: %w", kind, err)
	}
	return abs, nil
}

// checkPlaintextKey refuses to send an API key to a remote host over http://.
//
// The key is a bearer credential: on the wire in the clear it is readable by
// anything between here and the daemon, and replayable afterwards. Loopback is
// exempt — the traffic never leaves the machine — and so is an explicit
// HOSPITUS_ALLOW_PLAINTEXT_KEY=1, for a tunnel that already provides the
// confidentiality this is asking for.
func checkPlaintextKey(apiURL, apiKey string, skipVerify bool, caFile string) error {
	if apiKey == "" || isLoopbackTarget(apiURL) {
		return nil
	}
	// Mirror the scheme NewClientWithOptions will pick for a bare host:port.
	https := strings.HasPrefix(apiURL, "https://") ||
		(!strings.HasPrefix(apiURL, "http://") && (skipVerify || caFile != ""))
	if https {
		return nil
	}
	if os.Getenv("HOSPITUS_ALLOW_PLAINTEXT_KEY") == "1" {
		return nil
	}
	return fmt.Errorf("refusing to send the API key to %s over http://: anything on the path can read and reuse it\n"+
		"  → use https:// (with --tls-ca for a private CA)\n"+
		"  → or, if the link is already encrypted (an SSH tunnel, a VPN), set HOSPITUS_ALLOW_PLAINTEXT_KEY=1", apiURL)
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

// providerOf returns an instance's provider, or "" when there is no instance.
func providerOf(instance *datastore.Instance) string {
	if instance == nil {
		return ""
	}
	return instance.Provider
}

func checkProvider(name, actual, want string) error {
	if want == "" || actual == want {
		return nil
	}
	// An empty actual used to pass. It means the daemon told us nothing about
	// the instance's provider, which is not the same as telling us it is the
	// one asked for: a jail command would then act on whatever the name
	// resolved to.
	if actual == "" {
		return fmt.Errorf("%s: the daemon reported no provider, so it cannot be confirmed as %s", name, want)
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

// ResolveInstanceName tries exact match first, then fuzzy-matches against running instances.
// It also tries normalized matching (ignoring dashes/underscores) and label-based matching.
// Returns the resolved name (which may differ from the requested name) and a nil error on success.
// Returns the original name and the original error if no match is found.
// checkProvider refuses a name that exists under a different provider, so a
// provider-scoped command never reaches another provider's instance.
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
		if errors.Is(err, client.ErrNotFound) {
			return nil
		}
		// Anything else — the daemon down, a timeout, a 500 — left the check
		// unanswered. Passing meant "hospitus qemu stop" went on to act on a
		// name that may well be a bhyve VM, which is what this exists to stop.
		return fmt.Errorf("cannot confirm %q is a %s instance: %w", name, providerName, err)
	}
	return checkProvider(name, providerOf(instance), providerName)
}
