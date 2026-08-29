//go:build windows

package flatpkg

import "os"

// owners returns root:wheel on Windows, which has no uid or gid. Other
// policies are rejected before this is reached.
func owners(fi os.FileInfo, policy Ownership) (uint32, uint32) { return 0, 0 }
