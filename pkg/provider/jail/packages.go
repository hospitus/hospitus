package jail

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// validatePackageName rejects package names that could be interpreted as pkg(8)
// flags or that carry unexpected control characters. Package names/patterns may
// legitimately contain version operators and category slashes (e.g.
// "www/nginx", "nginx>=1.0"), but must not start with '-' or contain spaces or
// shell/control characters, since they are passed as argv to jexec/pkg.
func validatePackageName(name string) error {
	if name == "" {
		return fmt.Errorf("package name cannot be empty")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("package name %q must not start with '-'", name)
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune("._+/@-<>=~*", c):
		default:
			return fmt.Errorf("package name %q contains invalid character %q", name, c)
		}
	}
	return nil
}

// PackageInfo represents information about an installed package.
type PackageInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// InstallPackages installs FreeBSD packages in a jail using pkg(8).
//
// This implements CBSD's package installation functionality:
//   - Bootstraps pkg if not already installed
//   - Installs specified packages
//   - Can run during jail creation or on existing jail
//
// Examples:
//   - Web server: nginx, apache24
//   - Database: postgresql15-server, mysql80-server
//   - Development: git, vim, tmux
func (p *JailProvider) InstallPackages(ctx context.Context, handle provider.InstanceHandle, packages []string) error {
	if len(packages) == 0 {
		return nil
	}

	// SECURITY: validate the jail name and every package name before they reach
	// jexec/pkg as arguments (prevents argument injection, CWE-88).
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid jail name: %w", err)
	}
	for _, pkg := range packages {
		if err := validatePackageName(pkg); err != nil {
			return err
		}
	}

	jailName := handle.ID

	// Check if jail is running
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get jail state: %w", err)
	}

	// Start jail if not running
	wasRunning := state == provider.StateRunning
	if !wasRunning {
		if err := p.StartInstance(ctx, handle); err != nil {
			return fmt.Errorf("failed to start jail for package installation: %w", err)
		}
		// Ensure jail is stopped after package installation if it wasn't running.
		// Use a detached context so cleanup still runs even if the caller's
		// context has already been canceled by the time the defer fires.
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
			defer cancel()
			_ = p.StopInstance(stopCtx, handle, provider.StopOptions{Timeout: 30 * time.Second}) // best-effort cleanup
		}()
	}

	// Bootstrap pkg if not installed
	if err := p.bootstrapPkg(ctx, jailName); err != nil {
		return fmt.Errorf("failed to bootstrap pkg: %w", err)
	}

	// Install packages
	for _, pkg := range packages {
		if err := p.installPackage(ctx, jailName, pkg); err != nil {
			return fmt.Errorf("failed to install package %s: %w", pkg, err)
		}
	}

	return nil
}

// bootstrapPkg ensures pkg is installed and configured in the jail.
func (p *JailProvider) bootstrapPkg(ctx context.Context, jailName string) error {
	// Check if pkg is already installed
	if err := p.cmd().Run(ctx, "jexec", jailName, "which", "pkg"); err == nil {
		// pkg is already installed
		return nil
	}

	// Bootstrap pkg
	// Use ASSUME_ALWAYS_YES=yes to avoid interactive prompts
	output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "env", "ASSUME_ALWAYS_YES=yes", "/usr/sbin/pkg", "bootstrap")
	if err != nil {
		return fmt.Errorf("pkg bootstrap failed: %w (output: %s)", err, string(output))
	}

	return nil
}

// installPackage installs a single package in the jail.
func (p *JailProvider) installPackage(ctx context.Context, jailName, packageName string) error {
	// Use ASSUME_ALWAYS_YES=yes to avoid interactive prompts
	output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "env", "ASSUME_ALWAYS_YES=yes", "pkg", "install", packageName)
	if err != nil {
		return fmt.Errorf("pkg install failed: %w (output: %s)", err, string(output))
	}

	return nil
}

// ListPackages returns a list of installed packages in the jail.
func (p *JailProvider) ListPackages(ctx context.Context, handle provider.InstanceHandle) ([]PackageInfo, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid jail name: %w", err)
	}
	jailName := handle.ID

	// Check if jail is running
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("failed to get jail state: %w", err)
	}

	if state != provider.StateRunning {
		return nil, fmt.Errorf("jail must be running to list packages")
	}

	// "pkg query", not "pkg info -a": the latter prints full multi-line records,
	// so the parser below read each metadata line as another package and
	// dropped the one-field ones.
	//
	// %c, not %e: verified on FreeBSD that %e is the long description and spans
	// several lines, which would split one package across several records the
	// same way. %c is the one-line comment — 2469 lines for 2469 packages.
	output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "pkg", "query", "%n-%v\t%c")
	if err != nil {
		return nil, fmt.Errorf("failed to list packages: %w", err)
	}

	packages := []PackageInfo{}
	for _, line := range strings.Split(string(output), "\n") {
		if line == "" {
			continue
		}
		// A description may hold spaces; the tab is what separates the fields.
		nameVersion, description, _ := strings.Cut(line, "\t")
		if nameVersion == "" {
			continue
		}
		packages = append(packages, PackageInfo{
			Name:        nameVersion,
			Description: description,
		})
	}

	return packages, nil
}

// RemovePackage removes a package from the jail.
func (p *JailProvider) RemovePackage(ctx context.Context, handle provider.InstanceHandle, packageName string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid jail name: %w", err)
	}
	if err := validatePackageName(packageName); err != nil {
		return err
	}
	jailName := handle.ID

	// Check if jail is running
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get jail state: %w", err)
	}

	if state != provider.StateRunning {
		return fmt.Errorf("jail must be running to remove packages")
	}

	// Remove package
	output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "env", "ASSUME_ALWAYS_YES=yes", "pkg", "delete", packageName)
	if err != nil {
		return fmt.Errorf("failed to remove package: %w (output: %s)", err, string(output))
	}

	return nil
}
