package flatpkg

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
	"github.com/deploymenttheory/go-macos-pkg/pkg/cpio"
)

func TestSafeRelPath(t *testing.T) {
	cases := []struct {
		in     string
		rel    string
		reason bool
	}{
		{".", ".", false},
		{"./", ".", false},
		{"./usr/local/bin/tool", "usr/local/bin/tool", false},
		{"usr/local", "usr/local", false},
		{"./a/./b", "a/b", false},
		{"/etc/passwd", "", true},
		{"../x", "", true},
		{"./a/../../x", "", true},
		{"a/b/../../..", "", true},
		{"a\x00b", "", true},
		{"./a/b/..", "a", false},
	}
	for _, tc := range cases {
		rel, _, reason := SafeRelPath(tc.in)
		if (reason != "") != tc.reason {
			t.Errorf("SafeRelPath(%q) reason=%q, want refused=%v", tc.in, reason, tc.reason)
			continue
		}
		if !tc.reason && rel != tc.rel {
			t.Errorf("SafeRelPath(%q) = %q, want %q", tc.in, rel, tc.rel)
		}
	}
	if got := sanitizeWindowsName("con.txt"); got != "_con.txt" {
		t.Errorf("reserved name: %q", got)
	}
	if got := sanitizeWindowsName("a:b?c."); got != "a_b_c" {
		t.Errorf("bad chars: %q", got)
	}
}

// buildCPIO makes a gzip-less cpio stream from the given entries.
func buildCPIO(t *testing.T, entries []struct {
	name string
	mode uint32
	data string
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := cpio.NewWriter(&buf)
	for _, e := range entries {
		if err := w.WriteHeader(&cpio.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.data)), NLink: 1}); err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, e.data)
	}
	w.Close()
	return buf.Bytes()
}

func TestExtractCPIO(t *testing.T) {
	data := buildCPIO(t, []struct {
		name string
		mode uint32
		data string
	}{
		{".", cpio.ModeDir | 0o755, ""},
		{"./bin", cpio.ModeDir | 0o755, ""},
		{"./bin/tool", cpio.ModeRegular | 0o755, "#!/bin/sh\n"},
		{"./readme.txt", cpio.ModeRegular | 0o644, "hello"},
		{"./link", cpio.ModeSymlink | 0o755, "readme.txt"},
		{"../escape", cpio.ModeRegular | 0o644, "evil"},
		{"/abs", cpio.ModeRegular | 0o644, "evil"},
		{"./dev/null", cpio.ModeCharDev | 0o666, ""},
	})
	dir := t.TempDir()
	res, err := ExtractCPIO(cpio.NewReader(bytes.NewReader(data)), dir, ExtractOptions{
		Checksums: map[string]uint32{"./readme.txt": bom.CksumBytes([]byte("hello"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 2 || res.Dirs != 2 || res.Symlinks != 1 {
		t.Errorf("counts = %+v", res)
	}
	if len(res.Skipped) != 3 {
		t.Errorf("skipped = %+v", res.Skipped)
	}
	if len(res.Mismatched) != 0 {
		t.Errorf("mismatched = %v", res.Mismatched)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "readme.txt")); string(got) != "hello" {
		t.Errorf("readme.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); err == nil {
		t.Fatal("traversal entry was written outside the destination")
	}
	if _, err := os.Stat("/abs"); err == nil && runtime.GOOS != "windows" {
		t.Log("/abs exists (pre-existing?); traversal check inconclusive")
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, "bin", "tool"))
		if st.Mode().Perm() != 0o755 {
			t.Errorf("tool mode = %o", st.Mode().Perm())
		}
		target, err := os.Readlink(filepath.Join(dir, "link"))
		if err != nil || target != "readme.txt" {
			t.Errorf("link = %q, %v", target, err)
		}
	}

	// Pattern filter and SymlinkFile mode.
	dir2 := t.TempDir()
	res, err = ExtractCPIO(cpio.NewReader(bytes.NewReader(data)), dir2, ExtractOptions{
		Pattern:  regexp.MustCompile(`readme|link`),
		Symlinks: SymlinkFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 || res.Symlinks != 1 || res.Dirs != 0 {
		t.Errorf("pattern counts = %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(dir2, "link")); string(got) != "readme.txt" {
		t.Errorf("SymlinkFile wrote %q", got)
	}

	// A wrong checksum is reported, not silently accepted.
	dir3 := t.TempDir()
	res, _ = ExtractCPIO(cpio.NewReader(bytes.NewReader(data)), dir3, ExtractOptions{
		Checksums: map[string]uint32{"./readme.txt": 1},
	})
	if len(res.Mismatched) != 1 || !res.Partial() {
		t.Errorf("mismatch not reported: %+v", res)
	}
}

func TestExpandFixture(t *testing.T) {
	p, err := Open(fixturePath(t, "product-basic.pkg"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	dir := filepath.Join(t.TempDir(), "out")
	res, err := p.Expand(dir, ExpandOptions{Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Partial() {
		t.Errorf("partial: %+v", res)
	}
	for _, want := range []string{"Distribution", "Resources/welcome.html", "component-basic.pkg/PackageInfo", "component-basic.pkg/Bom", "component-basic.pkg/Payload", "component-basic.pkg/Scripts/postinstall"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(want))); err != nil {
			t.Errorf("%s not written: %v", want, err)
		}
	}
	// Payload stays a gzip cpio without Full.
	head := make([]byte, 3)
	f, _ := os.Open(filepath.Join(dir, "component-basic.pkg", "Payload"))
	f.Read(head)
	f.Close()
	if SniffPayload(head) != PayloadGzip {
		t.Error("Payload was unpacked without Full")
	}
	if _, err := p.Expand(dir, ExpandOptions{}); err == nil {
		t.Error("expanding into an existing directory was allowed")
	}

	full := filepath.Join(t.TempDir(), "full")
	if _, err := p.Expand(full, ExpandOptions{Full: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(full, "component-basic.pkg", "Payload", "usr", "local", "fixture", "hello.txt")); string(got) != "hello, world\n" {
		t.Errorf("Full payload hello.txt = %q", got)
	}
}
