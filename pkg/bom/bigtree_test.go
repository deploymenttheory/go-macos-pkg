package bom

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

// buildPaths writes a bill of materials holding n files under the root.
func buildPaths(tb testing.TB, n int) []byte {
	tb.Helper()
	b := NewBuilder()
	root := Entry{Path: ".", Type: TypeDirectory, Mode: 0o40755, Architecture: 15, ModTime: time.Unix(0, 0)}
	if err := b.Add(root); err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < n; i++ {
		e := Entry{
			Path: fmt.Sprintf("./f%07d", i), Type: TypeFile,
			Mode: 0o100644, Architecture: 15, ModTime: time.Unix(0, 0), Size: 1,
		}
		if err := b.Add(e); err != nil {
			tb.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := b.Build(&buf); err != nil {
		tb.Fatalf("%d paths: %v", n+1, err)
	}
	return buf.Bytes()
}

// TestBuildsPastOneBranchLevel covers the point where the Paths tree needs a
// second branch level. A 4096-byte block holds 510 entries, so one level
// spans 510*510 = 260,100 paths. Writing only ever one level ran off the end
// of the block and panicked on the next path, which any tree of that size
// reaches: Xcode's own receipt carries 156,994.
func TestBuildsPastOneBranchLevel(t *testing.T) {
	const perBlock = (pathsBlockSize - 12) / 8
	const oneLevel = perBlock * perBlock // 260,100 paths, the last that fits

	for _, n := range []int{oneLevel - 1, oneLevel, oneLevel + 1} {
		t.Run(fmt.Sprint(n+1), func(t *testing.T) {
			data := buildPaths(t, n)
			b, err := Parse(data)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			entries, err := b.Paths()
			if err != nil {
				t.Fatalf("paths: %v", err)
			}
			if len(entries) != n+1 {
				t.Fatalf("read %d paths, wrote %d", len(entries), n+1)
			}
			// The order must survive the extra level, since lsbom stops
			// reading at the first entry out of order.
			if entries[0].Path != "." {
				t.Errorf("first entry %q, want %q", entries[0].Path, ".")
			}
			if want := fmt.Sprintf("./f%07d", n-1); entries[len(entries)-1].Path != want {
				t.Errorf("last entry %q, want %q", entries[len(entries)-1].Path, want)
			}
		})
	}
}
