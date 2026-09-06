package jail

import (
	"strings"
	"testing"
)

func TestDefaultJailParameters(t *testing.T) {
	params := DefaultJailParameters()

	// Verify security defaults are restrictive
	if params.AllowRawSockets {
		t.Error("Default AllowRawSockets should be false")
	}
	if params.AllowSysVIPC {
		t.Error("Default AllowSysVIPC should be false")
	}

	// Verify safe defaults are set
	if !params.AllowSetHostname {
		t.Error("Default AllowSetHostname should be true")
	}
	if !params.MountDevfs {
		t.Error("Default MountDevfs should be true")
	}
	if params.DevfsRuleset != 4 {
		t.Errorf("Default DevfsRuleset should be 4, got %d", params.DevfsRuleset)
	}

	// Verify lifecycle defaults
	if params.ExecStart != "/bin/sh /etc/rc" {
		t.Errorf("Default ExecStart should be '/bin/sh /etc/rc', got %s", params.ExecStart)
	}
	if params.ExecStop != "/bin/sh /etc/rc.shutdown" {
		t.Errorf("Default ExecStop should be '/bin/sh /etc/rc.shutdown', got %s", params.ExecStop)
	}
}

func TestValidateJailParameters(t *testing.T) {
	tests := []struct {
		name    string
		params  JailParameters
		wantErr bool
	}{
		{
			name:    "default parameters are valid",
			params:  DefaultJailParameters(),
			wantErr: false,
		},
		{
			name: "negative exec.timeout is invalid",
			params: func() JailParameters {
				p := DefaultJailParameters()
				p.ExecTimeout = -1
				return p
			}(),
			wantErr: true,
		},
		{
			name: "negative stop.timeout is invalid",
			params: func() JailParameters {
				p := DefaultJailParameters()
				p.StopTimeout = -1
				return p
			}(),
			wantErr: true,
		},
		{
			name: "invalid enforce_statfs is invalid",
			params: func() JailParameters {
				p := DefaultJailParameters()
				p.EnforceStatfs = 5
				return p
			}(),
			wantErr: true,
		},
		{
			name: "securelevel 4 is invalid",
			params: func() JailParameters {
				p := DefaultJailParameters()
				p.Securelevel = 4
				return p
			}(),
			wantErr: true,
		},
		{
			name: "securelevel -2 is invalid",
			params: func() JailParameters {
				p := DefaultJailParameters()
				p.Securelevel = -2
				return p
			}(),
			wantErr: true,
		},
		{
			name: "valid custom parameters",
			params: JailParameters{
				AllowRawSockets: true,
				AllowSysVIPC:    true,
				ExecStart:       "/bin/sh /etc/rc",
				ExecStop:        "/bin/sh /etc/rc.shutdown",
				ExecTimeout:     120,
				StopTimeout:     60,
				MountDevfs:      true,
				DevfsRuleset:    5,
				EnforceStatfs:   1,
				Securelevel:     1,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateJailParameters(tt.params)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateJailParameters() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestToJailArgs(t *testing.T) {
	params := JailParameters{
		AllowRawSockets: true,
		AllowSysVIPC:    true,
		ExecStart:       "/bin/sh /etc/rc",
		ExecStop:        "/bin/sh /etc/rc.shutdown",
		MountDevfs:      true,
		DevfsRuleset:    5,
		Persist:         true,
	}

	args, err := params.ToJailArgs()
	if err != nil {
		t.Fatalf("ToJailArgs() failed: %v", err)
	}

	// Check that key arguments are present
	hasRawSockets := false
	hasSysVIPC := false
	hasDevfsRuleset := false
	hasPersist := false

	for _, arg := range args {
		if arg == "allow.raw_sockets=1" {
			hasRawSockets = true
		}
		if arg == "allow.sysvipc=1" {
			hasSysVIPC = true
		}
		if arg == "devfs_ruleset=5" {
			hasDevfsRuleset = true
		}
		if arg == "persist=1" {
			hasPersist = true
		}
	}

	if !hasRawSockets {
		t.Error("Expected allow.raw_sockets=1 in args")
	}
	if !hasSysVIPC {
		t.Error("Expected allow.sysvipc=1 in args")
	}
	if !hasDevfsRuleset {
		t.Error("Expected devfs_ruleset=5 in args")
	}
	if !hasPersist {
		t.Error("Expected persist=1 in args")
	}
}

func TestParseJailParametersFromMap(t *testing.T) {
	m := map[string]interface{}{
		"allow.raw_sockets": true,
		"allow.sysvipc":     true,
		"exec.prestart":     "/usr/local/bin/pre.sh",
		"devfs_ruleset":     5,
		"persist":           true,
		"securelevel":       1,
	}

	params, err := ParseJailParametersFromMap(m)
	if err != nil {
		t.Fatalf("ParseJailParametersFromMap() returned error: %v", err)
	}

	if !params.AllowRawSockets {
		t.Error("Expected AllowRawSockets to be true")
	}
	if !params.AllowSysVIPC {
		t.Error("Expected AllowSysVIPC to be true")
	}
	if params.ExecPrestart != "/usr/local/bin/pre.sh" {
		t.Errorf("Expected ExecPrestart to be '/usr/local/bin/pre.sh', got %s", params.ExecPrestart)
	}
	if params.DevfsRuleset != 5 {
		t.Errorf("Expected DevfsRuleset to be 5, got %d", params.DevfsRuleset)
	}
	if !params.Persist {
		t.Error("Expected Persist to be true")
	}
	if params.Securelevel != 1 {
		t.Errorf("Expected Securelevel to be 1, got %d", params.Securelevel)
	}
}

func TestBoolToJailValue(t *testing.T) {
	if boolToJailValue(true) != "1" {
		t.Error("Expected true to be '1'")
	}
	if boolToJailValue(false) != "0" {
		t.Error("Expected false to be '0'")
	}
}

func TestSysVIPCDefaultsToNew(t *testing.T) {
	// When AllowSysVIPC is true and sysv* values are default (0),
	// the args should use "new" instead of "inherit" (0)
	params := JailParameters{
		AllowSysVIPC: true,
		// sysv* values are default 0 (inherit)
	}

	args, err := params.ToJailArgs()
	if err != nil {
		t.Fatalf("ToJailArgs() failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	// Verify sysvmsg, sysvsem, sysvshm are set to "new"
	if !strings.Contains(argsStr, "sysvmsg=new") {
		t.Errorf("Expected sysvmsg=new in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "sysvsem=new") {
		t.Errorf("Expected sysvsem=new in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "sysvshm=new") {
		t.Errorf("Expected sysvshm=new in args, got: %s", argsStr)
	}
}

func TestSysVIPCExplicitValues(t *testing.T) {
	// When sysv* values are explicitly set, they should be preserved
	params := JailParameters{
		AllowSysVIPC: true,
		SysvMsg:      2, // disable
		SysvSem:      1, // new
		SysvShm:      0, // inherit -> should become new
	}

	args, err := params.ToJailArgs()
	if err != nil {
		t.Fatalf("ToJailArgs() failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "sysvmsg=disable") {
		t.Errorf("Expected sysvmsg=disable in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "sysvsem=new") {
		t.Errorf("Expected sysvsem=new in args, got: %s", argsStr)
	}
	// When value is 0 (inherit), it should become "new" for sysvipc to work
	if !strings.Contains(argsStr, "sysvshm=new") {
		t.Errorf("Expected sysvshm=new in args, got: %s", argsStr)
	}
}

func TestSysvIPCValueToString(t *testing.T) {
	tests := []struct {
		value    int
		expected string
	}{
		{0, "inherit"},
		{1, "new"},
		{2, "disable"},
		{99, "new"}, // default for invalid values
	}

	for _, tt := range tests {
		result := sysvIPCValueToString(tt.value)
		if result != tt.expected {
			t.Errorf("sysvIPCValueToString(%d) = %s, expected %s", tt.value, result, tt.expected)
		}
	}
}

func TestLifecycleHooksInArgs(t *testing.T) {
	params := JailParameters{
		ExecPrestart:  "/usr/local/bin/pre.sh",
		ExecPoststart: "/usr/local/bin/post.sh",
		ExecPrestop:   "/usr/local/bin/prestop.sh",
		ExecPoststop:  "/usr/local/bin/poststop.sh",
		ExecStart:     "/bin/sh /etc/rc",
		ExecStop:      "/bin/sh /etc/rc.shutdown",
	}

	args, err := params.ToJailArgs()
	if err != nil {
		t.Fatalf("ToJailArgs() failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "exec.prestart=/usr/local/bin/pre.sh") {
		t.Error("Expected exec.prestart in args")
	}
	if !strings.Contains(argsStr, "exec.poststart=/usr/local/bin/post.sh") {
		t.Error("Expected exec.poststart in args")
	}
	if !strings.Contains(argsStr, "exec.prestop=/usr/local/bin/prestop.sh") {
		t.Error("Expected exec.prestop in args")
	}
	if !strings.Contains(argsStr, "exec.poststop=/usr/local/bin/poststop.sh") {
		t.Error("Expected exec.poststop in args")
	}
}

func TestNewSecurityParametersInArgs(t *testing.T) {
	params := JailParameters{
		AllowSuser:         true,
		AllowReadMsgbuf:    true,
		AllowUnprivProcDbg: true,
		AllowExtattr:       true,
		AllowAdjtime:       true,
		AllowSettime:       true,
		AllowRouting:       true,
		AllowSetaudit:      true,
		AllowMountFusefs:   true,
	}

	args, err := params.ToJailArgs()
	if err != nil {
		t.Fatalf("ToJailArgs() failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	expectedParams := []string{
		"allow.suser=1",
		"allow.read_msgbuf=1",
		"allow.unprivileged_proc_debug=1",
		"allow.extattr=1",
		"allow.adjtime=1",
		"allow.settime=1",
		"allow.routing=1",
		"allow.setaudit=1",
		"allow.mount.fusefs=1",
	}

	for _, expected := range expectedParams {
		if !strings.Contains(argsStr, expected) {
			t.Errorf("Expected %s in args, got: %s", expected, argsStr)
		}
	}
}

func TestExecUserParametersInArgs(t *testing.T) {
	params := JailParameters{
		ExecConsolelog: "/var/log/jail.log",
		ExecSystemUser: "root",
		ExecJailUser:   "www",
		ExecFib:        1,
	}

	args, err := params.ToJailArgs()
	if err != nil {
		t.Fatalf("ToJailArgs() failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "exec.consolelog=/var/log/jail.log") {
		t.Errorf("Expected exec.consolelog in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "exec.system_user=root") {
		t.Errorf("Expected exec.system_user in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "exec.jail_user=www") {
		t.Errorf("Expected exec.jail_user in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "exec.fib=1") {
		t.Errorf("Expected exec.fib in args, got: %s", argsStr)
	}
}

func TestMountParametersInArgs(t *testing.T) {
	params := JailParameters{
		MountProcfs: true,
		MountFstab:  "/etc/fstab.myjail",
	}

	args, err := params.ToJailArgs()
	if err != nil {
		t.Fatalf("ToJailArgs() failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "mount.procfs=1") {
		t.Errorf("Expected mount.procfs=1 in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "mount.fstab=/etc/fstab.myjail") {
		t.Errorf("Expected mount.fstab in args, got: %s", argsStr)
	}
}

func TestIPParametersInArgs(t *testing.T) {
	params := JailParameters{
		IP4:         "new",
		IP6:         "disable",
		IP4Saddrsel: true,
		IP6Saddrsel: false,
	}

	args, err := params.ToJailArgs()
	if err != nil {
		t.Fatalf("ToJailArgs() failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "ip4=new") {
		t.Errorf("Expected ip4=new in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "ip6=disable") {
		t.Errorf("Expected ip6=disable in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "ip4.saddrsel=1") {
		t.Errorf("Expected ip4.saddrsel=1 in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "ip6.saddrsel=0") {
		t.Errorf("Expected ip6.saddrsel=0 in args, got: %s", argsStr)
	}
}

func TestOSCompatParametersInArgs(t *testing.T) {
	params := JailParameters{
		Osrelease: "13.2-RELEASE",
		Osreldate: 1302001,
	}

	args, err := params.ToJailArgs()
	if err != nil {
		t.Fatalf("ToJailArgs() failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "osrelease=13.2-RELEASE") {
		t.Errorf("Expected osrelease in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "osreldate=1302001") {
		t.Errorf("Expected osreldate in args, got: %s", argsStr)
	}
}

func TestHostParametersInArgs(t *testing.T) {
	params := JailParameters{
		HostHostname:   "myjail.example.com",
		HostHostid:     "12345678",
		HostHostUUID:   "550e8400-e29b-41d4-a716-446655440000",
		HostDomainname: "example.com",
	}

	args, err := params.ToJailArgs()
	if err != nil {
		t.Fatalf("ToJailArgs() failed: %v", err)
	}
	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "host.hostname=myjail.example.com") {
		t.Errorf("Expected host.hostname in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "host.hostid=12345678") {
		t.Errorf("Expected host.hostid in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "host.hostuuid=550e8400-e29b-41d4-a716-446655440000") {
		t.Errorf("Expected host.hostuuid in args, got: %s", argsStr)
	}
	if !strings.Contains(argsStr, "host.domainname=example.com") {
		t.Errorf("Expected host.domainname in args, got: %s", argsStr)
	}
}

func TestValidateIPModes(t *testing.T) {
	tests := []struct {
		name    string
		ip4     string
		ip6     string
		wantErr bool
	}{
		{"inherit mode", "inherit", "inherit", false},
		{"new mode", "new", "new", false},
		{"disable mode", "disable", "disable", false},
		{"empty mode", "", "", false},
		{"invalid ip4", "invalid", "new", true},
		{"invalid ip6", "new", "invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := DefaultJailParameters()
			params.IP4 = tt.ip4
			params.IP6 = tt.ip6
			err := ValidateJailParameters(params)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateJailParameters() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseNewParametersFromMap(t *testing.T) {
	m := map[string]interface{}{
		"allow.suser":                   true,
		"allow.read_msgbuf":             true,
		"allow.unprivileged_proc_debug": true,
		"allow.extattr":                 true,
		"allow.adjtime":                 true,
		"allow.settime":                 true,
		"allow.routing":                 true,
		"allow.setaudit":                true,
		"allow.mount.fusefs":            true,
		"allow.mount.fdescfs":           true,
		"allow.mount.linprocfs":         true,
		"allow.mount.linsysfs":          true,
		"exec.consolelog":               "/var/log/jail.log",
		"exec.system_user":              "root",
		"exec.jail_user":                "www",
		"exec.fib":                      1,
		"exec.created":                  "/usr/local/bin/created.sh",
		"mount.procfs":                  true,
		"mount.fstab":                   "/etc/fstab.jail",
		"ip4":                           "new",
		"ip6":                           "disable",
		"ip4.saddrsel":                  true,
		"ip6.saddrsel":                  false,
		"osrelease":                     "13.2-RELEASE",
		"osreldate":                     1302001,
		"host.hostname":                 "myjail",
		"host.hostid":                   "12345",
		"host.hostuuid":                 "uuid-here",
		"host.domainname":               "example.com",
	}

	params, err := ParseJailParametersFromMap(m)
	if err != nil {
		t.Fatalf("ParseJailParametersFromMap() returned error: %v", err)
	}

	if !params.AllowSuser {
		t.Error("Expected AllowSuser to be true")
	}
	if !params.AllowReadMsgbuf {
		t.Error("Expected AllowReadMsgbuf to be true")
	}
	if !params.AllowUnprivProcDbg {
		t.Error("Expected AllowUnprivProcDbg to be true")
	}
	if !params.AllowExtattr {
		t.Error("Expected AllowExtattr to be true")
	}
	if !params.AllowAdjtime {
		t.Error("Expected AllowAdjtime to be true")
	}
	if !params.AllowSettime {
		t.Error("Expected AllowSettime to be true")
	}
	if !params.AllowRouting {
		t.Error("Expected AllowRouting to be true")
	}
	if !params.AllowSetaudit {
		t.Error("Expected AllowSetaudit to be true")
	}
	if !params.AllowMountFusefs {
		t.Error("Expected AllowMountFusefs to be true")
	}
	if !params.AllowMountFdescfs {
		t.Error("Expected AllowMountFdescfs to be true")
	}
	if !params.AllowMountLinprocfs {
		t.Error("Expected AllowMountLinprocfs to be true")
	}
	if !params.AllowMountLinsysfs {
		t.Error("Expected AllowMountLinsysfs to be true")
	}
	if params.ExecConsolelog != "/var/log/jail.log" {
		t.Errorf("Expected ExecConsolelog to be '/var/log/jail.log', got %s", params.ExecConsolelog)
	}
	if params.ExecSystemUser != "root" {
		t.Errorf("Expected ExecSystemUser to be 'root', got %s", params.ExecSystemUser)
	}
	if params.ExecJailUser != "www" {
		t.Errorf("Expected ExecJailUser to be 'www', got %s", params.ExecJailUser)
	}
	if params.ExecFib != 1 {
		t.Errorf("Expected ExecFib to be 1, got %d", params.ExecFib)
	}
	if params.ExecCreated != "/usr/local/bin/created.sh" {
		t.Errorf("Expected ExecCreated to be '/usr/local/bin/created.sh', got %s", params.ExecCreated)
	}
	if !params.MountProcfs {
		t.Error("Expected MountProcfs to be true")
	}
	if params.MountFstab != "/etc/fstab.jail" {
		t.Errorf("Expected MountFstab to be '/etc/fstab.jail', got %s", params.MountFstab)
	}
	if params.IP4 != "new" {
		t.Errorf("Expected IP4 to be 'new', got %s", params.IP4)
	}
	if params.IP6 != "disable" {
		t.Errorf("Expected IP6 to be 'disable', got %s", params.IP6)
	}
	if !params.IP4Saddrsel {
		t.Error("Expected IP4Saddrsel to be true")
	}
	if params.IP6Saddrsel {
		t.Error("Expected IP6Saddrsel to be false")
	}
	if params.Osrelease != "13.2-RELEASE" {
		t.Errorf("Expected Osrelease to be '13.2-RELEASE', got %s", params.Osrelease)
	}
	if params.Osreldate != 1302001 {
		t.Errorf("Expected Osreldate to be 1302001, got %d", params.Osreldate)
	}
	if params.HostHostname != "myjail" {
		t.Errorf("Expected HostHostname to be 'myjail', got %s", params.HostHostname)
	}
	if params.HostHostid != "12345" {
		t.Errorf("Expected HostHostid to be '12345', got %s", params.HostHostid)
	}
	if params.HostHostUUID != "uuid-here" {
		t.Errorf("Expected HostHostUUID to be 'uuid-here', got %s", params.HostHostUUID)
	}
	if params.HostDomainname != "example.com" {
		t.Errorf("Expected HostDomainname to be 'example.com', got %s", params.HostDomainname)
	}
}

// ---- toBool / toInt / toString helpers ----

func TestToBool(t *testing.T) {
	tests := []struct {
		input interface{}
		want  bool
	}{
		{true, true},
		{false, false},
		{1, true},
		{0, false},
		{float64(1.0), true},
		{float64(0.0), false},
		{"true", true},
		{"1", true},
		{"yes", true},
		{"false", false},
		{"no", false},
		{"", false},
		{nil, false},
	}
	for _, tc := range tests {
		got := toBool(tc.input)
		if got != tc.want {
			t.Errorf("toBool(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestToInt(t *testing.T) {
	tests := []struct {
		input interface{}
		want  int
	}{
		{42, 42},
		{0, 0},
		{float64(3.7), 3},
		{"10", 10},
		{"0", 0},
		{"abc", 0},
		{nil, 0},
	}
	for _, tc := range tests {
		got := toInt(tc.input)
		if got != tc.want {
			t.Errorf("toInt(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestToString(t *testing.T) {
	tests := []struct {
		input interface{}
		want  string
	}{
		{"hello", "hello"},
		{"", ""},
		{42, "42"},
		{true, "true"},
		{nil, "<nil>"},
	}
	for _, tc := range tests {
		got := toString(tc.input)
		if got != tc.want {
			t.Errorf("toString(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestParseJailParametersFromMap_ErrorOnBadExecCommand(t *testing.T) {
	m := map[string]interface{}{
		"exec.prestart": "rm -rf /; evil",
	}
	_, err := ParseJailParametersFromMap(m)
	if err == nil {
		t.Error("expected error for invalid exec.prestart, got nil")
	}
}

// TestApplyJailParameterMapRejectsReservedExecKeys covers the two keys the
// provider owns: a manifest that set them would take over how the jail starts.
func TestApplyJailParameterMapRejectsReservedExecKeys(t *testing.T) {
	for _, key := range []string{"exec.jail", "exec.console"} {
		if _, err := ParseJailParametersFromMap(map[string]interface{}{key: "/bin/sh"}); err == nil {
			t.Errorf("%s was accepted", key)
		}
	}
}

func TestParseJailParametersFromMap_AdditionalFields(t *testing.T) {
	m := map[string]interface{}{
		"allow.mount":           true,
		"allow.mount.devfs":     true,
		"allow.mount.nullfs":    true,
		"allow.mount.procfs":    true,
		"allow.mount.tmpfs":     true,
		"allow.mount.zfs":       true,
		"allow.mount.fdescfs":   true,
		"allow.mount.linprocfs": true,
		"allow.mount.linsysfs":  true,
		"allow.mount.fusefs":    true,
		"allow.vmm":             true,
		"allow.mlock":           true,
		"allow.reserved_ports":  true,
		"allow.chflags":         true,
		"allow.quotas":          true,
		"allow.socket_af":       true,
		"allow.set_hostname":    true,
		"allow.nfsd":            true,
		"allow.suser":           true,
		"allow.read_msgbuf":     true,
		"allow.extattr":         true,
		"allow.adjtime":         true,
		"allow.settime":         true,
		"allow.routing":         true,
		"mount.devfs":           true,
		"mount.procfs":          true,
		"children.max":          float64(10),
		"children.cur":          float64(5),
	}
	params, err := ParseJailParametersFromMap(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !params.AllowMount {
		t.Error("AllowMount should be true")
	}
	if !params.AllowMountDevfs {
		t.Error("AllowMountDevfs should be true")
	}
	if !params.AllowMountZFS {
		t.Error("AllowMountZFS should be true")
	}
	if !params.AllowVMM {
		t.Error("AllowVMM should be true")
	}
	if !params.MountDevfs {
		t.Error("MountDevfs should be true")
	}
	if params.ChildrenMax != 10 {
		t.Errorf("ChildrenMax = %d, want 10", params.ChildrenMax)
	}
}
