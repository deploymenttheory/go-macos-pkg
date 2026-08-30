package bom

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"
)

// TestSidecarRecord pins the record pkgbuild writes for a "._" entry
// (from testdata/cli/component-links.probe.json): type 1, architecture
// 1, the owner's mode, everything else zero, 31 bytes.
func TestSidecarRecord(t *testing.T) {
	cases := map[string]Entry{
		"0101000181a400000000000000000000000000000000010000000000000000": {Type: TypeFile, Sidecar: true, Architecture: 1, Mode: 0o100644},
		"0101000141ed00000000000000000000000000000000010000000000000000": {Type: TypeFile, Sidecar: true, Architecture: 1, Mode: 0o40755},
		"01010001a1ed00000000000000000000000000000000010000000000000000": {Type: TypeFile, Sidecar: true, Architecture: 1, Mode: 0o120755},
	}
	for want, e := range cases {
		got := hex.EncodeToString(encodeRecord(e))
		if got != want {
			t.Errorf("mode %o:\n got %s\nwant %s", e.Mode, got, want)
		}
	}
}

// TestHLIndexPerInode checks that a hard-link set gets one HLIndex leaf,
// for its last member in path order, and sidecars always get one.
func TestHLIndexPerInode(t *testing.T) {
	b := NewBuilder()
	now := time.Unix(1704164645, 0)
	add := func(e Entry) {
		e.ModTime = now
		if err := b.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	add(Entry{Path: ".", Type: TypeDirectory, Mode: 0o40755, Architecture: 15})
	add(Entry{Path: "./a.txt", Type: TypeFile, Mode: 0o100644, Architecture: 15, Size: 15, Checksum: 1, HardLinkKey: 14})
	add(Entry{Path: "./._a.txt", Type: TypeFile, Sidecar: true, Mode: 0o100644, Architecture: 1, HardLinkKey: 14})
	add(Entry{Path: "./b.txt", Type: TypeFile, Mode: 0o100644, Architecture: 15, Size: 15, Checksum: 1, HardLinkKey: 14})
	add(Entry{Path: "./d", Type: TypeDirectory, Mode: 0o40755, Architecture: 15, Size: 96})
	add(Entry{Path: "./d/c.txt", Type: TypeFile, Mode: 0o100644, Architecture: 15, Size: 15, Checksum: 1, HardLinkKey: 14})
	add(Entry{Path: "./x", Type: TypeFile, Mode: 0o100644, Architecture: 15, Size: 1, Checksum: 2})
	var buf bytes.Buffer
	if err := b.Build(&buf); err != nil {
		t.Fatal(err)
	}
	bom, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	hl, ok := bom.Var(VarHLIndex)
	if !ok {
		t.Fatal("no HLIndex")
	}
	tree, err := bom.readTree(hl)
	if err != nil {
		t.Fatal(err)
	}
	// ., ._a.txt, d, d/c.txt, x, but not a.txt or b.txt.
	if tree.PathCount != 5 {
		t.Errorf("HLIndex has %d leaves, want 5", tree.PathCount)
	}
	paths, err := bom.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 7 {
		t.Errorf("Paths has %d entries, want 7", len(paths))
	}
}
