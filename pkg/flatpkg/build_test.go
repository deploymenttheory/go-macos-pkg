package flatpkg

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
)

// makeTree writes a deterministic source tree and returns its root.
func makeTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	mk := func(rel, content string, mode os.FileMode) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" {
			os.Chmod(p, mode)
		}
	}
	mk("usr/local/fixture/hello.txt", "hello, world\n", 0o644)
	mk("usr/local/fixture/empty.txt", "", 0o644)
	mk("usr/local/fixture/bin/tool", "#!/bin/sh\necho tool\n", 0o755)
	mk("usr/local/fixture/sub/nested/deep.txt", "deep\n", 0o644)
	mk("usr/local/fixture/big.bin", string(bytes.Repeat([]byte{1, 2, 3, 4}, 1200)), 0o644)
	if runtime.GOOS != "windows" {
		if err := os.Symlink("hello.txt", filepath.Join(root, "usr", "local", "fixture", "link")); err != nil {
			t.Fatal(err)
		}
	}
	mk("Applications/Fixture.app/Contents/Info.plist", `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.example.fixture</string>
<key>CFBundleShortVersionString</key><string>1.0</string>
<key>CFBundleVersion</key><string>100</string>
</dict></plist>`, 0o644)
	mk("Applications/Fixture.app/Contents/MacOS/Fixture", "#!/bin/sh\n", 0o755)
	return root
}

func TestBuildComponent(t *testing.T) {
	root := makeTree(t)
	scripts := filepath.Join(t.TempDir(), "scripts")
	os.MkdirAll(scripts, 0o755)
	os.WriteFile(filepath.Join(scripts, "postinstall"), []byte("#!/bin/sh\nexit 0\n"), 0o644) // deliberately not +x
	os.WriteFile(filepath.Join(scripts, "helper.txt"), []byte("resource"), 0o644)

	epoch := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	build := func() ([]byte, *BuildResult) {
		var out bytes.Buffer
		res, err := BuildComponent(ComponentOptions{
			Root:            root,
			Scripts:         scripts,
			Identifier:      "com.example.test",
			Version:         "1.2.3",
			InstallLocation: "/",
			Epoch:           epoch,
			TempDir:         t.TempDir(),
			Executable: func(rel string) bool {
				return rel == "./usr/local/fixture/bin/tool" || rel == "./Applications/Fixture.app/Contents/MacOS/Fixture"
			},
		}, &out)
		if err != nil {
			t.Fatal(err)
		}
		return out.Bytes(), res
	}
	pkgBytes, res := build()
	again, _ := build()
	if !bytes.Equal(pkgBytes, again) {
		t.Error("two builds with the same epoch differ")
	}

	path := filepath.Join(t.TempDir(), "out.pkg")
	os.WriteFile(path, pkgBytes, 0o644)
	p, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Kind != KindComponent {
		t.Fatal("not a component package")
	}
	c := p.Components[0]
	if c.Info.Identifier != "com.example.test" || c.Info.Version != "1.2.3" || c.Info.FormatVersion != 2 || c.Info.Auth != "root" {
		t.Errorf("PackageInfo: %+v", c.Info)
	}
	if c.Info.Payload == nil || c.Info.Payload.NumberOfFiles != res.NumberOfFiles {
		t.Errorf("payload element: %+v vs %+v", c.Info.Payload, res)
	}
	// . usr local fixture hello empty bin tool sub nested deep big
	// Applications Fixture.app Contents Info.plist MacOS Fixture
	wantFiles := 18
	if runtime.GOOS != "windows" {
		wantFiles++ // the symlink
	}
	if res.NumberOfFiles != wantFiles {
		t.Errorf("numberOfFiles = %d, want %d", res.NumberOfFiles, wantFiles)
	}
	if got := c.Info.Scripts.Names(); len(got) != 1 || got[0] != "postinstall" {
		t.Errorf("scripts = %v", got)
	}
	if len(res.Bundles) != 1 || res.Bundles[0].ID != "com.example.fixture" || res.Bundles[0].Path != "./Applications/Fixture.app" {
		t.Errorf("bundles = %+v", res.Bundles)
	}
	if c.Info.BundleVersion == nil || len(c.Info.BundleVersion.Bundles) != 1 || c.Info.Relocate == nil || len(c.Info.Relocate.Bundles) != 1 {
		t.Errorf("bundle-version/relocate not written: %+v %+v", c.Info.BundleVersion, c.Info.Relocate)
	}
	// Archive entries in pkgbuild's order, TOC digest valid, every entry verifies.
	var names []string
	for _, f := range p.XAR.Files() {
		names = append(names, f.Name())
		if err := p.XAR.Verify(f); err != nil {
			t.Errorf("%s: %v", f.Name(), err)
		}
	}
	if want := []string{"Bom", "Payload", "Scripts", "PackageInfo"}; !equal(names, want) {
		t.Errorf("entries = %v, want %v", names, want)
	}
	if !p.XAR.TOCDigestValid() {
		t.Error("TOC digest invalid")
	}
	if got := p.XAR.TOC().CreationTime; got != "2024-01-02T03:04:05" {
		t.Errorf("creation-time = %q", got)
	}

	// Payload and Bom agree with each other and with the source.
	b, err := c.Bom()
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := b.Paths()
	byPath := map[string]bom.Entry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	if len(entries) != res.NumberOfFiles {
		t.Errorf("bom has %d entries, payload %d", len(entries), res.NumberOfFiles)
	}
	hello := byPath["./usr/local/fixture/hello.txt"]
	if hello.Type != bom.TypeFile || hello.Size != 13 || hello.Checksum != 0x535fbd37 || hello.Mode != 0o100644 || hello.UID != 0 || hello.GID != 0 {
		t.Errorf("hello.txt bom entry = %+v", hello)
	}
	if !hello.ModTime.Equal(epoch) {
		t.Errorf("hello.txt mtime = %v, want epoch", hello.ModTime)
	}
	if tool := byPath["./usr/local/fixture/bin/tool"]; tool.Mode != 0o100755 {
		t.Errorf("tool mode = %o", tool.Mode)
	}
	if rootEntry := byPath["."]; rootEntry.Type != bom.TypeDirectory || rootEntry.Mode != 0o40755 || rootEntry.Size != 32*(2+2) {
		t.Errorf("root entry = %+v", rootEntry)
	}
	// Nested directories count their own children: fixture/ holds hello,
	// empty, bin, sub, big (and link on Unix).
	wantFixtureKids := 5
	if runtime.GOOS != "windows" {
		wantFixtureKids++
	}
	if d := byPath["./usr/local/fixture"]; d.Size != int64(32*(wantFixtureKids+2)) {
		t.Errorf("fixture dir size = %d, want %d (%d children)", d.Size, 32*(wantFixtureKids+2), wantFixtureKids)
	}
	if d := byPath["./usr/local/fixture/sub"]; d.Size != 32*(1+2) {
		t.Errorf("sub dir size = %d, want %d (1 child)", d.Size, 32*3)
	}
	if runtime.GOOS != "windows" {
		link := byPath["./usr/local/fixture/link"]
		if link.Type != bom.TypeLink || link.LinkTarget != "hello.txt" || link.Size != 9 {
			t.Errorf("link entry = %+v", link)
		}
	}
	cr, enc, closer, err := c.OpenPayloadCPIO()
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	if enc != PayloadGzip {
		t.Errorf("payload encoding = %s", enc)
	}
	n := 0
	for {
		h, err := cr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		n++
		if h.UID != 0 || h.GID != 0 {
			t.Errorf("%s owner %d:%d", h.Name, h.UID, h.GID)
		}
		if h.Name == "./usr/local/fixture/big.bin" {
			data, _ := io.ReadAll(cr)
			if len(data) != 4800 {
				t.Errorf("big.bin = %d bytes", len(data))
			}
		}
	}
	if n != res.NumberOfFiles {
		t.Errorf("payload has %d entries, PackageInfo says %d", n, res.NumberOfFiles)
	}
	// installKBytes by the pkgbuild formula: entries except root, rounded
	// up to 512-byte blocks, in whole KB. Seven small files and the link
	// are a block each, big.bin ten, and ten directories one each: 13312
	// bytes, 13 KB. Without the symlink (Windows) it is 12800, still 13.
	if res.InstallKBytes != 13 {
		t.Errorf("installKBytes = %d, want 13", res.InstallKBytes)
	}

	// Scripts: forced executable, resources carried.
	sr, _, sc, err := c.OpenScriptsCPIO()
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	seen := map[string]uint32{}
	for {
		h, err := sr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen[h.Name] = h.Mode
	}
	if seen["./postinstall"]&0o111 == 0 {
		t.Errorf("postinstall mode = %o, want executable", seen["./postinstall"])
	}
	if _, ok := seen["./helper.txt"]; !ok {
		t.Error("script resource not carried")
	}

	// A scripts dir without any known script is refused, and so is a
	// missing identifier.
	empty := t.TempDir()
	if _, err := BuildComponent(ComponentOptions{Root: root, Scripts: empty, Identifier: "x", Version: "1"}, io.Discard); err == nil {
		t.Error("empty scripts dir accepted")
	}
	if _, err := BuildComponent(ComponentOptions{Root: root, Version: "1"}, io.Discard); err == nil {
		t.Error("missing identifier accepted")
	}
	// --nopayload
	var out bytes.Buffer
	if _, err := BuildComponent(ComponentOptions{NoPayload: true, Scripts: scripts, Identifier: "x", Version: "1", TempDir: t.TempDir()}, &out); err != nil {
		t.Fatal(err)
	}
	np := filepath.Join(t.TempDir(), "np.pkg")
	os.WriteFile(np, out.Bytes(), 0o644)
	npp, err := Open(np)
	if err != nil {
		t.Fatal(err)
	}
	defer npp.Close()
	if npp.Components[0].HasPayload() || !npp.Components[0].HasScripts() {
		t.Error("nopayload package shape wrong")
	}
}

func TestInstallKBytes(t *testing.T) {
	// Cases from pkgbuild on real trees (see the probe in the tests'
	// history): the formula must reproduce each.
	dir := func(children int) int64 { return int64(32 * (children + 2)) }
	cases := []struct {
		name    string
		entries []payloadEntry
		want    int
	}{
		{"empty", []payloadEntry{{rel: ".", size: dir(0)}}, 0},
		{"one 1-byte", []payloadEntry{{rel: ".", size: dir(1)}, {rel: "./f", size: 1}}, 1},
		{"one 1025", []payloadEntry{{rel: ".", size: dir(1)}, {rel: "./f", size: 1025}}, 2},
		{"two dirs", []payloadEntry{{rel: ".", size: dir(1)}, {rel: "./a", size: dir(1)}, {rel: "./a/b", size: dir(0)}}, 1},
		{"three dirs", []payloadEntry{{rel: ".", size: dir(2)}, {rel: "./a", size: dir(1)}, {rel: "./a/b", size: dir(0)}, {rel: "./c", size: dir(0)}}, 2},
		{"big 4097", []payloadEntry{{rel: ".", size: dir(1)}, {rel: "./f", size: 4097}}, 5},
		{"two 4097", []payloadEntry{{rel: ".", size: dir(2)}, {rel: "./f", size: 4097}, {rel: "./g", size: 4097}}, 9},
		{"link", []payloadEntry{{rel: ".", size: dir(2)}, {rel: "./f", size: 1}, {rel: "./l", size: 1}}, 1},
	}
	for _, tc := range cases {
		if got := installKBytes(tc.entries); got != tc.want {
			t.Errorf("%s: installKBytes = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
