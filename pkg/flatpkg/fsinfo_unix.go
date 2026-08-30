//go:build !windows

package flatpkg

import (
	"os"
	"syscall"
)

// fileIdentity reports the device and inode a file lives on and its
// link count, so hard links can be recognised. ok is false when the host
// does not expose them.
func fileIdentity(fi os.FileInfo) (dev, ino uint64, nlink uint32, ok bool) {
	st, isStat := fi.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, 1, false
	}
	return uint64(st.Dev), uint64(st.Ino), uint32(st.Nlink), true //nolint:unconvert // field widths differ by platform
}
