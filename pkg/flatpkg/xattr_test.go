package flatpkg

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/appledouble"
	"github.com/deploymenttheory/go-macos-pkg/pkg/cpio"
)

// sidecarPayload writes a cpio with a file, its sidecar, a directory with
// a sidecar after its subtree, and a hard-link pair, the way pkgbuild lays
// them out.
func sidecarPayload(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	cw := cpio.NewWriter(&buf)
	now := time.Unix(1704164645, 0)
	add := func(h *cpio.Header, data []byte) {
		h.ModTime = now
		if h.NLink == 0 {
			h.NLink = 1
		}
		h.Size = int64(len(data))
		if err := cw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		cw.Write(data)
	}
	sc := func(attrs map[string][]byte) []byte {
		b, _ := appledouble.FromXattrs(attrs).Encode()
		return b
	}
	add(&cpio.Header{Name: ".", Inode: 1, Mode: cpio.ModeDir | 0o755, NLink: 4}, nil)
	add(&cpio.Header{Name: "./d", Inode: 2, Mode: cpio.ModeDir | 0o755, NLink: 3}, nil)
	add(&cpio.Header{Name: "./d/f", Inode: 3, Mode: cpio.ModeRegular | 0o644}, []byte("file\n"))
	add(&cpio.Header{Name: "./d/._f", Inode: 4, Mode: cpio.ModeRegular | 0o644}, sc(map[string][]byte{"user.one": []byte("1"), "user.two": []byte("22")}))
	add(&cpio.Header{Name: "./._d", Inode: 5, Mode: cpio.ModeRegular | 0o644, NLink: 3}, sc(map[string][]byte{"user.dir": []byte("d")}))
	add(&cpio.Header{Name: "./a", Inode: 6, Mode: cpio.ModeRegular | 0o644, NLink: 2}, []byte("linked\n"))
	add(&cpio.Header{Name: "./._a", Inode: 6, Mode: cpio.ModeRegular | 0o644, NLink: 2}, sc(map[string][]byte{"user.l": []byte("x")}))
	add(&cpio.Header{Name: "./b", Inode: 6, Mode: cpio.ModeRegular | 0o644, NLink: 2}, []byte("linked\n"))
	add(&cpio.Header{Name: "./._b", Inode: 6, Mode: cpio.ModeRegular | 0o644, NLink: 2}, sc(map[string][]byte{"user.l": []byte("x")}))
	add(&cpio.Header{Name: "./._plain", Inode: 7, Mode: cpio.ModeRegular | 0o644}, []byte("not appledouble"))
	if err := cw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractSidecarsAndHardLinks(t *testing.T) {
	payload := sidecarPayload(t)
	extract := func(o ExtractOptions) (string, *ExtractResult) {
		dir := t.TempDir()
		res, err := ExtractCPIO(cpio.NewReader(bytes.NewReader(payload)), dir, o)
		if err != nil {
			t.Fatal(err)
		}
		return dir, res
	}

	t.Run("file", func(t *testing.T) {
		dir, res := extract(ExtractOptions{Xattrs: XattrFile})
		if _, err := os.Stat(filepath.Join(dir, "d", "._f")); err != nil {
			t.Error("sidecar not written as a file")
		}
		// d/f ._f ._d a ._a b ._b ._plain; b linked to a, the sidecars not.
		if res.Xattrs != 0 || res.Files != 8 || res.HardLinks != 1 {
			t.Errorf("result %+v", res)
		}
	})
	t.Run("skip", func(t *testing.T) {
		dir, res := extract(ExtractOptions{Xattrs: XattrSkip})
		if _, err := os.Stat(filepath.Join(dir, "d", "._f")); err == nil {
			t.Error("sidecar written despite skip")
		}
		if _, err := os.Stat(filepath.Join(dir, "._plain")); err != nil {
			t.Error("a plain ._ file must still be extracted")
		}
		if len(res.Skipped) != 4 || res.Files != 4 {
			t.Errorf("result %+v", res)
		}
	})
	t.Run("apply", func(t *testing.T) {
		if !hostXattrsSupported {
			t.Skip("no extended attributes on this host")
		}
		dir, res := extract(ExtractOptions{Xattrs: XattrApply})
		if _, err := os.Stat(filepath.Join(dir, "d", "._f")); err == nil {
			t.Error("sidecar written despite apply")
		}
		got, err := hostXattrs(filepath.Join(dir, "d", "f"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got["user.one"]) != "1" || string(got["user.two"]) != "22" {
			t.Errorf("attributes on d/f: %q", got)
		}
		got, _ = hostXattrs(filepath.Join(dir, "d"))
		if string(got["user.dir"]) != "d" {
			t.Errorf("attributes on d: %q", got)
		}
		if res.Xattrs != 4 || len(res.Skipped) != 0 {
			t.Errorf("result %+v", res)
		}
	})
	t.Run("hard links", func(t *testing.T) {
		dir, res := extract(ExtractOptions{Xattrs: XattrSkip})
		a, _ := os.Stat(filepath.Join(dir, "a"))
		b, _ := os.Stat(filepath.Join(dir, "b"))
		if a == nil || b == nil || !os.SameFile(a, b) {
			t.Error("a and b are not the same file")
		}
		if res.HardLinks != 1 {
			t.Errorf("hardLinks = %d", res.HardLinks)
		}
		dir, res = extract(ExtractOptions{Xattrs: XattrSkip, NoHardLinks: true})
		a, _ = os.Stat(filepath.Join(dir, "a"))
		b, _ = os.Stat(filepath.Join(dir, "b"))
		if runtime.GOOS != "windows" && os.SameFile(a, b) {
			t.Error("copies requested, got a link")
		}
		if res.HardLinks != 0 {
			t.Errorf("hardLinks = %d", res.HardLinks)
		}
	})
}

// TestBuildReadsHostXattrs sets attributes on a tree and builds it: the
// sidecar bytes equal what FromXattrs gives for the same map.
func TestBuildReadsHostXattrs(t *testing.T) {
	if !hostXattrsSupported {
		t.Skip("no extended attributes on this host")
	}
	root := filepath.Join(t.TempDir(), "root")
	os.MkdirAll(root, 0o755)
	f := filepath.Join(root, "f")
	os.WriteFile(f, []byte("x"), 0o644)
	want := map[string][]byte{"user.a": []byte("1"), "user.b": bytes.Repeat([]byte{7}, 300)}
	if err := setHostXattrs(f, want); err != nil {
		t.Skip("cannot set attributes here:", err)
	}
	entries, err := collectPayload(ComponentOptions{Root: root, ExcludeXattr: hostNoise}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[2].rel != "./._f" {
		t.Fatalf("entries: %+v", entries)
	}
	wantRaw, _ := appledouble.FromXattrs(want).Encode()
	if !bytes.Equal(entries[2].sidecar, wantRaw) {
		t.Errorf("sidecar differs from FromXattrs")
	}
	// None: nothing from the host.
	entries, _ = collectPayload(ComponentOptions{Root: root, Xattrs: XattrsNone}, time.Time{})
	if len(entries) != 2 {
		t.Errorf("XattrsNone still produced %d entries", len(entries))
	}
}

// TestOversizeXattrsAreAnError covers attributes too large for
// AppleDouble's 64 KiB header. They can come from the host or, on any
// platform, from a manifest's file_xattrs, so the build has to report
// them rather than fail inside the encoder.
func TestOversizeXattrsAreAnError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := ComponentOptions{
		Root:   root,
		Xattrs: XattrsNone,
		XattrOverrides: []XattrOverride{
			{Path: "./f", Xattrs: map[string][]byte{"user.big": bytes.Repeat([]byte{1}, appledouble.MaxHeader+1)}},
		},
	}
	_, err := collectPayload(o, time.Time{})
	if err == nil {
		t.Fatal("oversize attributes were accepted")
	}
	if !errors.Is(err, appledouble.ErrTooLarge) {
		t.Errorf("error = %v, want %v", err, appledouble.ErrTooLarge)
	}
	if !strings.Contains(err.Error(), "./f") {
		t.Errorf("error %q does not name the file", err)
	}
	// A resource fork lives past the header, so a large one is fine.
	o.XattrOverrides = []XattrOverride{
		{Path: "./f", Xattrs: map[string][]byte{appledouble.ResourceForkName: bytes.Repeat([]byte{2}, appledouble.MaxHeader*2)}},
	}
	if _, err := collectPayload(o, time.Time{}); err != nil {
		t.Errorf("large resource fork: %v", err)
	}
}

// TestRefusedXattrsSurviveAsSidecarFiles is the round trip that matters
// on a host without Apple's attributes: unpacking a package built on
// macOS must not drop com.apple.* names that Linux will not store, and
// repacking the unpacked tree must put them back.
func TestRefusedXattrsSurviveAsSidecarFiles(t *testing.T) {
	if !hostXattrsSupported {
		t.Skip("no extended attributes on this host")
	}
	// Stand in a host that takes user.* and refuses everything else, as
	// Linux does.
	real := setXattr
	setXattr = func(p, name string, value []byte) error {
		if !strings.HasPrefix(name, "user.") {
			return errors.New("operation not supported")
		}
		return real(p, name, value)
	}
	defer func() { setXattr = real }()

	want := map[string][]byte{
		"user.kept":            []byte("k"),
		"com.apple.provenance": []byte("host"),
		"com.example.colour":   []byte("blue"),
	}
	var buf bytes.Buffer
	cw := cpio.NewWriter(&buf)
	now := time.Unix(1704164645, 0)
	raw, _ := appledouble.FromXattrs(want).Encode()
	for _, e := range []struct {
		name string
		mode uint32
		data []byte
	}{
		{".", cpio.ModeDir | 0o755, nil},
		{"./f", cpio.ModeRegular | 0o644, []byte("data\n")},
		{"./._f", cpio.ModeRegular | 0o644, raw},
	} {
		if err := cw.WriteHeader(&cpio.Header{
			Name: e.name, Inode: 1, Mode: e.mode, NLink: 1,
			ModTime: now, Size: int64(len(e.data)),
		}); err != nil {
			t.Fatal(err)
		}
		cw.Write(e.data)
	}
	if err := cw.Close(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	res, err := ExtractCPIO(cpio.NewReader(bytes.NewReader(buf.Bytes())), dir, ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Nothing was lost, so the extraction is not partial.
	if res.Partial() {
		t.Errorf("partial: skipped %+v, mismatched %v", res.Skipped, res.Mismatched)
	}
	if res.Xattrs != 1 || res.XattrFiles != 1 {
		t.Errorf("xattrs = %d, xattrFiles = %d, want 1 and 1", res.Xattrs, res.XattrFiles)
	}
	// The name the host took is on the file.
	got, err := hostXattrs(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got["user.kept"]) != "k" {
		t.Errorf("user.kept was not applied: %v", got)
	}
	// The names it refused are beside it, in a sidecar of their own.
	side, err := os.ReadFile(filepath.Join(dir, "._f"))
	if err != nil {
		t.Fatalf("refused attributes were dropped: %v", err)
	}
	kept, err := appledouble.Decode(side)
	if err != nil {
		t.Fatal(err)
	}
	if k := kept.Xattrs(); len(k) != 2 ||
		string(k["com.apple.provenance"]) != "host" ||
		string(k["com.example.colour"]) != "blue" {
		t.Errorf("kept sidecar holds %v", kept.Xattrs())
	}

	// Repacking the unpacked tree restores every attribute.
	entries, err := collectPayload(ComponentOptions{Root: dir}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.rel != "./f" {
			continue
		}
		round := e.xattrs.Xattrs()
		if len(round) != len(want) {
			t.Fatalf("repacked %d attributes, want %d: %v", len(round), len(want), round)
		}
		for name, value := range want {
			if !bytes.Equal(round[name], value) {
				t.Errorf("repacked %s = %q, want %q", name, round[name], value)
			}
		}
	}
}

// TestXattrOverrides covers the repack rules: what the tree carries is
// reapplied by default, and a rule overrides it for one file or for a
// folder and everything under it, with new values.
func TestXattrOverrides(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	for _, d := range []string{"keep", "sub/deep"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"keep/a", "sub/b", "sub/deep/c"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(f)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The tree already carries attributes, as an unpacked package does.
	for _, f := range []string{"keep/a", "sub/b", "sub/deep/c"} {
		raw, _ := appledouble.FromXattrs(map[string][]byte{"com.example.orig": []byte("tree")}).Encode()
		side := appledouble.SidecarName(filepath.ToSlash(f))
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(side)), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	attrsOf := func(entries []payloadEntry, rel string) map[string][]byte {
		for _, e := range entries {
			if e.rel == rel {
				if e.xattrs == nil {
					return nil
				}
				return e.xattrs.Xattrs()
			}
		}
		t.Fatalf("%s not in the payload", rel)
		return nil
	}

	// Default: the tree's attributes are reapplied, untouched.
	entries, err := collectPayload(ComponentOptions{Root: root, Xattrs: XattrsNone}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"./keep/a", "./sub/b", "./sub/deep/c"} {
		if got := attrsOf(entries, rel); string(got["com.example.orig"]) != "tree" {
			t.Errorf("%s = %v, want the tree's attribute reapplied", rel, got)
		}
	}

	// One file gets a new value; a folder rule covers the folder and
	// everything beneath it and replaces what is there.
	entries, err = collectPayload(ComponentOptions{
		Root:   root,
		Xattrs: XattrsNone,
		XattrOverrides: []XattrOverride{
			{Path: "keep/a", Xattrs: map[string][]byte{"com.example.new": []byte("v")}},
			{Path: "sub/", Xattrs: map[string][]byte{"com.example.folder": []byte("f")}, Replace: true},
		},
	}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// Merged: the tree's value survives beside the new one.
	got := attrsOf(entries, "./keep/a")
	if len(got) != 2 || string(got["com.example.orig"]) != "tree" || string(got["com.example.new"]) != "v" {
		t.Errorf("./keep/a = %v, want the tree's attribute plus the new one", got)
	}
	// Replaced, throughout the folder, the directory entry included.
	for _, rel := range []string{"./sub", "./sub/b", "./sub/deep", "./sub/deep/c"} {
		got := attrsOf(entries, rel)
		if len(got) != 1 || string(got["com.example.folder"]) != "f" {
			t.Errorf("%s = %v, want only the folder rule's attribute", rel, got)
		}
	}

	// Replace with nothing strips a path.
	entries, err = collectPayload(ComponentOptions{
		Root:           root,
		Xattrs:         XattrsNone,
		XattrOverrides: []XattrOverride{{Path: "./", Replace: true}},
	}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.xattrs != nil || e.sidecar != nil {
			t.Errorf("%s kept attributes after a whole-tree replace", e.rel)
		}
	}

	// A rule that matches nothing is a mistake, not a silent no-op.
	_, err = collectPayload(ComponentOptions{
		Root:           root,
		Xattrs:         XattrsNone,
		XattrOverrides: []XattrOverride{{Path: "./nope"}},
	}, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "./nope") {
		t.Errorf("unmatched rule error = %v", err)
	}
}
