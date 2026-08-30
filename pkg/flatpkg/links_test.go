package flatpkg

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/appledouble"
	"github.com/deploymenttheory/go-macos-pkg/pkg/cpio"
)

// linksProbe is testdata/cli/component-links.probe.json: pkgbuild's
// payload and bill of materials for the hard-link/xattr tree, decoded.
type linksProbe struct {
	Payload []struct {
		Name        string `json:"name"`
		Ino         uint64 `json:"ino"`
		NLink       uint32 `json:"nlink"`
		Mode        string `json:"mode"`
		Size        int64  `json:"size"`
		AppleDouble *struct {
			Raw string `json:"raw"`
		} `json:"appleDouble"`
	} `json:"Payload"`
	Bom struct {
		Records []struct {
			Path   string `json:"path"`
			Record string `json:"record"`
		} `json:"records"`
		HLIndex struct {
			Leaves []struct {
				Path string `json:"path"`
			} `json:"leaves"`
		} `json:"hlindex"`
	} `json:"bom"`
}

func loadLinksProbe(t *testing.T) linksProbe {
	t.Helper()
	b, err := os.ReadFile("../../testdata/cli/component-links.probe.json")
	if err != nil {
		t.Skip("no probe:", err)
	}
	var p linksProbe
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// linksTree recreates the fixture tree scripts/gen-fixtures.sh builds,
// with the attributes supplied as overrides so the test runs on every
// host; hard links need os.Link.
func linksTree(t *testing.T) (string, []XattrOverride) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(old, new string) {
		if err := os.Link(filepath.Join(root, filepath.FromSlash(old)), filepath.Join(root, filepath.FromSlash(new))); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "shared content\n")
	link("a.txt", "b.txt")
	os.MkdirAll(filepath.Join(root, "d"), 0o755)
	link("a.txt", "d/c.txt")
	write("p", "three links\n")
	link("p", "q")
	link("p", "r")
	write("attrs/x", "has attributes\n")
	write("attrs/finder", "finder\n")
	write("attrs/rsrc", "rsrc\n")
	write("attrs/empty", "")
	if err := os.Symlink("x", filepath.Join(root, "attrs", "link")); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 300)
	for i := range big {
		big[i] = byte(i * 7)
	}
	xattrs := []XattrOverride{
		{Path: "./attrs/x", Xattrs: map[string][]byte{"com.example.one": []byte("hello"), "com.example.big": big}},
		{Path: "./attrs/finder", Xattrs: map[string][]byte{appledouble.FinderInfoName: bytes.Repeat([]byte{0x41}, 32)}},
		{Path: "./attrs/rsrc", Xattrs: map[string][]byte{appledouble.ResourceForkName: []byte("resource fork bytes")}},
		{Path: "./attrs/empty", Xattrs: map[string][]byte{"com.example.empty": []byte("v")}},
		{Path: "./attrs/link", Xattrs: map[string][]byte{"com.example.onlink": []byte("yes")}},
		{Path: "./attrs", Xattrs: map[string][]byte{"com.example.ondir": []byte("dirval")}},
	}
	return root, xattrs
}

// TestLinksAndXattrsMatchPkgbuild builds the links tree and compares the
// payload and bill of materials with pkgbuild's, entry by entry: inode
// sharing, link counts, sidecar placement and bytes, Bom records and the
// HLIndex membership. pkgbuild's host stamped com.apple.provenance on
// everything, so that attribute is removed from its sidecars before
// comparing, and sidecars that carried nothing else are expected to be
// absent from ours.
func TestLinksAndXattrsMatchPkgbuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard links are not detected on Windows")
	}
	probe := loadLinksProbe(t)
	root, xattrs := linksTree(t)
	// The 300-byte value is the fixture generator's; take it from
	// pkgbuild's own sidecar so the bytes can be compared whole.
	for _, e := range probe.Payload {
		if e.Name == "./attrs/._x" && e.AppleDouble != nil {
			raw, _ := base64.StdEncoding.DecodeString(e.AppleDouble.Raw)
			f, err := appledouble.Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			attrs := f.Xattrs()
			delete(attrs, "com.apple.provenance")
			for i := range xattrs {
				if xattrs[i].Path == "./attrs/x" {
					xattrs[i].Xattrs = attrs
				}
			}
		}
	}
	epoch := time.Unix(1704164645, 0).UTC()
	outPath := filepath.Join(t.TempDir(), "links.pkg")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := BuildComponent(ComponentOptions{
		Root: root, Identifier: "com.deploymenttheory.fixture.links", Version: "1.0",
		Epoch: epoch, TempDir: t.TempDir(), XattrOverrides: xattrs, Xattrs: XattrsNone,
	}, out)
	out.Close()
	if err != nil {
		t.Fatal(err)
	}
	// What pkgbuild's entries look like once provenance is gone.
	type want struct {
		ino, nlink uint64
		sidecar    []byte
	}
	wants := map[string]want{}
	inoOf := map[string]uint64{}
	for _, e := range probe.Payload {
		w := want{ino: e.Ino, nlink: uint64(e.NLink)}
		if e.AppleDouble != nil {
			raw, _ := base64.StdEncoding.DecodeString(e.AppleDouble.Raw)
			f, err := appledouble.Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			kept := f.Attrs[:0]
			for _, a := range f.Attrs {
				if a.Name != "com.apple.provenance" {
					kept = append(kept, a)
				}
			}
			f.Attrs = kept
			if f.Empty() {
				continue // pkgbuild wrote it only for provenance
			}
			w.sidecar, _ = f.Encode()
		}
		wants[e.Name] = w
		inoOf[e.Name] = e.Ino
	}

	pkg, err := Open(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer pkg.Close()
	comp := pkg.Components[0]
	payload, err := comp.OpenPayload()
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()
	gz, err := gzip.NewReader(payload)
	if err != nil {
		t.Fatal(err)
	}
	cr := cpio.NewReader(gz)
	got := map[string]*cpio.Header{}
	data := map[string][]byte{}
	var order []string
	for {
		h, err := cr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(cr)
		got[h.Name] = h
		data[h.Name] = b
		order = append(order, h.Name)
	}
	if len(got) != len(wants) {
		t.Errorf("%d entries, pkgbuild (minus provenance-only sidecars) has %d\nours: %v", len(got), len(wants), order)
	}
	for name, w := range wants {
		h := got[name]
		if h == nil {
			t.Errorf("%s: missing", name)
			continue
		}
		if w.sidecar != nil && !bytes.Equal(data[name], w.sidecar) {
			t.Errorf("%s: sidecar bytes differ\n got %x\nwant %x", name, data[name], w.sidecar)
		}
		if uint64(h.NLink) != w.nlink {
			t.Errorf("%s: nlink %d, pkgbuild %d", name, h.NLink, w.nlink)
		}
	}
	// Inode sharing: the same partition of names as pkgbuild's.
	for a, ia := range inoOf {
		for b, ib := range inoOf {
			if got[a] == nil || got[b] == nil || a >= b {
				continue
			}
			if (ia == ib) != (got[a].Inode == got[b].Inode) {
				t.Errorf("%s and %s: share an inode in ours=%v, pkgbuild=%v", a, b, got[a].Inode == got[b].Inode, ia == ib)
			}
		}
	}
	// Placement: a file's sidecar right after it, a directory's after
	// its subtree.
	pos := map[string]int{}
	for i, n := range order {
		pos[n] = i
	}
	if pos["./attrs/._x"] != pos["./attrs/x"]+1 {
		t.Errorf("file sidecar not adjacent: %v", order)
	}
	if pos["./._attrs"] < pos["./attrs/x"] || pos["./._attrs"] < pos["./attrs/._link"] {
		t.Errorf("directory sidecar before its subtree: %v", order)
	}

	// Bill of materials: sidecar records as pkgbuild writes them (the
	// bytes are pinned in pkg/bom; here the decoded fields), and every
	// path present.
	b, err := comp.Bom()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := b.Paths()
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]string{}
	for _, r := range probe.Bom.Records {
		records[r.Path] = r.Record
	}
	modes := map[string]uint16{}
	for _, pe := range paths {
		modes[pe.Path] = pe.Mode
	}
	for _, pe := range paths {
		if records[pe.Path] == "" {
			t.Errorf("%s: pkgbuild has no such Bom record", pe.Path)
			continue
		}
		if !appledouble.IsSidecarName(pe.Path) {
			continue
		}
		owner, _ := appledouble.OwnerName(pe.Path)
		if pe.Architecture != 1 || pe.Mode != modes[owner] || pe.Size != 0 || pe.Checksum != 0 || !pe.ModTime.IsZero() && pe.ModTime.Unix() != 0 {
			t.Errorf("%s: Bom record %+v (owner mode %o)", pe.Path, pe, modes[owner])
		}
	}
	if len(paths) != len(wants) {
		t.Errorf("Bom has %d paths, want %d", len(paths), len(wants))
	}
	if res.InstallKBytes != 4 {
		t.Errorf("installKBytes = %d, pkgbuild wrote 4 (hard-link sets counted once)", res.InstallKBytes)
	}
	if res.NumberOfFiles != len(wants) {
		t.Errorf("numberOfFiles = %d, want %d", res.NumberOfFiles, len(wants))
	}
}

// TestSidecarFilesAreLifted packages a tree that already holds "._"
// files (exported from macOS) and gets the same sidecars as a build with
// the attributes on the files.
func TestSidecarFilesAreLifted(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "f"), []byte("data\n"), 0o644)
	f := appledouble.FromXattrs(map[string][]byte{"com.example.k": []byte("v")})
	raw, _ := f.Encode()
	os.WriteFile(filepath.Join(root, "._f"), raw, 0o644)
	os.WriteFile(filepath.Join(root, "._orphan"), []byte("not appledouble"), 0o644)

	build := func(o ComponentOptions) *BuildResult {
		o.Root, o.Identifier, o.Version, o.TempDir = root, "x", "1", t.TempDir()
		o.Epoch = time.Unix(1704164645, 0).UTC()
		o.Xattrs = XattrsNone
		res, err := BuildComponent(o, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := build(ComponentOptions{})
	// . f ._f ._orphan
	if res.NumberOfFiles != 4 {
		t.Errorf("numberOfFiles = %d, want 4", res.NumberOfFiles)
	}
	if res.InstallKBytes != 1 { // f and ._orphan, 512 bytes each; the sidecar is not counted
		t.Errorf("installKBytes = %d, want 1", res.InstallKBytes)
	}
	// Through the walker directly: the lifted attribute is on f.
	entries, err := collectPayload(ComponentOptions{Root: root, Xattrs: XattrsNone}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.rel)
		if e.rel == "./f" && (e.xattrs == nil || len(e.xattrs.Attrs) != 1 || e.xattrs.Attrs[0].Name != "com.example.k") {
			t.Errorf("f did not get the lifted attribute: %+v", e.xattrs)
		}
		if e.rel == "./._f" && !bytes.Equal(e.sidecar, raw) {
			t.Errorf("._f re-emitted differently")
		}
	}
	if len(names) != 4 || names[0] != "." || names[1] != "./._orphan" || names[2] != "./f" || names[3] != "./._f" {
		t.Errorf("order = %v", names)
	}
}

// TestExcludeXattrCoversLiftedSidecars checks that --exclude-xattr prunes
// attributes lifted from "._" files in the tree as it prunes the ones
// read from the host. Otherwise a macOS build and a Linux build of the
// same exported tree would disagree, which is what lifting is for.
func TestExcludeXattrCoversLiftedSidecars(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "f"), []byte("data\n"), 0o644)
	os.WriteFile(filepath.Join(root, "g"), []byte("data\n"), 0o644)
	raw, _ := appledouble.FromXattrs(map[string][]byte{
		"com.apple.provenance": []byte("host"),
		"com.example.keep":     []byte("v"),
	}).Encode()
	os.WriteFile(filepath.Join(root, "._f"), raw, 0o644)
	onlyNoise, _ := appledouble.FromXattrs(map[string][]byte{"com.apple.provenance": []byte("host")}).Encode()
	os.WriteFile(filepath.Join(root, "._g"), onlyNoise, 0o644)

	o := ComponentOptions{
		Root:         root,
		Xattrs:       XattrsNone,
		ExcludeXattr: func(n string) bool { return n == "com.apple.provenance" },
	}
	entries, err := collectPayload(o, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.rel)
		if e.rel == "./f" {
			got := e.xattrs.Xattrs()
			if len(got) != 1 || string(got["com.example.keep"]) != "v" {
				t.Errorf("f attributes = %v, want only com.example.keep", got)
			}
		}
		if e.rel == "./g" && e.xattrs != nil {
			t.Errorf("g kept excluded attributes: %+v", e.xattrs)
		}
	}
	// g's sidecar held nothing else, so no "._g" is re-emitted.
	for _, n := range names {
		if n == "./._g" {
			t.Errorf("._g survived with only excluded attributes: %v", names)
		}
	}
}
