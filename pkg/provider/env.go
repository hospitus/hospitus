package provider

import "strings"

// MinimalEnv returns a minimal, closed environment for spawning subprocesses
// (hook scripts, in-instance exec, system updaters). It deliberately does NOT
// inherit the daemon's process environment via os.Environ(), which would leak
// secrets such as HOSPITUS_API_KEY, database credentials, or anything loaded into
// hospitusd into every spawned child — including scripts and processes running
// inside untrusted guests.
//
// PATH is set to a safe system default and cannot be overridden through extra:
// some of what callers pass comes from a manifest, and a PATH there would send
// every command the child runs to a binary of the manifest author's choosing.
func MinimalEnv(extra ...string) []string {
	env := make([]string, 0, len(extra)+1)
	env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	for _, e := range extra {
		if strings.HasPrefix(e, "PATH=") {
			continue
		}
		env = append(env, e)
	}
	return env
}
