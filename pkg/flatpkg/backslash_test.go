package flatpkg

import (
	"runtime"
	"testing"
)

// TestBackslashIsNotAPathSeparator covers an entry whose name contains a
// backslash.
//
// Only "/" separates a payload path. A backslash is an ordinary character
// in a macOS file name, and pkgutil extracts "./back\\slash.txt" as the one
// file it is. Rewriting it to "/" turned that single file into a directory
// holding a file, on every platform and without reporting a rename.
func TestBackslashIsNotAPathSeparator(t *testing.T) {
	const name = `./back\slash.txt`

	rel, renamed, reason := SafeRelPath(name)
	if reason != "" {
		t.Fatalf("SafeRelPath(%q) refused it: %s", name, reason)
	}

	if runtime.GOOS == "windows" {
		// Windows cannot store a backslash in a file name, so it is
		// sanitised like the other characters it refuses, and the rename
		// is reported rather than done silently.
		if rel == `back\slash.txt` {
			t.Errorf("rel = %q, which Windows cannot store", rel)
		}
		if renamed == "" {
			t.Error("the name was changed for the host without reporting a rename")
		}
		return
	}

	if want := `back\slash.txt`; rel != want {
		t.Errorf("rel = %q, want %q: the backslash is part of the name, not a separator", rel, want)
	}
	if renamed != "" {
		t.Errorf("renamedFrom = %q, want empty: nothing needed renaming here", renamed)
	}
}

// A backslash must not become a way to climb out either: the traversal
// checks run on the name as it stands.
func TestBackslashDoesNotBypassTraversalChecks(t *testing.T) {
	for _, name := range []string{`./..\..\escape.txt`, `.\../escape.txt`} {
		rel, _, reason := SafeRelPath(name)
		if reason == "" && (rel == ".." || len(rel) > 2 && rel[:3] == "../") {
			t.Errorf("SafeRelPath(%q) = %q, which climbs out", name, rel)
		}
	}
}
