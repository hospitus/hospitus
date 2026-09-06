package bhyve

import "github.com/hospitus/hospitus/pkg/dataset"

// testZFSParent is the provider dataset the fixtures below use.
//
// Derived rather than spelled: AGENTS.md makes pkg/dataset the one place the
// ZFS layout comes from, and the pool is configurable with HOSPITUS_ZFS_PARENT.
// A fixture that spells "zroot" states a pool the host may not have.
var testZFSParent = dataset.Child("bhyve")
