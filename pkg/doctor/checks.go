package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/hospitus/hospitus/pkg/dataset"
)

// DefaultDataDir is the default Hospitus data directory (matches hospitusd --data-dir).
const DefaultDataDir = "/var/lib/hospitus"

// DefaultChecks returns the full prerequisite check set. Platform-awareness is
// handled per check: FreeBSD-only checks (jail, bhyve, kernel modules) report
// SKIP on other platforms, while QEMU/Podman and the data directory apply
// everywhere (macOS degraded mode).
//
// An optional dataDir names the directory the daemon was told to use
// (--data-dir), so a host running with a non-default one is not given a
// verdict about a directory it does not use. Omitted, it is DefaultDataDir.
func DefaultChecks(dataDir ...string) []Check {
	var checks []Check
	checks = append(checks, coreChecks(dataDirOrDefault(dataDir))...)
	checks = append(checks, jailChecks()...)
	checks = append(checks, bhyveChecks()...)
	checks = append(checks, qemuChecks()...)
	checks = append(checks, podmanChecks()...)
	checks = append(checks, cloudInitChecks()...)
	checks = append(checks, macOSChecks()...)
	checks = append(checks, networkChecks()...)
	return checks
}

// commandCheck builds a check that verifies a binary is on PATH.
func commandCheck(id, binary, purpose string, required bool, providers []string) Check {
	sev := StatusWarn
	if required {
		sev = StatusFail
	}
	return Check{
		ID:          id,
		Name:        binary,
		Description: purpose,
		Required:    required,
		Providers:   providers,
		Run: func(_ context.Context, s System) Result {
			if freebsdOnly(providers) && s.GOOS() != "freebsd" {
				return Result{Status: StatusSkip, Detail: "not applicable on " + s.GOOS()}
			}
			if s.HasCommand(binary) {
				return Result{Status: StatusOK, Detail: binary + " found"}
			}
			return Result{
				Status: sev, Detail: binary + " not found",
				Remediation: fmt.Sprintf("install %s (pkg install ...) — needed for %s", binary, purpose),
			}
		},
	}
}

// freebsdOnly reports whether a provider set is FreeBSD-only (jail/bhyve).
func freebsdOnly(providers []string) bool {
	for _, p := range providers {
		if p == ProviderQEMU || p == ProviderPodman || p == ProviderCore ||
			p == ProviderVFKit || p == ProviderContainer {
			return false
		}
	}
	return len(providers) > 0
}

// dataDirOrDefault picks the data directory the checks report on.
func dataDirOrDefault(dataDir []string) string {
	if len(dataDir) > 0 && dataDir[0] != "" {
		return dataDir[0]
	}
	return DefaultDataDir
}

func coreChecks(dataDir string) []Check {
	return []Check{
		{
			ID: "os", Name: "Operating system", Required: false, Providers: []string{ProviderCore},
			Description: "Jail and bhyve providers require FreeBSD; QEMU and Podman also run on macOS.",
			Run: func(_ context.Context, s System) Result {
				switch s.GOOS() {
				case "freebsd":
					return Result{Status: StatusOK, Detail: "FreeBSD — all providers available"}
				case "darwin":
					return Result{Status: StatusWarn, Detail: "macOS — only QEMU and Podman are available (jail/bhyve are FreeBSD-only)"}
				default:
					return Result{Status: StatusWarn, Detail: s.GOOS() + " — limited provider support"}
				}
			},
		},
		{
			ID: "zfs-pool", Name: "ZFS parent dataset", Required: true, Providers: []string{ProviderCore},
			Description: "Jail and bhyve storage live under the parent dataset (default zroot/hospitus; override with HOSPITUS_ZFS_PARENT).",
			Run: func(_ context.Context, s System) Result {
				if s.GOOS() != "freebsd" {
					return Result{Status: StatusSkip, Detail: "ZFS pool check applies to FreeBSD"}
				}
				parent := dataset.Parent()
				if v := s.Getenv("HOSPITUS_ZFS_PARENT"); v != "" {
					parent = v
				}
				pool := poolOf(parent)
				if s.ZFSPoolExists(pool) {
					return Result{Status: StatusOK, Detail: fmt.Sprintf("pool %q exists (parent %q)", pool, parent)}
				}
				return Result{
					Status:      StatusFail,
					Detail:      fmt.Sprintf("ZFS pool %q not found", pool),
					Remediation: "create/point at an existing pool: set HOSPITUS_ZFS_PARENT to a dataset under an existing pool (`zpool list`), e.g. HOSPITUS_ZFS_PARENT=tank/hospitus",
				}
			},
		},
		dirWritableCheck("data-dir", "Data directory", dataDir, true,
			"hospitusd stores instances, images, backups and state here (--data-dir)."),
		// zfs and fetch are scoped to the FreeBSD providers rather than to the core:
		// jail and bhyve store instances in ZFS datasets and jail downloads base
		// archives with fetch(1), while QEMU and Podman need neither. Scoping them
		// to ProviderCore made `hospitus init` unsatisfiable on macOS, where the only
		// available providers do not use either tool.
		commandCheck("cmd-zfs", "zfs", "ZFS dataset management", true, []string{ProviderJail, ProviderBhyve}),
		commandCheck("cmd-sysctl", "sysctl", "kernel tunable inspection", true, []string{ProviderCore}),
		commandCheck("cmd-tar", "tar", "image and export archives", true, []string{ProviderCore}),
		commandCheck("cmd-fetch", "fetch", "downloading base archives", true, []string{ProviderJail}),
	}
}

// dirWritableCheck builds a fixable check that the directory exists and is
// writable, fixing with `mkdir -p`.
func dirWritableCheck(id, name, path string, required bool, desc string) Check {
	return Check{
		ID: id, Name: name, Description: desc, Required: required, Providers: []string{ProviderCore},
		Run: func(_ context.Context, s System) Result {
			if s.DirWritable(path) {
				return Result{Status: StatusOK, Detail: path + " is writable"}
			}
			if !s.IsRoot() {
				// Existence needs no privileges, and a directory that is not
				// there at all is a real prerequisite failure the operator
				// should hear before the daemon fails to start.
				if !s.PathExists(path) {
					return Result{
						Status:      StatusWarn,
						Detail:      path + " does not exist",
						Remediation: "mkdir -p " + path + " (run as root)",
					}
				}
				// The daemon's directory is root's. An unprivileged --check
				// cannot write there and must not call that a missing
				// prerequisite: it reported the data directory FAIL on a host
				// where it was present and correct, and exited non-zero.
				return Result{
					Status: StatusSkip,
					Detail: "writing to " + path + " needs root — rerun with doas",
				}
			}
			if s.PathExists(path) {
				return Result{
					Status: StatusFail,
					Detail: path + " exists but is not a directory hospitusd can write to",
					Remediation: "remove or rename " + path + " if it is a file, then mkdir -p " +
						path + " (run as root)",
				}
			}
			return Result{
				Status: StatusFail, Detail: path + " missing or not writable",
				Remediation: "mkdir -p " + path + " (run as root)",
			}
		},
		Fix: func(ctx context.Context, s System) error { return s.Run(ctx, "mkdir", "-p", path) },
	}
}

// moduleCheck builds a fixable check that a kernel module is loaded, fixing with
// `kldload`. Non-FreeBSD hosts report SKIP.
func moduleCheck(id, module, desc string, required bool, providers []string) Check {
	sev := StatusWarn
	if required {
		sev = StatusFail
	}
	return Check{
		ID: id, Name: "kmod " + module, Description: desc, Required: required, Providers: providers,
		Run: func(_ context.Context, s System) Result {
			if s.GOOS() != "freebsd" {
				return Result{Status: StatusSkip, Detail: "kernel module check applies to FreeBSD"}
			}
			if s.KernelModuleLoaded(module) {
				return Result{Status: StatusOK, Detail: module + " loaded"}
			}
			return Result{
				Status: sev, Detail: module + " not loaded",
				Remediation: fmt.Sprintf("kldload %s (and add %s_load=\"YES\" to /boot/loader.conf to persist)", module, module),
			}
		},
		Fix: func(ctx context.Context, s System) error { return s.Run(ctx, "kldload", module) },
	}
}

// sysctlBoolCheck builds a check that a sysctl equals 1. fixable controls
// whether it can be set at runtime (some, like kern.racct.enable, require a
// reboot and are check-only).
func sysctlBoolCheck(id, oid, desc string, required, fixable bool, providers []string) Check {
	sev := StatusWarn
	if required {
		sev = StatusFail
	}
	c := Check{
		ID: id, Name: oid, Description: desc, Required: required, Providers: providers,
		Run: func(_ context.Context, s System) Result {
			if s.GOOS() != "freebsd" {
				return Result{Status: StatusSkip, Detail: "sysctl check applies to FreeBSD"}
			}
			v, err := s.Sysctl(oid)
			if err != nil {
				return Result{Status: sev, Detail: oid + " unavailable", Remediation: "ensure the module providing " + oid + " is loaded"}
			}
			if atoiDefault(v, 0) == 1 {
				return Result{Status: StatusOK, Detail: oid + "=1"}
			}
			rem := fmt.Sprintf("sysctl %s=1 (and add it to /etc/sysctl.conf to persist)", oid)
			if !fixable {
				rem = fmt.Sprintf("add %s=1 to /boot/loader.conf and reboot", oid)
			}
			return Result{Status: sev, Detail: fmt.Sprintf("%s=%s", oid, v), Remediation: rem}
		},
	}
	if fixable {
		c.Fix = func(ctx context.Context, s System) error { return s.Run(ctx, "sysctl", oid+"=1") }
	}
	return c
}

// crossArchJailCheck reports whether the host can actually run a jail whose
// binaries are not its own.
//
// Hospitus copies the emulator into the jail and registers nothing: what makes the
// kernel reach for it is an interpreter registered with binmiscctl(8). A host
// with qemu-user-static installed and no activator creates a foreign jail
// perfectly and cannot start it, under a message that names the wrong file:
//
//	jail: exec /bin/sh: No such file or directory
//
// /bin/sh is there; its interpreter is not.
//
// Silent on a host with no emulator installed: cross-architecture jails are a
// deliberate choice, and a warning aimed at everyone who has not made it is
// noise.
func crossArchJailCheck(providers []string) Check {
	return Check{
		ID: "jail-cross-arch", Name: "Foreign-architecture jails", Required: false,
		Providers:   providers,
		Description: "running aarch64 or riscv64 jails needs an interpreter registered with binmiscctl",
		Run: func(_ context.Context, s System) Result {
			if s.GOOS() != "freebsd" {
				return Result{Status: StatusSkip, Detail: "jails are FreeBSD-only"}
			}

			var installed []string
			for _, arch := range []string{"aarch64", "riscv64"} {
				if s.HasCommand("qemu-" + arch + "-static") {
					installed = append(installed, arch)
				}
			}
			if len(installed) == 0 {
				return Result{Status: StatusSkip, Detail: "qemu-user-static is not installed"}
			}

			if !s.KernelModuleLoaded("imgact_binmisc") {
				return Result{
					Status: StatusWarn,
					Detail: "qemu-user-static is installed but imgact_binmisc is not loaded",
					Remediation: "kldload imgact_binmisc, and add imgact_binmisc_load=\"YES\" to " +
						"/boot/loader.conf to keep it across reboots",
				}
			}

			activators := s.BinmiscActivators()
			registered := make(map[string]bool, len(activators))
			for _, name := range activators {
				registered[name] = true
			}

			var missing []string
			for _, arch := range installed {
				// binmiscctl takes any name for an activator; arm64 is the
				// other spelling of the same machine.
				if !registered[arch] && (arch != "aarch64" || !registered["arm64"]) {
					missing = append(missing, arch)
				}
			}
			if len(missing) == 0 {
				return Result{Status: StatusOK, Detail: "interpreters registered for " + strings.Join(installed, ", ")}
			}

			return Result{
				Status: StatusWarn,
				Detail: "no interpreter registered for " + strings.Join(missing, ", ") +
					"; a jail of that architecture will be created and will not start",
				Remediation: "register one per architecture, for example:\n" +
					"    binmiscctl add aarch64 --interpreter /usr/local/bin/qemu-aarch64-static \\\n" +
					"        --magic \"\\x7fELF\\x02\\x01\\x01\\x00\\x00\\x00\\x00\\x00\\x00\\x00\\x00\\x00\\x02\\x00\\xb7\\x00\" \\\n" +
					"        --mask \"\\xff\\xff\\xff\\xff\\xff\\xff\\xff\\x00\\xff\\xff\\xff\\xff\\xff\\xff\\xff\\xff\\xfe\\xff\\xff\\xff\" \\\n" +
					"        --size 20 --set-enabled\n" +
					"riscv64 differs only in the machine byte, \\xf3. Check with: binmiscctl list",
			}
		},
	}
}

func jailChecks() []Check {
	p := []string{ProviderJail}
	return []Check{
		crossArchJailCheck(p),
		commandCheck("cmd-jail", "jail", "creating and running jails", true, p),
		commandCheck("cmd-jls", "jls", "listing jails", true, p),
		commandCheck("cmd-jexec", "jexec", "console/exec into jails", true, p),
		commandCheck("cmd-ifconfig", "ifconfig", "VNET/epair/bridge networking", true, p),
		commandCheck("cmd-route", "route", "jail default routes", true, p),
		sysctlBoolCheck("ip-forward", "net.inet.ip.forwarding", "route jail VNET traffic to the internet", true, true, p),
		sysctlBoolCheck("racct", "kern.racct.enable", "RCTL resource limits for jails (requires reboot)", false, false, p),
		moduleCheck("kmod-linux", "linux64", "run Linux jails (linuxulator)", false, p),
		commandCheck("cmd-debootstrap", "debootstrap", "populate Debian/Ubuntu Linux jails", false, p),
		debootstrapVerifierCheck(p),
		linuxKeyringCheck(p),
	}
}

// linuxKeyringCheck covers the archive keyring debootstrap verifies packages
// against. Hospitus refuses to bootstrap without one, so a host missing it can
// install debootstrap and its verifier and still not create a Linux jail.
//
// The two distributions differ on FreeBSD: ubuntu-keyring is a package, while
// Debian's archive keyring is not in ports and has to be placed by hand.
func linuxKeyringCheck(providers []string) Check {
	const (
		debianKeyring = "/usr/local/share/keyrings/debian-archive-keyring.gpg"
		ubuntuKeyring = "/usr/local/share/keyrings/ubuntu-archive-keyring.gpg"
	)
	return Check{
		ID: "linux-archive-keyring", Name: "Linux jail archive keyring",
		Description: "verifying base packages when populating a Linux jail",
		Required:    false, Providers: providers,
		Run: func(_ context.Context, s System) Result {
			if freebsdOnly(providers) && s.GOOS() != "freebsd" {
				return Result{Status: StatusSkip, Detail: "not applicable on " + s.GOOS()}
			}
			if !s.HasCommand("debootstrap") {
				return Result{Status: StatusSkip, Detail: "debootstrap is not installed"}
			}

			debian, ubuntu := s.PathExists(debianKeyring), s.PathExists(ubuntuKeyring)
			switch {
			case debian && ubuntu:
				return Result{Status: StatusOK, Detail: "Debian and Ubuntu keyrings found"}
			// Warn, not OK: half the capability is missing and there is
			// something to do about it. An OK carrying a remediation is a line
			// an operator scanning a column of [ OK ] never reads.
			case ubuntu:
				return Result{
					Status: StatusWarn, Detail: "Ubuntu keyring found; Debian jails will not bootstrap",
					Remediation: "for Debian jails, place its archive keyring at " + debianKeyring +
						" — FreeBSD has no port for it",
				}
			case debian:
				return Result{
					Status: StatusWarn, Detail: "Debian keyring found; Ubuntu jails will not bootstrap",
					Remediation: "for Ubuntu jails: pkg install ubuntu-keyring",
				}
			}
			return Result{
				Status: StatusWarn, Detail: "no archive keyring found",
				Remediation: "pkg install ubuntu-keyring for Ubuntu jails; Debian's archive keyring is not " +
					"packaged and has to be placed at " + debianKeyring,
			}
		},
	}
}

// debootstrapVerifierCheck covers the tool debootstrap uses to verify the
// release signature. Having debootstrap alone is not enough: without one of
// these it aborts with "none of sopv, sqv or gpgv2 are installed", and the
// failure only appears when a Linux jail is created.
//
// It looks for gpgv2 specifically, which is what debootstrap invokes. FreeBSD's
// gnupg package installs the binary as gpgv, so having gnupg is not sufficient
// on its own.
func debootstrapVerifierCheck(providers []string) Check {
	return Check{
		ID: "cmd-debootstrap-verifier", Name: "debootstrap signature verifier",
		Description: "verifying the release signature when populating a Linux jail",
		Required:    false, Providers: providers,
		Run: func(_ context.Context, s System) Result {
			if freebsdOnly(providers) && s.GOOS() != "freebsd" {
				return Result{Status: StatusSkip, Detail: "not applicable on " + s.GOOS()}
			}
			if !s.HasCommand("debootstrap") {
				return Result{Status: StatusSkip, Detail: "debootstrap is not installed"}
			}
			for _, b := range []string{"sopv", "sqv", "gpgv2"} {
				if s.HasCommand(b) {
					return Result{Status: StatusOK, Detail: b + " found"}
				}
			}
			return Result{
				Status: StatusWarn, Detail: "none of sopv, sqv or gpgv2 found",
				Remediation: "install gnupg and expose its verifier under the name debootstrap looks for: " +
					"pkg install gnupg && ln -s /usr/local/bin/gpgv /usr/local/bin/gpgv2",
			}
		},
	}
}

func bhyveChecks() []Check {
	p := []string{ProviderBhyve}
	return []Check{
		commandCheck("cmd-bhyve", "bhyve", "running bhyve VMs", true, p),
		commandCheck("cmd-bhyvectl", "bhyvectl", "controlling bhyve VMs", true, p),
		moduleCheck("kmod-vmm", "vmm", "the bhyve hypervisor kernel module", true, p),
		moduleCheck("kmod-nmdm", "nmdm", "bhyve serial console (null-modem)", false, p),
		commandCheck("cmd-dnsmasq", "dnsmasq", "bhyve NAT networking (type=nat)", false, p),
		commandCheck("cmd-swtpm", "swtpm", "TPM 2.0 for Windows 11 guests", false, p),
		uefiFirmwareCheck(p),
	}
}

// uefiFirmwareCheck looks for the firmware every bhyve VM boots from.
//
// Without it bhyve refuses to start anything — "no bootrom was configured",
// exit 4, before the guest exists — so a host missing it can run no VM at all.
// bhyve, bhyvectl and vmm can all be present without it: the firmware ships in
// a package of its own.
//
// hospitusd resolves the path once at startup, so installing the package while it
// runs changes nothing until it is restarted. The remediation says so.
func uefiFirmwareCheck(providers []string) Check {
	paths := []string{
		"/usr/local/share/uefi-firmware/BHYVE_UEFI_CODE.fd",
		"/usr/local/share/uefi-firmware/BHYVE_UEFI.fd",
		"/usr/local/share/bhyve-firmware/BHYVE_UEFI.fd",
	}

	return Check{
		ID: "bhyve-uefi-firmware", Name: "bhyve UEFI firmware", Required: true, Providers: providers,
		Description: "the bootrom bhyve loads a guest with; without it no VM starts",
		Run: func(_ context.Context, s System) Result {
			if freebsdOnly(providers) && s.GOOS() != "freebsd" {
				return Result{Status: StatusSkip, Detail: "not applicable on " + s.GOOS()}
			}
			for _, path := range paths {
				if s.PathExists(path) {
					return Result{Status: StatusOK, Detail: "found " + path}
				}
			}
			return Result{
				Status: StatusFail,
				Detail: "no BHYVE_UEFI firmware found; bhyve fails with \"no bootrom was configured\"",
				Remediation: "pkg install edk2-bhyve\n" +
					"then restart hospitusd: the firmware path is resolved at startup",
			}
		},
	}
}

// cloudInitChecks covers the tool that builds the seed ISO a cloud image reads
// its configuration from.
//
// Without it a manifest with a cloud_init section fails at create time, after
// the image has been downloaded — a prerequisite the doctor never mentioned.
func cloudInitChecks() []Check {
	p := []string{ProviderQEMU, ProviderBhyve}
	return []Check{
		{
			ID: "cmd-mkisofs", Name: "mkisofs", Required: false, Providers: p,
			Description: "building the cloud-init seed ISO for cloud images",
			Run: func(_ context.Context, s System) Result {
				if s.HasCommand("mkisofs") || s.HasCommand("genisoimage") || s.HasCommand("xorrisofs") {
					return Result{Status: StatusOK, Detail: "an ISO builder is available"}
				}
				return Result{
					Status: StatusWarn, Detail: "no ISO builder found (mkisofs, genisoimage or xorrisofs)",
					Remediation: "needed only for cloud-init images: FreeBSD: pkg install cdrtools; macOS: brew install cdrtools",
				}
			},
		},
	}
}

func qemuChecks() []Check {
	p := []string{ProviderQEMU}
	return []Check{
		{
			ID: "cmd-qemu-system", Name: "qemu-system-*", Required: true, Providers: p,
			Description: "running QEMU VMs (any architecture: x86_64/aarch64/i386/riscv64)",
			Run: func(_ context.Context, s System) Result {
				for _, b := range []string{"qemu-system-x86_64", "qemu-system-aarch64", "qemu-system-i386", "qemu-system-riscv64"} {
					if s.HasCommand(b) {
						return Result{Status: StatusOK, Detail: b + " found"}
					}
				}
				return Result{
					Status: StatusFail, Detail: "no qemu-system-* binary found",
					Remediation: "install QEMU (FreeBSD: pkg install qemu; macOS: brew install qemu)",
				}
			},
		},
		commandCheck("cmd-qemu-img", "qemu-img", "QEMU disk image management", true, p),
	}
}

// podmanDefaultNetwork is the subnet Podman gives its default bridge. A host
// that changed it will not match, and gets a warning it can ignore rather than
// a silence it cannot.
const podmanDefaultNetwork = "10.88.0.0/16"

// podmanPFCheck warns when a default-deny PF ruleset has no rule for the
// Podman network.
//
// The symptom hides well: the container starts, the server inside it listens,
// and every connection to it times out. PF blocks the packet on its way out of
// a bridge no rule names, and the local socket is told "Permission denied".
// Nothing in Hospitus reports it, because nothing in Hospitus is involved — the
// Podman network belongs to Podman, and Hospitus never edits pf.conf.
//
// The check is deliberately loose. It asks two questions — does the ruleset end
// in a default block, and does any rule mention the Podman network — and warns
// only when the first is yes and the second is no. A host that permits the
// network some other way still gets a warning it can ignore; a host that would
// silently swallow its containers' traffic gets told, which is the case worth
// catching.
func podmanPFCheck(providers []string) Check {
	return Check{
		ID: "pf-podman-network", Name: "PF permits the Podman network",
		Description: "a default-deny ruleset drops container traffic unless the Podman network is passed",
		Required:    false, Providers: providers,
		Run: func(_ context.Context, s System) Result {
			if s.GOOS() != "freebsd" {
				return Result{Status: StatusSkip, Detail: "PF applies to FreeBSD"}
			}
			if !s.HasCommand("pfctl") || !s.ServiceEnabled("pf_enable") {
				return Result{Status: StatusSkip, Detail: "PF is not enabled"}
			}
			if !s.IsRoot() {
				// pfctl answers nothing useful unprivileged. Reporting what was
				// not seen would be a finding about the reader's privileges
				// dressed up as one about the host.
				return Result{
					Status: StatusSkip,
					Detail: "reading the PF ruleset needs root — rerun with doas",
				}
			}

			rules := s.PFFilterRules()
			if len(rules) == 0 {
				return Result{Status: StatusSkip, Detail: "no PF ruleset loaded"}
			}

			var defaultDeny, mentionsPodman bool
			for _, rule := range rules {
				if strings.HasPrefix(rule, "block") && strings.HasSuffix(rule, " all") {
					defaultDeny = true
				}
				if strings.Contains(rule, podmanDefaultNetwork) {
					mentionsPodman = true
				}
			}

			switch {
			case !defaultDeny:
				return Result{Status: StatusOK, Detail: "ruleset does not deny by default"}
			case mentionsPodman:
				return Result{Status: StatusOK, Detail: "a rule covers " + podmanDefaultNetwork}
			}

			return Result{
				Status: StatusWarn,
				Detail: "ruleset denies by default and no rule mentions " + podmanDefaultNetwork,
				Remediation: "containers will start and serve nothing. Add to pf.conf, using the subnet " +
					"'podman network inspect' reports:\n" +
					"    pass quick inet from " + podmanDefaultNetwork + " to any keep state\n" +
					"    pass quick inet from any to " + podmanDefaultNetwork + " keep state\n" +
					"then: service pf reload",
			}
		},
	}
}

// pfIncludeFile is a ruleset Hospitus never writes, for rules about networks it
// does not manage. pf.conf includes it, so the rules survive what the hospitus
// anchor does not: the daemon rewrites that anchor on every instance it creates
// or destroys, and anything added there by hand disappears without a message.
const pfIncludeFile = "/usr/local/etc/hospitus/pf.conf"

// pfIncludeFileCheck reports whether that file exists.
//
// Order matters and the wrong order takes the firewall down: pf refuses a
// ruleset whose include names a file that is not there, so `service pf reload`
// fails and the host keeps whatever was loaded before. The file has to exist
// first — empty is fine — and only then may the include line be added.
//
// Creating the file is safe on its own — an include nobody declared yet changes
// nothing — so the fix does that and leaves the include line to the operator.
func pfIncludeFileCheck(providers []string) Check {
	return Check{
		ID: "pf-include-file", Name: "Hospitus PF include file", Required: false,
		Providers:   providers,
		Description: "a ruleset of your own that survives the daemon rewriting its anchor",
		Run: func(_ context.Context, s System) Result {
			if s.GOOS() != "freebsd" {
				return Result{Status: StatusSkip, Detail: "PF applies to FreeBSD"}
			}
			if !s.HasCommand("pfctl") {
				return Result{Status: StatusSkip, Detail: "pfctl not found"}
			}
			if s.PathExists(pfIncludeFile) {
				return Result{Status: StatusOK, Detail: pfIncludeFile + " exists"}
			}
			return Result{
				Status: StatusWarn, Detail: pfIncludeFile + " does not exist",
				Remediation: "create it before referring to it, or pf will refuse the whole ruleset:\n" +
					"    install -m 0644 /dev/null " + pfIncludeFile + "\n" +
					"then add this beside anchor \"hospitus\" in pf.conf:\n" +
					"    include \"" + pfIncludeFile + "\"",
			}
		},
		Fix: func(ctx context.Context, s System) error {
			// Creating an empty file only. pf.conf is the operator's, and Hospitus
			// does not write it.
			return s.Run(ctx, "install", "-m", "0644", "/dev/null", pfIncludeFile)
		},
	}
}

func podmanChecks() []Check {
	p := []string{ProviderPodman}
	return []Check{
		podmanPFCheck(p),
		{
			ID: "cmd-podman", Name: "podman", Required: true, Providers: p,
			Description: "running OCI containers",
			Run: func(_ context.Context, s System) Result {
				if s.HasCommand("podman") || s.HasCommand("podman-remote") {
					return Result{Status: StatusOK, Detail: "podman found"}
				}
				return Result{
					Status: StatusFail, Detail: "podman not found",
					Remediation: "install Podman (FreeBSD: pkg install podman; macOS: brew install podman && podman machine init)",
				}
			},
		},
	}
}

// macOSChecks covers the two providers backed by Virtualization.framework.
//
// Both are macOS-only and optional: a Mac without them still runs QEMU and
// Podman, so a missing tool is a warning rather than a failure, and every other
// platform skips these entirely.
func macOSChecks() []Check {
	return []Check{
		{
			ID: "cmd-vfkit", Name: "vfkit", Required: false, Providers: []string{ProviderVFKit},
			Description: "macOS virtual machines through Virtualization.framework",
			Run: func(_ context.Context, s System) Result {
				if s.GOOS() != "darwin" {
					return Result{Status: StatusSkip, Detail: "vfkit applies to macOS"}
				}
				if s.HasCommand("vfkit") {
					return Result{Status: StatusOK, Detail: "vfkit found"}
				}
				return Result{
					Status: StatusWarn, Detail: "vfkit not found",
					Remediation: "install it to run accelerated VMs without QEMU: brew install vfkit",
				}
			},
		},
		{
			ID: "cmd-container", Name: "container", Required: false, Providers: []string{ProviderContainer},
			Description: "Linux containers through Apple's container tool",
			Run: func(_ context.Context, s System) Result {
				if s.GOOS() != "darwin" {
					return Result{Status: StatusSkip, Detail: "Apple's container tool applies to macOS"}
				}
				if !s.HasCommand("container") {
					return Result{
						Status: StatusWarn, Detail: "container not found",
						Remediation: "install it to run OCI containers without Podman: brew install container (needs macOS 26+ on Apple silicon)",
					}
				}
				return Result{Status: StatusOK, Detail: "container found (start its service with: container system start)"}
			},
		},
	}
}

func networkChecks() []Check {
	pf := []string{ProviderJail, ProviderBhyve}
	return []Check{
		pfIncludeFileCheck([]string{ProviderJail, ProviderBhyve, ProviderPodman}),
		commandCheck("cmd-pfctl", "pfctl", "PF NAT and port forwarding", false, pf),
		{
			ID: "pf-enabled", Name: "PF firewall enabled", Required: false, Providers: pf,
			Description: "PF must be enabled for jail/bhyve NAT and port forwarding.",
			Run: func(_ context.Context, s System) Result {
				if s.GOOS() != "freebsd" {
					return Result{Status: StatusSkip, Detail: "PF applies to FreeBSD"}
				}
				if s.ServiceEnabled("pf_enable") {
					return Result{Status: StatusOK, Detail: "pf_enable=YES"}
				}
				return Result{
					Status: StatusWarn, Detail: "PF is not enabled",
					Remediation: "sysrc pf_enable=YES && service pf start",
				}
			},
			// Enabling PF (sysrc + service) does NOT touch pf.conf; Hospitus manages
			// only its own anchors, so this is safe to auto-fix.
			Fix: func(ctx context.Context, s System) error {
				if err := s.Run(ctx, "sysrc", "pf_enable=YES"); err != nil {
					return err
				}
				return s.Run(ctx, "service", "pf", "start")
			},
		},
		{
			ID: "pf-anchor", Name: "Hospitus PF anchor declared", Required: false, Providers: pf,
			Description: "pf.conf must declare the hospitus anchor so Hospitus can load NAT/rdr rules into it.",
			Run: func(_ context.Context, s System) Result {
				if s.GOOS() != "freebsd" {
					return Result{Status: StatusSkip, Detail: "PF applies to FreeBSD"}
				}
				// Reading the loaded ruleset is enough to tell whether the anchor is
				// declared. This stays check-only: Hospitus reports what is missing and
				// never edits the operator's pf.conf.
				if !s.IsRoot() {
					// Unprivileged, pfctl reports nothing, and this check would
					// tell the operator to add anchor lines that are already
					// there — a finding about the reader, not the host.
					return Result{
						Status: StatusSkip,
						Detail: "reading the PF ruleset needs root — rerun with doas",
					}
				}
				if s.PFAnchorDeclared("hospitus") {
					return Result{Status: StatusOK, Detail: "hospitus anchor declared in the loaded ruleset"}
				}
				return Result{
					Status: StatusWarn,
					Detail: "hospitus anchor not declared in the loaded PF ruleset",
					Remediation: "add these lines to /etc/pf.conf (Hospitus never edits pf.conf for you):\n" +
						"    nat-anchor \"hospitus\"\n    rdr-anchor \"hospitus\"\n    anchor \"hospitus\"\n" +
						"then: pfctl -f /etc/pf.conf",
				}
			},
		},
	}
}
