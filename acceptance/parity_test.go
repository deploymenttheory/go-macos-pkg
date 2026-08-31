// Byte-level parity with Apple's tools.
//
// The other acceptance tests compare what pkgutil, lsbom and xar say about
// our packages and about Apple's. These compare the documents themselves,
// byte for byte, because a package that reads the same can still be written
// differently: attribute order, a trailing newline and a stray attribute all
// survive a semantic comparison.
//
// One attribute is normalised away. pkgbuild writes its own build number as
// generator-version and macospkg must not claim to be pkgbuild, so the
// comparison replaces that attribute on both sides and requires everything
// else to match exactly.
package acceptance

import (
	"bytes"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// generatorAttr matches the one attribute the two tools are allowed to
// disagree on.
var generatorAttr = regexp.MustCompile(`generator-version="[^"]*"`)

// normaliseGenerator blanks generator-version so the rest can be compared.
func normaliseGenerator(b []byte) []byte {
	return generatorAttr.ReplaceAll(b, []byte(`generator-version="NORMALISED"`))
}

// xarEntry extracts one archive entry with xar(1), Apple's own reader, so
// neither side of a comparison is read by the code under test.
func xarEntry(t *testing.T, pkg, entry string) []byte {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("xar", "-xf", pkg, entry)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("xar -xf %s %s: %v\n%s", pkg, entry, err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, entry))
	if err != nil {
		t.Fatalf("reading extracted %s: %v", entry, err)
	}
	return b
}

// sortBundleRuns sorts each run of sibling <bundle> lines.
//
// pkgbuild emits the bundle elements in the iteration order of one of its
// own hash tables. The order is deterministic for a given set of names, and
// independent of the order the tree was created in, but it is neither the
// walk order nor any sort: four applications named alpha, beta, gamma and
// delta come out alpha, delta, beta, gamma. Reproducing it would mean
// reimplementing Apple's hashing, and it would cost the reproducible output
// this tool promises, since the order would then be a property of whichever
// macOS built the package.
//
// So macospkg sorts by path and the comparison sorts both sides before
// looking. Everything outside these runs is still compared byte for byte,
// and the Installer keys on the identifier rather than the order.
func sortBundleRuns(b []byte) []byte {
	lines := strings.Split(string(b), "\n")
	isBundle := func(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "<bundle ") }
	for i := 0; i < len(lines); i++ {
		if !isBundle(lines[i]) {
			continue
		}
		j := i
		for j < len(lines) && isBundle(lines[j]) {
			j++
		}
		run := lines[i:j]
		sort.Strings(run)
		i = j - 1
	}
	return []byte(strings.Join(lines, "\n"))
}

// requireSameBytes reports the two documents side by side when they differ,
// which is the only useful way to read an attribute-order mismatch.
func requireSameBytes(t *testing.T, what string, apple, ours []byte) {
	t.Helper()
	apple, ours = normaliseGenerator(apple), normaliseGenerator(ours)
	if bytes.Equal(apple, ours) {
		attest(t, "%s is byte-identical to Apple's (%d bytes)", what, len(apple))
		return
	}
	t.Errorf("%s differs from Apple's\n--- Apple ---\n%s\n--- ours ---\n%s", what, apple, ours)
}

// bundleTree writes a destination root holding one application bundle, so
// pkgbuild records bundle, bundle-version, strict-identifier and relocate.
func bundleTree(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	contents := filepath.Join(base, "root", "Applications", "Fixture.app", "Contents")
	if err := os.MkdirAll(filepath.Join(contents, "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key><string>Fixture</string>
	<key>CFBundleIdentifier</key><string>com.deploymenttheory.fixture.app</string>
	<key>CFBundleName</key><string>Fixture</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>1.0</string>
	<key>CFBundleVersion</key><string>100</string>
</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contents, "MacOS", "Fixture"), []byte("#!/bin/sh\necho fixture\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, "root")
}

// TestPackageInfoBytesMatchPkgbuild pins the PackageInfo document: pkgbuild's
// attribute order, its script timeouts and its lack of a trailing newline.
func TestPackageInfoBytesMatchPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "xar")
	root, scripts := sourceTree(t)
	hostTool(t, "sh", "-c", `find "$1" "$2" -exec touch -h -t 202401020304.05 {} +`, "sh", root, scripts)

	ours := filepath.Join(t.TempDir(), "ours.pkg")
	theirs := filepath.Join(t.TempDir(), "theirs.pkg")
	mustRun(t, parityBuildArgs(root, scripts, ours)...)
	hostTool(t, "pkgbuild", "--quiet", "--root", root,
		"--identifier", "com.deploymenttheory.acceptance", "--version", "1.0.0",
		"--scripts", scripts, "--ownership", "recommended", theirs)

	requireSameBytes(t, "PackageInfo", xarEntry(t, theirs, "PackageInfo"), xarEntry(t, ours, "PackageInfo"))
}

// TestBundlePackageInfoBytesMatchPkgbuild covers the bundle elements, where
// pkgbuild writes the identifier once as id and never as CFBundleIdentifier.
func TestBundlePackageInfoBytesMatchPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "xar")
	root := bundleTree(t)
	hostTool(t, "sh", "-c", `find "$1" -exec touch -h -t 202401020304.05 {} +`, "sh", root)

	ours := filepath.Join(t.TempDir(), "ours.pkg")
	theirs := filepath.Join(t.TempDir(), "theirs.pkg")
	mustRun(t, "build", root, ours, "--identifier", "com.deploymenttheory.fixture.bundle",
		"--version", "1.0", "--install-location", "/", "--source-date-epoch", epoch)
	hostTool(t, "pkgbuild", "--quiet", "--root", root,
		"--identifier", "com.deploymenttheory.fixture.bundle", "--version", "1.0",
		"--install-location", "/", "--ownership", "recommended", theirs)

	pi := xarEntry(t, ours, "PackageInfo")
	if bytes.Contains(pi, []byte("CFBundleIdentifier=")) {
		t.Errorf("our PackageInfo writes CFBundleIdentifier, which pkgbuild never does:\n%s", pi)
	}
	requireSameBytes(t, "PackageInfo (bundle)", xarEntry(t, theirs, "PackageInfo"), pi)
}

// TestDistributionBytesMatchProductbuild pins the synthesised Distribution
// against the one productbuild embeds, which is not the same document
// productbuild --synthesize writes to a file.
func TestDistributionBytesMatchProductbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "productbuild", "xar")
	root, scripts := sourceTree(t)
	hostTool(t, "sh", "-c", `find "$1" "$2" -exec touch -h -t 202401020304.05 {} +`, "sh", root, scripts)

	work := t.TempDir()
	// Two components, so the choices-outline and the choice/pkg-ref
	// interleaving are both exercised. One package hides both.
	first := filepath.Join(work, "first.pkg")
	second := filepath.Join(work, "second.pkg")
	hostTool(t, "pkgbuild", "--quiet", "--root", root,
		"--identifier", "com.deploymenttheory.acceptance.first", "--version", "1.0.0",
		"--scripts", scripts, "--ownership", "recommended", first)
	hostTool(t, "pkgbuild", "--quiet", "--root", root,
		"--identifier", "com.deploymenttheory.acceptance.second", "--version", "2.1",
		"--install-location", "/opt/fixture", "--ownership", "recommended", second)

	// productbuild --synthesize writes the file shape; feeding it back
	// through --distribution gives the shape a package actually carries.
	synth := filepath.Join(work, "synth.xml")
	hostTool(t, "productbuild", "--quiet", "--synthesize", "--package", first, "--package", second, synth)
	theirs := filepath.Join(work, "theirs.pkg")
	cmd := exec.Command("productbuild", "--quiet", "--distribution", synth, "--package-path", work, theirs)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("productbuild --distribution: %v\n%s", err, out)
	}

	ours := filepath.Join(work, "ours.pkg")
	mustRun(t, "product", ours, "--package", first, "--package", second, "--source-date-epoch", epoch)

	requireSameBytes(t, "Distribution", xarEntry(t, theirs, "Distribution"), xarEntry(t, ours, "Distribution"))
}

// filterTree writes a root holding everything pkgbuild's default filters
// are supposed to catch and, just as importantly, the near misses they must
// not: CVSdir, notCVS.txt and .DS_Store_dir are all kept.
func filterTree(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	write := func(rel string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Caught: as directories, and as plain files of the same name.
	write("a/.svn/entries")
	write("a/CVS/Root")
	write("CVS/Root")
	write(".DS_Store")
	write("b/.DS_Store")
	write("b/sub/.DS_Store")
	write("plain/CVS")
	write("plain/.svn")
	// Kept: the names only look like the filtered ones.
	write("CVSdir/f.txt")
	write("notCVS.txt")
	write("a/.svnfile")
	write(".DS_Store_dir/f")
	write("keep/file.txt")
	write("b/sub/real.txt")
	return root
}

// payloadPaths lists what a package installs, sidecars aside: a "._" entry
// only exists because its owner does, so comparing owners is enough.
func payloadPaths(t *testing.T, pkg string) []string {
	t.Helper()
	var out []string
	for _, l := range nonEmptyLines(hostTool(t, "pkgutil", "--payload-files", pkg)) {
		if strings.HasPrefix(path.Base(l), "._") {
			continue
		}
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// TestDefaultFiltersMatchPkgbuild pins the payload pkgbuild produces when
// neither tool is given a filter: .svn, CVS and .DS_Store are dropped, and
// the names that merely resemble them are not.
func TestDefaultFiltersMatchPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "pkgutil")
	root := filterTree(t)
	hostTool(t, "sh", "-c", `find "$1" -exec touch -h -t 202401020304.05 {} +`, "sh", root)

	work := t.TempDir()
	ours := filepath.Join(work, "ours.pkg")
	theirs := filepath.Join(work, "theirs.pkg")
	mustRun(t, "build", root, ours, "--identifier", "com.deploymenttheory.filters",
		"--version", "1.0", "--install-location", "/", "--source-date-epoch", epoch)
	hostTool(t, "pkgbuild", "--quiet", "--root", root,
		"--identifier", "com.deploymenttheory.filters", "--version", "1.0",
		"--install-location", "/", "--ownership", "recommended", theirs)

	a, b := payloadPaths(t, theirs), payloadPaths(t, ours)
	if !equalStrings(a, b) {
		t.Errorf("default filters differ\npkgbuild:\n%s\nours:\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
	for _, gone := range []string{"./a/.svn", "./a/CVS", "./CVS", "./.DS_Store", "./plain/CVS", "./plain/.svn"} {
		for _, got := range b {
			if got == gone {
				t.Errorf("%s should have been filtered out", gone)
			}
		}
	}
	for _, kept := range []string{"./CVSdir", "./notCVS.txt", "./a/.svnfile", "./.DS_Store_dir"} {
		found := false
		for _, got := range b {
			if got == kept {
				found = true
			}
		}
		if !found {
			t.Errorf("%s should have been kept", kept)
		}
	}
	attest(t, "default filters agree with pkgbuild on %d payload entries", len(a))
}

// TestFilterInhibitsDefaults pins the other half of pkgbuild's rule: naming
// even one filter replaces the defaults rather than adding to them.
func TestFilterInhibitsDefaults(t *testing.T) {
	requireTools(t, "pkgbuild", "pkgutil")
	root := filterTree(t)
	hostTool(t, "sh", "-c", `find "$1" -exec touch -h -t 202401020304.05 {} +`, "sh", root)

	work := t.TempDir()
	ours := filepath.Join(work, "ours.pkg")
	theirs := filepath.Join(work, "theirs.pkg")
	mustRun(t, "build", root, ours, "--identifier", "com.deploymenttheory.filters",
		"--version", "1.0", "--install-location", "/", "--filter", "/keep$",
		"--source-date-epoch", epoch)
	hostTool(t, "pkgbuild", "--quiet", "--root", root,
		"--identifier", "com.deploymenttheory.filters", "--version", "1.0",
		"--install-location", "/", "--ownership", "recommended", "--filter", "/keep$", theirs)

	a, b := payloadPaths(t, theirs), payloadPaths(t, ours)
	if !equalStrings(a, b) {
		t.Errorf("--filter differs\npkgbuild:\n%s\nours:\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
	// The defaults are off, so .DS_Store is back and keep/ is gone.
	var sawDS, sawKeep bool
	for _, p := range b {
		if p == "./.DS_Store" {
			sawDS = true
		}
		if strings.HasPrefix(p, "./keep") {
			sawKeep = true
		}
	}
	if !sawDS {
		t.Error("--filter should have inhibited the default .DS_Store filter")
	}
	if sawKeep {
		t.Error("--filter /keep$ should have dropped ./keep")
	}
	attest(t, "a named filter replaces the defaults, as pkgbuild does (%d entries)", len(a))
}

// writeBundle writes a minimal bundle whose Info.plist sits where that kind
// of bundle keeps it.
func writeBundle(t *testing.T, dir, id, plistRel string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(plistRel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key><string>` + id + `</string>
	<key>CFBundleShortVersionString</key><string>1.0</string>
	<key>CFBundleVersion</key><string>1</string>
</dict>
</plist>
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBundleKindsMatchPkgbuild covers the rules that decide which of the
// bundle lists a bundle is referenced from. They are not uniform: an
// application is relocated and strictly identified, every other kind is
// only version-checked and upgraded, and a bundle nested inside another is
// described but never referenced.
func TestBundleKindsMatchPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "xar")
	base := t.TempDir()
	root := filepath.Join(base, "root")

	// An application: relocatable and strictly identified.
	writeBundle(t, root, "com.example.app", "Applications/Thing.app/Contents/Info.plist")
	// A well-formed framework, found through its top-level Resources link,
	// so pkgbuild names the framework and not a version directory.
	fw := filepath.Join(root, "Library", "Frameworks", "Solo.framework")
	writeBundle(t, root, "com.example.solo", "Library/Frameworks/Solo.framework/Versions/A/Resources/Info.plist")
	if err := os.Symlink("A", filepath.Join(fw, "Versions", "Current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.FromSlash("Versions/Current/Resources"), filepath.Join(fw, "Resources")); err != nil {
		t.Fatal(err)
	}
	// A plug-in: neither relocatable nor strictly identified.
	writeBundle(t, root, "com.example.plug", "Library/Plug-Ins/P.plugin/Contents/Info.plist")
	// A framework nested in an application: described, never referenced,
	// and named by its version directory because it has no Resources link.
	writeBundle(t, root, "com.example.inner", "Applications/Thing.app/Contents/Frameworks/Inner.framework/Versions/A/Resources/Info.plist")

	hostTool(t, "sh", "-c", `find "$1" -exec touch -h -t 202401020304.05 {} +`, "sh", root)

	work := t.TempDir()
	ours := filepath.Join(work, "ours.pkg")
	theirs := filepath.Join(work, "theirs.pkg")
	mustRun(t, "build", root, ours, "--identifier", "com.deploymenttheory.kinds",
		"--version", "1.0", "--install-location", "/", "--source-date-epoch", epoch)
	hostTool(t, "pkgbuild", "--quiet", "--root", root,
		"--identifier", "com.deploymenttheory.kinds", "--version", "1.0",
		"--install-location", "/", "--ownership", "recommended", theirs)

	pi := xarEntry(t, ours, "PackageInfo")
	requireSameBytes(t, "PackageInfo (bundle kinds)",
		sortBundleRuns(xarEntry(t, theirs, "PackageInfo")), sortBundleRuns(pi))

	// Spelled out, so a regression says which rule broke rather than just
	// dumping two documents.
	got := string(pi)
	for _, want := range []string{
		`path="./Library/Frameworks/Solo.framework"`,
		`path="./Applications/Thing.app/Contents/Frameworks/Inner.framework/Versions/A"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PackageInfo is missing %s", want)
		}
	}
	_, relocate, ok := strings.Cut(got, "<relocate>")
	if !ok {
		t.Fatal("PackageInfo has no <relocate> element")
	}
	if strings.Contains(relocate, "com.example.solo") || strings.Contains(relocate, "com.example.plug") {
		t.Error("only an application should be relocated")
	}
	if strings.Contains(got, `<bundle id="com.example.inner"/>`) {
		t.Error("a nested bundle should be described but never referenced")
	}
}
