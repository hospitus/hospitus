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

// mockDestroyClient is a mock API client for testing destroy operations
type mockDestroyClient struct {
	baseMockClient // embed base mock for default method stubs
	deleteError    error
	gotForce       bool // records the force flag passed to DeleteInstance
	deleteCalled   bool
}

func (m *mockDestroyClient) DeleteInstance(ctx context.Context, id string, force bool) error {
	m.deleteCalled = true
	m.gotForce = force
	return m.deleteError
}

func (m *mockDestroyClient) CreateInstance(ctx context.Context, req client.CreateInstanceRequest) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockDestroyClient) ListInstances(ctx context.Context, filter client.ListInstancesFilter) ([]*datastore.Instance, error) {
	return nil, nil
}

func (m *mockDestroyClient) GetInstance(ctx context.Context, id string) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockDestroyClient) UpdateInstance(ctx context.Context, id string, req client.UpdateInstanceRequest) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockDestroyClient) StartInstance(ctx context.Context, id string, _ io.Writer) error {
	return nil
}

func (m *mockDestroyClient) StopInstance(ctx context.Context, id string, force bool) error {
	return nil
}

func (m *mockDestroyClient) RestartInstance(ctx context.Context, id string) error {
	return nil
}

func (m *mockDestroyClient) FetchImage(ctx context.Context, version string, _ io.Writer) error {
	return nil
}

func (m *mockDestroyClient) ListImages(ctx context.Context) ([]map[string]interface{}, error) {
	return nil, nil
}

func (m *mockDestroyClient) DeleteImage(ctx context.Context, imageName string) error {
	return nil
}

func (m *mockDestroyClient) ExportInstance(ctx context.Context, instanceID string, opts client.ExportOptions) (*client.ExportResult, error) {
	return nil, nil
}

func (m *mockDestroyClient) ImportInstance(ctx context.Context, opts client.ImportOptions) (*client.ImportResult, error) {
	return nil, nil
}

func (m *mockDestroyClient) ExposePort(ctx context.Context, instanceName string, req client.ExposePortRequest) (*client.ExposePortResult, error) {
	return nil, nil
}

func (m *mockDestroyClient) UnexposePort(ctx context.Context, instanceName string, hostPort int, protocol string) error {
	return nil
}

func (m *mockDestroyClient) ListExposedPorts(ctx context.Context, instanceName string) ([]client.PortMapping, error) {
	return nil, nil
}

func (m *mockDestroyClient) ExecCommand(ctx context.Context, instanceID string, req client.ExecRequest) (*client.ExecResult, error) {
	return nil, nil
}

func (m *mockDestroyClient) ExecCommandStream(ctx context.Context, instanceID string, req client.ExecRequest, stdout, stderr io.Writer) (int, error) {
	return 0, nil
}

func (m *mockDestroyClient) ListSnapshots(ctx context.Context, instanceID string) ([]client.SnapshotInfo, error) {
	return nil, nil
}

func (m *mockDestroyClient) CreateSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockDestroyClient) DeleteSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockDestroyClient) RestoreSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockDestroyClient) CloneInstance(ctx context.Context, instanceID string, opts client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

func (m *mockDestroyClient) CloneFromSnapshot(ctx context.Context, instanceID, snapshotName string, opts client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

func (m *mockDestroyClient) GetInstanceMetrics(ctx context.Context, instanceID string) (*client.InstanceMetrics, error) {
	return nil, nil
}

func (m *mockDestroyClient) GetInstanceHealth(ctx context.Context, instanceID string) (*client.InstanceHealth, error) {
	return nil, nil
}

func (m *mockDestroyClient) EjectMedia(ctx context.Context, instanceID string, deviceID string) error {
	return nil
}

func (m *mockDestroyClient) InsertMedia(ctx context.Context, instanceID string, spec provider.MediaSpec) error {
	return nil
}

func (m *mockDestroyClient) ListMedia(ctx context.Context, instanceID string) ([]provider.MediaInfo, error) {
	return nil, nil
}

func (m *mockDestroyClient) SetBootOrder(ctx context.Context, instanceID string, order provider.BootOrder) error {
	return nil
}

func (m *mockDestroyClient) GetBootOrder(ctx context.Context, instanceID string) (*provider.BootOrder, error) {
	return nil, nil
}

func (m *mockDestroyClient) ListAutoStart(ctx context.Context) ([]client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockDestroyClient) GetAutoStart(ctx context.Context, providerName, instanceID string) (*client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockDestroyClient) SetAutoStart(ctx context.Context, providerName, instanceID string, cfg provider.AutoStartConfig) (*client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockDestroyClient) DisableAutoStart(ctx context.Context, providerName, instanceID string) error {
	return nil
}

func (m *mockDestroyClient) ListBackups(_ context.Context, _ string) ([]client.BackupInfo, error) {
	return nil, nil
}

func (m *mockDestroyClient) GetBackup(_ context.Context, _ string) (*client.BackupInfo, error) {
	return nil, nil
}

func (m *mockDestroyClient) CreateBackup(_ context.Context, _, _ string) (*client.BackupInfo, error) {
	return nil, nil
}
func (m *mockDestroyClient) DeleteBackup(_ context.Context, _ string) error     { return nil }
func (m *mockDestroyClient) RestoreBackup(_ context.Context, _, _ string) error { return nil }
func (m *mockDestroyClient) VerifyBackup(_ context.Context, _ string) error     { return nil }
func (m *mockDestroyClient) SetBackupConfig(_ context.Context, _ string, _ client.BackupConfigRequest) error {
	return nil
}

func (m *mockDestroyClient) GetBackupConfig(_ context.Context, _ string) (map[string]interface{}, error) {
	return nil, nil
}

func (m *mockDestroyClient) ListJobs(_ context.Context, _ string) (*client.JobListResponse, error) {
	return &client.JobListResponse{}, nil
}

func (m *mockDestroyClient) GetJob(_ context.Context, _ string) (*job.Job, error) { return nil, nil }
func (m *mockDestroyClient) CancelJob(_ context.Context, _ string) error          { return nil }
func (m *mockDestroyClient) DeleteJob(_ context.Context, _ string) error          { return nil }
func (m *mockDestroyClient) JobStats(_ context.Context) (map[string]int, error)   { return nil, nil }

func (m *mockDestroyClient) ListPortForwardsVM(_ context.Context, _ string) ([]provider.PortForward, error) {
	return nil, nil
}

func (m *mockDestroyClient) AddPortForwardVM(_ context.Context, _ string, _ provider.PortForward) error {
	return nil
}

func (m *mockDestroyClient) RemovePortForwardVM(_ context.Context, _ string, _ string, _ int) error {
	return nil
}

func TestDestroyCommand_WithForce(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		mockClient     *mockDestroyClient
		expectedOutput []string
		expectError    bool
	}{
		{
			name: "destroy jail successfully with force",
			args: []string{"testjail"},
			mockClient: &mockDestroyClient{
				deleteError: nil,
			},
			expectedOutput: []string{
				"Jail destroyed: testjail",
			},
			expectError: false,
		},
		{
			name: "destroy fails with API error",
			args: []string{"failjail"},
			mockClient: &mockDestroyClient{
				deleteError: errors.New("connection refused"),
			},
			expectedOutput: []string{},
			expectError:    true,
		},
		{
			name: "destroy non-existent jail",
			args: []string{"nonexistent"},
			mockClient: &mockDestroyClient{
				deleteError: errors.New("instance not found"),
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
			cmd := newDestroyCommand()
			cmd.SetArgs(tt.args)

			// Set --yes to skip the prompt and --force so we can verify the
			// flag is propagated to DeleteInstance.
			if err := cmd.Flags().Set("yes", "true"); err != nil {
				t.Fatalf("failed to set yes flag: %v", err)
			}
			if err := cmd.Flags().Set("force", "true"); err != nil {
				t.Fatalf("failed to set force flag: %v", err)
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

			// Verify --force was forwarded to the delete call.
			if !tt.mockClient.deleteCalled {
				t.Errorf("expected DeleteInstance to be called")
			}
			if !tt.mockClient.gotForce {
				t.Errorf("expected DeleteInstance to be called with force=true")
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

func TestDestroyCommand_WithoutForce_ShowsWarning(t *testing.T) {
	mockClient := &mockDestroyClient{
		deleteError: nil,
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mockClient
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := newDestroyCommand()
	cmd.SetArgs([]string{"testjail"})

	// Answer "no" at the confirmation prompt: the command must print a warning
	// and abort without ever calling DeleteInstance.
	cmd.SetIn(strings.NewReader("no\n"))

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "WARNING: This will permanently destroy jail 'testjail'") {
		t.Errorf("expected destruction warning, got:\n%s", output)
	}
	if !strings.Contains(output, "Operation canceled") {
		t.Errorf("expected the operation to be canceled, got:\n%s", output)
	}
	if mockClient.deleteCalled {
		t.Errorf("DeleteInstance must not be called when the user declines")
	}
}

func TestDestroyCommand_NoArgs(t *testing.T) {
	cmd := newDestroyCommand()
	cmd.SetArgs([]string{})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	if err == nil {
		t.Errorf("expected error when no jail name provided, but got none")
	}
}
