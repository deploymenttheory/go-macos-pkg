// Version reporting: the one place the program's version is written down.
package tools

import (
	"runtime/debug"
	"strings"
)

// version is stamped in by the linker on a release build, so the binary names
// the tag it was cut from. It is deliberately the only place a version is
// written down: goreleaser sets it with -X, and everything else asks Version().
var version = "dev"

// Version returns the version to report.
//
// A plain "go build" from a source tree reports "dev" rather than a constant
// that someone has to remember to bump: a stale fallback is worse than an
// honest one, because it names a release the binary is not.
func Version() string {
	if version != "dev" {
		return version
	}
	// go install stamps the module version into the build info, so a binary
	// installed from a tag reports that tag without the linker being involved.
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return strings.TrimPrefix(v, "v")
		}
	}
	return version
}
