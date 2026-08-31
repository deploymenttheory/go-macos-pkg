package acceptance

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
)

// TestBigBomReadableByLsbom is the oracle for a Paths tree deep enough to
// need more than one branch level, which starts at 260,101 paths. The unit
// test in pkg/bom proves we can read our own tree back; only lsbom proves
// Apple can, and a tree that is self-consistent but shaped wrongly would
// satisfy the former while being unreadable on a Mac.
func TestBigBomReadableByLsbom(t *testing.T) {
	requireTools(t, "lsbom")

	// One 4096-byte block holds 510 entries, so a single branch level spans
	// 510*510 paths. Go just past it.
	const n = 510*510 + 50

	b := bom.NewBuilder()
	if err := b.Add(bom.Entry{Path: ".", Type: bom.TypeDirectory, Mode: 0o40755,
		Architecture: 15, ModTime: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := b.Add(bom.Entry{
			Path: fmt.Sprintf("./f%07d", i), Type: bom.TypeFile, Mode: 0o100644,
			Architecture: 15, ModTime: time.Unix(0, 0), Size: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(t.TempDir(), "big.bom")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	if err := b.Build(w); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// lsbom stops at the first entry out of order, so a short listing means
	// the tree is malformed even though every block parses.
	out, err := exec.Command("lsbom", "-s", path).Output()
	if err != nil {
		t.Fatalf("lsbom refused a %d-path bom: %v", n+1, err)
	}
	got := len(nonEmptyLines(string(out)))
	if got != n+1 {
		t.Errorf("lsbom listed %d paths, want %d", got, n+1)
	}
}
