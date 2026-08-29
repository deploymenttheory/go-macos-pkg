package bom

import (
	"bytes"
	"testing"
	"time"
)

func TestBuilderRoundTrip(t *testing.T) {
	mtime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := []Entry{
		{Path: ".", Type: TypeDirectory, Mode: 0o40755, Architecture: 15, ModTime: mtime, Size: 96},
		{Path: "./usr", Type: TypeDirectory, Mode: 0o40755, Architecture: 15, ModTime: mtime, Size: 96},
		{Path: "./usr/hello.txt", Type: TypeFile, Mode: 0o100644, Architecture: 15, ModTime: mtime, Size: 13, Checksum: 0x535fbd37},
		{Path: "./usr/link", Type: TypeLink, Mode: 0o120755, Architecture: 15, ModTime: mtime, Size: 9, Checksum: 0x0df22415, LinkTarget: "hello.txt"},
		{Path: "./usr/huge", Type: TypeFile, Mode: 0o100644, Architecture: 15, ModTime: mtime, Size: 5 << 30, Checksum: 1},
		{Path: "./Applications", Type: TypeDirectory, Mode: 0o40755, Architecture: 15, ModTime: mtime, Size: 64},
	}
	b := NewBuilder()
	for _, e := range entries {
		if err := b.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Add(Entry{Path: "./usr/hello.txt"}); err == nil {
		t.Error("duplicate path accepted")
	}
	if err := b.Add(Entry{Path: "./nope/child"}); err == nil {
		t.Error("orphan path accepted")
	}
	var buf bytes.Buffer
	if err := b.Build(&buf); err != nil {
		t.Fatal(err)
	}
	var buf2 bytes.Buffer
	b.Build(&buf2)
	if !bytes.Equal(buf.Bytes(), buf2.Bytes()) {
		t.Error("two builds differ")
	}

	parsed, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Vars(); len(got) != 5 || got[0] != VarBomInfo || got[4] != VarSize64 {
		t.Errorf("vars = %v", got)
	}
	back, err := parsed.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != len(entries) {
		t.Fatalf("read back %d entries, want %d", len(back), len(entries))
	}
	for i, e := range entries {
		g := back[i]
		if g.Path != e.Path || g.Type != e.Type || g.Mode != e.Mode || g.Size != e.Size || g.Checksum != e.Checksum || g.LinkTarget != e.LinkTarget || !g.ModTime.Equal(mtime) {
			t.Errorf("entry %d = %+v\nwant     %+v", i, g, e)
		}
		if g.ID != uint32(i+1) {
			t.Errorf("entry %d id = %d", i, g.ID)
		}
	}
	if back[2].ParentID != 2 || back[5].ParentID != 1 {
		t.Errorf("parents: hello %d, Applications %d", back[2].ParentID, back[5].ParentID)
	}

	// A big tree needs several leaves and a branch level.
	big := NewBuilder()
	big.Add(Entry{Path: ".", Type: TypeDirectory, Mode: 0o40755})
	for i := 0; i < 2000; i++ {
		big.Add(Entry{Path: "./f" + itoa(i), Type: TypeFile, Mode: 0o100644, Size: int64(i)})
	}
	buf.Reset()
	if err := big.Build(&buf); err != nil {
		t.Fatal(err)
	}
	parsed, err = Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	back, err = parsed.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 2001 {
		t.Errorf("big tree read back %d entries", len(back))
	}
	if back[1500].Path != "./f1499" || back[1500].Size != 1499 {
		t.Errorf("entry 1500 = %+v", back[1500])
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var d []byte
	for i > 0 {
		d = append([]byte{byte('0' + i%10)}, d...)
		i /= 10
	}
	return string(d)
}

// TestBuilderMatchesApple rebuilds the fixture's bill of materials from
// what Apple's Bom records and checks the two agree entry for entry.
func TestBuilderMatchesApple(t *testing.T) {
	apple := fixtureBom(t, "component-basic.pkg")
	want, err := apple.Paths()
	if err != nil {
		t.Fatal(err)
	}
	b := NewBuilder()
	for _, e := range want {
		if err := b.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := b.Build(&buf); err != nil {
		t.Fatal(err)
	}
	ours, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	got, err := ours.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d entries, Apple has %d", len(got), len(want))
	}
	for i := range want {
		w, g := want[i], got[i]
		if w.Path != g.Path || w.Type != g.Type || w.Mode != g.Mode || w.UID != g.UID || w.GID != g.GID ||
			w.Size != g.Size || w.Checksum != g.Checksum || w.LinkTarget != g.LinkTarget || !w.ModTime.Equal(g.ModTime) ||
			w.Architecture != g.Architecture {
			t.Errorf("entry %d differs\napple: %+v\nours:  %+v", i, w, g)
		}
	}
}
