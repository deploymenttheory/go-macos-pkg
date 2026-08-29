//go:build !windows

package flatpkg

import (
	"os"
	"syscall"
)

// owners returns the uid and gid to record for an entry under the policy.
func owners(fi os.FileInfo, policy Ownership) (uint32, uint32) {
	if policy == OwnershipRecommended {
		return 0, 0
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	if policy == OwnershipPreserveOther && int(st.Uid) == os.Getuid() {
		return 0, 0
	}
	return st.Uid, st.Gid
}
