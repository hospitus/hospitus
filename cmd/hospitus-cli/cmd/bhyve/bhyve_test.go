package bhyve

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/job"
	"github.com/hospitus/hospitus/pkg/provider"
)

// mockBhyveClient is a full-interface mock for testing bhyve CLI commands.
// Set the exported fields to control what individual methods return; all other
// methods are harmless no-op stubs that return nil/nil (or zero values).
type mockBhyveClient struct {
	// GetInstanceMetrics
	metricsResult *client.InstanceMetrics
	metricsErr    error

	// ListAutoStart
	autoStartList []client.AutoStartInfo
	autoStartErr  error

	// GetAutoStart / SetAutoStart / DisableAutoStart
	autoStartInfo    *client.AutoStartInfo
	getAutoStartErr  error
	setAutoStartErr  error
	disableAutoStart error

	// ListExposedPorts
	portMappings []client.PortMapping
	portListErr  error

	// ExposePort
	exposeResult *client.ExposePortResult
	exposeErr    error

	// UnexposePort
	unexposeErr error

	// GetInstance / ListInstances  (needed by ResolveInstanceName)
	getInstance      *datastore.Instance
	getInstanceErr   error
	listInstances    []*datastore.Instance
	listInstancesErr error
}

// ─── Metrics ────────────────────────────────────────────────────────────────

func (m *mockBhyveClient) GetInstanceMetrics(_ context.Context, _ string) (*client.InstanceMetrics, error) {
	return m.metricsResult, m.metricsErr
}

func (m *mockBhyveClient) GetInstanceHealth(_ context.Context, _ string) (*client.InstanceHealth, error) {
	return nil, nil
}

// ─── Instance CRUD ──────────────────────────────────────────────────────────

func (m *mockBhyveClient) CreateInstance(_ context.Context, _ client.CreateInstanceRequest) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockBhyveClient) ListInstances(_ context.Context, _ client.ListInstancesFilter) ([]*datastore.Instance, error) {
	return m.listInstances, m.listInstancesErr
}

func (m *mockBhyveClient) GetInstance(_ context.Context, _ string) (*datastore.Instance, error) {
	return m.getInstance, m.getInstanceErr
}

func (m *mockBhyveClient) UpdateInstance(_ context.Context, _ string, _ client.UpdateInstanceRequest) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockBhyveClient) StartInstance(_ context.Context, _ string, _ io.Writer) error { return nil }

func (m *mockBhyveClient) StopInstance(_ context.Context, _ string, _ bool) error { return nil }

func (m *mockBhyveClient) RestartInstance(_ context.Context, _ string) error { return nil }

func (m *mockBhyveClient) DeleteInstance(_ context.Context, _ string, _ bool) error { return nil }

// ─── Images ─────────────────────────────────────────────────────────────────

func (m *mockBhyveClient) FetchImage(_ context.Context, _ string, _ io.Writer) error { return nil }

func (m *mockBhyveClient) ListImages(_ context.Context) ([]map[string]interface{}, error) {
	return nil, nil
}
func (m *mockBhyveClient) DeleteImage(_ context.Context, _ string) error { return nil }

// ─── Export / Import ────────────────────────────────────────────────────────

func (m *mockBhyveClient) ExportInstance(_ context.Context, _ string, _ client.ExportOptions) (*client.ExportResult, error) {
	return nil, nil
}

func (m *mockBhyveClient) ImportInstance(_ context.Context, _ client.ImportOptions) (*client.ImportResult, error) {
	return nil, nil
}

// ─── Port forwarding ────────────────────────────────────────────────────────

func (m *mockBhyveClient) ExposePort(_ context.Context, _ string, _ client.ExposePortRequest) (*client.ExposePortResult, error) {
	return m.exposeResult, m.exposeErr
}

func (m *mockBhyveClient) UnexposePort(_ context.Context, _ string, _ int, _ string) error {
	return m.unexposeErr
}

func (m *mockBhyveClient) ListExposedPorts(_ context.Context, _ string) ([]client.PortMapping, error) {
	return m.portMappings, m.portListErr
}

// ─── Exec ────────────────────────────────────────────────────────────────────

func (m *mockBhyveClient) ExecCommand(_ context.Context, _ string, _ client.ExecRequest) (*client.ExecResult, error) {
	return nil, nil
}

func (m *mockBhyveClient) ExecCommandStream(_ context.Context, _ string, _ client.ExecRequest, _, _ io.Writer) (int, error) {
	return 0, nil
}

// ─── Snapshots & Clones ─────────────────────────────────────────────────────

func (m *mockBhyveClient) ListSnapshots(_ context.Context, _ string) ([]client.SnapshotInfo, error) {
	return nil, nil
}
func (m *mockBhyveClient) CreateSnapshot(_ context.Context, _, _ string) error  { return nil }
func (m *mockBhyveClient) DeleteSnapshot(_ context.Context, _, _ string) error  { return nil }
func (m *mockBhyveClient) RestoreSnapshot(_ context.Context, _, _ string) error { return nil }

func (m *mockBhyveClient) CloneInstance(_ context.Context, _ string, _ client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

func (m *mockBhyveClient) CloneFromSnapshot(_ context.Context, _, _ string, _ client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

// ─── Media & Boot ────────────────────────────────────────────────────────────

func (m *mockBhyveClient) InsertMedia(_ context.Context, _ string, _ provider.MediaSpec) error {
	return nil
}
func (m *mockBhyveClient) EjectMedia(_ context.Context, _ string, _ string) error { return nil }
func (m *mockBhyveClient) ListMedia(_ context.Context, _ string) ([]provider.MediaInfo, error) {
	return nil, nil
}

func (m *mockBhyveClient) SetBootOrder(_ context.Context, _ string, _ provider.BootOrder) error {
	return nil
}

func (m *mockBhyveClient) GetBootOrder(_ context.Context, _ string) (*provider.BootOrder, error) {
	return nil, nil
}

// ─── Services ────────────────────────────────────────────────────────────────

func (m *mockBhyveClient) ServiceAction(_ context.Context, _ string, _ client.ServiceActionRequest) (*client.ServiceActionResult, error) {
	return nil, nil
}

func (m *mockBhyveClient) GetServiceStatus(_ context.Context, _, _ string) (*client.ServiceStatus, error) {
	return nil, nil
}

func (m *mockBhyveClient) ListServices(_ context.Context, _, _ string) ([]client.ServiceInfo, error) {
	return nil, nil
}

// ─── Volumes ─────────────────────────────────────────────────────────────────

func (m *mockBhyveClient) CreateVolume(_ context.Context, _ client.CreateVolumeRequest) (*client.VolumeInfo, error) {
	return nil, nil
}
func (m *mockBhyveClient) DeleteVolume(_ context.Context, _ string) error { return nil }
func (m *mockBhyveClient) ListVolumes(_ context.Context) ([]client.VolumeInfo, error) {
	return nil, nil
}

func (m *mockBhyveClient) GetVolume(_ context.Context, _ string) (*client.VolumeInfo, error) {
	return nil, nil
}
func (m *mockBhyveClient) AttachVolume(_ context.Context, _, _, _ string) error { return nil }
func (m *mockBhyveClient) DetachVolume(_ context.Context, _, _ string) error    { return nil }

// ─── Network interfaces ──────────────────────────────────────────────────────

func (m *mockBhyveClient) AddNetworkInterface(_ context.Context, _ string, _ client.NetworkInterfaceRequest) (*client.NetworkInterfaceInfo, error) {
	return nil, nil
}

func (m *mockBhyveClient) RemoveNetworkInterface(_ context.Context, _, _ string) error { return nil }

func (m *mockBhyveClient) ListNetworkInterfaces(_ context.Context, _ string) ([]client.NetworkInterfaceInfo, error) {
	return nil, nil
}

// ─── Lifecycle ───────────────────────────────────────────────────────────────

func (m *mockBhyveClient) RenameInstance(_ context.Context, _, _ string) (map[string]string, error) {
	return nil, nil
}

func (m *mockBhyveClient) UpgradeInstance(_ context.Context, _, _ string) (map[string]string, error) {
	return nil, nil
}
func (m *mockBhyveClient) PauseInstance(_ context.Context, _ string) error  { return nil }
func (m *mockBhyveClient) ResumeInstance(_ context.Context, _ string) error { return nil }

// ─── Checkpoints ─────────────────────────────────────────────────────────────

func (m *mockBhyveClient) ListCheckpoints(_ context.Context, _ string) ([]client.CheckpointInfo, error) {
	return nil, nil
}
func (m *mockBhyveClient) CreateCheckpoint(_ context.Context, _, _ string) error  { return nil }
func (m *mockBhyveClient) RestoreCheckpoint(_ context.Context, _, _ string) error { return nil }
func (m *mockBhyveClient) DeleteCheckpoint(_ context.Context, _, _ string) error  { return nil }

// ─── Auto-start ───────────────────────────────────────────────────────────────

func (m *mockBhyveClient) ListAutoStart(_ context.Context) ([]client.AutoStartInfo, error) {
	return m.autoStartList, m.autoStartErr
}

func (m *mockBhyveClient) GetAutoStart(_ context.Context, _, _ string) (*client.AutoStartInfo, error) {
	return m.autoStartInfo, m.getAutoStartErr
}

func (m *mockBhyveClient) SetAutoStart(_ context.Context, _, _ string, _ provider.AutoStartConfig) (*client.AutoStartInfo, error) {
	return m.autoStartInfo, m.setAutoStartErr
}

func (m *mockBhyveClient) DisableAutoStart(_ context.Context, _, _ string) error {
	return m.disableAutoStart
}

func (m *mockBhyveClient) ListBackups(_ context.Context, _ string) ([]client.BackupInfo, error) {
	return nil, nil
}

func (m *mockBhyveClient) GetBackup(_ context.Context, _ string) (*client.BackupInfo, error) {
	return nil, nil
}

func (m *mockBhyveClient) CreateBackup(_ context.Context, _, _ string) (*client.BackupInfo, error) {
	return nil, nil
}
func (m *mockBhyveClient) DeleteBackup(_ context.Context, _ string) error     { return nil }
func (m *mockBhyveClient) RestoreBackup(_ context.Context, _, _ string) error { return nil }
func (m *mockBhyveClient) VerifyBackup(_ context.Context, _ string) error     { return nil }
func (m *mockBhyveClient) SetBackupConfig(_ context.Context, _ string, _ client.BackupConfigRequest) error {
	return nil
}

func (m *mockBhyveClient) GetBackupConfig(_ context.Context, _ string) (map[string]interface{}, error) {
	return nil, nil
}

func (m *mockBhyveClient) ListJobs(_ context.Context, _ string) (*client.JobListResponse, error) {
	return &client.JobListResponse{}, nil
}
func (m *mockBhyveClient) GetJob(_ context.Context, _ string) (*job.Job, error) { return nil, nil }
func (m *mockBhyveClient) CancelJob(_ context.Context, _ string) error          { return nil }
func (m *mockBhyveClient) DeleteJob(_ context.Context, _ string) error          { return nil }
func (m *mockBhyveClient) JobStats(_ context.Context) (map[string]int, error)   { return nil, nil }
func (m *mockBhyveClient) ListPortForwardsVM(_ context.Context, _ string) ([]provider.PortForward, error) {
	return nil, nil
}

func (m *mockBhyveClient) AddPortForwardVM(_ context.Context, _ string, _ provider.PortForward) error {
	return nil
}

func (m *mockBhyveClient) RemovePortForwardVM(_ context.Context, _ string, _ string, _ int) error {
	return nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// stats command tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestStatsCommand_Success(t *testing.T) {
	mock := &mockBhyveClient{
		metricsResult: &client.InstanceMetrics{
			CPUUsagePercent: 42.5,
			MemoryUsedMB:    512,
			MemoryTotalMB:   1024,
			DiskReadBytes:   1048576, // 1 MiB
			DiskWriteBytes:  524288,  // 512 KiB
			NetRxBytes:      2048,
			NetTxBytes:      4096,
			Timestamp:       "2025-01-01T12:00:00Z",
		},
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := newStatsCommand()
	cmd.SetArgs([]string{"testvm"})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	for _, want := range []string{
		"testvm",
		"RESOURCES",
		"CPU Usage:",
		"42.5%",
		"Memory Used:",
		"Memory Total:",
		"DISK I/O",
		"NETWORK",
		"2025-01-01T12:00:00Z",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected output to contain %q\nfull output:\n%s", want, output)
		}
	}
}

func TestStatsCommand_APIError(t *testing.T) {
	mock := &mockBhyveClient{
		metricsErr: errors.New("metrics not available"),
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := newStatsCommand()
	cmd.SetArgs([]string{"brokenvm"})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when API fails, got nil")
	}
	if !strings.Contains(err.Error(), "failed to get metrics") {
		t.Errorf("error should mention 'failed to get metrics', got: %v", err)
	}
}

func TestStatsCommand_NoArgs(t *testing.T) {
	cmd := newStatsCommand()
	cmd.SetArgs([]string{})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when no VM name is provided, got nil")
	}
}

// Verify the stats command is registered under the bhyve root command.
func TestStatsCommand_RegisteredUnderBhyve(t *testing.T) {
	root := NewBhyveCommand()
	for _, sub := range root.Commands() {
		if sub.Use == "stats <name>" {
			return
		}
	}
	t.Fatal("'stats' command not registered under bhyve")
}

// ═══════════════════════════════════════════════════════════════════════════════
// autostart list command tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestAutostartListCommand_Success(t *testing.T) {
	mock := &mockBhyveClient{
		autoStartList: []client.AutoStartInfo{
			{
				ID:       "dbvm",
				Provider: "bhyve",
				AutoStart: provider.AutoStartConfig{
					Enabled:  true,
					Priority: 10,
					DelayMS:  2000,
				},
			},
			{
				ID:       "webvm",
				Provider: "bhyve",
				AutoStart: provider.AutoStartConfig{
					Enabled:  true,
					Priority: 50,
					DelayMS:  0,
				},
			},
		},
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := autostartListCmd("bhyve", "VM", "VMs")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	for _, want := range []string{
		"NAME", "PRIORITY", "DELAY",
		"dbvm", "10", "2000",
		"webvm", "50",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected output to contain %q\nfull output:\n%s", want, output)
		}
	}
	// Must not show the empty-list message.
	if strings.Contains(output, "No VMs configured for auto-start.") {
		t.Error("unexpected 'No VMs configured' message when VMs exist")
	}
}

func TestAutostartListCommand_Empty(t *testing.T) {
	mock := &mockBhyveClient{
		autoStartList: []client.AutoStartInfo{},
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := autostartListCmd("bhyve", "VM", "VMs")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "No VMs configured for auto-start.") {
		t.Errorf("expected 'No VMs configured for auto-start.' message\nfull output:\n%s", output)
	}
}

// Only jail entries present — bhyve list should still say empty.
func TestAutostartListCommand_NonBhyveEntries(t *testing.T) {
	mock := &mockBhyveClient{
		autoStartList: []client.AutoStartInfo{
			{
				ID:        "jailone",
				Provider:  "jail",
				AutoStart: provider.AutoStartConfig{Enabled: true, Priority: 5},
			},
		},
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := autostartListCmd("bhyve", "VM", "VMs")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "No VMs configured for auto-start.") {
		t.Errorf("expected empty-list message when only jail entries exist\nfull output:\n%s", output)
	}
	if strings.Contains(output, "jailone") {
		t.Error("jail entry should not appear in bhyve autostart list output")
	}
}

func TestAutostartListCommand_APIError(t *testing.T) {
	mock := &mockBhyveClient{
		autoStartErr: errors.New("connection refused"),
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := autostartListCmd("bhyve", "VM", "VMs")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when API fails, got nil")
	}
	if !strings.Contains(err.Error(), "failed to list auto-start VMs") {
		t.Errorf("error should mention 'failed to list auto-start VMs', got: %v", err)
	}
}

// Verify the autostart command is registered under the bhyve root command.
func TestAutostartCommand_RegisteredUnderBhyve(t *testing.T) {
	root := NewBhyveCommand()
	for _, sub := range root.Commands() {
		if sub.Use == "autostart" {
			return
		}
	}
	t.Fatal("'autostart' command not registered under bhyve")
}

// ═══════════════════════════════════════════════════════════════════════════════
// expose list command tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestExposeListCommand_Success(t *testing.T) {
	mock := &mockBhyveClient{
		portMappings: []client.PortMapping{
			{
				Protocol:   "tcp",
				HostPort:   80,
				TargetPort: 8080,
				TargetIP:   "192.168.1.50",
			},
			{
				Protocol:   "udp",
				HostPort:   53,
				TargetPort: 53,
				TargetIP:   "192.168.1.50",
			},
		},
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := newBhyveExposeListCommand()
	cmd.SetArgs([]string{"myvm"})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	for _, want := range []string{
		"PROTOCOL", "HOST PORT", "TARGET PORT", "TARGET IP",
		"tcp", "80", "8080", "192.168.1.50",
		"udp", "53",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected output to contain %q\nfull output:\n%s", want, output)
		}
	}
}

func TestExposeListCommand_Empty(t *testing.T) {
	mock := &mockBhyveClient{
		portMappings: []client.PortMapping{},
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := newBhyveExposeListCommand()
	cmd.SetArgs([]string{"emptyvm"})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	want := "No port forwarding rules configured for VM emptyvm"
	if !strings.Contains(output, want) {
		t.Errorf("expected %q in output\nfull output:\n%s", want, output)
	}
}

func TestExposeListCommand_APIError(t *testing.T) {
	mock := &mockBhyveClient{
		portListErr: errors.New("daemon unavailable"),
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := newBhyveExposeListCommand()
	cmd.SetArgs([]string{"myvm"})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when API fails, got nil")
	}
	if !strings.Contains(err.Error(), "failed to list port forwarding rules") {
		t.Errorf("error should mention 'failed to list port forwarding rules', got: %v", err)
	}
}

func TestExposeListCommand_NoArgs(t *testing.T) {
	cmd := newBhyveExposeListCommand()
	cmd.SetArgs([]string{})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when no VM name is provided, got nil")
	}
}

// Verify the expose command is registered under the bhyve root command.
func TestExposeCommand_RegisteredUnderBhyve(t *testing.T) {
	root := NewBhyveCommand()
	for _, sub := range root.Commands() {
		if sub.Use == "expose" {
			return
		}
	}
	t.Fatal("'expose' command not registered under bhyve")
}

// autostartListCmd returns the shared autostart command primed to run "list".
//
// It returns the parent rather than the sub-command because cobra's Execute
// walks up to the root, so executing a child directly re-dispatches and just
// prints help. The implementation moved to cmdutil once four providers needed
// it; these tests stay here because what they check is the per-provider
// filtering.
func autostartListCmd(providerName, noun, nounPlural string) *cobra.Command {
	cmd := cmdutil.NewAutostartCommand(providerName, noun, nounPlural)
	cmd.SetArgs([]string{"list"})
	return cmd
}
