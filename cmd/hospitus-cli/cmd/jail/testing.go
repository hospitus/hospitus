package jail

import (
	"context"
	"fmt"
	"io"

	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
)

// baseMockClient provides default stub implementations for all API methods
// Specific test mocks can embed this and override only the methods they need
type baseMockClient struct{}

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
