package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/hospitus/hospitus/pkg/cloudinit"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Converter converts manifest types to provider InstanceSpec.
//
// cloudInitRoot is the one directory a manifest may name in
// cloud_init.user_data_file. The daemon resolves that path as root while
// converting a stack posted to the API, so an unconfined path would read any
// host file into a guest. An empty root refuses the field outright, which is
// what a converter built without one gets.
type Converter struct {
	cloudInitRoot string
}

// NewConverter creates a new converter that refuses user_data_file.
func NewConverter() *Converter {
	return &Converter{}
}

// NewConverterWithCloudInitRoot creates a converter that accepts a
// user_data_file resolving inside root.
func NewConverterWithCloudInitRoot(root string) *Converter {
	return &Converter{cloudInitRoot: root}
}

// ToInstanceSpec is a convenience function that creates a converter and converts a workload
func ToInstanceSpec(m *WorkloadManifest) (*provider.InstanceSpec, error) {
	return NewConverter().ToInstanceSpec(m)
}

// ToInstanceSpec converts a WorkloadManifest to a provider.InstanceSpec
func (c *Converter) ToInstanceSpec(m *WorkloadManifest) (*provider.InstanceSpec, error) {
	spec := &provider.InstanceSpec{
		Name:        m.Workload.Name,
		Description: m.Workload.Description,
		Labels:      m.Workload.Labels,
		Annotations: m.Workload.Annotations,
	}

	// Convert resources
	spec.CPUs = m.Resources.CPU

	if m.Resources.Memory != "" {
		memMB, err := ParseMemoryToMB(m.Resources.Memory)
		if err != nil {
			return nil, fmt.Errorf("invalid memory size: %w", err)
		}
		spec.MemoryMB = memMB
	}

	// Determine if this is a Linux jail (no image source needed)
	isLinuxJail := false
	if m.Provider.Type == ProviderTypeJail {
		if override, ok := m.ProviderOverrides[ProviderTypeJail]; ok && override.OSType == "linux" {
			isLinuxJail = true
		}
	}

	// Convert image (skip for Linux jails — debootstrap handles base system)
	if !isLinuxJail {
		imageType, reference, err := ParseImageSource(m.Image.Source)
		if err != nil {
			return nil, fmt.Errorf("invalid image source: %w", err)
		}
		spec.Image = c.convertImageReference(imageType, reference)
		spec.Arch = m.Image.Arch

		// Set OS type based on image type
		switch imageType {
		case ImageTypeFreeBSD:
			spec.OSType = "freebsd"
			spec.OSVersion = reference
		case ImageTypeOCI:
			spec.OSType = "oci"
		case ImageTypeCloud:
			// Parse cloud image name (e.g., "ubuntu-24.04")
			spec.OSType, spec.OSVersion = c.parseCloudImage(reference)
		case ImageTypeISO:
			spec.OSType = "iso"
		}
	} else {
		spec.Arch = m.Image.Arch
	}

	// Convert networks
	for i := range m.Networks {
		net := m.Networks[i]
		netSpec, err := c.convertNetwork(net, i)
		if err != nil {
			return nil, fmt.Errorf("invalid network[%d]: %w", i, err)
		}
		spec.Networks = append(spec.Networks, netSpec)
	}

	disks, err := c.convertStorage(m.Storage)
	if err != nil {
		return nil, fmt.Errorf("invalid storage: %w", err)
	}
	spec.Disks = disks

	// Convert provider-specific config
	spec.ProviderConfig = c.convertProviderConfig(m, spec)

	// Generate cloud-init for VM providers (bhyve, qemu) if needed
	if err := c.generateCloudInitForVM(m, spec); err != nil {
		return nil, err
	}

	return spec, nil
}

// ToInstanceSpecs converts a StackManifest to multiple provider.InstanceSpec
func (c *Converter) ToInstanceSpecs(m *StackManifest) ([]*provider.InstanceSpec, error) {
	var specs []*provider.InstanceSpec

	for i := range m.Instances {
		inst := m.Instances[i]
		spec, err := c.instanceConfigToSpec(&inst)
		if err != nil {
			return nil, fmt.Errorf("instance[%d] (%s): %w", i, inst.Name, err)
		}
		specs = append(specs, spec)
	}

	return specs, nil
}

// instanceConfigToSpec converts a single InstanceConfig to InstanceSpec
func (c *Converter) instanceConfigToSpec(inst *InstanceConfig) (*provider.InstanceSpec, error) {
	spec := &provider.InstanceSpec{
		Name: inst.Name,
	}

	// Convert resources
	spec.CPUs = inst.Resources.CPU

	if inst.Resources.Memory != "" {
		memMB, err := ParseMemoryToMB(inst.Resources.Memory)
		if err != nil {
			return nil, fmt.Errorf("invalid memory size: %w", err)
		}
		spec.MemoryMB = memMB
	}

	// Convert image
	imageType, reference, err := ParseImageSource(inst.Image.Source)
	if err != nil {
		return nil, fmt.Errorf("invalid image source: %w", err)
	}
	spec.Image = c.convertImageReference(imageType, reference)
	spec.Arch = inst.Image.Arch

	// Set OS type based on image type
	switch imageType {
	case ImageTypeFreeBSD:
		spec.OSType = "freebsd"
		spec.OSVersion = reference
	case ImageTypeOCI:
		spec.OSType = "oci"
	case ImageTypeCloud:
		spec.OSType, spec.OSVersion = c.parseCloudImage(reference)
	case ImageTypeISO:
		spec.OSType = "iso"
	}

	// Convert networks
	for i := range inst.Networks {
		net := inst.Networks[i]
		netSpec, err := c.convertNetwork(net, i)
		if err != nil {
			return nil, fmt.Errorf("invalid network[%d]: %w", i, err)
		}
		spec.Networks = append(spec.Networks, netSpec)
	}

	disks, err := c.convertStorage(inst.Storage)
	if err != nil {
		return nil, fmt.Errorf("invalid storage: %w", err)
	}
	spec.Disks = disks

	// Convert provider-specific config for stack instance
	config := make(map[string]interface{})

	// Get provider-specific overrides
	if inst.ProviderOverrides != nil {
		if override, ok := inst.ProviderOverrides[inst.Provider]; ok {
			// Copy parameters
			if override.Parameters != nil {
				for k, v := range override.Parameters {
					config[k] = v
				}
			}

			// OS overrides
			if override.OSType != "" {
				spec.OSType = override.OSType
			}
			if override.OSVersion != "" {
				spec.OSVersion = override.OSVersion
			}

			// Extra fields for bhyve/QEMU/etc if they are in stack
			if override.Bootloader != "" {
				config["bootloader"] = override.Bootloader
			}
			if override.ConsoleType != "" {
				config["console_type"] = override.ConsoleType
			}
			if len(override.Passthrough) > 0 {
				config["passthrough"] = override.Passthrough
			}
			if override.VNC != "" {
				config["vnc"] = override.VNC
			}
			if override.DiskDriver != "" {
				config["disk_driver"] = override.DiskDriver
			}
			if override.CPU != "" {
				config["cpu"] = override.CPU
			}
			if len(override.Command) > 0 {
				config["command"] = override.Command
			}
		}
	}

	// Add volume mounts to config
	var mounts []map[string]interface{}
	for _, vol := range inst.Storage.Volumes {
		if vol.HostPath != "" {
			mount := map[string]interface{}{
				"name":       vol.Name,
				"host_path":  vol.HostPath,
				"mount_path": vol.MountPath,
				"read_only":  vol.ReadOnly,
			}
			mounts = append(mounts, mount)
		}
	}
	if len(mounts) > 0 {
		config["mounts"] = mounts
	}

	// Add ZFS config
	for _, vol := range inst.Storage.Volumes {
		if vol.ZFS == nil {
			continue
		}
		zfsConfig := map[string]interface{}{}
		if vol.ZFS.Compression != "" {
			zfsConfig["compression"] = vol.ZFS.Compression
		}
		if vol.ZFS.Quota != "" {
			zfsConfig["quota"] = vol.ZFS.Quota
		}
		if len(zfsConfig) > 0 {
			config["zfs_"+vol.Name] = zfsConfig
		}
	}

	// Add port forwarding
	var ports []map[string]interface{}
	for i := range inst.Networks {
		net := inst.Networks[i]
		for _, port := range net.Ports {
			p := map[string]interface{}{
				"host":      port.Host,
				"container": port.Container,
				"protocol":  port.Protocol,
			}
			ports = append(ports, p)
		}
	}
	if len(ports) > 0 {
		config["port_forwards"] = ports
	}

	// Environment variables, as a single workload can declare them.
	//
	// A stack instance declares [environment] like any workload: a container
	// without it cannot be given so much as a database password.
	if len(inst.Environment) > 0 {
		config["environment"] = inst.Environment
	}

	// Add autostart config
	if inst.Lifecycle.Autostart != nil && inst.Lifecycle.Autostart.Enabled {
		config["autostart"] = map[string]interface{}{
			"enabled":  inst.Lifecycle.Autostart.Enabled,
			"priority": inst.Lifecycle.Autostart.Priority,
			"delay":    inst.Lifecycle.Autostart.Delay,
		}
	}

	// Add hooks
	if inst.Lifecycle.Hooks != nil {
		hooks := map[string]interface{}{}
		if inst.Lifecycle.Hooks.PreStart != "" {
			hooks["pre_start"] = inst.Lifecycle.Hooks.PreStart
		}
		if inst.Lifecycle.Hooks.PostStart != "" {
			hooks["post_start"] = inst.Lifecycle.Hooks.PostStart
		}
		if inst.Lifecycle.Hooks.PreStop != "" {
			hooks["pre_stop"] = inst.Lifecycle.Hooks.PreStop
		}
		// Add post_create hooks (structured format with commands)
		if len(inst.Lifecycle.Hooks.PostCreate) > 0 {
			var postCreateHooks []map[string]interface{}
			for _, hook := range inst.Lifecycle.Hooks.PostCreate {
				hookMap := map[string]interface{}{
					"type":     hook.Type,
					"commands": hook.Commands,
				}
				if hook.OnFailure != "" {
					hookMap["on_failure"] = hook.OnFailure
				}
				postCreateHooks = append(postCreateHooks, hookMap)
			}
			hooks["post_create"] = postCreateHooks
		}
		if len(hooks) > 0 {
			config["hooks"] = hooks
		}
	}

	// Graceful-stop timeout and health check.
	addLifecycleExtras(config, inst.Lifecycle)

	spec.ProviderConfig = config

	// A stack instance declares cloud-init the same way a workload does, and a
	// VM that is not provisioned is not the VM the manifest describes.
	if inst.CloudInit != nil && inst.CloudInit.Enabled != nil && *inst.CloudInit.Enabled {
		if !providerAppliesCloudInit(inst.Provider) {
			return nil, errCloudInitUnsupported(inst.Provider)
		}
		if err := c.generateFromCloudInitSpec(inst.CloudInit, inst.Networks, spec); err != nil {
			return nil, fmt.Errorf("invalid cloud-init: %w", err)
		}
	}

	return spec, nil
}

// InstanceConfigToSpec converts one stack instance into a provider spec.
//
// It is the only conversion of a stack instance. Three of them existed — this
// one, the daemon's and the CLI's — for a single manifest contract, and they
// had drifted: what a stack deployed depended on which entry point was used.
func InstanceConfigToSpec(inst *InstanceConfig, cloudInitRoot string) (*provider.InstanceSpec, error) {
	spec, err := NewConverterWithCloudInitRoot(cloudInitRoot).instanceConfigToSpec(inst)
	if err != nil {
		return nil, err
	}
	// The instances endpoint validates every spec it builds; a stack reaching
	// the same providers through this function must clear the same bar. Without
	// it a manifest could put "br0,helper=/tmp/x" in a bridge name, which QEMU
	// reads as one more option and runs as root.
	if err := validation.ValidateInstanceSpec(*spec); err != nil {
		return nil, fmt.Errorf("instance %s: %w", inst.Name, err)
	}
	return spec, nil
}

// convertImageReference converts image type and reference to provider format
func (c *Converter) convertImageReference(imageType, reference string) string {
	switch imageType {
	case ImageTypeFreeBSD:
		// For jail provider, convert "14.3-RELEASE" to "14.3-RELEASE-amd64" format
		return reference
	case ImageTypeOCI:
		// Keep OCI reference as-is (e.g., "nginx:latest")
		return reference
	case ImageTypeCloud:
		// Cloud images keep their reference
		return reference
	case ImageTypeISO:
		// ISO images keep their reference
		return reference
	default:
		return reference
	}
}

// parseCloudImage parses cloud image reference (e.g., "ubuntu-24.04" → "linux", "24.04")
func (c *Converter) parseCloudImage(reference string) (osType, osVersion string) {
	parts := strings.SplitN(reference, "-", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return reference, ""
}

// convertNetwork converts a manifest NetworkSpec to provider.NetworkSpec
func (c *Converter) convertNetwork(net NetworkSpec, index int) (provider.NetworkSpec, error) {
	pNet := provider.NetworkSpec{
		ID:          net.Name,
		Bridge:      net.Bridge,
		BridgeFlags: net.BridgeFlags,
		VLAN:        net.VLAN,
		MAC:         net.MAC,
		IPPool:      net.IPPool,
		MTU:         net.MTU,
	}

	// Convert network type
	switch net.Type {
	case NetworkTypeBridge, "":
		pNet.Type = provider.NetworkTypeBridge
	case NetworkTypeNAT:
		pNet.Type = provider.NetworkTypeNAT
	case NetworkTypeMacvlan:
		pNet.Type = provider.NetworkTypeMacvlan
	case NetworkTypeVXLAN:
		pNet.Type = provider.NetworkTypeVXLAN
	case NetworkTypeNone:
		pNet.Type = provider.NetworkTypeNone
	default:
		return pNet, fmt.Errorf("unknown network type: %s", net.Type)
	}

	// Convert IP configuration
	if net.IP != nil {
		switch net.IP.Mode {
		case IPModeDHCP, "":
			pNet.IPv4 = "dhcp"
		case IPModeStatic:
			pNet.IPv4 = net.IP.Address
		case IPModeNone:
			pNet.IPv4 = ""
		}
	}

	return pNet, nil
}

// convertStorage converts manifest StorageSpec to provider.DiskSpec slice
func (c *Converter) convertStorage(storage StorageSpec) ([]provider.DiskSpec, error) {
	var disks []provider.DiskSpec

	// Convert root disk if specified
	if storage.RootDisk != nil {
		rootDisk := provider.DiskSpec{
			ID:       "root",
			Bootable: true,
		}

		// Convert disk type
		switch storage.RootDisk.Type {
		case DiskTypeZVOL:
			rootDisk.Type = provider.DiskTypeZVOL
		case DiskTypeQCOW2:
			rootDisk.Type = provider.DiskTypeQCOW2
		case DiskTypeRaw:
			rootDisk.Type = provider.DiskTypeRaw
		case DiskTypePhysical:
			rootDisk.Type = provider.DiskTypePhysical
			rootDisk.Path = storage.RootDisk.Path
		case DiskTypeAuto, "":
			// Let provider decide
			rootDisk.Type = ""
		}

		// Parse size only if provided (physical disks and passthrough may omit size)
		if storage.RootDisk.Size != "" {
			sizeBytes, err := ParseMemorySize(storage.RootDisk.Size)
			if err != nil {
				return nil, fmt.Errorf("invalid root disk size: %w", err)
			}
			rootDisk.SizeGB = bytesToGiBRoundUp(sizeBytes)
		}

		disks = append(disks, rootDisk)
	}

	// Convert additional volumes
	for i, vol := range storage.Volumes {
		disk := provider.DiskSpec{
			ID:       vol.Name,
			ReadOnly: vol.ReadOnly,
		}

		// Host path volumes are different from sized volumes
		if vol.HostPath != "" {
			// This is a bind/nullfs mount - handle via ProviderConfig
			// as it's not a traditional disk
			continue
		}

		if vol.Size != "" {
			sizeBytes, err := ParseMemorySize(vol.Size)
			if err != nil {
				return nil, fmt.Errorf("invalid volume[%d] size: %w", i, err)
			}
			disk.SizeGB = bytesToGiBRoundUp(sizeBytes)
		}

		disks = append(disks, disk)
	}

	return disks, nil
}

// bytesToGiBRoundUp converts a byte count to whole GiB, rounding up so that any
// non-zero size below 1 GiB becomes 1 GiB instead of being silently truncated
// to 0 (which would create a zero-size disk).
func bytesToGiBRoundUp(sizeBytes int64) int {
	const bytesPerGiB = 1024 * 1024 * 1024
	if sizeBytes <= 0 {
		return 0
	}
	return int((sizeBytes + bytesPerGiB - 1) / bytesPerGiB)
}

// convertProviderConfig converts provider-specific overrides
func (c *Converter) convertProviderConfig(m *WorkloadManifest, spec *provider.InstanceSpec) map[string]interface{} {
	config := make(map[string]interface{})

	// Get provider-specific overrides
	providerType := m.Provider.Type
	if override, ok := m.ProviderOverrides[providerType]; ok {
		// Copy parameters
		if override.Parameters != nil {
			for k, v := range override.Parameters {
				config[k] = v
			}
		}

		// bhyve/QEMU specific
		if override.Bootloader != "" {
			config["bootloader"] = override.Bootloader
		}
		if override.ConsoleType != "" {
			config["console_type"] = override.ConsoleType
		}
		if len(override.Passthrough) > 0 {
			config["passthrough"] = override.Passthrough
		}
		// VNC: "host:port" or bare port number → forwarded to bhyve provider
		if override.VNC != "" {
			config["vnc"] = override.VNC
		}
		if override.DiskDriver != "" {
			config["disk_driver"] = override.DiskDriver
		}

		// QEMU specific
		if override.Machine != "" {
			config["machine"] = override.Machine
		}
		if override.CPU != "" {
			config["cpu"] = override.CPU
		}

		// Podman specific
		if len(override.Command) > 0 {
			config["command"] = override.Command
		}

		// OS overrides
		if override.OSType != "" {
			spec.OSType = override.OSType
		}
		if override.OSVersion != "" {
			spec.OSVersion = override.OSVersion
		}
	}

	// Add volume mounts to config
	var mounts []map[string]interface{}
	for _, vol := range m.Storage.Volumes {
		if vol.HostPath != "" {
			mount := map[string]interface{}{
				"name":       vol.Name,
				"host_path":  vol.HostPath,
				"mount_path": vol.MountPath,
				"read_only":  vol.ReadOnly,
			}
			mounts = append(mounts, mount)
		}
	}
	if len(mounts) > 0 {
		config["mounts"] = mounts
	}

	// Add ZFS config
	for _, vol := range m.Storage.Volumes {
		if vol.ZFS == nil {
			continue
		}
		zfsConfig := map[string]interface{}{}
		if vol.ZFS.Compression != "" {
			zfsConfig["compression"] = vol.ZFS.Compression
		}
		if vol.ZFS.Quota != "" {
			zfsConfig["quota"] = vol.ZFS.Quota
		}
		if len(zfsConfig) > 0 {
			config["zfs_"+vol.Name] = zfsConfig
		}
	}

	// Add port forwarding
	var ports []map[string]interface{}
	for i := range m.Networks {
		net := m.Networks[i]
		for _, port := range net.Ports {
			p := map[string]interface{}{
				"host":      port.Host,
				"container": port.Container,
				"protocol":  port.Protocol,
			}
			ports = append(ports, p)
		}
	}
	if len(ports) > 0 {
		config["port_forwards"] = ports
	}

	// Add autostart config
	if m.Lifecycle.Autostart != nil && m.Lifecycle.Autostart.Enabled {
		config["autostart"] = map[string]interface{}{
			"enabled":  m.Lifecycle.Autostart.Enabled,
			"priority": m.Lifecycle.Autostart.Priority,
			"delay":    m.Lifecycle.Autostart.Delay,
		}
	}

	// Add hooks
	if m.Lifecycle.Hooks != nil {
		hooks := map[string]interface{}{}
		if m.Lifecycle.Hooks.PreStart != "" {
			hooks["pre_start"] = m.Lifecycle.Hooks.PreStart
		}
		if m.Lifecycle.Hooks.PostStart != "" {
			hooks["post_start"] = m.Lifecycle.Hooks.PostStart
		}
		if m.Lifecycle.Hooks.PreStop != "" {
			hooks["pre_stop"] = m.Lifecycle.Hooks.PreStop
		}
		if len(m.Lifecycle.Hooks.PreCreate) > 0 {
			var preCreateHooks []map[string]interface{}
			for _, hook := range m.Lifecycle.Hooks.PreCreate {
				hookMap := map[string]interface{}{
					"type":     hook.Type,
					"commands": hook.Commands,
				}
				if hook.OnFailure != "" {
					hookMap["on_failure"] = hook.OnFailure
				}
				preCreateHooks = append(preCreateHooks, hookMap)
			}
			hooks["pre_create"] = preCreateHooks
		}
		// Add post_create hooks (structured format with commands)
		if len(m.Lifecycle.Hooks.PostCreate) > 0 {
			var postCreateHooks []map[string]interface{}
			for _, hook := range m.Lifecycle.Hooks.PostCreate {
				hookMap := map[string]interface{}{
					"type":     hook.Type,
					"commands": hook.Commands,
				}
				if hook.OnFailure != "" {
					hookMap["on_failure"] = hook.OnFailure
				}
				postCreateHooks = append(postCreateHooks, hookMap)
			}
			hooks["post_create"] = postCreateHooks
		}
		if len(hooks) > 0 {
			config["hooks"] = hooks
		}
	}

	// Graceful-stop timeout and health check.
	addLifecycleExtras(config, m.Lifecycle)

	// Workload-level environment variables.
	if len(m.Environment) > 0 {
		config["environment"] = m.Environment
	}

	return config
}

// addLifecycleExtras forwards the stop_timeout and health_check lifecycle
// settings into the provider config map. They share this path with autostart
// and hooks so providers receive the full lifecycle definition.
func addLifecycleExtras(config map[string]interface{}, l LifecycleSpec) {
	if l.StopTimeout > 0 {
		config["stop_timeout"] = l.StopTimeout
	}
	if l.HealthCheck != nil && len(l.HealthCheck.Command) > 0 {
		hc := map[string]interface{}{"command": l.HealthCheck.Command}
		if l.HealthCheck.Interval != "" {
			hc["interval"] = l.HealthCheck.Interval
		}
		if l.HealthCheck.Timeout != "" {
			hc["timeout"] = l.HealthCheck.Timeout
		}
		if l.HealthCheck.Retries > 0 {
			hc["retries"] = l.HealthCheck.Retries
		}
		if l.HealthCheck.StartPeriod != "" {
			hc["start_period"] = l.HealthCheck.StartPeriod
		}
		config["health_check"] = hc
	}
}

// GetMounts extracts mount configurations from a workload manifest
func (c *Converter) GetMounts(m *WorkloadManifest) []Mount {
	var mounts []Mount
	for _, vol := range m.Storage.Volumes {
		if vol.HostPath != "" {
			mounts = append(mounts, Mount{
				Name:      vol.Name,
				HostPath:  vol.HostPath,
				MountPath: vol.MountPath,
				ReadOnly:  vol.ReadOnly,
			})
		}
	}
	return mounts
}

// Mount represents a volume mount
type Mount struct {
	Name      string
	HostPath  string
	MountPath string
	ReadOnly  bool
}

// GetPortForwards extracts port forwarding configurations
func (c *Converter) GetPortForwards(m *WorkloadManifest) []PortForward {
	var forwards []PortForward
	for i := range m.Networks {
		net := m.Networks[i]
		for _, port := range net.Ports {
			forwards = append(forwards, PortForward{
				HostPort:      port.Host,
				ContainerPort: port.Container,
				Protocol:      port.Protocol,
			})
		}
	}
	return forwards
}

// PortForward represents a port forwarding rule
type PortForward struct {
	HostPort      int
	ContainerPort int
	Protocol      string
}

// generateCloudInitForVM generates cloud-init configuration for VM providers (bhyve, qemu)
// This is called when the manifest uses cloud images and has network configuration.
// Compatible with both Linux cloud-init and FreeBSD nuageinit (14.1+).
func (c *Converter) generateCloudInitForVM(m *WorkloadManifest, spec *provider.InstanceSpec) error {
	providerType := m.Provider.Type
	declared := m.CloudInit != nil && m.CloudInit.Enabled != nil && *m.CloudInit.Enabled

	if !providerAppliesCloudInit(providerType) {
		if declared {
			return errCloudInitUnsupported(providerType)
		}
		return nil
	}

	// If manifest has explicit cloud_init section, use it
	if declared {
		return c.generateFromCloudInitSpec(m.CloudInit, m.Networks, spec)
	}

	// Check if OS type suggests this needs cloud-init (auto-detect)
	needsCloudInit := spec.OSType == "linux" || spec.OSType == "ubuntu" ||
		spec.OSType == "debian" || spec.OSType == "centos" || spec.OSType == "fedora" ||
		spec.OSType == "alpine" || spec.OSType == "rocky" || spec.OSType == "almalinux" ||
		spec.OSType == "arch" || spec.OSType == "opensuse" ||
		spec.OSType == "freebsd" // FreeBSD 14.1+ supports nuageinit

	// Also check for cloud image source
	if strings.HasPrefix(m.Image.Source, "cloud:") {
		needsCloudInit = true
	}

	if !needsCloudInit {
		return nil
	}

	// Don't override existing cloud-init config
	if spec.CloudInit != nil && (spec.CloudInit.UserData != "" || spec.CloudInit.MetaData != "") {
		return nil
	}

	// Build network configs from manifest networks
	var networks []cloudinit.NetworkConfig
	for i := range m.Networks {
		net := m.Networks[i]
		ifName := fmt.Sprintf("eth%d", i)
		if i == 0 {
			ifName = "ens3" // Common default for modern Linux
		}

		var gateway string
		var dns []string
		var ipv4 string

		if net.IP != nil {
			switch net.IP.Mode {
			case IPModeDHCP, "":
				ipv4 = "dhcp"
			case IPModeStatic:
				ipv4 = net.IP.Address
				gateway = net.IP.Gateway
				dns = net.IP.DNS
			}
		} else {
			ipv4 = "dhcp"
		}

		networks = append(networks, cloudinit.NetworkFromProviderSpec(
			ifName,
			ipv4,
			gateway,
			dns,
			net.MAC,
		))
	}

	// Get SSH keys from provider overrides if present
	var sshKeys []string
	if override, ok := m.ProviderOverrides[providerType]; ok {
		if keys, ok := override.Parameters["ssh_authorized_keys"].([]interface{}); ok {
			for _, k := range keys {
				if ks, ok := k.(string); ok {
					sshKeys = append(sshKeys, ks)
				}
			}
		}
	}

	// Create cloud-init config
	ciConfig := cloudinit.ConfigFromSpec(
		spec.Name,
		spec.Name,
		networks,
		sshKeys,
	)

	// Generate cloud-init YAML files
	c.generateCloudInitYAML(ciConfig, spec)
	return nil
}

// generateFromCloudInitSpec generates cloud-init from manifest CloudInitSpec
func (c *Converter) generateFromCloudInitSpec(ciSpec *CloudInitSpec, manifestNetworks []NetworkSpec, spec *provider.InstanceSpec) error {
	ciConfig := &cloudinit.Config{
		InstanceID:        spec.Name,
		LocalHostname:     spec.Name,
		Packages:          ciSpec.Packages,
		RunCMD:            ciSpec.RunCMD,
		SSHAuthorizedKeys: ciSpec.SSHAuthorizedKeys,
	}

	// Override hostname if specified
	if ciSpec.Hostname != "" {
		ciConfig.LocalHostname = ciSpec.Hostname
	}

	// Convert users
	for i := range ciSpec.Users {
		u := ciSpec.Users[i]
		ciConfig.Users = append(ciConfig.Users, cloudinit.UserConfig{
			Name:              u.Name,
			Groups:            u.Groups,
			Shell:             u.Shell,
			Sudo:              u.Sudo,
			Doas:              u.Doas,
			SSHAuthorizedKeys: u.SSHAuthorizedKeys,
			LockPasswd:        u.LockPasswd,
			PlainTextPasswd:   u.PlainTextPasswd,
		})
	}

	// Build network configs from manifest networks
	for i := range manifestNetworks {
		net := manifestNetworks[i]
		ifName := fmt.Sprintf("eth%d", i)
		if i == 0 {
			ifName = "ens3" // Common default for modern Linux
		}

		var gateway string
		var dns []string
		var ipv4 string

		if net.IP != nil {
			switch net.IP.Mode {
			case IPModeDHCP, "":
				ipv4 = "dhcp"
			case IPModeStatic:
				ipv4 = net.IP.Address
				gateway = net.IP.Gateway
				dns = net.IP.DNS
			case IPModeNone:
				continue // Skip networks without IP
			}
		} else {
			ipv4 = "dhcp"
		}

		ciConfig.Networks = append(ciConfig.Networks, cloudinit.NetworkFromProviderSpec(
			ifName,
			ipv4,
			gateway,
			dns,
			"",
		))
	}

	// Handle custom user-data. A referenced file overrides inline user-data,
	// matching ToCloudInitConfig and the documented behavior of user_data_file.
	if ciSpec.UserDataFile != "" {
		data, err := readUserDataFile(ciSpec.UserDataFile, c.cloudInitRoot)
		if err != nil {
			return err
		}
		ciConfig.CustomUserData = data
	} else if ciSpec.UserData != "" {
		ciConfig.CustomUserData = ciSpec.UserData
	}

	// Generate cloud-init YAML files
	c.generateCloudInitYAML(ciConfig, spec)
	return nil
}

// ciUserYAML is the YAML shape of a single cloud-init user. Encoding through a
// struct (rather than string concatenation) guarantees values are properly
// quoted/escaped, preventing YAML injection.
type ciUserYAML struct {
	Name              string   `yaml:"name"`
	Groups            string   `yaml:"groups,omitempty"`
	Shell             string   `yaml:"shell,omitempty"`
	Sudo              string   `yaml:"sudo,omitempty"`
	Doas              string   `yaml:"doas,omitempty"`
	PlainTextPasswd   string   `yaml:"plain_text_passwd,omitempty"`
	LockPasswd        *bool    `yaml:"lock_passwd,omitempty"`
	SSHAuthorizedKeys []string `yaml:"ssh_authorized_keys,omitempty"`
}

// ciUserDataYAML is the top-level #cloud-config document shape.
type ciUserDataYAML struct {
	Users             []ciUserYAML `yaml:"users,omitempty"`
	SSHAuthorizedKeys []string     `yaml:"ssh_authorized_keys,omitempty"`
	Packages          []string     `yaml:"packages,omitempty"`
	RunCMD            []string     `yaml:"runcmd,omitempty"`
}

// buildUserDataYAML renders the #cloud-config user-data document using the YAML
// encoder so attacker-controlled values (user names, packages, commands, keys)
// cannot break out of their scalar and inject arbitrary cloud-config keys. Any
// CustomUserData is appended verbatim after the generated document.
func buildUserDataYAML(ciConfig *cloudinit.Config) string {
	doc := ciUserDataYAML{
		SSHAuthorizedKeys: ciConfig.SSHAuthorizedKeys,
		Packages:          ciConfig.Packages,
		RunCMD:            ciConfig.RunCMD,
	}
	for i := range ciConfig.Users {
		u := ciConfig.Users[i]
		yu := ciUserYAML{
			Name:              u.Name,
			Shell:             u.Shell,
			Sudo:              u.Sudo,
			Doas:              u.Doas,
			PlainTextPasswd:   u.PlainTextPasswd,
			SSHAuthorizedKeys: u.SSHAuthorizedKeys,
		}
		if len(u.Groups) > 0 {
			yu.Groups = strings.Join(u.Groups, ", ")
		}
		// Preserve prior semantics: emit lock_passwd:true when locked; emit
		// lock_passwd:false when a password is set but the account is unlocked
		// (otherwise cloud-init/nuageinit leaves it locked and login fails).
		if u.LockPasswd {
			locked := true
			yu.LockPasswd = &locked
		} else if u.PlainTextPasswd != "" {
			unlocked := false
			yu.LockPasswd = &unlocked
		}
		doc.Users = append(doc.Users, yu)
	}

	userData := "#cloud-config\n"
	if len(doc.Users) > 0 || len(doc.SSHAuthorizedKeys) > 0 ||
		len(doc.Packages) > 0 || len(doc.RunCMD) > 0 {
		if out, err := yaml.Marshal(&doc); err == nil {
			userData += string(out)
		}
	}
	if ciConfig.CustomUserData != "" {
		userData += ciConfig.CustomUserData
	}
	return userData
}

// generateCloudInitYAML generates the YAML files for cloud-init
func (c *Converter) generateCloudInitYAML(ciConfig *cloudinit.Config, spec *provider.InstanceSpec) {
	// Generate user-data YAML via a real encoder so attacker-controlled values
	// cannot inject additional cloud-config keys (YAML injection).
	userData := buildUserDataYAML(ciConfig)

	// Generate network config.
	// FreeBSD nuageinit requires version 1 (v2 is not supported).
	// Linux cloud-init supports version 2 (preferred).
	// For pure-DHCP configs, omit network-config entirely — both nuageinit and
	// cloud-init discover DHCP interfaces without explicit configuration.
	networkConfig := ""
	if len(ciConfig.Networks) > 0 {
		isFreeBSD := spec.OSType == "freebsd"
		allDHCP := true
		for _, net := range ciConfig.Networks {
			if net.Type != "dhcp" {
				allDHCP = false
				break
			}
		}

		if !allDHCP {
			if isFreeBSD {
				// version 1 format required by FreeBSD nuageinit
				networkConfig = "version: 1\nconfig:\n"
				for _, net := range ciConfig.Networks {
					networkConfig += fmt.Sprintf("  - type: physical\n    name: %s\n    subnets:\n", net.Name)
					if net.Type == "dhcp" {
						networkConfig += "      - type: dhcp4\n"
					} else {
						networkConfig += fmt.Sprintf("      - type: static\n        address: %s\n", net.Address)
						if net.Gateway != "" {
							networkConfig += fmt.Sprintf("        gateway: %s\n", net.Gateway)
						}
						if len(net.DNS) > 0 {
							networkConfig += "        dns_nameservers:\n"
							for _, d := range net.DNS {
								networkConfig += fmt.Sprintf("          - %s\n", d)
							}
						}
					}
				}
			} else {
				// version 2 format for Linux cloud-init
				networkConfig = "version: 2\nethernets:\n"
				for _, net := range ciConfig.Networks {
					networkConfig += fmt.Sprintf("  %s:\n", net.Name)
					if net.Type == "dhcp" {
						networkConfig += "    dhcp4: true\n"
					} else {
						networkConfig += "    dhcp4: false\n"
						networkConfig += fmt.Sprintf("    addresses:\n      - %s\n", net.Address)
						if net.Gateway != "" {
							networkConfig += fmt.Sprintf("    routes:\n      - to: default\n        via: %s\n", net.Gateway)
						}
						if len(net.DNS) > 0 {
							networkConfig += "    nameservers:\n      addresses:\n"
							for _, d := range net.DNS {
								networkConfig += fmt.Sprintf("        - %s\n", d)
							}
						}
					}
				}
			}
		}
		// allDHCP: omit network-config; both nuageinit and cloud-init auto-configure DHCP.
	}

	// Create CloudInit config on spec
	spec.CloudInit = &provider.CloudInitConfig{
		UserData: userData,
		MetaData: fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", ciConfig.InstanceID, ciConfig.LocalHostname),
		Network:  networkConfig,
	}
}

// ToCloudInitConfig converts a CloudInitSpec to cloudinit.Config for direct use by providers
func ToCloudInitConfig(spec *CloudInitSpec, instanceID, hostname string, networks []NetworkSpec, cloudInitRoot string) (*cloudinit.Config, error) {
	if spec == nil || spec.Enabled == nil || !*spec.Enabled {
		return nil, nil
	}

	config := &cloudinit.Config{
		InstanceID:        instanceID,
		LocalHostname:     hostname,
		Packages:          spec.Packages,
		RunCMD:            spec.RunCMD,
		SSHAuthorizedKeys: spec.SSHAuthorizedKeys,
	}

	// Override hostname if specified
	if spec.Hostname != "" {
		config.LocalHostname = spec.Hostname
	}

	// Handle user_data_file
	if spec.UserDataFile != "" {
		data, err := readUserDataFile(spec.UserDataFile, cloudInitRoot)
		if err != nil {
			return nil, err
		}
		config.CustomUserData = data
	} else if spec.UserData != "" {
		config.CustomUserData = spec.UserData
	}

	// Convert users
	for i := range spec.Users {
		u := spec.Users[i]
		config.Users = append(config.Users, cloudinit.UserConfig{
			Name:              u.Name,
			Groups:            u.Groups,
			Shell:             u.Shell,
			Sudo:              u.Sudo,
			Doas:              u.Doas,
			SSHAuthorizedKeys: u.SSHAuthorizedKeys,
			LockPasswd:        u.LockPasswd,
			PlainTextPasswd:   u.PlainTextPasswd,
		})
	}

	// Convert networks
	for i := range networks {
		net := networks[i]
		if net.IP == nil || net.IP.Mode == IPModeNone {
			continue
		}

		ifName := net.Name
		if ifName == "" {
			ifName = fmt.Sprintf("eth%d", i)
		}

		var ipv4, gateway string
		var dns []string

		switch net.IP.Mode {
		case IPModeDHCP, "":
			ipv4 = "dhcp"
		case IPModeStatic:
			ipv4 = net.IP.Address
			gateway = net.IP.Gateway
			dns = net.IP.DNS
		}

		config.Networks = append(config.Networks, cloudinit.NetworkFromProviderSpec(
			ifName, ipv4, gateway, dns, "",
		))
	}

	return config, nil
}

// readUserDataFile reads a user-data file from disk.
// The path is validated to prevent directory traversal attacks.
func readUserDataFile(path, root string) (string, error) {
	// SECURITY: Reject paths with traversal attempts or null bytes
	cleaned := filepath.Clean(path)
	if strings.Contains(cleaned, "..") {
		return "", fmt.Errorf("path traversal detected in user_data_file: %s", path)
	}
	if strings.Contains(path, "\x00") {
		return "", fmt.Errorf("null byte in user_data_file path: %s", path)
	}

	// SECURITY: the file is read by the daemon, as root, and its contents are
	// handed to the guest. Without a configured directory to read from, there
	// is no path this is safe for.
	if root == "" {
		return "", fmt.Errorf("user_data_file is not accepted here: no cloud-init directory is configured")
	}
	within, err := validation.PathWithin(cleaned, root)
	if err != nil {
		return "", fmt.Errorf("cannot resolve user_data_file %s: %w", path, err)
	}
	if !within {
		return "", fmt.Errorf("user_data_file %s must be under %s", path, root)
	}

	data, err := os.ReadFile(cleaned)
	if err != nil {
		return "", fmt.Errorf("failed to read user_data_file %s: %w", cleaned, err)
	}
	return string(data), nil
}

// providerAppliesCloudInit reports whether a provider can act on a cloud-init
// block.
//
// Only the VM providers can: cloud-init reaches the guest as a seed image the
// guest's own agent reads at first boot. A jail or a container has no such
// agent and no seed device to attach one to.
func providerAppliesCloudInit(providerType string) bool {
	return providerType == "bhyve" || providerType == "qemu"
}

// errCloudInitUnsupported refuses a cloud-init block the named provider cannot
// act on, rather than dropping it silently and leaving an unprovisioned
// instance that looks like the one the manifest describes.
func errCloudInitUnsupported(providerType string) error {
	return fmt.Errorf("provider %q cannot apply a cloud_init block: only bhyve and qemu can, "+
		"because cloud-init reaches the guest through a seed image; "+
		"use lifecycle.hooks.post_create to provision this instance", providerType)
}
