package flatpkg

import "os"

// fileIdentity reports no identity on Windows: os.FileInfo carries no
// inode there, so hard links are packaged as separate files.
func fileIdentity(fi os.FileInfo) (dev, ino uint64, nlink uint32, ok bool) {
	return 0, 0, 1, false
}
