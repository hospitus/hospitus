package qemu

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

// mockQemuClient is a full-interface mock for testing QEMU CLI commands.
// Set the exported fields to control what individual methods return; all other
// methods are harmless no-op stubs that return nil/nil (or zero values).
type mockQemuClient struct {
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

func (m *mockQemuClient) GetInstanceMetrics(_ context.Context, _ string) (*client.InstanceMetrics, error) {
	return m.metricsResult, m.metricsErr
}

func (m *mockQemuClient) GetInstanceHealth(_ context.Context, _ string) (*client.InstanceHealth, error) {
	return nil, nil
}

// ─── Instance CRUD ──────────────────────────────────────────────────────────

func (m *mockQemuClient) CreateInstance(_ context.Context, _ client.CreateInstanceRequest) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockQemuClient) ListInstances(_ context.Context, _ client.ListInstancesFilter) ([]*datastore.Instance, error) {
	return m.listInstances, m.listInstancesErr
}

func (m *mockQemuClient) GetInstance(_ context.Context, _ string) (*datastore.Instance, error) {
	return m.getInstance, m.getInstanceErr
}

func (m *mockQemuClient) UpdateInstance(_ context.Context, _ string, _ client.UpdateInstanceRequest) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockQemuClient) StartInstance(_ context.Context, _ string, _ io.Writer) error { return nil }

func (m *mockQemuClient) StopInstance(_ context.Context, _ string, _ bool) error { return nil }

func (m *mockQemuClient) RestartInstance(_ context.Context, _ string) error { return nil }

func (m *mockQemuClient) DeleteInstance(_ context.Context, _ string, _ bool) error { return nil }

// ─── Images ─────────────────────────────────────────────────────────────────

func (m *mockQemuClient) FetchImage(_ context.Context, _ string, _ io.Writer) error { return nil }

func (m *mockQemuClient) ListImages(_ context.Context) ([]map[string]interface{}, error) {
	return nil, nil
}
func (m *mockQemuClient) DeleteImage(_ context.Context, _ string) error { return nil }

// ─── Export / Import ────────────────────────────────────────────────────────

func (m *mockQemuClient) ExportInstance(_ context.Context, _ string, _ client.ExportOptions) (*client.ExportResult, error) {
	return nil, nil
}

func (m *mockQemuClient) ImportInstance(_ context.Context, _ client.ImportOptions) (*client.ImportResult, error) {
	return nil, nil
}

// ─── Port forwarding ────────────────────────────────────────────────────────

func (m *mockQemuClient) ExposePort(_ context.Context, _ string, _ client.ExposePortRequest) (*client.ExposePortResult, error) {
	return m.exposeResult, m.exposeErr
}

func (m *mockQemuClient) UnexposePort(_ context.Context, _ string, _ int, _ string) error {
	return m.unexposeErr
}

func (m *mockQemuClient) ListExposedPorts(_ context.Context, _ string) ([]client.PortMapping, error) {
	return m.portMappings, m.portListErr
}

// ─── Exec ────────────────────────────────────────────────────────────────────

func (m *mockQemuClient) ExecCommand(_ context.Context, _ string, _ client.ExecRequest) (*client.ExecResult, error) {
	return nil, nil
}

func (m *mockQemuClient) ExecCommandStream(_ context.Context, _ string, _ client.ExecRequest, _, _ io.Writer) (int, error) {
	return 0, nil
}

// ─── Snapshots & Clones ─────────────────────────────────────────────────────

func (m *mockQemuClient) ListSnapshots(_ context.Context, _ string) ([]client.SnapshotInfo, error) {
	return nil, nil
}
func (m *mockQemuClient) CreateSnapshot(_ context.Context, _, _ string) error  { return nil }
func (m *mockQemuClient) DeleteSnapshot(_ context.Context, _, _ string) error  { return nil }
func (m *mockQemuClient) RestoreSnapshot(_ context.Context, _, _ string) error { return nil }

func (m *mockQemuClient) CloneInstance(_ context.Context, _ string, _ client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

func (m *mockQemuClient) CloneFromSnapshot(_ context.Context, _, _ string, _ client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

// ─── Media & Boot ────────────────────────────────────────────────────────────

func (m *mockQemuClient) InsertMedia(_ context.Context, _ string, _ provider.MediaSpec) error {
	return nil
}
func (m *mockQemuClient) EjectMedia(_ context.Context, _ string, _ string) error { return nil }
func (m *mockQemuClient) ListMedia(_ context.Context, _ string) ([]provider.MediaInfo, error) {
	return nil, nil
}

func (m *mockQemuClient) SetBootOrder(_ context.Context, _ string, _ provider.BootOrder) error {
	return nil
}

func (m *mockQemuClient) GetBootOrder(_ context.Context, _ string) (*provider.BootOrder, error) {
	return nil, nil
}

// ─── Services ────────────────────────────────────────────────────────────────

func (m *mockQemuClient) ServiceAction(_ context.Context, _ string, _ client.ServiceActionRequest) (*client.ServiceActionResult, error) {
	return nil, nil
}

func (m *mockQemuClient) GetServiceStatus(_ context.Context, _, _ string) (*client.ServiceStatus, error) {
	return nil, nil
}

func (m *mockQemuClient) ListServices(_ context.Context, _, _ string) ([]client.ServiceInfo, error) {
	return nil, nil
}

// ─── Volumes ─────────────────────────────────────────────────────────────────

func (m *mockQemuClient) CreateVolume(_ context.Context, _ client.CreateVolumeRequest) (*client.VolumeInfo, error) {
	return nil, nil
}
func (m *mockQemuClient) DeleteVolume(_ context.Context, _ string) error { return nil }
func (m *mockQemuClient) ListVolumes(_ context.Context) ([]client.VolumeInfo, error) {
	return nil, nil
}

func (m *mockQemuClient) GetVolume(_ context.Context, _ string) (*client.VolumeInfo, error) {
	return nil, nil
}
func (m *mockQemuClient) AttachVolume(_ context.Context, _, _, _ string) error { return nil }
func (m *mockQemuClient) DetachVolume(_ context.Context, _, _ string) error    { return nil }

// ─── Network interfaces ──────────────────────────────────────────────────────

func (m *mockQemuClient) AddNetworkInterface(_ context.Context, _ string, _ client.NetworkInterfaceRequest) (*client.NetworkInterfaceInfo, error) {
	return nil, nil
}
func (m *mockQemuClient) RemoveNetworkInterface(_ context.Context, _, _ string) error { return nil }
func (m *mockQemuClient) ListNetworkInterfaces(_ context.Context, _ string) ([]client.NetworkInterfaceInfo, error) {
	return nil, nil
}

// ─── Lifecycle ───────────────────────────────────────────────────────────────

func (m *mockQemuClient) RenameInstance(_ context.Context, _, _ string) (map[string]string, error) {
	return nil, nil
}

func (m *mockQemuClient) UpgradeInstance(_ context.Context, _, _ string) (map[string]string, error) {
	return nil, nil
}
func (m *mockQemuClient) PauseInstance(_ context.Context, _ string) error  { return nil }
func (m *mockQemuClient) ResumeInstance(_ context.Context, _ string) error { return nil }

// ─── Checkpoints ─────────────────────────────────────────────────────────────

func (m *mockQemuClient) ListCheckpoints(_ context.Context, _ string) ([]client.CheckpointInfo, error) {
	return nil, nil
}
func (m *mockQemuClient) CreateCheckpoint(_ context.Context, _, _ string) error  { return nil }
func (m *mockQemuClient) RestoreCheckpoint(_ context.Context, _, _ string) error { return nil }
func (m *mockQemuClient) DeleteCheckpoint(_ context.Context, _, _ string) error  { return nil }

// ─── Auto-start ───────────────────────────────────────────────────────────────

func (m *mockQemuClient) ListAutoStart(_ context.Context) ([]client.AutoStartInfo, error) {
	return m.autoStartList, m.autoStartErr
}

func (m *mockQemuClient) GetAutoStart(_ context.Context, _, _ string) (*client.AutoStartInfo, error) {
	return m.autoStartInfo, m.getAutoStartErr
}

func (m *mockQemuClient) SetAutoStart(_ context.Context, _, _ string, _ provider.AutoStartConfig) (*client.AutoStartInfo, error) {
	return m.autoStartInfo, m.setAutoStartErr
}

func (m *mockQemuClient) DisableAutoStart(_ context.Context, _, _ string) error {
	return m.disableAutoStart
}

func (m *mockQemuClient) ListBackups(_ context.Context, _ string) ([]client.BackupInfo, error) {
	return nil, nil
}

func (m *mockQemuClient) GetBackup(_ context.Context, _ string) (*client.BackupInfo, error) {
	return nil, nil
}

func (m *mockQemuClient) CreateBackup(_ context.Context, _, _ string) (*client.BackupInfo, error) {
	return nil, nil
}
func (m *mockQemuClient) DeleteBackup(_ context.Context, _ string) error     { return nil }
func (m *mockQemuClient) RestoreBackup(_ context.Context, _, _ string) error { return nil }
func (m *mockQemuClient) VerifyBackup(_ context.Context, _ string) error     { return nil }
func (m *mockQemuClient) SetBackupConfig(_ context.Context, _ string, _ client.BackupConfigRequest) error {
	return nil
}

func (m *mockQemuClient) GetBackupConfig(_ context.Context, _ string) (map[string]interface{}, error) {
	return nil, nil
}

func (m *mockQemuClient) ListJobs(_ context.Context, _ string) (*client.JobListResponse, error) {
	return &client.JobListResponse{}, nil
}
func (m *mockQemuClient) GetJob(_ context.Context, _ string) (*job.Job, error) { return nil, nil }
func (m *mockQemuClient) CancelJob(_ context.Context, _ string) error          { return nil }
func (m *mockQemuClient) DeleteJob(_ context.Context, _ string) error          { return nil }
func (m *mockQemuClient) JobStats(_ context.Context) (map[string]int, error)   { return nil, nil }
func (m *mockQemuClient) ListPortForwardsVM(_ context.Context, _ string) ([]provider.PortForward, error) {
	return nil, nil
}

func (m *mockQemuClient) AddPortForwardVM(_ context.Context, _ string, _ provider.PortForward) error {
	return nil
}

func (m *mockQemuClient) RemovePortForwardVM(_ context.Context, _ string, _ string, _ int) error {
	return nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// stats command tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestStatsCommand_Success(t *testing.T) {
	mock := &mockQemuClient{
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
	mock := &mockQemuClient{
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

// TestStatsCommand_WatchModeAPIError verifies that --watch exits immediately
// when the first metrics call fails (so the infinite loop does not block).
func TestStatsCommand_WatchModeAPIError(t *testing.T) {
	mock := &mockQemuClient{
		metricsErr: errors.New("daemon unreachable"),
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	// Call runQEMUStats directly with watch=true so the error is returned on
	// the first iteration, before the refresh interval elapses.
	cmd := newStatsCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := runQEMUStats(cmd, []string{"watchvm"}, true, 1)
	if err == nil {
		t.Fatal("expected error from watch mode when API fails, got nil")
	}
	if !strings.Contains(err.Error(), "failed to get metrics") {
		t.Errorf("error should mention 'failed to get metrics', got: %v", err)
	}
}

// TestStatsCommand_WatchFlagRegistered verifies --watch and --interval flags
// are registered on the stats command.
func TestStatsCommand_WatchFlagRegistered(t *testing.T) {
	cmd := newStatsCommand()
	if f := cmd.Flags().Lookup("watch"); f == nil {
		t.Error("expected --watch flag to be registered on stats command")
	}
	if f := cmd.Flags().Lookup("interval"); f == nil {
		t.Error("expected --interval flag to be registered on stats command")
	}
}

// TestStatsCommand_RegisteredUnderQemu verifies the stats subcommand is
// present under the QEMU root command.
func TestStatsCommand_RegisteredUnderQemu(t *testing.T) {
	root := NewQemuCommand()
	for _, sub := range root.Commands() {
		if sub.Use == "stats <name>" {
			return
		}
	}
	t.Fatal("'stats' command not registered under qemu")
}

// ═══════════════════════════════════════════════════════════════════════════════
// autostart list command tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestAutostartListCommand_Success(t *testing.T) {
	mock := &mockQemuClient{
		autoStartList: []client.AutoStartInfo{
			{
				ID:       "dbvm",
				Provider: "qemu",
				AutoStart: provider.AutoStartConfig{
					Enabled:  true,
					Priority: 10,
					DelayMS:  2000,
				},
			},
			{
				ID:       "webvm",
				Provider: "qemu",
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

	cmd := autostartListCmd("qemu", "VM", "VMs")
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
	mock := &mockQemuClient{
		autoStartList: []client.AutoStartInfo{},
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := autostartListCmd("qemu", "VM", "VMs")
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

// TestAutostartListCommand_NonQemuEntries verifies that entries from other
// providers (e.g. "bhyve", "jail") are not shown in the QEMU autostart list.
func TestAutostartListCommand_NonQemuEntries(t *testing.T) {
	mock := &mockQemuClient{
		autoStartList: []client.AutoStartInfo{
			{
				ID:        "bhyvevm",
				Provider:  "bhyve",
				AutoStart: provider.AutoStartConfig{Enabled: true, Priority: 5},
			},
			{
				ID:        "jailone",
				Provider:  "jail",
				AutoStart: provider.AutoStartConfig{Enabled: true, Priority: 10},
			},
		},
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := autostartListCmd("qemu", "VM", "VMs")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "No VMs configured for auto-start.") {
		t.Errorf("expected empty-list message when only non-qemu entries exist\nfull output:\n%s", output)
	}
	if strings.Contains(output, "bhyvevm") {
		t.Error("bhyve entry should not appear in qemu autostart list output")
	}
	if strings.Contains(output, "jailone") {
		t.Error("jail entry should not appear in qemu autostart list output")
	}
}

// TestAutostartListCommand_DisabledEntries verifies that disabled QEMU entries
// are not shown in the autostart list.
func TestAutostartListCommand_DisabledEntries(t *testing.T) {
	mock := &mockQemuClient{
		autoStartList: []client.AutoStartInfo{
			{
				ID:       "disabledvm",
				Provider: "qemu",
				AutoStart: provider.AutoStartConfig{
					Enabled:  false,
					Priority: 20,
					DelayMS:  0,
				},
			},
		},
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := autostartListCmd("qemu", "VM", "VMs")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "No VMs configured for auto-start.") {
		t.Errorf("expected empty-list message when only disabled entries exist\nfull output:\n%s", output)
	}
	if strings.Contains(output, "disabledvm") {
		t.Error("disabled VM should not appear in autostart list output")
	}
}

func TestAutostartListCommand_APIError(t *testing.T) {
	mock := &mockQemuClient{
		autoStartErr: errors.New("connection refused"),
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mock
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := autostartListCmd("qemu", "VM", "VMs")
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

// TestAutostartCommand_RegisteredUnderQemu verifies the autostart subcommand
// is present under the QEMU root command.
func TestAutostartCommand_RegisteredUnderQemu(t *testing.T) {
	root := NewQemuCommand()
	for _, sub := range root.Commands() {
		if sub.Use == "autostart" {
			return
		}
	}
	t.Fatal("'autostart' command not registered under qemu")
}

// ═══════════════════════════════════════════════════════════════════════════════
// expose list command tests
// ═══════════════════════════════════════════════════════════════════════════════

// TestExposeListCommand_SucceedsWithEmptyList verifies that the expose list
// command succeeds and prints the empty-list message when no rules exist.
func TestExposeListCommand_SucceedsWithEmptyList(t *testing.T) {
	oldClient := cmdutil.APIClient
	cmdutil.APIClient = &mockQemuClient{}
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := newQemuExposeListCommand()
	cmd.SetArgs([]string{"myvm"})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(buf.String(), "myvm") {
		t.Errorf("expected output to mention VM name, got: %s", buf.String())
	}
}

func TestExposeListCommand_NoArgs(t *testing.T) {
	cmd := newQemuExposeListCommand()
	cmd.SetArgs([]string{})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when no VM name is provided, got nil")
	}
}

// TestExposeListCommand_PrintsNoRulesMessage verifies that the empty-list message
// is shown when no forwarding rules are configured.
func TestExposeListCommand_PrintsNoRulesMessage(t *testing.T) {
	oldClient := cmdutil.APIClient
	cmdutil.APIClient = &mockQemuClient{}
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := newQemuExposeListCommand()
	cmd.SetArgs([]string{"uniquevm-42"})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "no port forwarding") {
		t.Errorf("expected 'no port forwarding' message, got: %s", out)
	}
}

// TestExposeCommand_RegisteredUnderQemu verifies the expose subcommand is
// present under the QEMU root command.
func TestExposeCommand_RegisteredUnderQemu(t *testing.T) {
	root := NewQemuCommand()
	for _, sub := range root.Commands() {
		if sub.Use == "expose" {
			return
		}
	}
	t.Fatal("'expose' command not registered under qemu")
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
