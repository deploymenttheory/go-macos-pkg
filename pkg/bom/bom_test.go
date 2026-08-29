package bom

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// fixtureBom extracts the Bom of a committed pkgbuild-made package.
func fixtureBom(t *testing.T, name string) *BOM {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "cli", name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s not committed", name)
	}
	x, err := xar.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	f := x.Lookup("Bom")
	if f == nil {
		t.Fatalf("%s has no Bom", name)
	}
	rc, err := x.Open(f)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := Read(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReadsAppleBom(t *testing.T) {
	b := fixtureBom(t, "component-basic.pkg")
	if b.Version != 1 {
		t.Errorf("version = %d", b.Version)
	}
	vars := b.Vars()
	for _, want := range []string{VarBomInfo, VarPaths, VarHLIndex, VarVIndex, VarSize64} {
		found := false
		for _, v := range vars {
			if v == want {
				found = true
			}
		}
		if !found {
			t.Errorf("variable %s missing from %v", want, vars)
		}
	}
	entries, err := b.Paths()
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]Entry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	root, ok := byPath["."]
	if !ok || root.Type != TypeDirectory || root.ID != 1 || root.ParentID != 0 {
		t.Fatalf("root entry: %+v", root)
	}
	hello, ok := byPath["./usr/local/fixture/hello.txt"]
	if !ok {
		t.Fatalf("hello.txt missing; have %d entries", len(entries))
	}
	if hello.Type != TypeFile || hello.Size != 13 || hello.Mode&0o777 != 0o644 || hello.UID != 0 || hello.GID != 0 {
		t.Errorf("hello.txt = %+v", hello)
	}
	if hello.Checksum == 0 {
		t.Error("hello.txt has no checksum")
	}
	if hello.ModTime.Year() != 2024 {
		t.Errorf("hello.txt mtime = %v", hello.ModTime)
	}
	tool := byPath["./usr/local/fixture/bin/tool"]
	if tool.Mode&0o777 != 0o755 {
		t.Errorf("tool mode = %o", tool.Mode)
	}
	link, ok := byPath["./usr/local/fixture/link"]
	if !ok || link.Type != TypeLink || link.LinkTarget != "hello.txt" {
		t.Errorf("link = %+v", link)
	}
	big := byPath["./usr/local/fixture/big.bin"]
	if big.Size != 307200 {
		t.Errorf("big.bin size = %d", big.Size)
	}
	if _, ok := byPath["./usr/local/fixture/unicode-é.txt"]; !ok {
		t.Error("unicode name not found")
	}
	if _, err := ReadFile(filepath.Join("..", "..", "go.mod")); err == nil {
		t.Error("go.mod parsed as a BOM")
	}
	_ = io.EOF
}
