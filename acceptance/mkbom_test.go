package acceptance

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
)

// bomInfoTotal returns the byte total BomInfo records: version, path count
// and entry count, then a 16-byte info entry whose third word is the sum of
// the sizes of everything that occupies bytes.
func bomInfoTotal(t *testing.T, path string) uint32 {
	t.Helper()
	d, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(d) < 32 {
		t.Fatalf("%s is too short", path)
	}
	// magic(8) version(4) blocks(4) indexOffset(4) indexLength(4)
	// varsOffset(4) varsLength(4)
	indexOff := binary.BigEndian.Uint32(d[16:])
	varsOff := binary.BigEndian.Uint32(d[24:])
	count := binary.BigEndian.Uint32(d[indexOff:])
	block := func(i uint32) []byte {
		p := indexOff + 4 + i*8
		if i >= count || int(p)+8 > len(d) {
			t.Fatalf("block %d out of range", i)
		}
		a := binary.BigEndian.Uint32(d[p:])
		l := binary.BigEndian.Uint32(d[p+4:])
		return d[a : a+l]
	}
	n := binary.BigEndian.Uint32(d[varsOff:])
	p := varsOff + 4
	for i := uint32(0); i < n; i++ {
		idx := binary.BigEndian.Uint32(d[p:])
		ln := d[p+4]
		name := string(d[p+5 : p+5+uint32(ln)])
		p += 5 + uint32(ln)
		if name == "BomInfo" {
			b := block(idx)
			if len(b) < 24 {
				t.Fatalf("BomInfo is %d bytes", len(b))
			}
			return binary.BigEndian.Uint32(b[20:])
		}
	}
	t.Fatalf("%s has no BomInfo", path)
	return 0
}

// TestBomInfoTotalMatchesMkbom uses mkbom as an oracle for the byte total in
// BomInfo. mkbom writes bills of materials independently of pkgbuild, and
// nothing here had ever compared against it.
//
// The total counts everything that occupies bytes, which includes the target
// of a symbolic link: a tree of one 100-byte file and a link to "abcde"
// totals 105. Counting only regular files gave 100.
func TestBomInfoTotalMatchesMkbom(t *testing.T) {
	requireTools(t, "mkbom")

	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "f1"), bytes.Repeat([]byte{0}, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("abcde", filepath.Join(tree, "lnk")); err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	apple := filepath.Join(work, "apple.bom")
	hostTool(t, "mkbom", tree, apple)

	// Rebuild the same bill of materials from Apple's own entries, so the
	// two describe exactly the same tree and only the writers differ.
	data, err := os.ReadFile(apple)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := bom.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parsed.Paths()
	if err != nil {
		t.Fatal(err)
	}
	b := bom.NewBuilder()
	for _, e := range entries {
		if err := b.Add(e); err != nil {
			t.Fatalf("add %s: %v", e.Path, err)
		}
	}
	ours := filepath.Join(work, "ours.bom")
	f, err := os.Create(ours)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Build(f); err != nil {
		t.Fatal(err)
	}
	f.Close()

	want, got := bomInfoTotal(t, apple), bomInfoTotal(t, ours)
	if got != want {
		t.Errorf("BomInfo total = %d, mkbom says %d", got, want)
	}
	if want != 105 {
		t.Errorf("mkbom totalled %d for a 100-byte file and a 5-byte link target; expected 105", want)
	}
}
