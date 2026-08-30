package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// linksTree builds a tree with hard links and, on macOS, extended
// attributes, mirroring the fixture generator's root-links.
func linksTree(t *testing.T) string {
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
	if runtime.GOOS != "windows" {
		os.Symlink("x", filepath.Join(root, "attrs", "link"))
	}
	if runtime.GOOS == "darwin" {
		x := func(args ...string) { hostTool(t, "xattr", args...) }
		x("-w", "com.example.one", "hello", filepath.Join(root, "attrs", "x"))
		x("-wx", "com.example.big", strings.Repeat("0011223344556677", 20), filepath.Join(root, "attrs", "x"))
		x("-wx", "com.apple.FinderInfo", strings.Repeat("41", 32), filepath.Join(root, "attrs", "finder"))
		x("-w", "com.apple.ResourceFork", "resource fork bytes", filepath.Join(root, "attrs", "rsrc"))
		x("-w", "com.example.empty", "v", filepath.Join(root, "attrs", "empty"))
		x("-s", "-w", "com.example.onlink", "yes", filepath.Join(root, "attrs", "link"))
		x("-w", "com.example.ondir", "dirval", filepath.Join(root, "attrs"))
	}
	return root
}

// TestLinksRoundTrip builds the links tree, reads the package back and
// extracts it: hard links come back as hard links and (where the host
// has them) attributes come back as attributes.
func TestLinksRoundTrip(t *testing.T) {
	root := linksTree(t)
	out := filepath.Join(t.TempDir(), "links.pkg")
	var rep buildJSON
	mustRunJSON(t, &rep, "build", root, out, "--identifier", "com.deploymenttheory.links", "--version", "1", "--source-date-epoch", epoch, "--exclude-xattr", hostNoiseXattrs)

	lines := listJSON(t, out)
	names := map[string]bool{}
	for _, l := range lines {
		names[l.Path] = true
	}
	if runtime.GOOS == "darwin" {
		for _, want := range []string{"./attrs/._x", "./attrs/._finder", "./attrs/._rsrc", "./attrs/._empty", "./attrs/._link", "./._attrs"} {
			if !names[want] {
				t.Errorf("%s missing from the package", want)
			}
		}
		if names["./._a.txt"] || names["./._d"] {
			t.Error("sidecars for files without attributes")
		}
	}

	dir := filepath.Join(t.TempDir(), "x")
	var xr extractJSON
	mustRunJSON(t, &xr, "extract", "--verify", out, dir)
	if runtime.GOOS != "windows" {
		a, _ := os.Stat(filepath.Join(dir, "a.txt"))
		c, _ := os.Stat(filepath.Join(dir, "d", "c.txt"))
		if a == nil || c == nil || !os.SameFile(a, c) {
			t.Error("a.txt and d/c.txt were not extracted as one file")
		}
		if xr.Components[0].HardLinks != 4 { // b, c, q, r
			t.Errorf("hardLinks = %d, want 4", xr.Components[0].HardLinks)
		}
	}
	if runtime.GOOS == "darwin" {
		if got := hostTool(t, "xattr", "-p", "com.example.one", filepath.Join(dir, "attrs", "x")); strings.TrimSpace(got) != "hello" {
			t.Errorf("com.example.one = %q", got)
		}
		if got := hostTool(t, "xattr", "-p", "com.example.ondir", filepath.Join(dir, "attrs")); strings.TrimSpace(got) != "dirval" {
			t.Errorf("com.example.ondir = %q", got)
		}
		if got := hostTool(t, "xattr", "-s", "-p", "com.example.onlink", filepath.Join(dir, "attrs", "link")); strings.TrimSpace(got) != "yes" {
			t.Errorf("com.example.onlink = %q", got)
		}
		if _, err := os.Stat(filepath.Join(dir, "attrs", "._x")); err == nil {
			t.Error("sidecar written as a file on macOS")
		}
		if xr.Components[0].Xattrs != 6 {
			t.Errorf("xattrs applied = %d, want 6", xr.Components[0].Xattrs)
		}
		// Written as files on request.
		dir2 := filepath.Join(t.TempDir(), "y")
		mustRun(t, "extract", "--xattrs", "file", out, dir2)
		if _, err := os.Stat(filepath.Join(dir2, "attrs", "._x")); err != nil {
			t.Error("--xattrs file did not write the sidecar")
		}
	}
	attest(t, "links package: %d files, %d KB", rep.NumberOfFiles, rep.InstallKBytes)
}

// --- oracle tests -------------------------------------------------------

// TestBuildLinksAndXattrsParity builds the links tree with pkgbuild and
// with macospkg, and compares Apple's reading of both, sidecars
// included; then unpacks ours with pkgutil and checks the links and
// attributes are back.
func TestBuildLinksAndXattrsParity(t *testing.T) {
	requireTools(t, "pkgbuild", "pkgutil", "lsbom", "xattr", "stat")
	root := linksTree(t)
	hostTool(t, "sh", "-c", `find "$1" -exec touch -h -t 202401020304.05 {} +`, "sh", root)

	ours := filepath.Join(t.TempDir(), "ours.pkg")
	theirs := filepath.Join(t.TempDir(), "theirs.pkg")
	mustRun(t, "build", root, ours, "--identifier", "com.deploymenttheory.links", "--version", "1", "--source-date-epoch", epoch)
	hostTool(t, "pkgbuild", "--quiet", "--root", root, "--identifier", "com.deploymenttheory.links", "--version", "1", "--ownership", "recommended", theirs)

	oursDir := filepath.Join(t.TempDir(), "ours")
	theirsDir := filepath.Join(t.TempDir(), "theirs")
	hostTool(t, "pkgutil", "--expand", ours, oursDir)
	hostTool(t, "pkgutil", "--expand", theirs, theirsDir)
	lsbom := func(dir string) []string {
		out := nonEmptyLines(hostTool(t, "lsbom", "-p", "fmugsc", filepath.Join(dir, "Bom")))
		sort.Strings(out)
		return out
	}
	if a, b := lsbom(oursDir), lsbom(theirsDir); !equalStrings(a, b) {
		t.Errorf("lsbom differs\nours:\n%s\ntheirs:\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
	pf := func(p string) []string {
		out := nonEmptyLines(hostTool(t, "pkgutil", "--payload-files", p))
		sort.Strings(out)
		return out
	}
	if a, b := pf(ours), pf(theirs); !equalStrings(a, b) {
		t.Errorf("pkgutil --payload-files differ\nours:\n%v\ntheirs:\n%v", a, b)
	}
	var oursInfo, theirsInfo infoJSON
	mustRunJSON(t, &oursInfo, "info", ours)
	mustRunJSON(t, &theirsInfo, "info", theirs)
	op, tp := oursInfo.Packages[0].Payload, theirsInfo.Packages[0].Payload
	if op.InstallKBytes != tp.InstallKBytes || op.NumberOfFiles != tp.NumberOfFiles {
		t.Errorf("payload: ours %d files %d KB, pkgbuild %d files %d KB", op.NumberOfFiles, op.InstallKBytes, tp.NumberOfFiles, tp.InstallKBytes)
	}

	// pkgutil --expand-full restores links and attributes from ours as
	// from pkgbuild's.
	full := filepath.Join(t.TempDir(), "full")
	hostTool(t, "pkgutil", "--expand-full", ours, full)
	payload := filepath.Join(full, "Payload")
	inode := func(rel string) string {
		return strings.TrimSpace(hostTool(t, "stat", "-f", "%i", filepath.Join(payload, filepath.FromSlash(rel))))
	}
	if inode("a.txt") != inode("b.txt") || inode("a.txt") != inode("d/c.txt") {
		t.Error("hard links not restored by pkgutil --expand-full")
	}
	if got := strings.TrimSpace(hostTool(t, "xattr", "-p", "com.example.one", filepath.Join(payload, "attrs", "x"))); got != "hello" {
		t.Errorf("com.example.one after pkgutil --expand-full = %q", got)
	}
	if got := strings.TrimSpace(hostTool(t, "xattr", "-p", "com.example.ondir", filepath.Join(payload, "attrs"))); got != "dirval" {
		t.Errorf("com.example.ondir after pkgutil --expand-full = %q", got)
	}
	// Our expand --full does the same.
	mine := filepath.Join(t.TempDir(), "mine")
	mustRun(t, "expand", "--full", ours, mine)
	a := strings.TrimSpace(hostTool(t, "xattr", "-l", filepath.Join(payload, "attrs", "x")))
	b := strings.TrimSpace(hostTool(t, "xattr", "-l", filepath.Join(mine, "Payload", "attrs", "x")))
	if a != b {
		t.Errorf("xattr -l differs between pkgutil and macospkg expand --full:\n%s\n---\n%s", a, b)
	}
	attest(t, "links/xattrs: lsbom and pkgutil agree with pkgbuild on %d entries", len(lsbom(oursDir)))
}

// TestInstallerInstallsOurLinksPackage installs the links package and
// checks the installed volume has the links and attributes.
func TestInstallerInstallsOurLinksPackage(t *testing.T) {
	requireTools(t, "installer", "hdiutil", "sudo", "pkgutil", "xattr", "stat")
	requireInstallerOptIn(t)
	root := linksTree(t)
	ours := filepath.Join(t.TempDir(), "ours.pkg")
	mustRun(t, "build", root, ours, "--identifier", "com.deploymenttheory.links", "--version", "1")

	dmg := filepath.Join(t.TempDir(), "target.dmg")
	hostTool(t, "hdiutil", "create", "-quiet", "-size", "64m", "-fs", "HFS+", "-volname", "MacospkgLinks", dmg)
	attach := hostTool(t, "hdiutil", "attach", "-nobrowse", dmg)
	mount := ""
	for _, line := range nonEmptyLines(attach) {
		if i := strings.Index(line, "/Volumes/"); i >= 0 {
			mount = strings.TrimSpace(line[i:])
		}
	}
	if mount == "" {
		t.Fatalf("unable to find mount point in:\n%s", attach)
	}
	defer exec.Command("hdiutil", "detach", "-quiet", mount).Run()

	out, err := exec.Command("sudo", "-n", "installer", "-pkg", ours, "-target", mount, "-verboseR").CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, out)
	}
	inode := func(rel string) string {
		return strings.TrimSpace(hostTool(t, "stat", "-f", "%i", filepath.Join(mount, filepath.FromSlash(rel))))
	}
	if inode("a.txt") != inode("b.txt") || inode("a.txt") != inode("d/c.txt") || inode("p") != inode("r") {
		t.Error("installed hard links do not share an inode")
	}
	if got := strings.TrimSpace(hostTool(t, "xattr", "-p", "com.example.one", filepath.Join(mount, "attrs", "x"))); got != "hello" {
		t.Errorf("installed com.example.one = %q", got)
	}
	if got := strings.TrimSpace(hostTool(t, "xattr", "-p", "com.example.ondir", filepath.Join(mount, "attrs"))); got != "dirval" {
		t.Errorf("installed com.example.ondir = %q", got)
	}
	attest(t, "installer installed hard links and extended attributes from our package onto %s", mount)
}

// TestManifestXattrOverrides drives the manifest's file_xattrs through
// the binary on every platform: base64 values, a per-file rule, a folder
// rule that covers a subtree, replace, and stripping a path. It is the
// only cover for the manifest decoding, and it needs no host attributes,
// since the rules supply them.
func TestManifestXattrOverrides(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	for _, f := range []string{"keep/a.txt", "sub/b.txt", "sub/deep/c.txt", "gone/d.txt"} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// "hello" and "team" in base64.
	manifest := filepath.Join(base, "build-info.yaml")
	if err := os.WriteFile(manifest, []byte(`
identifier: com.deploymenttheory.overrides
version: "1.0"
xattrs: none
file_xattrs:
  - path: keep/a.txt
    xattrs:
      com.example.tag: aGVsbG8=
  - path: sub/
    replace: true
    xattrs:
      com.example.owner: dGVhbQ==
  - path: gone/
    replace: true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(base, "out.pkg")
	mustRun(t, "build", root, out, "--manifest", manifest, "--source-date-epoch", epoch)

	// The sidecars written are exactly the paths the rules name.
	var got []string
	for _, l := range nonEmptyLines(mustRun(t, "list", out)) {
		if isAppleDouble(l) {
			got = append(got, l)
		}
	}
	sort.Strings(got)
	// keep/a.txt from its own rule; ./sub and everything under it from the
	// folder rule; nothing under ./gone, which was replaced with nothing.
	want := []string{"./._sub", "./keep/._a.txt", "./sub/._b.txt", "./sub/._deep", "./sub/deep/._c.txt"}
	if !equalStrings(got, want) {
		t.Errorf("sidecars = %v, want %v", got, want)
	}

	// And they carry the values the rules gave.
	dir := filepath.Join(base, "x")
	mustRun(t, "extract", out, dir, "--xattrs", "file")
	attrs := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		return string(b)
	}
	if a := attrs("keep/._a.txt"); !strings.Contains(a, "com.example.tag") || !strings.Contains(a, "hello") {
		t.Errorf("keep/._a.txt does not carry com.example.tag=hello")
	}
	for _, rel := range []string{"._sub", "sub/._b.txt", "sub/._deep", "sub/deep/._c.txt"} {
		if a := attrs(rel); !strings.Contains(a, "com.example.owner") || !strings.Contains(a, "team") {
			t.Errorf("%s does not carry the folder rule's com.example.owner=team", rel)
		}
	}
	attest(t, "manifest file_xattrs applied per file, per folder, and stripped a subtree")
}
