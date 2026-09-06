package jail

import (
	"context"
	"fmt"
	"io"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/job"
	"github.com/hospitus/hospitus/pkg/provider"
)

// baseMockClient provides default stub implementations for all API methods.
// Specific test mocks can embed this and override only the methods they need.
//
// In a _test.go file: as testing.go it was compiled into the hospitus binary,
// which shipped a set of mocks that every build carried and nothing used.
type baseMockClient struct{}

// Asserted here so a method added to the interface breaks this file rather
// than each mock that embeds it.
var _ cmdutil.APIClientInterface = (*baseMockClient)(nil)

func (m *baseMockClient) CreateInstance(ctx context.Context, req client.CreateInstanceRequest) (*datastore.Instance, error) {
	return nil, fmt.Errorf("CreateInstance not implemented in this mock")
}

func (m *baseMockClient) ListInstances(ctx context.Context, filter client.ListInstancesFilter) ([]*datastore.Instance, error) {
	return nil, fmt.Errorf("ListInstances not implemented in this mock")
}

func (m *baseMockClient) GetInstance(ctx context.Context, id string) (*datastore.Instance, error) {
	return nil, fmt.Errorf("GetInstance not implemented in this mock")
}

func (m *baseMockClient) StartInstance(ctx context.Context, id string, w io.Writer) error {
	return fmt.Errorf("StartInstance not implemented in this mock")
}

func (m *baseMockClient) StopInstance(ctx context.Context, id string, force bool) error {
	return fmt.Errorf("StopInstance not implemented in this mock")
}

func (m *baseMockClient) RestartInstance(ctx context.Context, id string) error {
	return fmt.Errorf("RestartInstance not implemented in this mock")
}

func (m *baseMockClient) DeleteInstance(ctx context.Context, id string, force bool) error {
	return fmt.Errorf("DeleteInstance not implemented in this mock")
}

func (m *baseMockClient) FetchImage(ctx context.Context, version string, w io.Writer) error {
	return fmt.Errorf("FetchImage not implemented in this mock")
}

func (m *baseMockClient) DeleteImage(ctx context.Context, imageName string) error {
	return fmt.Errorf("DeleteImage not implemented in this mock")
}

func (m *baseMockClient) ExportInstance(ctx context.Context, instanceID string, opts client.ExportOptions) (*client.ExportResult, error) {
	return nil, fmt.Errorf("ExportInstance not implemented in this mock")
}

func (m *baseMockClient) ImportInstance(ctx context.Context, opts client.ImportOptions) (*client.ImportResult, error) {
	return nil, fmt.Errorf("ImportInstance not implemented in this mock")
}

// Service management stubs
func (m *baseMockClient) ServiceAction(ctx context.Context, instanceID string, req client.ServiceActionRequest) (*client.ServiceActionResult, error) {
	return nil, fmt.Errorf("ServiceAction not implemented in this mock")
}

func (m *baseMockClient) GetServiceStatus(ctx context.Context, instanceID, serviceName string) (*client.ServiceStatus, error) {
	return nil, fmt.Errorf("GetServiceStatus not implemented in this mock")
}

func (m *baseMockClient) ListServices(ctx context.Context, instanceID, filter string) ([]client.ServiceInfo, error) {
	return nil, fmt.Errorf("ListServices not implemented in this mock")
}

// Volume management stubs
func (m *baseMockClient) CreateVolume(ctx context.Context, req client.CreateVolumeRequest) (*client.VolumeInfo, error) {
	return nil, fmt.Errorf("CreateVolume not implemented in this mock")
}

func (m *baseMockClient) DeleteVolume(ctx context.Context, volumeName string) error {
	return fmt.Errorf("DeleteVolume not implemented in this mock")
}

func (m *baseMockClient) ListVolumes(ctx context.Context) ([]client.VolumeInfo, error) {
	return nil, fmt.Errorf("ListVolumes not implemented in this mock")
}

func (m *baseMockClient) GetVolume(ctx context.Context, volumeName string) (*client.VolumeInfo, error) {
	return nil, fmt.Errorf("GetVolume not implemented in this mock")
}

func (m *baseMockClient) AttachVolume(ctx context.Context, instanceID, volumeName, mountPoint string) error {
	return fmt.Errorf("AttachVolume not implemented in this mock")
}

func (m *baseMockClient) DetachVolume(ctx context.Context, instanceID, volumeName string) error {
	return fmt.Errorf("DetachVolume not implemented in this mock")
}

// Network interface stubs
func (m *baseMockClient) AddNetworkInterface(ctx context.Context, instanceID string, req client.NetworkInterfaceRequest) (*client.NetworkInterfaceInfo, error) {
	return nil, fmt.Errorf("AddNetworkInterface not implemented in this mock")
}

func (m *baseMockClient) RemoveNetworkInterface(ctx context.Context, instanceID, interfaceName string) error {
	return fmt.Errorf("RemoveNetworkInterface not implemented in this mock")
}

func (m *baseMockClient) ListNetworkInterfaces(ctx context.Context, instanceID string) ([]client.NetworkInterfaceInfo, error) {
	return nil, fmt.Errorf("ListNetworkInterfaces not implemented in this mock")
}

// Lifecycle management stubs
func (m *baseMockClient) RenameInstance(ctx context.Context, id, newName string) (map[string]string, error) {
	return nil, fmt.Errorf("RenameInstance not implemented in this mock")
}

func (m *baseMockClient) UpgradeInstance(ctx context.Context, id, targetRelease string) (map[string]string, error) {
	return nil, fmt.Errorf("UpgradeInstance not implemented in this mock")
}

func (m *baseMockClient) PauseInstance(ctx context.Context, id string) error {
	return fmt.Errorf("PauseInstance not implemented in this mock")
}

func (m *baseMockClient) ResumeInstance(ctx context.Context, id string) error {
	return fmt.Errorf("ResumeInstance not implemented in this mock")
}

// Checkpoint management stubs
func (m *baseMockClient) ListCheckpoints(ctx context.Context, instanceID string) ([]client.CheckpointInfo, error) {
	return nil, fmt.Errorf("ListCheckpoints not implemented in this mock")
}

func (m *baseMockClient) CreateCheckpoint(ctx context.Context, instanceID, name string) error {
	return fmt.Errorf("CreateCheckpoint not implemented in this mock")
}

func (m *baseMockClient) RestoreCheckpoint(ctx context.Context, instanceID, name string) error {
	return fmt.Errorf("RestoreCheckpoint not implemented in this mock")
}

func (m *baseMockClient) DeleteCheckpoint(ctx context.Context, instanceID, name string) error {
	return fmt.Errorf("DeleteCheckpoint not implemented in this mock")
}

// The rest of cmdutil.APIClientInterface. Every one fails loudly: a test
// mock that answers a method it never meant to implement hides the call.

func (m *baseMockClient) AddPortForwardVM(ctx context.Context, instanceID string, pf provider.PortForward) error {
	return fmt.Errorf("AddPortForwardVM not implemented in this mock")
}

func (m *baseMockClient) CancelJob(ctx context.Context, id string) error {
	return fmt.Errorf("CancelJob not implemented in this mock")
}

func (m *baseMockClient) CloneFromSnapshot(ctx context.Context, instanceID, snapshotName string, opts client.CloneOptions) (*client.CloneResult, error) {
	return nil, fmt.Errorf("CloneFromSnapshot not implemented in this mock")
}

func (m *baseMockClient) CloneInstance(ctx context.Context, instanceID string, opts client.CloneOptions) (*client.CloneResult, error) {
	return nil, fmt.Errorf("CloneInstance not implemented in this mock")
}

func (m *baseMockClient) CreateBackup(ctx context.Context, instanceID, backupType string) (*client.BackupInfo, error) {
	return nil, fmt.Errorf("CreateBackup not implemented in this mock")
}

func (m *baseMockClient) CreateSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return fmt.Errorf("CreateSnapshot not implemented in this mock")
}

func (m *baseMockClient) DeleteBackup(ctx context.Context, backupID string) error {
	return fmt.Errorf("DeleteBackup not implemented in this mock")
}

func (m *baseMockClient) DeleteJob(ctx context.Context, id string) error {
	return fmt.Errorf("DeleteJob not implemented in this mock")
}

func (m *baseMockClient) DeleteSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return fmt.Errorf("DeleteSnapshot not implemented in this mock")
}

func (m *baseMockClient) DisableAutoStart(ctx context.Context, providerName, instanceID string) error {
	return fmt.Errorf("DisableAutoStart not implemented in this mock")
}

func (m *baseMockClient) EjectMedia(ctx context.Context, instanceID string, deviceID string) error {
	return fmt.Errorf("EjectMedia not implemented in this mock")
}

func (m *baseMockClient) ExecCommand(ctx context.Context, instanceID string, req client.ExecRequest) (*client.ExecResult, error) {
	return nil, fmt.Errorf("ExecCommand not implemented in this mock")
}

func (m *baseMockClient) ExecCommandStream(ctx context.Context, instanceID string, req client.ExecRequest, stdout, stderr io.Writer) (int, error) {
	return 0, fmt.Errorf("ExecCommandStream not implemented in this mock")
}

func (m *baseMockClient) ExposePort(ctx context.Context, instanceName string, req client.ExposePortRequest) (*client.ExposePortResult, error) {
	return nil, fmt.Errorf("ExposePort not implemented in this mock")
}

func (m *baseMockClient) GetAutoStart(ctx context.Context, providerName, instanceID string) (*client.AutoStartInfo, error) {
	return nil, fmt.Errorf("GetAutoStart not implemented in this mock")
}

func (m *baseMockClient) GetBackup(ctx context.Context, backupID string) (*client.BackupInfo, error) {
	return nil, fmt.Errorf("GetBackup not implemented in this mock")
}

func (m *baseMockClient) GetBackupConfig(ctx context.Context, instanceID string) (map[string]interface{}, error) {
	return nil, fmt.Errorf("GetBackupConfig not implemented in this mock")
}

func (m *baseMockClient) GetBootOrder(ctx context.Context, instanceID string) (*provider.BootOrder, error) {
	return nil, fmt.Errorf("GetBootOrder not implemented in this mock")
}

func (m *baseMockClient) GetInstanceHealth(ctx context.Context, instanceID string) (*client.InstanceHealth, error) {
	return nil, fmt.Errorf("GetInstanceHealth not implemented in this mock")
}

func (m *baseMockClient) GetInstanceMetrics(ctx context.Context, instanceID string) (*client.InstanceMetrics, error) {
	return nil, fmt.Errorf("GetInstanceMetrics not implemented in this mock")
}

func (m *baseMockClient) GetJob(ctx context.Context, id string) (*job.Job, error) {
	return nil, fmt.Errorf("GetJob not implemented in this mock")
}

func (m *baseMockClient) InsertMedia(ctx context.Context, instanceID string, spec provider.MediaSpec) error {
	return fmt.Errorf("InsertMedia not implemented in this mock")
}

func (m *baseMockClient) JobStats(ctx context.Context) (map[string]int, error) {
	return nil, fmt.Errorf("JobStats not implemented in this mock")
}

func (m *baseMockClient) ListAutoStart(ctx context.Context) ([]client.AutoStartInfo, error) {
	return nil, fmt.Errorf("ListAutoStart not implemented in this mock")
}

func (m *baseMockClient) ListBackups(ctx context.Context, instanceID string) ([]client.BackupInfo, error) {
	return nil, fmt.Errorf("ListBackups not implemented in this mock")
}

func (m *baseMockClient) ListExposedPorts(ctx context.Context, instanceName string) ([]client.PortMapping, error) {
	return nil, fmt.Errorf("ListExposedPorts not implemented in this mock")
}

func (m *baseMockClient) ListImages(ctx context.Context) ([]map[string]interface{}, error) {
	return nil, fmt.Errorf("ListImages not implemented in this mock")
}

func (m *baseMockClient) ListJobs(ctx context.Context, status string) (*client.JobListResponse, error) {
	return nil, fmt.Errorf("ListJobs not implemented in this mock")
}

func (m *baseMockClient) ListMedia(ctx context.Context, instanceID string) ([]provider.MediaInfo, error) {
	return nil, fmt.Errorf("ListMedia not implemented in this mock")
}

func (m *baseMockClient) ListPortForwardsVM(ctx context.Context, instanceID string) ([]provider.PortForward, error) {
	return nil, fmt.Errorf("ListPortForwardsVM not implemented in this mock")
}

func (m *baseMockClient) ListSnapshots(ctx context.Context, instanceID string) ([]client.SnapshotInfo, error) {
	return nil, fmt.Errorf("ListSnapshots not implemented in this mock")
}

func (m *baseMockClient) RemovePortForwardVM(ctx context.Context, instanceID string, protocol string, hostPort int) error {
	return fmt.Errorf("RemovePortForwardVM not implemented in this mock")
}

func (m *baseMockClient) RestoreBackup(ctx context.Context, backupID, targetInstanceID string) error {
	return fmt.Errorf("RestoreBackup not implemented in this mock")
}

func (m *baseMockClient) RestoreSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return fmt.Errorf("RestoreSnapshot not implemented in this mock")
}

func (m *baseMockClient) SetAutoStart(ctx context.Context, providerName, instanceID string, cfg provider.AutoStartConfig) (*client.AutoStartInfo, error) {
	return nil, fmt.Errorf("SetAutoStart not implemented in this mock")
}

func (m *baseMockClient) SetBackupConfig(ctx context.Context, instanceID string, req client.BackupConfigRequest) error {
	return fmt.Errorf("SetBackupConfig not implemented in this mock")
}

func (m *baseMockClient) SetBootOrder(ctx context.Context, instanceID string, order provider.BootOrder) error {
	return fmt.Errorf("SetBootOrder not implemented in this mock")
}

func (m *baseMockClient) UnexposePort(ctx context.Context, instanceName string, hostPort int, protocol string) error {
	return fmt.Errorf("UnexposePort not implemented in this mock")
}

func (m *baseMockClient) UpdateInstance(ctx context.Context, id string, req client.UpdateInstanceRequest) (*datastore.Instance, error) {
	return nil, fmt.Errorf("UpdateInstance not implemented in this mock")
}

func (m *baseMockClient) VerifyBackup(ctx context.Context, backupID string) error {
	return fmt.Errorf("VerifyBackup not implemented in this mock")
}
