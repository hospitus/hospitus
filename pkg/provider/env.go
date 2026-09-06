package provider

import "strings"

// blockedEnvPrefixes are the variables a caller must never be able to set on a
// spawned child. PATH picks the binary; the loader variables pick the code that
// binary runs, which is the same power by another route. Some of what reaches
// MinimalEnv comes from a manifest, so the author of that manifest would
// otherwise choose both.
var blockedEnvPrefixes = []string{
	"PATH=",
	"LD_",   // FreeBSD/Linux rtld: LD_PRELOAD, LD_LIBRARY_PATH, LD_32_*, ...
	"DYLD_", // macOS dyld: DYLD_INSERT_LIBRARIES, ...
}

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
		if blockedEnv(e) {
			continue
		}
		env = append(env, e)
	}
	return env
}

func blockedEnv(assignment string) bool {
	for _, prefix := range blockedEnvPrefixes {
		if strings.HasPrefix(assignment, prefix) {
			return true
		}
	}
	return false
}
