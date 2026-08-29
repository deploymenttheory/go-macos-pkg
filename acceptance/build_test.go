// Command tests for build and product: round trips through our own
// reader everywhere, and parity with pkgbuild, productbuild and installer
// where those exist.
package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/exitcode"
)

// sourceTree writes a deterministic payload tree and scripts directory
// and returns their paths. It mirrors the fixture tree so pkgbuild parity
// can be checked on the same shapes.
func sourceTree(t *testing.T) (root, scripts string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "root")
	scripts = filepath.Join(base, "scripts")
	write := func(rel, content string, mode os.FileMode) {
		p := filepath.Join(base, filepath.FromSlash(rel))
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
	write("root/usr/local/fixture/hello.txt", "hello, world\n", 0o644)
	write("root/usr/local/fixture/empty.txt", "", 0o644)
	write("root/usr/local/fixture/bin/tool", "#!/bin/sh\necho tool\n", 0o755)
	write("root/usr/local/fixture/sub/nested/deep.txt", "deep\n", 0o644)
	write("root/usr/local/fixture/unicode-é.txt", "unicode\n", 0o644)
	write("root/usr/local/fixture/big.bin", strings.Repeat("0123456789abcdef", 19200), 0o644) // 300 KiB
	if runtime.GOOS != "windows" {
		if err := os.Symlink("hello.txt", filepath.Join(root, "usr", "local", "fixture", "link")); err != nil {
			t.Fatal(err)
		}
	}
	write("scripts/preinstall", "#!/bin/sh\nexit 0\n", 0o755)
	write("scripts/postinstall", "#!/bin/sh\nexit 0\n", 0o755)
	return root, scripts
}

type buildJSON struct {
	Output        string   `json:"output"`
	Kind          string   `json:"kind"`
	Identifier    string   `json:"identifier"`
	Version       string   `json:"version"`
	NumberOfFiles int      `json:"numberOfFiles"`
	InstallKBytes int      `json:"installKBytes"`
	Scripts       []string `json:"scripts"`
	SHA256        string   `json:"sha256"`
	Signed        bool     `json:"signed"`
}

const epoch = "1704164645" // 2024-01-02T03:04:05Z

func buildArgs(root, scripts, out string) []string {
	args := []string{"build", root, out, "--identifier", "com.deploymenttheory.acceptance", "--version", "1.0.0", "--scripts", scripts, "--source-date-epoch", epoch}
	if runtime.GOOS == "windows" {
		args = append(args, "--executable", `bin/tool$`)
	}
	return args
}

func TestBuildRoundTrip(t *testing.T) {
	root, scripts := sourceTree(t)
	out := filepath.Join(t.TempDir(), "out.pkg")
	var rep buildJSON
	mustRunJSON(t, &rep, buildArgs(root, scripts, out)...)
	if rep.Kind != "component" || rep.Identifier != "com.deploymenttheory.acceptance" || rep.Version != "1.0.0" {
		t.Errorf("report: %+v", rep)
	}
	wantFiles := 13 // . usr local fixture hello empty bin tool sub nested deep unicode big
	if runtime.GOOS != "windows" {
		wantFiles++ // link
	}
	if rep.NumberOfFiles != wantFiles {
		t.Errorf("numberOfFiles = %d, want %d", rep.NumberOfFiles, wantFiles)
	}
	if !equalStrings(rep.Scripts, []string{"preinstall", "postinstall"}) {
		t.Errorf("scripts = %v", rep.Scripts)
	}

	var info infoJSON
	mustRunJSON(t, &info, "info", out)
	if info.Kind != "component" || !info.XAR.TOCDigestValid || info.XAR.ChecksumAlgorithm != "sha256" {
		t.Errorf("info: %+v", info)
	}
	if info.Packages[0].Payload.NumberOfFiles != rep.NumberOfFiles || info.Packages[0].Payload.InstallKBytes != rep.InstallKBytes {
		t.Errorf("info disagrees with build: %+v vs %+v", info.Packages[0].Payload, rep)
	}
	if info.Packages[0].Payload.Encoding != "gzip-cpio" {
		t.Errorf("encoding = %s", info.Packages[0].Payload.Encoding)
	}

	// Extract and compare with the source.
	dir := filepath.Join(t.TempDir(), "x")
	mustRun(t, "extract", "--verify", out, dir)
	for _, rel := range []string{"usr/local/fixture/hello.txt", "usr/local/fixture/big.bin", "usr/local/fixture/sub/nested/deep.txt", "usr/local/fixture/unicode-é.txt"} {
		a, _ := fileSHA256(filepath.Join(root, filepath.FromSlash(rel)))
		b, err := fileSHA256(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || a != b {
			t.Errorf("%s: extracted %s, source %s (%v)", rel, b, a, err)
		}
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, "usr", "local", "fixture", "bin", "tool"))
		if st == nil || st.Mode().Perm()&0o111 == 0 {
			t.Error("tool lost its execute bit")
		}
		target, _ := os.Readlink(filepath.Join(dir, "usr", "local", "fixture", "link"))
		if target != "hello.txt" {
			t.Errorf("link target = %q", target)
		}
	}
	lines := listJSON(t, out)
	for _, l := range lines {
		if l.UID != 0 || l.GID != 0 {
			t.Errorf("%s owner %d:%d, want 0:0", l.Path, l.UID, l.GID)
		}
	}
	sc := filepath.Join(t.TempDir(), "scripts")
	mustRun(t, "extract", "--scripts", out, sc)
	if _, err := os.Stat(filepath.Join(sc, "postinstall")); err != nil {
		t.Error("postinstall not in Scripts")
	}
	attest(t, "built %d files, %d KB, sha256 %s", rep.NumberOfFiles, rep.InstallKBytes, rep.SHA256[:12])
}

func TestBuildReproducible(t *testing.T) {
	root, scripts := sourceTree(t)
	a := filepath.Join(t.TempDir(), "a.pkg")
	b := filepath.Join(t.TempDir(), "b.pkg")
	mustRun(t, buildArgs(root, scripts, a)...)
	mustRun(t, buildArgs(root, scripts, b)...)
	ha, _ := fileSHA256(a)
	hb, _ := fileSHA256(b)
	if ha != hb {
		t.Errorf("two builds differ: %s vs %s", ha, hb)
	}
	// The bare SOURCE_DATE_EPOCH variable is honoured, and the flag beats it.
	c := filepath.Join(t.TempDir(), "c.pkg")
	args := buildArgs(root, scripts, c)
	args = args[:len(args)-2] // drop --source-date-epoch
	if runtime.GOOS == "windows" {
		args = append(args, "--executable", `bin/tool$`)
	}
	_, stderr, code := runEnv(t, []string{"SOURCE_DATE_EPOCH=" + epoch}, args...)
	if code != 0 {
		t.Fatalf("build with SOURCE_DATE_EPOCH: %s", stderr)
	}
	hc, _ := fileSHA256(c)
	if hc != ha {
		t.Errorf("SOURCE_DATE_EPOCH build differs from --source-date-epoch build")
	}
	d := filepath.Join(t.TempDir(), "d.pkg")
	_, _, code = runEnv(t, []string{"SOURCE_DATE_EPOCH=1"}, buildArgs(root, scripts, d)...)
	if hd, _ := fileSHA256(d); code != 0 || hd != ha {
		t.Errorf("--source-date-epoch did not override SOURCE_DATE_EPOCH")
	}
	attest(t, "reproducible build sha256 %s", ha)
}

func TestBuildUsageErrors(t *testing.T) {
	root, _ := sourceTree(t)
	out := filepath.Join(t.TempDir(), "out.pkg")
	_, _, code := run(t, "build", root, out, "--version", "1")
	if code != exitcode.Usage {
		t.Errorf("missing identifier: exit %d", code)
	}
	_, _, code = run(t, "build", root, out, "--identifier", "x")
	if code != exitcode.Usage {
		t.Errorf("missing version: exit %d", code)
	}
	_, _, code = run(t, "build", root, "--identifier", "x", "--version", "1")
	if code != exitcode.Usage {
		t.Errorf("missing output: exit %d", code)
	}
	_, _, code = run(t, "build", root, out, "--identifier", "x", "--version", "1", "--ownership", "sideways")
	if code != exitcode.Usage {
		t.Errorf("bad ownership: exit %d", code)
	}
	if runtime.GOOS == "windows" {
		_, _, code = run(t, "build", root, out, "--identifier", "x", "--version", "1", "--ownership", "preserve")
		if code != exitcode.Unsupported {
			t.Errorf("--ownership preserve on Windows: exit %d, want %d", code, exitcode.Unsupported)
		}
	}
	_, _, code = run(t, "build", filepath.Join(root, "nope"), out, "--identifier", "x", "--version", "1")
	if code == 0 {
		t.Error("missing root accepted")
	}
}

func TestBuildManifestProject(t *testing.T) {
	root, scripts := sourceTree(t)
	project := filepath.Join(t.TempDir(), "project")
	os.MkdirAll(project, 0o755)
	os.Rename(root, filepath.Join(project, "payload"))
	os.Rename(scripts, filepath.Join(project, "scripts"))
	os.WriteFile(filepath.Join(project, "build-info.yaml"), []byte(`name: Fixture-${version}.pkg
identifier: com.deploymenttheory.manifest
version: "2.5"
install_location: /opt/fixture
minimum_os_version: "12.0"
executable_patterns:
  - 'bin/tool$'
`), 0o644)
	var rep buildJSON
	mustRunJSON(t, &rep, "build", project, "--source-date-epoch", epoch)
	if rep.Identifier != "com.deploymenttheory.manifest" || rep.Version != "2.5" {
		t.Errorf("manifest identity not used: %+v", rep)
	}
	if want := filepath.Join(project, "build", "Fixture-2.5.pkg"); rep.Output != want {
		t.Errorf("output = %s, want %s", rep.Output, want)
	}
	var info infoJSON
	mustRunJSON(t, &info, "info", rep.Output)
	if info.Packages[0].InstallLocation != "/opt/fixture" {
		t.Errorf("install location = %s", info.Packages[0].InstallLocation)
	}
	if !equalStrings(info.Packages[0].Scripts, []string{"preinstall", "postinstall"}) {
		t.Errorf("scripts = %v", info.Packages[0].Scripts)
	}
	// A flag overrides the manifest.
	out := filepath.Join(t.TempDir(), "override.pkg")
	mustRunJSON(t, &rep, "build", project, out, "--version", "3.0", "--source-date-epoch", epoch)
	if rep.Version != "3.0" || rep.Output != out {
		t.Errorf("flag override: %+v", rep)
	}
}

func TestProduct(t *testing.T) {
	root, scripts := sourceTree(t)
	a := filepath.Join(t.TempDir(), "A.pkg")
	mustRun(t, buildArgs(root, scripts, a)...)
	b := filepath.Join(t.TempDir(), "B.pkg")
	mustRun(t, "build", root, b, "--identifier", "com.deploymenttheory.b", "--version", "2.0", "--install-location", "/opt/b", "--source-date-epoch", epoch)

	res := filepath.Join(t.TempDir(), "res")
	os.MkdirAll(filepath.Join(res, "en.lproj"), 0o755)
	os.WriteFile(filepath.Join(res, "en.lproj", "welcome.html"), []byte("<h1>hi</h1>"), 0o644)
	out := filepath.Join(t.TempDir(), "Suite.pkg")
	mustRun(t, "product", out, "--package", a, "--package", b, "--title", "Suite", "--min-os-version", "12.0", "--host-architectures", "arm64,x86_64", "--resources", res, "--source-date-epoch", epoch)

	var info infoJSON
	mustRunJSON(t, &info, "info", out)
	if info.Kind != "product" || info.Distribution == nil {
		t.Fatalf("not a product: %+v", info)
	}
	if info.Distribution.Title != "Suite" || !equalStrings(info.Distribution.HostArchitectures, []string{"arm64", "x86_64"}) {
		t.Errorf("distribution: %+v", info.Distribution)
	}
	var names []string
	for _, c := range info.Packages {
		names = append(names, c.Name)
	}
	if !equalStrings(names, []string{"A.pkg", "B.pkg"}) {
		t.Errorf("components = %v", names)
	}
	if !equalStrings(info.Distribution.Resources, []string{"Resources/en.lproj/welcome.html"}) {
		t.Errorf("resources = %v", info.Distribution.Resources)
	}
	dist := mustRun(t, "cat", out, "Distribution")
	for _, want := range []string{`<pkg-ref id="com.deploymenttheory.acceptance"`, "#A.pkg</pkg-ref>", "#B.pkg</pkg-ref>", `<os-version min="12.0"/>`, `<choice id="com.deploymenttheory.b" visible="false">`} {
		if !strings.Contains(dist, want) {
			t.Errorf("Distribution lacks %s:\n%s", want, dist)
		}
	}
	if got := mustRun(t, "cat", out, "--component", "B.pkg", "--payload", "./usr/local/fixture/hello.txt"); got != "hello, world\n" {
		t.Errorf("nested payload = %q", got)
	}
	// Reproducible too.
	out2 := filepath.Join(t.TempDir(), "Suite2.pkg")
	mustRun(t, "product", out2, "--package", a, "--package", b, "--title", "Suite", "--min-os-version", "12.0", "--host-architectures", "arm64,x86_64", "--resources", res, "--source-date-epoch", epoch)
	h1, _ := fileSHA256(out)
	h2, _ := fileSHA256(out2)
	if h1 != h2 {
		t.Error("two product builds differ")
	}
	// A custom Distribution is used as given.
	custom := filepath.Join(t.TempDir(), "dist.xml")
	os.WriteFile(custom, []byte(`<?xml version="1.0"?><installer-gui-script minSpecVersion="1"><title>Custom</title><choices-outline><line choice="a"/></choices-outline><choice id="a"><pkg-ref id="com.deploymenttheory.acceptance"/></choice><pkg-ref id="com.deploymenttheory.acceptance" version="1.0.0">#A.pkg</pkg-ref></installer-gui-script>`), 0o644)
	out3 := filepath.Join(t.TempDir(), "Custom.pkg")
	mustRun(t, "product", out3, "--package", a, "--distribution", custom)
	mustRunJSON(t, &info, "info", out3)
	if info.Distribution.Title != "Custom" || len(info.Packages) != 1 {
		t.Errorf("custom distribution: %+v", info.Distribution)
	}
	_, _, code := run(t, "product", filepath.Join(t.TempDir(), "x.pkg"), "--package", out)
	if code == 0 {
		t.Error("embedding a product archive was accepted")
	}
	_, _, code = run(t, "product", filepath.Join(t.TempDir(), "x.pkg"))
	if code != exitcode.Usage {
		t.Errorf("no packages: exit %d", code)
	}
}

// --- oracle tests -------------------------------------------------------

// TestBuildParityWithPkgbuild builds the same tree with pkgbuild and with
// macospkg and compares what Apple's own readers say about each.
func TestBuildParityWithPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "pkgutil", "lsbom", "xar")
	root, scripts := sourceTree(t)
	// pkgbuild copies the tree's mtimes; pin them so the two agree.
	hostTool(t, "sh", "-c", `find "$1" "$2" -exec touch -h -t 202401020304.05 {} +`, "sh", root, scripts)

	ours := filepath.Join(t.TempDir(), "ours.pkg")
	theirs := filepath.Join(t.TempDir(), "theirs.pkg")
	mustRun(t, buildArgs(root, scripts, ours)...)
	hostTool(t, "pkgbuild", "--quiet", "--root", root, "--identifier", "com.deploymenttheory.acceptance", "--version", "1.0.0", "--scripts", scripts, "--ownership", "recommended", theirs)

	// Bill of materials, as lsbom prints it (dropping the ._ sidecars a
	// provenance-tracking host makes pkgbuild add).
	oursDir := filepath.Join(t.TempDir(), "ours")
	theirsDir := filepath.Join(t.TempDir(), "theirs")
	hostTool(t, "pkgutil", "--expand", ours, oursDir)
	hostTool(t, "pkgutil", "--expand", theirs, theirsDir)
	lsbom := func(dir string) []string {
		var out []string
		for _, l := range nonEmptyLines(hostTool(t, "lsbom", "-p", "fmugsc", filepath.Join(dir, "Bom"))) {
			cols := strings.Split(l, "\t")
			if isAppleDouble(cols[0]) {
				continue
			}
			// pkgbuild's --ownership recommended misses files whose names
			// are not ASCII (the NFD name on disk does not match) and
			// records the builder's own uid:gid for them. That is a
			// pkgbuild bug, not a rule to copy; compare everything else.
			if len(cols) >= 4 && !isASCII(cols[0]) {
				cols[2], cols[3] = "-", "-"
			}
			out = append(out, strings.Join(cols, "\t"))
		}
		sort.Strings(out)
		return out
	}
	a, b := lsbom(oursDir), lsbom(theirsDir)
	if !equalStrings(a, b) {
		t.Errorf("lsbom differs\nours:\n%s\ntheirs:\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
	attest(t, "lsbom agrees with pkgbuild on %d entries", len(a))

	// PackageInfo numbers: files differ by the sidecars, kilobytes should
	// not (sidecars are not counted).
	var oursInfo, theirsInfo infoJSON
	mustRunJSON(t, &oursInfo, "info", ours)
	mustRunJSON(t, &theirsInfo, "info", theirs)
	op, tp := oursInfo.Packages[0].Payload, theirsInfo.Packages[0].Payload
	if op.InstallKBytes != tp.InstallKBytes {
		t.Errorf("installKBytes: ours %d, pkgbuild %d", op.InstallKBytes, tp.InstallKBytes)
	}
	if !manifest.Generator.AppleDouble && op.NumberOfFiles != tp.NumberOfFiles {
		t.Errorf("numberOfFiles: ours %d, pkgbuild %d", op.NumberOfFiles, tp.NumberOfFiles)
	}
	if !equalStrings(oursInfo.Packages[0].Scripts, theirsInfo.Packages[0].Scripts) {
		t.Errorf("scripts: ours %v, pkgbuild %v", oursInfo.Packages[0].Scripts, theirsInfo.Packages[0].Scripts)
	}

	// Archive shape.
	ot := nonEmptyLines(hostTool(t, "xar", "-tf", ours))
	tt := nonEmptyLines(hostTool(t, "xar", "-tf", theirs))
	sort.Strings(ot)
	sort.Strings(tt)
	if !equalStrings(ot, tt) {
		t.Errorf("xar -tf: ours %v, pkgbuild %v", ot, tt)
	}

	// pkgutil's payload listing.
	pf := func(p string) []string {
		var out []string
		for _, l := range nonEmptyLines(hostTool(t, "pkgutil", "--payload-files", p)) {
			if !isAppleDouble(l) {
				out = append(out, l)
			}
		}
		sort.Strings(out)
		return out
	}
	if a, b := pf(ours), pf(theirs); !equalStrings(a, b) {
		t.Errorf("pkgutil --payload-files: ours %v, pkgbuild %v", a, b)
	}

	// xar itself extracts our archive and its payload gunzips to a cpio
	// that cpio(1) lists.
	xdir := filepath.Join(t.TempDir(), "xar")
	os.MkdirAll(xdir, 0o755)
	cmd := exec.Command("xar", "-xf", ours)
	cmd.Dir = xdir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("xar -x: %v\n%s", err, out)
	}
	listing := hostTool(t, "sh", "-c", `gunzip -c "$1" | cpio -itv 2>/dev/null`, "sh", filepath.Join(xdir, "Payload"))
	if !strings.Contains(listing, "./usr/local/fixture/hello.txt") {
		t.Errorf("cpio(1) did not list the payload:\n%s", listing)
	}
	if !strings.Contains(listing, "root") || !strings.Contains(listing, "wheel") {
		t.Errorf("payload owners are not root:wheel:\n%s", listing)
	}
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// TestInstallerInstallsOurPackage is the final oracle: macOS's installer
// installs a package we built, onto a scratch volume, and the result
// matches the source. It needs root (passwordless sudo, as on CI).
func TestInstallerInstallsOurPackage(t *testing.T) {
	requireTools(t, "installer", "hdiutil", "sudo", "pkgutil")
	if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
		t.Skip("passwordless sudo is not available; installer needs root")
	}
	root, scripts := sourceTree(t)
	ours := filepath.Join(t.TempDir(), "ours.pkg")
	mustRun(t, buildArgs(root, scripts, ours)...)

	// A scratch volume, so nothing lands on the real system.
	dmg := filepath.Join(t.TempDir(), "target.dmg")
	hostTool(t, "hdiutil", "create", "-quiet", "-size", "64m", "-fs", "HFS+", "-volname", "MacospkgTarget", dmg)
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
	for _, rel := range []string{"usr/local/fixture/hello.txt", "usr/local/fixture/big.bin", "usr/local/fixture/bin/tool", "usr/local/fixture/sub/nested/deep.txt"} {
		a, _ := fileSHA256(filepath.Join(root, filepath.FromSlash(rel)))
		b, err := fileSHA256(filepath.Join(mount, filepath.FromSlash(rel)))
		if err != nil || a != b {
			t.Errorf("%s: installed %s, source %s (%v)", rel, b, a, err)
		}
	}
	target, _ := os.Readlink(filepath.Join(mount, "usr", "local", "fixture", "link"))
	if target != "hello.txt" {
		t.Errorf("installed link target = %q", target)
	}
	pkgs := hostTool(t, "pkgutil", "--volume", mount, "--pkgs")
	if !strings.Contains(pkgs, "com.deploymenttheory.acceptance") {
		t.Errorf("receipt not recorded:\n%s", pkgs)
	}
	files := hostTool(t, "pkgutil", "--volume", mount, "--files", "com.deploymenttheory.acceptance")
	if !strings.Contains(files, "usr/local/fixture/hello.txt") {
		t.Errorf("receipt files:\n%s", files)
	}
	attest(t, "installer installed our package onto %s; receipt lists %d paths", mount, len(nonEmptyLines(files)))
}
