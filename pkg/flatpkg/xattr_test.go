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
		ExtraXattrs: map[string]map[string][]byte{
			"./f": {"user.big": bytes.Repeat([]byte{1}, appledouble.MaxHeader+1)},
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
	o.ExtraXattrs = map[string]map[string][]byte{
		"./f": {appledouble.ResourceForkName: bytes.Repeat([]byte{2}, appledouble.MaxHeader*2)},
	}
	if _, err := collectPayload(o, time.Time{}); err != nil {
		t.Errorf("large resource fork: %v", err)
	}
}
