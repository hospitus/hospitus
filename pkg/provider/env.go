package provider

// MinimalEnv returns a minimal, closed environment for spawning subprocesses
// (hook scripts, in-instance exec, system updaters). It deliberately does NOT
// inherit the daemon's process environment via os.Environ(), which would leak
// secrets such as HOSPITUS_API_KEY, database credentials, or anything loaded into
// hospitusd into every spawned child — including scripts and processes running
// inside untrusted guests.
//
// PATH is set to a safe system default. Callers append their own context
// variables (e.g. HOSPITUS_JAIL_NAME=…, PAGER=cat) via extra.
func MinimalEnv(extra ...string) []string {
	env := make([]string, 0, len(extra)+1)
	env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	env = append(env, extra...)
	return env
}
