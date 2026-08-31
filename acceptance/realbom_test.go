package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
)

// receiptBoms are the bills of materials macOS keeps for what it has
// installed. They are written by Apple, not by this project, which makes
// them the one large corpus here that no assumption of ours produced.
var receiptBoms = []string{
	"/private/var/db/receipts",
	"/Library/Apple/System/Library/Receipts",
}

// TestRealBomsMatchLsbom compares every column this reader decodes against
// lsbom, over every receipt on the machine.
//
// Path listings alone are not enough. Comparing only paths is what let the
// Size64 defect stand, where a file over 4 GiB read back as its size
// modulo 2^32 while lsbom reported it correctly. This compares mode, uid,
// gid, size and checksum too.
//
// What it does not cover: no receipt on a Mac carries a file over 4 GiB,
// the largest here being 328 MB, so this corpus never reaches the Size64
// tree. That path is covered by TestLargePayloadReadsPkgbuildSegments,
// which builds a file big enough to need it. Perturbing one bit of the
// mode makes 121 of the 123 comparisons here fail, which is the check
// that this test can fail at all.
func TestRealBomsMatchLsbom(t *testing.T) {
	requireTools(t, "lsbom")

	var files []string
	for _, dir := range receiptBoms {
		found, err := filepath.Glob(filepath.Join(dir, "*.bom"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, found...)
	}
	if len(files) == 0 {
		t.Skip("no receipt bills of materials on this machine")
	}

	var entries int
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			out, err := exec.Command("lsbom", "-p", "fmugsc", path).Output()
			if err != nil {
				t.Skipf("lsbom could not read it: %v", err)
			}
			want := map[string]bool{}
			for _, line := range nonEmptyLines(string(out)) {
				want[line] = true
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("unreadable: %v", err)
			}
			b, err := bom.Parse(data)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got, err := b.Paths()
			if err != nil {
				t.Fatalf("paths: %v", err)
			}

			for _, e := range got {
				// lsbom lists the tree under ".". A few Apple bills of
				// materials carry more than one root: the data volume
				// template has ".", ".." and ".TemporaryItems" side by
				// side, and lsbom shows only the first. Compare the part
				// both agree to describe.
				if e.Path != "." && !strings.HasPrefix(e.Path, "./") {
					continue
				}
				entries++
				line := formatFmugsc(e)
				if !want[line] {
					t.Errorf("lsbom does not have this line:\n  %s", line)
				}
				delete(want, line)
			}
			for line := range want {
				t.Errorf("we did not produce this lsbom line:\n  %s", line)
			}
		})
	}
	t.Logf("compared %d entries across %d bills of materials", entries, len(files))
}

// formatFmugsc renders an entry the way "lsbom -p fmugsc" prints it:
// name, octal mode, uid, gid, size, checksum, tab separated, with size and
// checksum left empty for anything that is not a file or a link.
func formatFmugsc(e bom.Entry) string {
	switch e.Type {
	case bom.TypeFile, bom.TypeLink:
		return fmt.Sprintf("%s\t%o\t%d\t%d\t%d\t%d", e.Path, e.Mode, e.UID, e.GID, e.Size, e.Checksum)
	default:
		return fmt.Sprintf("%s\t%o\t%d\t%d\t\t", e.Path, e.Mode, e.UID, e.GID)
	}
}
