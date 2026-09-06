// Package dataset centralizes the ZFS dataset layout Hospitus uses, so that no
// other package hardcodes a pool name. Deriving the layout from one place is
// what lets the parent be configured with HOSPITUS_ZFS_PARENT, and what keeps
// Hospitus working on a host whose pool is not named "zroot".
package dataset

import (
	"os"
	"strings"
)

// defaultParent is the parent dataset used when HOSPITUS_ZFS_PARENT is unset.
const defaultParent = "zroot/hospitus"

// Parent returns the parent ZFS dataset under which Hospitus stores jails, VMs,
// volumes, base skeletons and backups. It defaults to "zroot/hospitus" and can be
// overridden with the HOSPITUS_ZFS_PARENT environment variable.
func Parent() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("HOSPITUS_ZFS_PARENT")), "/"); v != "" {
		return v
	}
	return defaultParent
}

// Pool returns the ZFS pool name — the first component of Parent (e.g. "zroot"
// for "zroot/hospitus"). Used for pool-level storage queries.
func Pool() string {
	p := Parent()
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// Child returns Parent joined with the given sub-path, e.g. Child("jails")
// yields "zroot/hospitus/jails".
func Child(sub string) string {
	return Parent() + "/" + strings.TrimLeft(sub, "/")
}
