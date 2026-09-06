package manifest

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	imagepkg "github.com/hospitus/hospitus/pkg/image"
	"github.com/hospitus/hospitus/pkg/manifest"
)

// appliedResources records what an apply has already built, so a failure part
// way through can take it back down.
//
// A volume outlives its instance by design, so deleting the instance is not
// enough: the dataset stays, full size, under a name the next apply will not
// reuse. Orphans found on a real host came from exactly this.
type appliedResources struct {
	Instance string
	Volumes  []string
}

// rollbackTimeout bounds the cleanup of a failed apply. Deleting an instance
// can take a while, and giving up halfway is what this exists to avoid.
const rollbackTimeout = 2 * time.Minute

// rollbackApply removes what an apply built before it failed.
//
// Best effort and loud: each failure to clean up is reported with the command
// that finishes the job by hand, because a half-removed instance is worse to
// discover than one that was never removed.
func rollbackApply(ctx context.Context, apiClient cmdutil.APIClientInterface, instances, volumes []string) {
	if len(instances) == 0 && len(volumes) == 0 {
		return
	}

	// Cleanup has to outlive the request that triggered it: on a timeout or a
	// Ctrl-C the caller's context is already done, and every delete below would
	// fail on the spot.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()

	fmt.Println("\nRolling back what was created...")

	for _, id := range instances {
		if err := apiClient.DeleteInstance(ctx, id, true); err != nil {
			fmt.Printf("  could not remove instance %s: %v\n", id, err)
			fmt.Printf("    remove it with: hospitus jail destroy %s -y\n", id)
			continue
		}
		fmt.Printf("  removed instance %s\n", id)
	}

	for _, name := range volumes {
		if err := apiClient.DeleteVolume(ctx, name); err != nil {
			fmt.Printf("  could not remove volume %s: %v\n", name, err)
			fmt.Printf("    remove it with: hospitus jail volume delete %s -y\n", name)
			continue
		}
		fmt.Printf("  removed volume %s\n", name)
	}
}

// instanceList renders an apply's instance as a list, empty when nothing was
// created.
func instanceList(a appliedResources) []string {
	if a.Instance == "" {
		return nil
	}
	return []string{a.Instance}
}

// reportKept names what --keep-failed left behind, and how to remove it.
//
// A volume kept for inspection is a full-size dataset under a name the next
// apply will not reuse, so the operator has to be told it is there.
func reportKept(instances, volumes []string) {
	if len(instances) == 0 && len(volumes) == 0 {
		return
	}
	fmt.Println("\nLeft in place for inspection:")
	for _, id := range instances {
		fmt.Printf("  instance %s   (remove: hospitus jail destroy %s -y)\n", id, id)
	}
	for _, name := range volumes {
		fmt.Printf("  volume %s   (remove: hospitus jail volume delete %s -y)\n", name, name)
	}
}

// managedVolumeName scopes a declared volume to its instance.
//
// Volumes are host-wide objects under one dataset, so two manifests each
// declaring "data" must not end up sharing one. Apply and delete both go
// through this, or delete leaves behind the datasets apply created.
func managedVolumeName(instanceID, declared string) string {
	return instanceID + "-" + declared
}

// managedVolumes lists the ZFS-backed volumes a manifest declares for one
// instance. A volume carrying a host_path is a bind mount with no dataset of
// its own, and is not one of these.
func managedVolumes(workload *manifest.WorkloadManifest, instanceID string) []string {
	if workload.Provider.Type != "jail" {
		return nil
	}
	var names []string
	for i := range workload.Storage.Volumes {
		vol := &workload.Storage.Volumes[i]
		if vol.HostPath == "" {
			names = append(names, managedVolumeName(instanceID, vol.Name))
		}
	}
	return names
}

// applyManagedVolumes creates the ZFS-backed volumes a manifest declares and
// mounts them into the instance.
//
// A volume carrying a host_path is a bind mount and travels with the instance
// spec. One declared by size has its own dataset and its own lifecycle, so it
// goes through the volume API instead — the instance spec has nowhere to carry
// a quota or a compression setting.
func applyManagedVolumes(ctx context.Context, apiClient cmdutil.APIClientInterface, workload *manifest.WorkloadManifest, instanceID string) (created []string, err error) {
	// Only a jail has volume management. For a VM a sized volume is another
	// disk, and the converter already puts it in the instance spec as one;
	// asking the volume API for it would fail with "volume management is only
	// supported for jails" over a manifest that is perfectly valid.
	if workload.Provider.Type != "jail" {
		return created, nil
	}

	for i := range workload.Storage.Volumes {
		vol := &workload.Storage.Volumes[i]
		if vol.HostPath != "" {
			continue
		}

		volumeName := managedVolumeName(instanceID, vol.Name)

		if existing, err := apiClient.GetVolume(ctx, volumeName); err == nil && existing != nil {
			fmt.Printf("Reusing volume: %s\n", volumeName)
		} else {
			req := client.CreateVolumeRequest{
				Name:        volumeName,
				Description: fmt.Sprintf("volume %q of workload %s", vol.Name, instanceID),
			}
			// zfs(8) reads K/M/G/T, while a manifest writes Ki/Mi/Gi/Ti. Send the
			// byte count so the two never have to agree on a suffix.
			var err error
			if req.Size, err = bytesForZFS(vol.Size); err != nil {
				return created, fmt.Errorf("volume %s: invalid size: %w", vol.Name, err)
			}
			if vol.ZFS != nil {
				if req.Quota, err = bytesForZFS(vol.ZFS.Quota); err != nil {
					return created, fmt.Errorf("volume %s: invalid quota: %w", vol.Name, err)
				}
				req.Compression = vol.ZFS.Compression
			}

			fmt.Printf("Creating volume: %s\n", volumeName)
			if _, err := apiClient.CreateVolume(ctx, req); err != nil {
				return created, fmt.Errorf("failed to create volume %s: %w", vol.Name, err)
			}
			// Only what this run created is ours to undo; a reused volume predates it.
			created = append(created, volumeName)
		}

		if err := apiClient.AttachVolume(ctx, instanceID, volumeName, vol.MountPath); err != nil {
			return created, fmt.Errorf("failed to attach volume %s at %s: %w", vol.Name, vol.MountPath, err)
		}
		fmt.Printf("Attached %s at %s\n", volumeName, vol.MountPath)
	}

	return created, nil
}

// bytesForZFS renders a manifest size as a plain byte count. An empty size
// stays empty: the property is then not set.
func bytesForZFS(size string) (string, error) {
	if size == "" {
		return "", nil
	}
	n, err := manifest.ParseMemorySize(size)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(n, 10), nil
}

// executePostCreateHooks executes the post_create hooks defined in the manifest
// These hooks run commands inside the running instance
func executePostCreateHooks(ctx context.Context, apiClient cmdutil.APIClientInterface, instanceID string, hooks []manifest.HookSpec) error {
	if len(hooks) == 0 {
		return nil
	}

	fmt.Println("\nExecuting post-create provisioning hooks...")

	for i, hook := range hooks {
		if hook.Type != "exec" {
			fmt.Printf("  Skipping hook %d: unsupported type '%s'\n", i+1, hook.Type)
			continue
		}

		if len(hook.Commands) == 0 {
			continue
		}

		fmt.Printf("  Hook %d: executing %d commands...\n", i+1, len(hook.Commands))

		for j, cmd := range hook.Commands {
			// Skip empty commands
			if cmd == "" {
				continue
			}

			fmt.Printf("    [%d/%d] %s\n", j+1, len(hook.Commands), truncateCommand(cmd, 60))

			// Execute command inside instance using /bin/sh -c
			// Use streaming exec for real-time output (e.g., pkg install progress)
			req := client.ExecRequest{
				Command: "/bin/sh",
				Args:    []string{"-c", cmd},
				Timeout: 1800, // 30 minute timeout per command (for large pkg install, etc.)
			}

			exitCode, execErr := apiClient.ExecCommandStream(ctx, instanceID, req, os.Stdout, os.Stderr)
			if execErr != nil {
				// If the provider does not support exec (e.g. bhyve), skip hooks gracefully
				if strings.Contains(execErr.Error(), "does not support exec") {
					fmt.Printf("      Skipping: provider does not support in-instance execution\n")
					// Skip remaining commands for this hook since provider lacks exec support
					break
				}
				if hook.OnFailure == "continue" {
					fmt.Printf("      Warning: command failed: %v (continuing)\n", execErr)
					continue
				}
				return fmt.Errorf("hook command failed: %w", execErr)
			}

			if exitCode != 0 {
				if hook.OnFailure == "continue" {
					fmt.Printf("      Warning: command exited with code %d (continuing)\n", exitCode)
					continue
				}
				return fmt.Errorf("hook command exited with code %d", exitCode)
			}
		}
	}

	fmt.Println("  Post-create hooks completed successfully")
	return nil
}

// truncateCommand truncates a command string for display
func truncateCommand(cmd string, maxLen int) string {
	if len(cmd) <= maxLen {
		return cmd
	}
	return cmd[:maxLen-3] + "..."
}

func newApplyCommand() *cobra.Command {
	var (
		dryRun     bool
		timeout    time.Duration
		start      bool
		pull       bool
		keepFailed bool
		namespace  string
		varFlags   []string
		valuesFile string
		envPrefix  string
	)

	cmd := &cobra.Command{
		Use:   "apply <manifest.toml>",
		Short: "Apply a manifest to create workloads",
		Long: `Apply a workload or stack manifest to create instances.

This command reads a TOML manifest file and creates the specified workloads;
an instance that already exists is an error, not an update.
For stack manifests, instances are created in dependency order.

Examples:
  # Apply a workload manifest
  hospitus apply webserver.toml

  # Apply with variables
  hospitus apply --var domain=example.com webserver.toml

  # Apply with values file
  hospitus apply --values prod.toml webserver.toml

  # Apply and start the workload
  hospitus apply --start webserver.toml

  # Dry run - show what would be created
  hospitus apply --dry-run webserver.toml

  # Apply with custom timeout
  hospitus apply --timeout 5m mystack.toml

  # Apply a stack manifest
  hospitus apply stack.toml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vars, err := mergeVariables(varFlags, valuesFile, envPrefix)
			if err != nil {
				return err
			}
			return runApply(cmd.Context(), args[0], dryRun, start, pull, keepFailed, timeout, namespace, vars)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be created without actually applying")
	cmd.Flags().BoolVar(&start, "start", false, "Start the instance after creation")
	cmd.Flags().BoolVar(&pull, "pull", false, "Download a missing image instead of asking (required when not on a terminal)")
	cmd.Flags().BoolVar(&keepFailed, "keep-failed", false, "Leave what was created when the apply fails, to inspect it")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "Timeout for operations (includes provisioning hooks)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Namespace prefix for instance names")
	cmd.Flags().StringSliceVar(&varFlags, "var", []string{}, "Set variables (key=value)")
	cmd.Flags().StringVar(&valuesFile, "values", "", "Path to a TOML file containing variable values")
	cmd.Flags().StringVar(&envPrefix, "env-prefix", "HOSPITUS_VAR_", "Prefix for environment variables to be imported as manifest variables")

	return cmd
}

func mergeVariables(varFlags []string, valuesFile, envPrefix string) (map[string]any, error) {
	vars := make(map[string]any)

	// 1. Environment variables (lowest precedence)
	if envPrefix != "" {
		for _, env := range os.Environ() {
			if strings.HasPrefix(env, envPrefix) {
				parts := strings.SplitN(env, "=", 2)
				key := strings.TrimPrefix(parts[0], envPrefix)
				vars[key] = parts[1]
			}
		}
	}

	// 2. Values file
	if valuesFile != "" {
		data, err := os.ReadFile(valuesFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read values file: %w", err)
		}
		var fileVars map[string]any
		if _, err := toml.Decode(string(data), &fileVars); err != nil {
			return nil, fmt.Errorf("failed to parse values file: %w", err)
		}
		for k, v := range fileVars {
			vars[k] = v
		}
	}

	// 3. CLI flags (highest precedence)
	for _, f := range varFlags {
		parts := strings.SplitN(f, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid var flag format: %s (expected key=value)", f)
		}
		vars[parts[0]] = parts[1]
	}

	return vars, nil
}

func runApply(ctx context.Context, manifestPath string, dryRun, start, pull, keepFailed bool, timeout time.Duration, namespace string, vars map[string]any) error {
	// Check if file exists
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return fmt.Errorf("manifest file not found: %s", manifestPath)
	}

	// Parse manifest with vars.
	//
	// The placeholder store belongs to validate and dry runs. Applying with it
	// wrote PLACEHOLDER-<scope>-<name> wherever a manifest asked for a secret,
	// so the WordPress example created its database user with a password
	// spelled out of two strings anyone can read in the repository.
	secrets := manifest.NewFileSecretStore("")
	parser := manifest.NewParser(secrets)
	parsed, err := parser.ParseFile(manifestPath, vars)
	if err != nil {
		return fmt.Errorf("failed to parse manifest: %w", err)
	}

	// Validate
	validator := manifest.NewValidator()
	validationErrs := validator.Validate(parsed)
	if validationErrs.HasErrors() {
		fmt.Println("Validation errors:")
		for _, e := range validationErrs {
			fmt.Printf("  ERROR: [%s] %s\n", e.Field, e.Message)
		}
		return fmt.Errorf("manifest validation failed")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if parsed.IsWorkload() {
		applied, applyErr := applyWorkload(ctx, parsed.Workload, dryRun, start, pull, namespace)
		if applyErr != nil && !keepFailed {
			rollbackApply(ctx, cmdutil.GetClient(), instanceList(applied), applied.Volumes)
		} else if applyErr != nil {
			reportKept(instanceList(applied), applied.Volumes)
		}
		return applyErr
	} else if parsed.IsStack() {
		return applyStack(ctx, parsed.Stack, dryRun, start, pull, namespace, keepFailed)
	}

	return fmt.Errorf("unknown manifest type")
}

// imageReference strips the provider prefix a manifest writes in front of an
// image, so "freebsd:14.3-RELEASE-riscv64" becomes the name the catalog is
// asked for. A source with no prefix is already the reference.
func imageReference(source string) string {
	if _, ref, found := strings.Cut(source, ":"); found {
		return ref
	}
	return source
}

func applyWorkload(ctx context.Context, workload *manifest.WorkloadManifest, dryRun, start, pull bool, namespace string) (applied appliedResources, err error) {
	name := workload.Workload.Name
	if namespace != "" {
		name = namespace + "-" + name
	}

	fmt.Printf("Applying workload: %s\n", name)
	fmt.Printf("  Provider: %s\n", workload.Provider.Type)
	fmt.Printf("  Image: %s\n", workload.Image.Source)

	if workload.Resources.CPU > 0 {
		fmt.Printf("  CPUs: %d\n", workload.Resources.CPU)
	}
	if workload.Resources.Memory != "" {
		fmt.Printf("  Memory: %s\n", workload.Resources.Memory)
	}

	if len(workload.Networks) > 0 {
		fmt.Printf("  Networks:\n")
		for i := range workload.Networks {
			net := &workload.Networks[i]
			fmt.Printf("    - %s (%s)\n", net.Name, net.Type)
		}
	}

	if len(workload.Storage.Volumes) > 0 {
		fmt.Printf("  Volumes:\n")
		for i := range workload.Storage.Volumes {
			vol := &workload.Storage.Volumes[i]
			if vol.HostPath != "" {
				fmt.Printf("    - %s (%s from %s)\n", vol.Name, vol.MountPath, vol.HostPath)
				continue
			}
			fmt.Printf("    - %s (%s, %s)\n", vol.Name, vol.MountPath, vol.Size)
		}
	}

	if dryRun {
		fmt.Println("\n[Dry run] Would create workload - no changes made")
		return applied, nil
	}

	// Add manifest source label for traceability (helps with name mismatch resolution)
	if workload.Workload.Labels == nil {
		workload.Workload.Labels = make(map[string]string)
	}
	workload.Workload.Labels["hospitus.io/manifest-source"] = name

	// Convert to InstanceSpec
	spec, err := manifest.ToInstanceSpec(workload)
	if err != nil {
		return applied, fmt.Errorf("failed to convert manifest to instance spec: %w", err)
	}
	spec.Name = name

	apiClient := cmdutil.GetClient()
	if apiClient == nil {
		return applied, fmt.Errorf("API client not initialized")
	}

	// Warn about Linux jails requiring longer debootstrap time
	if workload.Provider.Type == "jail" {
		if override, ok := workload.ProviderOverrides["jail"]; ok {
			if override.OSType == "linux" {
				fmt.Println("\nNote: Linux jail detected. Debootstrap may take 3-5 minutes.")
				fmt.Println("If you hit a timeout, retry with: hospitus manifest apply --timeout 10m <manifest>")
			}
		}
	}

	// Create instance via the generic API
	fmt.Printf("\nCreating %s instance...\n", workload.Provider.Type)

	req := client.CreateInstanceRequest{
		Provider: workload.Provider.Type,
		Spec:     *spec,
	}

	instance, createErr := apiClient.CreateInstance(ctx, req)
	if createErr != nil {
		// Check if the error is "image not found" and offer to download
		imageRef := imageReference(workload.Image.Source)

		if cmdutil.IsImageNotFoundError(createErr) && imageRef != "" {
			// Check if the image exists in the available catalog
			imageDir := "/var/lib/hospitus/images"
			if dir := os.Getenv("HOSPITUS_IMAGE_DIR"); dir != "" {
				imageDir = dir
			}
			catalog := imagepkg.NewCatalog(imageDir)
			// ResolveProfile, not FindProfile: a manifest writes the image as
			// "freebsd:14.3-RELEASE-riscv64", which leaves the reference
			// "14.3-RELEASE-riscv64" while the catalog is keyed on
			// "freebsd-14.3-RELEASE-riscv64". FindProfile misses it, and apply
			// then says the image is not in the catalog while telling the user
			// to fetch it by the name it just refused.
			profile := catalog.ResolveProfile(imageRef)

			if profile != nil {
				// Image exists in catalog, offer to download
				fmt.Printf("\nImage '%s' is not downloaded but is available in the catalog.\n", imageRef)
				if cmdutil.ConfirmDownload("Would you like to download it now?", pull) {
					fmt.Printf("\nDownloading image %s...\n", imageRef)
					fmt.Printf("This may take a few minutes...\n")

					if fetchErr := apiClient.FetchImage(ctx, imageRef, os.Stdout); fetchErr != nil {
						return applied, fmt.Errorf("failed to download image: %w", fetchErr)
					}
					fmt.Printf("Image downloaded successfully.\n\n")

					// Retry creating the instance
					fmt.Printf("Retrying %s creation...\n", workload.Provider.Type)
					instance, createErr = apiClient.CreateInstance(ctx, req)
					if createErr != nil {
						return applied, fmt.Errorf("failed to create instance after downloading image: %w", createErr)
					}
				} else {
					fmt.Printf("\nTo download the image manually, run:\n")
					fmt.Printf("  hospitus image fetch %s\n", imageRef)
					return applied, fmt.Errorf("failed to apply manifest: image not downloaded")
				}
			} else {
				return applied, fmt.Errorf("failed to create instance: %w (image '%s' is not in the available catalog, run 'hospitus image available' to see available images)", createErr, imageRef)
			}
		} else {
			return applied, fmt.Errorf("failed to create instance: %w", createErr)
		}
	}

	fmt.Printf("Created instance: %s\n", instance.ID)
	applied.Instance = instance.ID

	volumes, volErr := applyManagedVolumes(ctx, apiClient, workload, instance.ID)
	applied.Volumes = append(applied.Volumes, volumes...)
	if volErr != nil {
		return applied, volErr
	}

	// Start if requested
	if start {
		fmt.Printf("Starting instance...\n")
		if err := apiClient.StartInstance(ctx, instance.ID, os.Stdout); err != nil {
			return applied, fmt.Errorf("failed to start instance: %w", err)
		}
		fmt.Println("Instance started successfully")

		// Execute post_create hooks if defined
		if workload.Lifecycle.Hooks != nil && len(workload.Lifecycle.Hooks.PostCreate) > 0 {
			// Give the instance a moment to fully start
			select {
			case <-ctx.Done():
				return applied, ctx.Err()
			case <-time.After(2 * time.Second):
			}

			if err := executePostCreateHooks(ctx, apiClient, instance.ID, workload.Lifecycle.Hooks.PostCreate); err != nil {
				return applied, fmt.Errorf("post-create hooks failed: %w", err)
			}
		}
	}

	return applied, nil
}

func applyStack(ctx context.Context, stack *manifest.StackManifest, dryRun, start, pull bool, namespace string, keepFailed bool) (err error) {
	stackName := stack.Stack.Name
	if namespace != "" {
		stackName = namespace + "-" + stackName
	}

	fmt.Printf("Applying stack: %s\n", stackName)
	fmt.Printf("  Instances: %d\n", len(stack.Instances))

	order, err := buildDependencyOrder(stack.Instances)
	if err != nil {
		return fmt.Errorf("failed to resolve dependencies: %w", err)
	}

	fmt.Printf("\nDeployment order:\n")
	for i := range order {
		inst := &order[i]
		fmt.Printf("  %d. %s (%s)\n", i+1, inst.Name, inst.Provider)
	}

	if dryRun {
		fmt.Println("\n[Dry run] Would create stack - no changes made")
		return nil
	}

	// Create instances in order
	fmt.Println("\nCreating instances...")

	// A stack that fails partway has already built instances, and volumes that
	// outlive their instance by design. Undo both, newest first, so a failed
	// deploy leaves the host as it found it.
	var created []string
	var volumes []string
	defer func() {
		if err == nil {
			return
		}
		if keepFailed {
			reportKept(created, volumes)
			return
		}
		rollbackApply(ctx, cmdutil.GetClient(), reversed(created), volumes)
	}()

	for i := range order {
		inst := &order[i]
		instanceName := stackName + "_" + inst.Name

		fmt.Printf("\nCreating %s...\n", instanceName)

		// Convert to workload manifest for reuse
		workload := &manifest.WorkloadManifest{
			Workload: manifest.WorkloadMeta{
				Name: instanceName,
			},
			Provider: manifest.ProviderSpec{
				Type: inst.Provider,
			},
			Image:             inst.Image,
			Resources:         inst.Resources,
			Networks:          inst.Networks,
			Storage:           inst.Storage,
			CloudInit:         inst.CloudInit,
			Lifecycle:         inst.Lifecycle,
			Environment:       inst.Environment,
			ProviderOverrides: inst.ProviderOverrides,
		}

		instanceApplied, applyErr := applyWorkload(ctx, workload, false, start, pull, "")
		created = append(created, instanceList(instanceApplied)...)
		volumes = append(volumes, instanceApplied.Volumes...)
		if applyErr != nil {
			return fmt.Errorf("failed to create %s: %w", instanceName, applyErr)
		}

		// Small delay between instances
		if start {
			fmt.Println("  Waiting for instance to stabilize...")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}

	fmt.Printf("\nStack %s deployed successfully!\n", stackName)
	return nil
}

// buildDependencyOrder returns instances in dependency order (dependencies first)
func buildDependencyOrder(instances []manifest.InstanceConfig) ([]manifest.InstanceConfig, error) {
	// Build dependency graph
	// deps[A] = [B, C] means A depends on B and C (B and C must start before A)
	deps := make(map[string][]string)
	byName := make(map[string]manifest.InstanceConfig)

	for i := range instances {
		inst := &instances[i]
		byName[inst.Name] = *inst
		if inst.DependsOn != nil {
			deps[inst.Name] = inst.DependsOn.Services
		} else {
			deps[inst.Name] = nil
		}
	}

	// Validate that every dependency refers to a known service, so an unknown
	// name is reported explicitly instead of being misdiagnosed as a cycle.
	for name, depList := range deps {
		for _, d := range depList {
			if _, ok := byName[d]; !ok {
				return nil, fmt.Errorf("instance %q depends on unknown service %q", name, d)
			}
		}
	}

	// Topological sort (Kahn's algorithm)
	// If A depends on B, edge goes B -> A, so A has in-degree 1
	var result []manifest.InstanceConfig
	inDegree := make(map[string]int)

	// Initialize in-degrees to 0 for all instances
	for name := range byName {
		inDegree[name] = 0
	}

	// Calculate in-degrees: count how many dependencies each instance has
	// If A depends on B, A's in-degree increases
	for name, depList := range deps {
		inDegree[name] = len(depList)
	}

	// Find all nodes with in-degree 0 (no dependencies)
	var queue []string
	for name, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, name)
		}
	}

	// Process queue
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]

		if inst, ok := byName[name]; ok {
			result = append(result, inst)
		}

		// This instance is now "done", reduce in-degree for all instances that depend on it
		for depName, depList := range deps {
			for _, d := range depList {
				if d == name {
					inDegree[depName]--
					if inDegree[depName] == 0 {
						queue = append(queue, depName)
					}
				}
			}
		}
	}

	// Check for cycles
	if len(result) != len(instances) {
		return nil, fmt.Errorf("circular dependency detected in stack")
	}

	return result, nil
}

// reversed returns names in the opposite order.
//
// A stack is built in dependency order, so taking it down in reverse removes a
// dependent before the instance it depends on.
func reversed(names []string) []string {
	out := make([]string, 0, len(names))
	for i := len(names) - 1; i >= 0; i-- {
		out = append(out, names[i])
	}
	return out
}
