package jail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/job"
	"github.com/hospitus/hospitus/pkg/provider"
)

// mockStopClient is a mock API client for testing stop operations
type mockStopClient struct {
	baseMockClient // embed base mock for default method stubs
	stopError      error
	gotForce       bool // records the force flag passed to StopInstance
}

func (m *mockStopClient) StopInstance(ctx context.Context, id string, force bool) error {
	m.gotForce = force
	return m.stopError
}

func (m *mockStopClient) CreateInstance(ctx context.Context, req client.CreateInstanceRequest) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockStopClient) ListInstances(ctx context.Context, filter client.ListInstancesFilter) ([]*datastore.Instance, error) {
	return nil, nil
}

func (m *mockStopClient) GetInstance(ctx context.Context, id string) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockStopClient) UpdateInstance(ctx context.Context, id string, req client.UpdateInstanceRequest) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockStopClient) StartInstance(ctx context.Context, id string, _ io.Writer) error {
	return nil
}

func (m *mockStopClient) RestartInstance(ctx context.Context, id string) error {
	return nil
}

func (m *mockStopClient) DeleteInstance(ctx context.Context, id string, force bool) error {
	return nil
}

func (m *mockStopClient) FetchImage(ctx context.Context, version string, _ io.Writer) error {
	return nil
}

func (m *mockStopClient) ListImages(ctx context.Context) ([]map[string]interface{}, error) {
	return nil, nil
}

func (m *mockStopClient) DeleteImage(ctx context.Context, imageName string) error {
	return nil
}

func (m *mockStopClient) ExportInstance(ctx context.Context, instanceID string, opts client.ExportOptions) (*client.ExportResult, error) {
	return nil, nil
}

func (m *mockStopClient) ImportInstance(ctx context.Context, opts client.ImportOptions) (*client.ImportResult, error) {
	return nil, nil
}

func (m *mockStopClient) ExposePort(ctx context.Context, instanceName string, req client.ExposePortRequest) (*client.ExposePortResult, error) {
	return nil, nil
}

func (m *mockStopClient) UnexposePort(ctx context.Context, instanceName string, hostPort int, protocol string) error {
	return nil
}

func (m *mockStopClient) ListExposedPorts(ctx context.Context, instanceName string) ([]client.PortMapping, error) {
	return nil, nil
}

func (m *mockStopClient) ExecCommand(ctx context.Context, instanceID string, req client.ExecRequest) (*client.ExecResult, error) {
	return nil, nil
}

func (m *mockStopClient) ExecCommandStream(ctx context.Context, instanceID string, req client.ExecRequest, stdout, stderr io.Writer) (int, error) {
	return 0, nil
}

func (m *mockStopClient) ListSnapshots(ctx context.Context, instanceID string) ([]client.SnapshotInfo, error) {
	return nil, nil
}

func (m *mockStopClient) CreateSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockStopClient) DeleteSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockStopClient) RestoreSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockStopClient) CloneInstance(ctx context.Context, instanceID string, opts client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

func (m *mockStopClient) CloneFromSnapshot(ctx context.Context, instanceID, snapshotName string, opts client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

func (m *mockStopClient) GetInstanceMetrics(ctx context.Context, instanceID string) (*client.InstanceMetrics, error) {
	return nil, nil
}

func (m *mockStopClient) GetInstanceHealth(ctx context.Context, instanceID string) (*client.InstanceHealth, error) {
	return nil, nil
}

func (m *mockStopClient) EjectMedia(ctx context.Context, instanceID string, deviceID string) error {
	return nil
}

func (m *mockStopClient) InsertMedia(ctx context.Context, instanceID string, spec provider.MediaSpec) error {
	return nil
}

func (m *mockStopClient) ListMedia(ctx context.Context, instanceID string) ([]provider.MediaInfo, error) {
	return nil, nil
}

func (m *mockStopClient) SetBootOrder(ctx context.Context, instanceID string, order provider.BootOrder) error {
	return nil
}

func (m *mockStopClient) GetBootOrder(ctx context.Context, instanceID string) (*provider.BootOrder, error) {
	return nil, nil
}

func (m *mockStopClient) ListAutoStart(ctx context.Context) ([]client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockStopClient) GetAutoStart(ctx context.Context, providerName, instanceID string) (*client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockStopClient) SetAutoStart(ctx context.Context, providerName, instanceID string, cfg provider.AutoStartConfig) (*client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockStopClient) DisableAutoStart(ctx context.Context, providerName, instanceID string) error {
	return nil
}

func (m *mockStopClient) ListBackups(_ context.Context, _ string) ([]client.BackupInfo, error) {
	return nil, nil
}

func (m *mockStopClient) GetBackup(_ context.Context, _ string) (*client.BackupInfo, error) {
	return nil, nil
}

func (m *mockStopClient) CreateBackup(_ context.Context, _, _ string) (*client.BackupInfo, error) {
	return nil, nil
}
func (m *mockStopClient) DeleteBackup(_ context.Context, _ string) error     { return nil }
func (m *mockStopClient) RestoreBackup(_ context.Context, _, _ string) error { return nil }
func (m *mockStopClient) VerifyBackup(_ context.Context, _ string) error     { return nil }
func (m *mockStopClient) SetBackupConfig(_ context.Context, _ string, _ client.BackupConfigRequest) error {
	return nil
}

func (m *mockStopClient) GetBackupConfig(_ context.Context, _ string) (map[string]interface{}, error) {
	return nil, nil
}

func (m *mockStopClient) ListJobs(_ context.Context, _ string) (*client.JobListResponse, error) {
	return &client.JobListResponse{}, nil
}
func (m *mockStopClient) GetJob(_ context.Context, _ string) (*job.Job, error) { return nil, nil }
func (m *mockStopClient) CancelJob(_ context.Context, _ string) error          { return nil }
func (m *mockStopClient) DeleteJob(_ context.Context, _ string) error          { return nil }
func (m *mockStopClient) JobStats(_ context.Context) (map[string]int, error)   { return nil, nil }
func (m *mockStopClient) ListPortForwardsVM(_ context.Context, _ string) ([]provider.PortForward, error) {
	return nil, nil
}

func (m *mockStopClient) AddPortForwardVM(_ context.Context, _ string, _ provider.PortForward) error {
	return nil
}

func (m *mockStopClient) RemovePortForwardVM(_ context.Context, _ string, _ string, _ int) error {
	return nil
}

func TestStopCommand(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		flags          map[string]string
		mockClient     *mockStopClient
		expectedOutput []string
		expectError    bool
	}{
		{
			name: "stop jail successfully",
			args: []string{"testjail"},
			mockClient: &mockStopClient{
				stopError: nil,
			},
			expectedOutput: []string{
				"Jail stopped: testjail",
			},
			expectError: false,
		},
		{
			name: "stop jail with force flag",
			args: []string{"testjail"},
			flags: map[string]string{
				"force": "true",
			},
			mockClient: &mockStopClient{
				stopError: nil,
			},
			expectedOutput: []string{
				"Jail stopped: testjail",
			},
			expectError: false,
		},
		{
			name: "stop fails with API error",
			args: []string{"failjail"},
			mockClient: &mockStopClient{
				stopError: errors.New("connection refused"),
			},
			expectedOutput: []string{},
			expectError:    true,
		},
		{
			name: "stop non-existent jail",
			args: []string{"nonexistent"},
			mockClient: &mockStopClient{
				stopError: errors.New("instance not found"),
			},
			expectedOutput: []string{},
			expectError:    true,
		},
		{
			name: "stop already stopped jail",
			args: []string{"stoppedjail"},
			mockClient: &mockStopClient{
				stopError: errors.New("instance is already stopped"),
			},
			expectedOutput: []string{},
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Override the global API client
			oldClient := cmdutil.APIClient
			cmdutil.APIClient = tt.mockClient
			defer func() { cmdutil.APIClient = oldClient }()

			// Create command
			cmd := newStopCommand()
			cmd.SetArgs(tt.args)

			// Set flags
			for key, value := range tt.flags {
				if err := cmd.Flags().Set(key, value); err != nil {
					t.Fatalf("failed to set flag %s=%s: %v", key, value, err)
				}
			}

			// Capture output
			buf := new(bytes.Buffer)
			cmd.SetOut(buf)
			cmd.SetErr(buf)

			// Execute command
			err := cmd.Execute()

			// Check error expectation
			if tt.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			// When --force is requested, verify it was forwarded to StopInstance.
			if tt.flags["force"] == "true" && !tt.expectError && !tt.mockClient.gotForce {
				t.Errorf("expected StopInstance to be called with force=true")
			}

			// Check output
			output := buf.String()
			for _, expected := range tt.expectedOutput {
				if !strings.Contains(output, expected) {
					t.Errorf("expected output to contain %q, but got:\n%s", expected, output)
				}
			}
		})
	}
}

func TestStopCommand_NoArgs(t *testing.T) {
	cmd := newStopCommand()
	cmd.SetArgs([]string{})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	if err == nil {
		t.Errorf("expected error when no jail name provided, but got none")
	}
}
