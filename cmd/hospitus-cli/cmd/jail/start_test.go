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

// mockStartClient is a mock API client for testing start operations
type mockStartClient struct {
	baseMockClient // embed base mock for default method stubs
	startError     error
}

func (m *mockStartClient) StartInstance(ctx context.Context, id string, _ io.Writer) error {
	return m.startError
}

func (m *mockStartClient) CreateInstance(ctx context.Context, req client.CreateInstanceRequest) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockStartClient) ListInstances(ctx context.Context, filter client.ListInstancesFilter) ([]*datastore.Instance, error) {
	return nil, nil
}

// A real instance, provider included: returning nil made the resolver report
// no provider, which a jail command must refuse rather than act on.
func (m *mockStartClient) GetInstance(ctx context.Context, id string) (*datastore.Instance, error) {
	return &datastore.Instance{ID: id, Name: id, Provider: "jail"}, nil
}

func (m *mockStartClient) UpdateInstance(ctx context.Context, id string, req client.UpdateInstanceRequest) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockStartClient) StopInstance(ctx context.Context, id string, force bool) error {
	return nil
}

func (m *mockStartClient) RestartInstance(ctx context.Context, id string) error {
	return nil
}

func (m *mockStartClient) DeleteInstance(ctx context.Context, id string, force bool) error {
	return nil
}

func (m *mockStartClient) FetchImage(ctx context.Context, version string, _ io.Writer) error {
	return nil
}

func (m *mockStartClient) ListImages(ctx context.Context) ([]map[string]interface{}, error) {
	return nil, nil
}

func (m *mockStartClient) DeleteImage(ctx context.Context, imageName string) error {
	return nil
}

func (m *mockStartClient) ExportInstance(ctx context.Context, instanceID string, opts client.ExportOptions) (*client.ExportResult, error) {
	return nil, nil
}

func (m *mockStartClient) ImportInstance(ctx context.Context, opts client.ImportOptions) (*client.ImportResult, error) {
	return nil, nil
}

func (m *mockStartClient) ExposePort(ctx context.Context, instanceName string, req client.ExposePortRequest) (*client.ExposePortResult, error) {
	return nil, nil
}

func (m *mockStartClient) UnexposePort(ctx context.Context, instanceName string, hostPort int, protocol string) error {
	return nil
}

func (m *mockStartClient) ListExposedPorts(ctx context.Context, instanceName string) ([]client.PortMapping, error) {
	return nil, nil
}

func (m *mockStartClient) ExecCommand(ctx context.Context, instanceID string, req client.ExecRequest) (*client.ExecResult, error) {
	return nil, nil
}

func (m *mockStartClient) ExecCommandStream(ctx context.Context, instanceID string, req client.ExecRequest, stdout, stderr io.Writer) (int, error) {
	return 0, nil
}

func (m *mockStartClient) ListSnapshots(ctx context.Context, instanceID string) ([]client.SnapshotInfo, error) {
	return nil, nil
}

func (m *mockStartClient) CreateSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockStartClient) DeleteSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockStartClient) RestoreSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockStartClient) CloneInstance(ctx context.Context, instanceID string, opts client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

func (m *mockStartClient) CloneFromSnapshot(ctx context.Context, instanceID, snapshotName string, opts client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

func (m *mockStartClient) GetInstanceMetrics(ctx context.Context, instanceID string) (*client.InstanceMetrics, error) {
	return nil, nil
}

func (m *mockStartClient) GetInstanceHealth(ctx context.Context, instanceID string) (*client.InstanceHealth, error) {
	return nil, nil
}

func (m *mockStartClient) EjectMedia(ctx context.Context, instanceID string, deviceID string) error {
	return nil
}

func (m *mockStartClient) InsertMedia(ctx context.Context, instanceID string, spec provider.MediaSpec) error {
	return nil
}

func (m *mockStartClient) ListMedia(ctx context.Context, instanceID string) ([]provider.MediaInfo, error) {
	return nil, nil
}

func (m *mockStartClient) SetBootOrder(ctx context.Context, instanceID string, order provider.BootOrder) error {
	return nil
}

func (m *mockStartClient) GetBootOrder(ctx context.Context, instanceID string) (*provider.BootOrder, error) {
	return nil, nil
}

func (m *mockStartClient) ListAutoStart(ctx context.Context) ([]client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockStartClient) GetAutoStart(ctx context.Context, providerName, instanceID string) (*client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockStartClient) SetAutoStart(ctx context.Context, providerName, instanceID string, cfg provider.AutoStartConfig) (*client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockStartClient) DisableAutoStart(ctx context.Context, providerName, instanceID string) error {
	return nil
}

func (m *mockStartClient) ListBackups(_ context.Context, _ string) ([]client.BackupInfo, error) {
	return nil, nil
}

func (m *mockStartClient) GetBackup(_ context.Context, _ string) (*client.BackupInfo, error) {
	return nil, nil
}

func (m *mockStartClient) CreateBackup(_ context.Context, _, _ string) (*client.BackupInfo, error) {
	return nil, nil
}
func (m *mockStartClient) DeleteBackup(_ context.Context, _ string) error     { return nil }
func (m *mockStartClient) RestoreBackup(_ context.Context, _, _ string) error { return nil }
func (m *mockStartClient) VerifyBackup(_ context.Context, _ string) error     { return nil }
func (m *mockStartClient) SetBackupConfig(_ context.Context, _ string, _ client.BackupConfigRequest) error {
	return nil
}

func (m *mockStartClient) GetBackupConfig(_ context.Context, _ string) (map[string]interface{}, error) {
	return nil, nil
}

func (m *mockStartClient) ListJobs(_ context.Context, _ string) (*client.JobListResponse, error) {
	return &client.JobListResponse{}, nil
}
func (m *mockStartClient) GetJob(_ context.Context, _ string) (*job.Job, error) { return nil, nil }
func (m *mockStartClient) CancelJob(_ context.Context, _ string) error          { return nil }
func (m *mockStartClient) DeleteJob(_ context.Context, _ string) error          { return nil }
func (m *mockStartClient) JobStats(_ context.Context) (map[string]int, error)   { return nil, nil }
func (m *mockStartClient) ListPortForwardsVM(_ context.Context, _ string) ([]provider.PortForward, error) {
	return nil, nil
}

func (m *mockStartClient) AddPortForwardVM(_ context.Context, _ string, _ provider.PortForward) error {
	return nil
}

func (m *mockStartClient) RemovePortForwardVM(_ context.Context, _ string, _ string, _ int) error {
	return nil
}

func TestStartCommand(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		mockClient     *mockStartClient
		expectedOutput []string
		expectError    bool
	}{
		{
			name: "start jail successfully",
			args: []string{"testjail"},
			mockClient: &mockStartClient{
				startError: nil,
			},
			expectedOutput: []string{
				"Jail started: testjail",
			},
			expectError: false,
		},
		{
			name: "start fails with API error",
			args: []string{"failjail"},
			mockClient: &mockStartClient{
				startError: errors.New("connection refused"),
			},
			expectedOutput: []string{},
			expectError:    true,
		},
		{
			name: "start non-existent jail",
			args: []string{"nonexistent"},
			mockClient: &mockStartClient{
				startError: errors.New("instance not found"),
			},
			expectedOutput: []string{},
			expectError:    true,
		},
		{
			name: "start already running jail",
			args: []string{"runningjail"},
			mockClient: &mockStartClient{
				startError: errors.New("instance is already running"),
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
			cmd := newStartCommand()
			cmd.SetArgs(tt.args)

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

func TestStartCommand_NoArgs(t *testing.T) {
	cmd := newStartCommand()
	cmd.SetArgs([]string{})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	if err == nil {
		t.Errorf("expected error when no jail name provided, but got none")
	}
}
