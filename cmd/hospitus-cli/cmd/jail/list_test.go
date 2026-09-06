package jail

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/job"
	"github.com/hospitus/hospitus/pkg/provider"
)

// mockListClient is a mock API client for testing
type mockListClient struct {
	baseMockClient // embed base mock for default method stubs
	instances      []*datastore.Instance
	err            error
}

func (m *mockListClient) ListInstances(ctx context.Context, filter client.ListInstancesFilter) ([]*datastore.Instance, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.instances, nil
}

func (m *mockListClient) CreateInstance(ctx context.Context, req client.CreateInstanceRequest) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockListClient) GetInstance(ctx context.Context, id string) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockListClient) UpdateInstance(ctx context.Context, id string, req client.UpdateInstanceRequest) (*datastore.Instance, error) {
	return nil, nil
}

func (m *mockListClient) StartInstance(ctx context.Context, id string, _ io.Writer) error {
	return nil
}

func (m *mockListClient) StopInstance(ctx context.Context, id string, force bool) error {
	return nil
}

func (m *mockListClient) RestartInstance(ctx context.Context, id string) error {
	return nil
}

func (m *mockListClient) DeleteInstance(ctx context.Context, id string, force bool) error {
	return nil
}

func (m *mockListClient) FetchImage(ctx context.Context, version string, _ io.Writer) error {
	return nil
}

func (m *mockListClient) ListImages(ctx context.Context) ([]map[string]interface{}, error) {
	return nil, nil
}

func (m *mockListClient) DeleteImage(ctx context.Context, imageName string) error {
	return nil
}

func (m *mockListClient) ExportInstance(ctx context.Context, instanceID string, opts client.ExportOptions) (*client.ExportResult, error) {
	return nil, nil
}

func (m *mockListClient) ImportInstance(ctx context.Context, opts client.ImportOptions) (*client.ImportResult, error) {
	return nil, nil
}

func (m *mockListClient) ExposePort(ctx context.Context, instanceName string, req client.ExposePortRequest) (*client.ExposePortResult, error) {
	return nil, nil
}

func (m *mockListClient) UnexposePort(ctx context.Context, instanceName string, hostPort int, protocol string) error {
	return nil
}

func (m *mockListClient) ListExposedPorts(ctx context.Context, instanceName string) ([]client.PortMapping, error) {
	return nil, nil
}

func (m *mockListClient) ExecCommand(ctx context.Context, instanceID string, req client.ExecRequest) (*client.ExecResult, error) {
	return nil, nil
}

func (m *mockListClient) ExecCommandStream(ctx context.Context, instanceID string, req client.ExecRequest, stdout, stderr io.Writer) (int, error) {
	return 0, nil
}

func (m *mockListClient) ListSnapshots(ctx context.Context, instanceID string) ([]client.SnapshotInfo, error) {
	return nil, nil
}

func (m *mockListClient) CreateSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockListClient) DeleteSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockListClient) RestoreSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockListClient) CloneInstance(ctx context.Context, instanceID string, opts client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

func (m *mockListClient) CloneFromSnapshot(ctx context.Context, instanceID, snapshotName string, opts client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

func (m *mockListClient) GetInstanceMetrics(ctx context.Context, instanceID string) (*client.InstanceMetrics, error) {
	return nil, nil
}

func (m *mockListClient) GetInstanceHealth(ctx context.Context, instanceID string) (*client.InstanceHealth, error) {
	return nil, nil
}

func (m *mockListClient) EjectMedia(ctx context.Context, instanceID string, deviceID string) error {
	return nil
}

func (m *mockListClient) InsertMedia(ctx context.Context, instanceID string, spec provider.MediaSpec) error {
	return nil
}

func (m *mockListClient) ListMedia(ctx context.Context, instanceID string) ([]provider.MediaInfo, error) {
	return nil, nil
}

func (m *mockListClient) SetBootOrder(ctx context.Context, instanceID string, order provider.BootOrder) error {
	return nil
}

func (m *mockListClient) GetBootOrder(ctx context.Context, instanceID string) (*provider.BootOrder, error) {
	return nil, nil
}

func (m *mockListClient) ListAutoStart(ctx context.Context) ([]client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockListClient) GetAutoStart(ctx context.Context, providerName, instanceID string) (*client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockListClient) SetAutoStart(ctx context.Context, providerName, instanceID string, cfg provider.AutoStartConfig) (*client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockListClient) DisableAutoStart(ctx context.Context, providerName, instanceID string) error {
	return nil
}

func (m *mockListClient) ListBackups(_ context.Context, _ string) ([]client.BackupInfo, error) {
	return nil, nil
}

func (m *mockListClient) GetBackup(_ context.Context, _ string) (*client.BackupInfo, error) {
	return nil, nil
}

func (m *mockListClient) CreateBackup(_ context.Context, _, _ string) (*client.BackupInfo, error) {
	return nil, nil
}
func (m *mockListClient) DeleteBackup(_ context.Context, _ string) error     { return nil }
func (m *mockListClient) RestoreBackup(_ context.Context, _, _ string) error { return nil }
func (m *mockListClient) VerifyBackup(_ context.Context, _ string) error     { return nil }
func (m *mockListClient) SetBackupConfig(_ context.Context, _ string, _ client.BackupConfigRequest) error {
	return nil
}

func (m *mockListClient) GetBackupConfig(_ context.Context, _ string) (map[string]interface{}, error) {
	return nil, nil
}

func (m *mockListClient) ListJobs(_ context.Context, _ string) (*client.JobListResponse, error) {
	return &client.JobListResponse{}, nil
}
func (m *mockListClient) GetJob(_ context.Context, _ string) (*job.Job, error) { return nil, nil }
func (m *mockListClient) CancelJob(_ context.Context, _ string) error          { return nil }
func (m *mockListClient) DeleteJob(_ context.Context, _ string) error          { return nil }
func (m *mockListClient) JobStats(_ context.Context) (map[string]int, error)   { return nil, nil }
func (m *mockListClient) ListPortForwardsVM(_ context.Context, _ string) ([]provider.PortForward, error) {
	return nil, nil
}

func (m *mockListClient) AddPortForwardVM(_ context.Context, _ string, _ provider.PortForward) error {
	return nil
}

func (m *mockListClient) RemovePortForwardVM(_ context.Context, _ string, _ string, _ int) error {
	return nil
}

func TestListCommand(t *testing.T) {
	tests := []struct {
		name           string
		instances      []*datastore.Instance
		expectedOutput []string
		expectError    bool
	}{
		{
			name: "list multiple jails",
			instances: []*datastore.Instance{
				{
					ID:       "jail1",
					Name:     "jail1",
					Provider: "jail",
					State:    provider.StateRunning,
					Spec: provider.InstanceSpec{
						Name:     "jail1",
						CPUs:     2,
						MemoryMB: 1024,
					},
					CreatedAt: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
				},
				{
					ID:       "jail2",
					Name:     "jail2",
					Provider: "jail",
					State:    provider.StateStopped,
					Spec: provider.InstanceSpec{
						Name:     "jail2",
						CPUs:     1,
						MemoryMB: 512,
					},
					CreatedAt: time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC),
				},
			},
			expectedOutput: []string{
				"ID",
				"NAME",
				"PROVIDER",
				"STATE",
				"jail1",
				"jail2",
				"running",
				"stopped",
			},
			expectError: false,
		},
		{
			name:           "no jails found",
			instances:      []*datastore.Instance{},
			expectedOutput: []string{"No instances found"},
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock client
			mockClient := &mockListClient{
				instances: tt.instances,
			}

			// Override the global API client
			oldClient := cmdutil.APIClient
			cmdutil.APIClient = mockClient
			defer func() { cmdutil.APIClient = oldClient }()

			// Create command
			cmd := newListCommand()

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

func TestListCommand_JSON_Output(t *testing.T) {
	mockClient := &mockListClient{
		instances: []*datastore.Instance{
			{
				ID:       "jail1",
				Name:     "jail1",
				Provider: "jail",
				State:    provider.StateRunning,
				Spec: provider.InstanceSpec{
					Name:     "jail1",
					CPUs:     2,
					MemoryMB: 1024,
				},
				CreatedAt: time.Now(),
			},
		},
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mockClient
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := newListCommand()
	cmd.Flags().Set("output", "json")

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	output := buf.String()
	// Verify JSON output contains expected fields
	expectedFields := []string{"\"id\"", "\"name\"", "\"provider\"", "\"state\""}
	for _, field := range expectedFields {
		if !strings.Contains(output, field) {
			t.Errorf("expected JSON output to contain %s, but got:\n%s", field, output)
		}
	}
}
