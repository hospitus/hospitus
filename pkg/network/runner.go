//go:build freebsd || linux

package network

import "github.com/hospitus/hospitus/pkg/provider/execx"

// runnerOf returns the injected runner, or the production one.
func runnerOf(r execx.Runner) execx.Runner {
	if r != nil {
		return r
	}
	return execx.OS{}
}
