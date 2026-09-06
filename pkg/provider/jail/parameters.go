package jail

import (
	"fmt"
	"strings"

	"github.com/hospitus/hospitus/pkg/validation"
)

// JailParameters represents all configurable jail(8) parameters
// See jail(8) man page for complete documentation
type JailParameters struct {
	// Security parameters - control what the jail can do
	AllowRawSockets     bool `json:"allow.raw_sockets"`             // Allow raw sockets (ping, traceroute)
	AllowSysVIPC        bool `json:"allow.sysvipc"`                 // Allow System V IPC (PostgreSQL, etc.)
	AllowMount          bool `json:"allow.mount"`                   // Allow mounting filesystems
	AllowMountDevfs     bool `json:"allow.mount.devfs"`             // Allow mounting devfs
	AllowMountNullfs    bool `json:"allow.mount.nullfs"`            // Allow mounting nullfs
	AllowMountProcfs    bool `json:"allow.mount.procfs"`            // Allow mounting procfs
	AllowMountTmpfs     bool `json:"allow.mount.tmpfs"`             // Allow mounting tmpfs
	AllowMountZFS       bool `json:"allow.mount.zfs"`               // Allow mounting ZFS datasets
	AllowMountFdescfs   bool `json:"allow.mount.fdescfs"`           // Allow mounting fdescfs
	AllowMountLinprocfs bool `json:"allow.mount.linprocfs"`         // Allow mounting linprocfs
	AllowMountLinsysfs  bool `json:"allow.mount.linsysfs"`          // Allow mounting linsysfs
	AllowMountFusefs    bool `json:"allow.mount.fusefs"`            // Allow mounting fusefs
	AllowVMM            bool `json:"allow.vmm"`                     // Allow vmm operations (bhyve in jail)
	AllowMlock          bool `json:"allow.mlock"`                   // Allow locking memory
	AllowReservedPorts  bool `json:"allow.reserved_ports"`          // Allow binding to reserved ports
	AllowChflags        bool `json:"allow.chflags"`                 // Allow changing file flags
	AllowQuotas         bool `json:"allow.quotas"`                  // Allow filesystem quotas
	AllowSocketAF       bool `json:"allow.socket_af"`               // Allow access to other socket address families
	AllowSetHostname    bool `json:"allow.set_hostname"`            // Allow setting hostname
	AllowNfsd           bool `json:"allow.nfsd"`                    // Allow NFS server
	AllowSuser          bool `json:"allow.suser"`                   // Allow superuser inside jail
	AllowReadMsgbuf     bool `json:"allow.read_msgbuf"`             // Allow reading kernel message buffer
	AllowUnprivProcDbg  bool `json:"allow.unprivileged_proc_debug"` // Allow unprivileged process debugging
	AllowExtattr        bool `json:"allow.extattr"`                 // Allow extended attributes
	AllowAdjtime        bool `json:"allow.adjtime"`                 // Allow adjusting time (small adjustments)
	AllowSettime        bool `json:"allow.settime"`                 // Allow setting time (requires allow.adjtime)
	AllowRouting        bool `json:"allow.routing"`                 // Allow routing table modifications
	AllowSetaudit       bool `json:"allow.setaudit"`                // Allow audit settings modifications

	// Lifecycle hooks - scripts run at various stages
	ExecPrestart   string `json:"exec.prestart"`    // Command to run before starting jail
	ExecStart      string `json:"exec.start"`       // Command to run to start jail (default: /bin/sh /etc/rc)
	ExecPoststart  string `json:"exec.poststart"`   // Command to run after starting jail
	ExecPrestop    string `json:"exec.prestop"`     // Command to run before stopping jail
	ExecStop       string `json:"exec.stop"`        // Command to run to stop jail (default: /bin/sh /etc/rc.shutdown)
	ExecPoststop   string `json:"exec.poststop"`    // Command to run after stopping jail
	ExecClean      bool   `json:"exec.clean"`       // Run commands in clean environment
	ExecCreated    string `json:"exec.created"`     // Command to run after jail is created
	ExecPrepare    string `json:"exec.prepare"`     // Command to run to prepare jail
	ExecRelease    string `json:"exec.release"`     // Command to run to release jail
	ExecConsolelog string `json:"exec.consolelog"`  // File to log console output
	ExecFib        int    `json:"exec.fib"`         // FIB (routing table) for commands
	ExecSystemUser string `json:"exec.system_user"` // User to run system commands as (on host)
	ExecJailUser   string `json:"exec.jail_user"`   // User to run commands inside jail as

	// Timeout settings
	ExecTimeout int `json:"exec.timeout"` // Maximum time for exec commands (seconds)
	StopTimeout int `json:"stop.timeout"` // Maximum time to wait for jail to stop (seconds)

	// DevFS and mount configuration
	MountDevfs     bool   `json:"mount.devfs"`     // Mount devfs inside jail
	DevfsRuleset   int    `json:"devfs_ruleset"`   // DevFS ruleset number (default: 4)
	MountFdescfs   bool   `json:"mount.fdescfs"`   // Mount fdescfs inside jail
	MountProcfs    bool   `json:"mount.procfs"`    // Mount procfs inside jail
	MountLinprocfs bool   `json:"mount.linprocfs"` // Mount linprocfs inside jail
	MountLinsysfs  bool   `json:"mount.linsysfs"`  // Mount linsysfs inside jail
	MountTmpfs     bool   `json:"mount.tmpfs"`     // Mount tmpfs inside jail
	MountFstab     string `json:"mount.fstab"`     // Path to fstab file for additional mounts
	Fdescfs        string `json:"fdescfs"`         // fdescfs mount point

	// IP configuration - controls IP address handling
	IP4         string `json:"ip4"`          // IPv4 handling: "inherit", "new", "disable"
	IP6         string `json:"ip6"`          // IPv6 handling: "inherit", "new", "disable"
	IP4Saddrsel bool   `json:"ip4.saddrsel"` // Enable IPv4 source address selection
	IP6Saddrsel bool   `json:"ip6.saddrsel"` // Enable IPv6 source address selection

	// Process and behavior settings
	Persist       bool `json:"persist"`        // Keep jail running even if no processes
	ChildrenMax   int  `json:"children.max"`   // Maximum number of child jails
	EnforceStatfs int  `json:"enforce_statfs"` // Restrict statfs(2): 0=no, 1=df, 2=all

	// System V IPC settings (when allow.sysvipc is true)
	SysvMsg int `json:"sysvmsg"` // SYSV message queue access: 0=inherit, 1=new, 2=disable
	SysvSem int `json:"sysvsem"` // SYSV semaphore access: 0=inherit, 1=new, 2=disable
	SysvShm int `json:"sysvshm"` // SYSV shared memory access: 0=inherit, 1=new, 2=disable

	// Securelevel
	Securelevel int `json:"securelevel"` // Securelevel of the jail (-1 to 3)

	// Linux compatibility
	LinuxOsrelease string `json:"linux.osrelease"` // Linux kernel version to emulate
	LinuxOsname    string `json:"linux.osname"`    // Linux OS name to emulate

	// OS compatibility - FreeBSD version spoofing
	Osrelease string `json:"osrelease"` // OS release string to report
	Osreldate int    `json:"osreldate"` // OS release date to report (e.g., 1400097)

	// IP settings (legacy - prefer using NetworkSpec)
	IPHostname bool `json:"ip_hostname"` // Set hostname to jail IP

	// Host settings
	HostHostname   string `json:"host.hostname"`   // Hostname for jail
	HostHostUUID   string `json:"host.hostuuid"`   // Host UUID for jail
	HostHostid     string `json:"host.hostid"`     // Host ID for jail
	HostDomainname string `json:"host.domainname"` // Domain name for jail

	// Depending settings - controls what jail sees of parent
	Depending bool `json:"depending"` // Whether this jail depends on another
}

// DefaultJailParameters returns default parameter values
func DefaultJailParameters() JailParameters {
	return JailParameters{
		// Security defaults - restrictive
		AllowRawSockets:     false,
		AllowSysVIPC:        false,
		AllowMount:          false,
		AllowMountDevfs:     false,
		AllowMountNullfs:    false,
		AllowMountProcfs:    false,
		AllowMountTmpfs:     false,
		AllowMountZFS:       false,
		AllowMountFdescfs:   false,
		AllowMountLinprocfs: false,
		AllowMountLinsysfs:  false,
		AllowMountFusefs:    false,
		AllowVMM:            false,
		AllowMlock:          false,
		AllowReservedPorts:  false,
		AllowChflags:        false,
		AllowQuotas:         false,
		AllowSocketAF:       false,
		AllowSetHostname:    true, // Usually safe and often needed
		AllowNfsd:           false,
		AllowSuser:          true, // Usually needed for normal operation
		AllowReadMsgbuf:     false,
		AllowUnprivProcDbg:  false,
		AllowExtattr:        false,
		AllowAdjtime:        false,
		AllowSettime:        false,
		AllowRouting:        false,
		AllowSetaudit:       false,

		// Lifecycle hooks defaults
		ExecStart: "/bin/sh /etc/rc",
		ExecStop:  "/bin/sh /etc/rc.shutdown",
		ExecClean: false,
		ExecFib:   0,

		// Timeouts
		ExecTimeout: 60,
		StopTimeout: 30,

		MountDevfs:   true,
		DevfsRuleset: 4, // Default safe ruleset
		MountFdescfs: false,
		MountProcfs:  false,
		MountTmpfs:   false,

		// IP configuration
		IP4:         "new", // New IP stack for the jail
		IP6:         "new", // New IP stack for the jail
		IP4Saddrsel: true,  // Enable source address selection
		IP6Saddrsel: true,  // Enable source address selection

		// Process settings
		Persist:       true,
		ChildrenMax:   0,
		EnforceStatfs: 2,

		// SysV IPC
		SysvMsg: 0,
		SysvSem: 0,
		SysvShm: 0,

		// Securelevel
		Securelevel: -1, // Inherit from host
	}
}

// ValidateJailParameters validates the parameters
func ValidateJailParameters(params JailParameters) error {
	// Validate timeout values
	if params.ExecTimeout < 0 {
		return fmt.Errorf("exec.timeout must be non-negative, got %d", params.ExecTimeout)
	}
	if params.StopTimeout < 0 {
		return fmt.Errorf("stop.timeout must be non-negative, got %d", params.StopTimeout)
	}

	// Validate children.max
	if params.ChildrenMax < 0 {
		return fmt.Errorf("children.max must be non-negative, got %d", params.ChildrenMax)
	}

	// Validate enforce_statfs
	if params.EnforceStatfs < 0 || params.EnforceStatfs > 2 {
		return fmt.Errorf("enforce_statfs must be 0, 1, or 2, got %d", params.EnforceStatfs)
	}

	// Validate SysV IPC settings
	if params.SysvMsg < 0 || params.SysvMsg > 2 {
		return fmt.Errorf("sysvmsg must be 0, 1, or 2, got %d", params.SysvMsg)
	}
	if params.SysvSem < 0 || params.SysvSem > 2 {
		return fmt.Errorf("sysvsem must be 0, 1, or 2, got %d", params.SysvSem)
	}
	if params.SysvShm < 0 || params.SysvShm > 2 {
		return fmt.Errorf("sysvshm must be 0, 1, or 2, got %d", params.SysvShm)
	}

	// Validate securelevel
	if params.Securelevel < -1 || params.Securelevel > 3 {
		return fmt.Errorf("securelevel must be between -1 and 3, got %d", params.Securelevel)
	}

	// Validate devfs_ruleset
	if params.DevfsRuleset < 0 {
		return fmt.Errorf("devfs_ruleset must be non-negative, got %d", params.DevfsRuleset)
	}

	// Validate exec.fib
	if params.ExecFib < 0 {
		return fmt.Errorf("exec.fib must be non-negative, got %d", params.ExecFib)
	}

	// Validate IP configuration
	validIPModes := map[string]bool{"inherit": true, "new": true, "disable": true, "": true}
	if !validIPModes[params.IP4] {
		return fmt.Errorf("ip4 must be 'inherit', 'new', or 'disable', got %s", params.IP4)
	}
	if !validIPModes[params.IP6] {
		return fmt.Errorf("ip6 must be 'inherit', 'new', or 'disable', got %s", params.IP6)
	}

	// Validate osreldate
	if params.Osreldate < 0 {
		return fmt.Errorf("osreldate must be non-negative, got %d", params.Osreldate)
	}

	return nil
}

// ToJailArgs converts parameters to jail(8) command line arguments
func (p *JailParameters) ToJailArgs() ([]string, error) {
	var args []string

	// Security parameters - allow.*
	args = append(args,
		fmt.Sprintf("allow.raw_sockets=%s", boolToJailValue(p.AllowRawSockets)),
		fmt.Sprintf("allow.sysvipc=%s", boolToJailValue(p.AllowSysVIPC)),
		fmt.Sprintf("allow.mount=%s", boolToJailValue(p.AllowMount)),
		fmt.Sprintf("allow.mount.devfs=%s", boolToJailValue(p.AllowMountDevfs)),
		fmt.Sprintf("allow.mount.nullfs=%s", boolToJailValue(p.AllowMountNullfs)),
		fmt.Sprintf("allow.mount.procfs=%s", boolToJailValue(p.AllowMountProcfs)),
		fmt.Sprintf("allow.mount.tmpfs=%s", boolToJailValue(p.AllowMountTmpfs)),
		fmt.Sprintf("allow.mount.zfs=%s", boolToJailValue(p.AllowMountZFS)),
		fmt.Sprintf("allow.mount.fdescfs=%s", boolToJailValue(p.AllowMountFdescfs)),
		fmt.Sprintf("allow.mount.linprocfs=%s", boolToJailValue(p.AllowMountLinprocfs)),
		fmt.Sprintf("allow.mount.linsysfs=%s", boolToJailValue(p.AllowMountLinsysfs)),
		fmt.Sprintf("allow.mount.fusefs=%s", boolToJailValue(p.AllowMountFusefs)),
		fmt.Sprintf("allow.vmm=%s", boolToJailValue(p.AllowVMM)),
		fmt.Sprintf("allow.mlock=%s", boolToJailValue(p.AllowMlock)),
		fmt.Sprintf("allow.reserved_ports=%s", boolToJailValue(p.AllowReservedPorts)),
		fmt.Sprintf("allow.chflags=%s", boolToJailValue(p.AllowChflags)),
		fmt.Sprintf("allow.quotas=%s", boolToJailValue(p.AllowQuotas)),
		fmt.Sprintf("allow.socket_af=%s", boolToJailValue(p.AllowSocketAF)),
		fmt.Sprintf("allow.set_hostname=%s", boolToJailValue(p.AllowSetHostname)),
		fmt.Sprintf("allow.nfsd=%s", boolToJailValue(p.AllowNfsd)),
		fmt.Sprintf("allow.suser=%s", boolToJailValue(p.AllowSuser)),
		fmt.Sprintf("allow.read_msgbuf=%s", boolToJailValue(p.AllowReadMsgbuf)),
		fmt.Sprintf("allow.unprivileged_proc_debug=%s", boolToJailValue(p.AllowUnprivProcDbg)),
		fmt.Sprintf("allow.extattr=%s", boolToJailValue(p.AllowExtattr)),
		fmt.Sprintf("allow.adjtime=%s", boolToJailValue(p.AllowAdjtime)),
		fmt.Sprintf("allow.settime=%s", boolToJailValue(p.AllowSettime)),
		fmt.Sprintf("allow.routing=%s", boolToJailValue(p.AllowRouting)),
		fmt.Sprintf("allow.setaudit=%s", boolToJailValue(p.AllowSetaudit)),
	)

	// Lifecycle hooks - only add if non-empty
	if p.ExecPrestart != "" {
		if err := validation.ValidateExecCommand(p.ExecPrestart); err != nil {
			return nil, fmt.Errorf("invalid exec.prestart command: %w", err)
		}
		args = append(args, fmt.Sprintf("exec.prestart=%s", p.ExecPrestart))
	}
	if p.ExecStart != "" {
		if err := validation.ValidateExecCommand(p.ExecStart); err != nil {
			return nil, fmt.Errorf("invalid exec.start command: %w", err)
		}
		args = append(args, fmt.Sprintf("exec.start=%s", p.ExecStart))
	}
	if p.ExecPoststart != "" {
		if err := validation.ValidateExecCommand(p.ExecPoststart); err != nil {
			return nil, fmt.Errorf("invalid exec.poststart command: %w", err)
		}
		args = append(args, fmt.Sprintf("exec.poststart=%s", p.ExecPoststart))
	}
	if p.ExecPrestop != "" {
		if err := validation.ValidateExecCommand(p.ExecPrestop); err != nil {
			return nil, fmt.Errorf("invalid exec.prestop command: %w", err)
		}
		args = append(args, fmt.Sprintf("exec.prestop=%s", p.ExecPrestop))
	}
	if p.ExecStop != "" {
		if err := validation.ValidateExecCommand(p.ExecStop); err != nil {
			return nil, fmt.Errorf("invalid exec.stop command: %w", err)
		}
		args = append(args, fmt.Sprintf("exec.stop=%s", p.ExecStop))
	}
	if p.ExecPoststop != "" {
		if err := validation.ValidateExecCommand(p.ExecPoststop); err != nil {
			return nil, fmt.Errorf("invalid exec.poststop command: %w", err)
		}
		args = append(args, fmt.Sprintf("exec.poststop=%s", p.ExecPoststop))
	}
	if p.ExecCreated != "" {
		if err := validation.ValidateExecCommand(p.ExecCreated); err != nil {
			return nil, fmt.Errorf("invalid exec.created command: %w", err)
		}
		args = append(args, fmt.Sprintf("exec.created=%s", p.ExecCreated))
	}
	if p.ExecPrepare != "" {
		if err := validation.ValidateExecCommand(p.ExecPrepare); err != nil {
			return nil, fmt.Errorf("invalid exec.prepare command: %w", err)
		}
		args = append(args, fmt.Sprintf("exec.prepare=%s", p.ExecPrepare))
	}
	if p.ExecRelease != "" {
		if err := validation.ValidateExecCommand(p.ExecRelease); err != nil {
			return nil, fmt.Errorf("invalid exec.release command: %w", err)
		}
		args = append(args, fmt.Sprintf("exec.release=%s", p.ExecRelease))
	}
	if p.ExecConsolelog != "" {
		if err := validation.ValidateFilePath(p.ExecConsolelog, true); err != nil {
			return nil, fmt.Errorf("invalid exec.consolelog path: %w", err)
		}
		args = append(args, fmt.Sprintf("exec.consolelog=%s", p.ExecConsolelog))
	}
	if p.ExecSystemUser != "" {
		if err := validation.ValidateUsername(p.ExecSystemUser); err != nil {
			return nil, fmt.Errorf("invalid exec.system_user: %w", err)
		}
		args = append(args, fmt.Sprintf("exec.system_user=%s", p.ExecSystemUser))
	}
	if p.ExecJailUser != "" {
		if err := validation.ValidateUsername(p.ExecJailUser); err != nil {
			return nil, fmt.Errorf("invalid exec.jail_user: %w", err)
		}
		args = append(args, fmt.Sprintf("exec.jail_user=%s", p.ExecJailUser))
	}
	if p.ExecFib > 0 {
		args = append(args, fmt.Sprintf("exec.fib=%d", p.ExecFib))
	}

	args = append(args, fmt.Sprintf("exec.clean=%s", boolToJailValue(p.ExecClean)))

	// Timeouts
	if p.ExecTimeout > 0 {
		args = append(args, fmt.Sprintf("exec.timeout=%d", p.ExecTimeout))
	}
	if p.StopTimeout > 0 {
		args = append(args, fmt.Sprintf("stop.timeout=%d", p.StopTimeout))
	}

	args = append(args, fmt.Sprintf("mount.devfs=%s", boolToJailValue(p.MountDevfs)))
	if p.DevfsRuleset > 0 {
		args = append(args, fmt.Sprintf("devfs_ruleset=%d", p.DevfsRuleset))
	}
	args = append(args,
		fmt.Sprintf("mount.fdescfs=%s", boolToJailValue(p.MountFdescfs)),
		fmt.Sprintf("mount.procfs=%s", boolToJailValue(p.MountProcfs)),
	)
	// mount.linprocfs, mount.linsysfs, and mount.tmpfs are NOT valid jail(8)
	// parameters. These filesystems are mounted via mount.fstab instead.
	if p.MountFstab != "" {
		if err := validation.ValidateFilePath(p.MountFstab, true); err != nil {
			return nil, fmt.Errorf("invalid mount.fstab path: %w", err)
		}
		args = append(args, fmt.Sprintf("mount.fstab=%s", p.MountFstab))
	}

	// IP configuration - only emit when IP4/IP6 are set.
	// VNET jails use a separate network stack; ip4/ip6 restrictions and
	// ip4.saddrsel/ip6.saddrsel are incompatible with vnet in jail(8).
	// Callers clear IP4/IP6 for VNET jails so no special-casing is needed here.
	if p.IP4 != "" {
		args = append(args,
			fmt.Sprintf("ip4=%s", p.IP4),
			fmt.Sprintf("ip4.saddrsel=%s", boolToJailValue(p.IP4Saddrsel)),
		)
	}
	if p.IP6 != "" {
		args = append(args,
			fmt.Sprintf("ip6=%s", p.IP6),
			fmt.Sprintf("ip6.saddrsel=%s", boolToJailValue(p.IP6Saddrsel)),
		)
	}

	// Process settings
	args = append(args, fmt.Sprintf("persist=%s", boolToJailValue(p.Persist)))
	if p.ChildrenMax > 0 {
		args = append(args, fmt.Sprintf("children.max=%d", p.ChildrenMax))
	}
	args = append(args, fmt.Sprintf("enforce_statfs=%d", p.EnforceStatfs))

	// SysV IPC (only if allow.sysvipc is true)
	if p.AllowSysVIPC {
		// Use "new" (1) instead of "inherit" (0) when sysvipc is enabled
		// This ensures the jail gets its own IPC namespace that actually works
		sysvMsg := p.SysvMsg
		sysvSem := p.SysvSem
		sysvShm := p.SysvShm
		if sysvMsg == 0 {
			sysvMsg = 1 // new
		}
		if sysvSem == 0 {
			sysvSem = 1 // new
		}
		if sysvShm == 0 {
			sysvShm = 1 // new
		}
		args = append(args,
			fmt.Sprintf("sysvmsg=%s", sysvIPCValueToString(sysvMsg)),
			fmt.Sprintf("sysvsem=%s", sysvIPCValueToString(sysvSem)),
			fmt.Sprintf("sysvshm=%s", sysvIPCValueToString(sysvShm)),
		)
	}

	// Securelevel
	if p.Securelevel != -1 {
		args = append(args, fmt.Sprintf("securelevel=%d", p.Securelevel))
	}

	// Linux compatibility
	if p.LinuxOsrelease != "" {
		if err := validation.ValidateJailParameterValue(p.LinuxOsrelease); err != nil {
			return nil, fmt.Errorf("invalid linux.osrelease value: %w", err)
		}
		args = append(args, fmt.Sprintf("linux.osrelease=%s", p.LinuxOsrelease))
	}
	if p.LinuxOsname != "" {
		if err := validation.ValidateJailParameterValue(p.LinuxOsname); err != nil {
			return nil, fmt.Errorf("invalid linux.osname value: %w", err)
		}
		args = append(args, fmt.Sprintf("linux.osname=%s", p.LinuxOsname))
	}

	// OS compatibility - FreeBSD version spoofing
	if p.Osrelease != "" {
		if err := validation.ValidateJailParameterValue(p.Osrelease); err != nil {
			return nil, fmt.Errorf("invalid osrelease value: %w", err)
		}
		args = append(args, fmt.Sprintf("osrelease=%s", p.Osrelease))
	}
	if p.Osreldate > 0 {
		args = append(args, fmt.Sprintf("osreldate=%d", p.Osreldate))
	}

	// Host settings
	if p.HostHostname != "" {
		if err := validation.ValidateJailParameterValue(p.HostHostname); err != nil {
			return nil, fmt.Errorf("invalid host.hostname value: %w", err)
		}
		args = append(args, fmt.Sprintf("host.hostname=%s", p.HostHostname))
	}
	if p.HostHostUUID != "" {
		if err := validation.ValidateJailParameterValue(p.HostHostUUID); err != nil {
			return nil, fmt.Errorf("invalid host.hostuuid value: %w", err)
		}
		args = append(args, fmt.Sprintf("host.hostuuid=%s", p.HostHostUUID))
	}
	if p.HostHostid != "" {
		if err := validation.ValidateJailParameterValue(p.HostHostid); err != nil {
			return nil, fmt.Errorf("invalid host.hostid value: %w", err)
		}
		args = append(args, fmt.Sprintf("host.hostid=%s", p.HostHostid))
	}
	if p.HostDomainname != "" {
		if err := validation.ValidateJailParameterValue(p.HostDomainname); err != nil {
			return nil, fmt.Errorf("invalid host.domainname value: %w", err)
		}
		args = append(args, fmt.Sprintf("host.domainname=%s", p.HostDomainname))
	}

	return args, nil
}

// boolToJailValue converts a bool to jail value format
func boolToJailValue(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// sysvIPCValueToString converts SysV IPC value to jail parameter string
// 0 = inherit, 1 = new, 2 = disable
func sysvIPCValueToString(v int) string {
	switch v {
	case 0:
		return "inherit"
	case 1:
		return "new"
	case 2:
		return "disable"
	default:
		return "new" // Safe default
	}
}

// ParseJailParametersFromMap parses parameters from a map, starting from the
// defaults.
func ParseJailParametersFromMap(m map[string]interface{}) (JailParameters, error) {
	return ApplyJailParameterMap(DefaultJailParameters(), m)
}

// ApplyJailParameterMap overlays the parameters a map names onto base and leaves
// the rest as they were.
//
// Keys the map holds that are not jail parameters are ignored: the same map
// carries a provider's own bookkeeping.
func ApplyJailParameterMap(base JailParameters, m map[string]interface{}) (JailParameters, error) {
	params := base

	for key, value := range m {
		switch strings.ToLower(key) {
		// Security parameters
		case "allow.raw_sockets":
			params.AllowRawSockets = toBool(value)
		case "allow.sysvipc":
			params.AllowSysVIPC = toBool(value)
		case "allow.mount":
			params.AllowMount = toBool(value)
		case "allow.mount.devfs":
			params.AllowMountDevfs = toBool(value)
		case "allow.mount.nullfs":
			params.AllowMountNullfs = toBool(value)
		case "allow.mount.procfs":
			params.AllowMountProcfs = toBool(value)
		case "allow.mount.tmpfs":
			params.AllowMountTmpfs = toBool(value)
		case "allow.mount.zfs":
			params.AllowMountZFS = toBool(value)
		case "allow.mount.fdescfs":
			params.AllowMountFdescfs = toBool(value)
		case "allow.mount.linprocfs":
			params.AllowMountLinprocfs = toBool(value)
		case "allow.mount.linsysfs":
			params.AllowMountLinsysfs = toBool(value)
		case "allow.mount.fusefs":
			params.AllowMountFusefs = toBool(value)
		case "allow.vmm":
			params.AllowVMM = toBool(value)
		case "allow.mlock":
			params.AllowMlock = toBool(value)
		case "allow.reserved_ports":
			params.AllowReservedPorts = toBool(value)
		case "allow.chflags":
			params.AllowChflags = toBool(value)
		case "allow.quotas":
			params.AllowQuotas = toBool(value)
		case "allow.socket_af":
			params.AllowSocketAF = toBool(value)
		case "allow.set_hostname":
			params.AllowSetHostname = toBool(value)
		case "allow.nfsd":
			params.AllowNfsd = toBool(value)
		case "allow.suser":
			params.AllowSuser = toBool(value)
		case "allow.read_msgbuf":
			params.AllowReadMsgbuf = toBool(value)
		case "allow.unprivileged_proc_debug":
			params.AllowUnprivProcDbg = toBool(value)
		case "allow.extattr":
			params.AllowExtattr = toBool(value)
		case "allow.adjtime":
			params.AllowAdjtime = toBool(value)
		case "allow.settime":
			params.AllowSettime = toBool(value)
		case "allow.routing":
			params.AllowRouting = toBool(value)
		case "allow.setaudit":
			params.AllowSetaudit = toBool(value)

			// Lifecycle hooks
		case "exec.prestart":
			val := toString(value)
			if val != "" {
				if err := validation.ValidateExecCommand(val); err != nil {
					return JailParameters{}, fmt.Errorf("invalid exec.prestart: %w", err)
				}
			}
			params.ExecPrestart = val
		case "exec.start":
			val := toString(value)
			if val != "" {
				if err := validation.ValidateExecCommand(val); err != nil {
					return JailParameters{}, fmt.Errorf("invalid exec.start: %w", err)
				}
			}
			params.ExecStart = val
		case "exec.poststart":
			val := toString(value)
			if val != "" {
				if err := validation.ValidateExecCommand(val); err != nil {
					return JailParameters{}, fmt.Errorf("invalid exec.poststart: %w", err)
				}
			}
			params.ExecPoststart = val
		case "exec.prestop":
			val := toString(value)
			if val != "" {
				if err := validation.ValidateExecCommand(val); err != nil {
					return JailParameters{}, fmt.Errorf("invalid exec.prestop: %w", err)
				}
			}
			params.ExecPrestop = val
		case "exec.stop":
			val := toString(value)
			if val != "" {
				if err := validation.ValidateExecCommand(val); err != nil {
					return JailParameters{}, fmt.Errorf("invalid exec.stop: %w", err)
				}
			}
			params.ExecStop = val
		case "exec.poststop":
			val := toString(value)
			if val != "" {
				if err := validation.ValidateExecCommand(val); err != nil {
					return JailParameters{}, fmt.Errorf("invalid exec.poststop: %w", err)
				}
			}
			params.ExecPoststop = val
		case "exec.clean":
			params.ExecClean = toBool(value)
		case "exec.created":
			val := toString(value)
			if val != "" {
				if err := validation.ValidateExecCommand(val); err != nil {
					return JailParameters{}, fmt.Errorf("invalid exec.created: %w", err)
				}
			}
			params.ExecCreated = val
		case "exec.prepare":
			val := toString(value)
			if val != "" {
				if err := validation.ValidateExecCommand(val); err != nil {
					return JailParameters{}, fmt.Errorf("invalid exec.prepare: %w", err)
				}
			}
			params.ExecPrepare = val
		case "exec.release":
			val := toString(value)
			if val != "" {
				if err := validation.ValidateExecCommand(val); err != nil {
					return JailParameters{}, fmt.Errorf("invalid exec.release: %w", err)
				}
			}
			params.ExecRelease = val
		case "exec.jail", "exec.console":
			// These are not real jail(8) parameters and are never emitted into
			// the jail command, so accepting them silently would mislead the
			// caller into thinking they take effect. Reject them explicitly.
			return JailParameters{}, fmt.Errorf("jail parameter %q is not supported", key)
		case "exec.consolelog":
			val := toString(value)
			if val != "" {
				if err := validation.ValidateExecCommand(val); err != nil {
					return JailParameters{}, fmt.Errorf("invalid exec.consolelog: %w", err)
				}
			}
			params.ExecConsolelog = val
		case "exec.fib":
			params.ExecFib = toInt(value)
		case "exec.system_user":
			val := toString(value)
			if val != "" {
				if err := validation.ValidateUsername(val); err != nil {
					return JailParameters{}, fmt.Errorf("invalid exec.system_user: %w", err)
				}
			}
			params.ExecSystemUser = val
		case "exec.jail_user":
			val := toString(value)
			if val != "" {
				if err := validation.ValidateUsername(val); err != nil {
					return JailParameters{}, fmt.Errorf("invalid exec.jail_user: %w", err)
				}
			}
			params.ExecJailUser = val

		// Timeouts
		case "exec.timeout":
			params.ExecTimeout = toInt(value)
		case "stop.timeout":
			params.StopTimeout = toInt(value)

		case "mount.devfs":
			params.MountDevfs = toBool(value)
		case "devfs_ruleset":
			params.DevfsRuleset = toInt(value)
		case "mount.fdescfs":
			params.MountFdescfs = toBool(value)
		case "mount.procfs":
			params.MountProcfs = toBool(value)
		case "mount.linprocfs":
			params.MountLinprocfs = toBool(value)
		case "mount.linsysfs":
			params.MountLinsysfs = toBool(value)
		case "mount.tmpfs":
			params.MountTmpfs = toBool(value)
		case "mount.fstab":
			params.MountFstab = toString(value)
		case "fdescfs":
			params.Fdescfs = toString(value)

		// IP configuration
		case "ip4":
			params.IP4 = toString(value)
		case "ip6":
			params.IP6 = toString(value)
		case "ip4.saddrsel":
			params.IP4Saddrsel = toBool(value)
		case "ip6.saddrsel":
			params.IP6Saddrsel = toBool(value)

		// Process settings
		case "persist":
			params.Persist = toBool(value)
		case "children.max":
			params.ChildrenMax = toInt(value)
		case "enforce_statfs":
			params.EnforceStatfs = toInt(value)

		// SysV IPC
		case "sysvmsg":
			params.SysvMsg = toInt(value)
		case "sysvsem":
			params.SysvSem = toInt(value)
		case "sysvshm":
			params.SysvShm = toInt(value)

		// Securelevel
		case "securelevel":
			params.Securelevel = toInt(value)

		// Linux compatibility
		case "linux.osrelease":
			params.LinuxOsrelease = toString(value)
		case "linux.osname":
			params.LinuxOsname = toString(value)

		// OS compatibility
		case "osrelease":
			params.Osrelease = toString(value)
		case "osreldate":
			params.Osreldate = toInt(value)

		// Host settings
		case "host.hostname":
			params.HostHostname = toString(value)
		case "host.hostuuid":
			params.HostHostUUID = toString(value)
		case "host.hostid":
			params.HostHostid = toString(value)
		case "host.domainname":
			params.HostDomainname = toString(value)

		// IP hostname
		case "ip_hostname":
			params.IPHostname = toBool(value)

		// Depending
		case "depending":
			params.Depending = toBool(value)
		}
	}

	return params, nil
}

// Helper functions for type conversion
func toBool(v interface{}) bool {
	switch val := v.(type) {
	case bool:
		return val
	case int:
		return val != 0
	case float64:
		return val != 0
	case string:
		return val == "true" || val == "1" || val == "yes"
	default:
		return false
	}
}

func toInt(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	case string:
		var i int
		_, _ = fmt.Sscanf(val, "%d", &i)
		return i
	default:
		return 0
	}
}

func toString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	default:
		return fmt.Sprintf("%v", v)
	}
}
