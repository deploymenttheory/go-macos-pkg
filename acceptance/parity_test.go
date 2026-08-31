// Byte-level parity with Apple's tools, over the shapes real packages have.
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
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"howett.net/plist"
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
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "xar -xf %s %s: %s", pkg, entry, out)
	b, err := os.ReadFile(filepath.Join(dir, entry))
	require.NoErrorf(t, err, "reading extracted %s", entry)
	return b
}

// sortBundleRuns sorts the runs of lines whose order is pkgbuild's rather
// than anybody's: the <bundle> elements, and the bundle-specific scripts.
//
// pkgbuild emits both in the iteration order of one of its own hash tables.
// The order is deterministic for a given set of names, and independent of
// the order the tree was created in, but it is neither the walk order nor
// any sort: four applications named alpha, beta, gamma and delta come out
// alpha, delta, beta, gamma, and the same package can order its bundle
// elements one way and its bundle scripts another. Reproducing it would
// mean reimplementing Apple's hashing, and it would cost the reproducible
// output this tool promises, since the order would then be a property of
// whichever macOS built the package.
//
// So macospkg sorts by path and the comparison sorts both sides before
// looking. Only these runs are reordered: everything else, the package's
// own scripts included, is compared byte for byte, and their position after
// the bundle-specific ones is pkgbuild's rule rather than an accident.
func sortBundleRuns(b []byte) []byte {
	lines := strings.Split(string(b), "\n")
	// A bundle-specific script is the one kind of script that carries a
	// component-id, which is exactly the set whose order is Apple's.
	runnable := func(s string) bool {
		s = strings.TrimSpace(s)
		return strings.HasPrefix(s, "<bundle ") || strings.Contains(s, " component-id=")
	}
	for i := 0; i < len(lines); i++ {
		if !runnable(lines[i]) {
			continue
		}
		j := i
		for j < len(lines) && runnable(lines[j]) {
			j++
		}
		sort.Strings(lines[i:j])
		i = j - 1
	}
	return []byte(strings.Join(lines, "\n"))
}

// requireSameBytes reports the two documents side by side when they differ,
// which is the only useful way to read an attribute-order mismatch.
func requireSameBytes(t *testing.T, what string, apple, ours []byte) {
	t.Helper()
	apple, ours = normaliseGenerator(apple), normaliseGenerator(ours)
	require.Equalf(t, string(apple), string(ours), "%s differs from Apple's", what)
	attest(t, "%s is byte-identical to Apple's (%d bytes)", what, len(apple))
}

// ---------------------------------------------------------------- fixtures

// writeFile writes one file, creating its parents.
func writeFile(t *testing.T, p, body string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), mode))
}

// infoPlist is a minimal but well-formed bundle Info.plist.
func infoPlist(id, short, version string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key><string>` + id + `</string>
	<key>CFBundleName</key><string>Example</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>` + short + `</string>
	<key>CFBundleVersion</key><string>` + version + `</string>
</dict>
</plist>
`
}

// writeBundle writes a bundle whose Info.plist sits where that kind of
// bundle keeps it.
func writeBundle(t *testing.T, dir, id, plistRel string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, filepath.FromSlash(plistRel)), infoPlist(id, "1.0", "1"), 0o644)
}

// productTree writes the destination root of a plausible shipping product,
// rather than a bundle in isolation. It holds every shape that changes what
// pkgbuild records:
//
//   - an application, the only kind of bundle that is relocated and matched
//     on a strict identifier;
//   - a framework inside it, laid out the way a real framework is, with
//     Versions/A, a Current link and the top-level Resources link that is
//     how pkgbuild comes to name the framework rather than a version
//     directory;
//   - an app extension inside it, so there is a second nested bundle of a
//     different kind;
//   - a preference pane beside it, a top-level bundle that is neither
//     relocatable nor strictly identified;
//   - a privileged helper, a launch daemon plist and a symbolic link into
//     the application, which are ordinary payload rather than bundles.
func productTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	app := filepath.Join(root, "Applications", "Example.app")

	writeFile(t, filepath.Join(app, "Contents", "Info.plist"), infoPlist("com.example.Example", "2.4.1", "2410"), 0o644)
	writeFile(t, filepath.Join(app, "Contents", "MacOS", "Example"), "#!/bin/sh\necho example\n", 0o755)
	writeFile(t, filepath.Join(app, "Contents", "Resources", "en.lproj", "Localizable.strings"), "\"key\" = \"value\";\n", 0o644)

	fw := filepath.Join(app, "Contents", "Frameworks", "Shared.framework")
	writeFile(t, filepath.Join(fw, "Versions", "A", "Resources", "Info.plist"), infoPlist("com.example.Shared", "2.4.1", "2410"), 0o644)
	writeFile(t, filepath.Join(fw, "Versions", "A", "Shared"), "binary\n", 0o755)
	require.NoError(t, os.Symlink("A", filepath.Join(fw, "Versions", "Current")))
	require.NoError(t, os.Symlink(filepath.FromSlash("Versions/Current/Resources"), filepath.Join(fw, "Resources")))
	require.NoError(t, os.Symlink(filepath.FromSlash("Versions/Current/Shared"), filepath.Join(fw, "Shared")))

	ext := filepath.Join(app, "Contents", "PlugIns", "Helper.appex")
	writeFile(t, filepath.Join(ext, "Contents", "Info.plist"), infoPlist("com.example.Helper", "2.4.1", "2410"), 0o644)
	writeFile(t, filepath.Join(ext, "Contents", "MacOS", "Helper"), "#!/bin/sh\n", 0o755)

	writeBundle(t, root, "com.example.pref", "Library/PreferencePanes/Example.prefPane/Contents/Info.plist")

	writeFile(t, filepath.Join(root, "Library", "PrivilegedHelperTools", "com.example.helper"), "#!/bin/sh\n", 0o755)
	writeFile(t, filepath.Join(root, "Library", "LaunchDaemons", "com.example.helper.plist"), infoPlist("com.example.helper", "1.0", "1"), 0o644)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "usr", "local", "bin"), 0o755))
	require.NoError(t, os.Symlink("/Applications/Example.app/Contents/MacOS/Example", filepath.Join(root, "usr", "local", "bin", "example")))

	return root
}

// stampTree pins every mtime, since pkgbuild copies the tree's and the two
// packages have to agree.
func stampTree(t *testing.T, dirs ...string) {
	t.Helper()
	args := append([]string{"sh"}, dirs...)
	script := `find "$@" -exec touch -h -t 202401020304.05 {} +`
	hostTool(t, "sh", append([]string{"-c", script}, args...)...)
}

// buildBothWays builds one source tree with macospkg and with pkgbuild,
// passing each the same options, and returns the two packages.
func buildBothWays(t *testing.T, root, identifier string, extra ...string) (ours, theirs string) {
	t.Helper()
	work := t.TempDir()
	ours = filepath.Join(work, "ours.pkg")
	theirs = filepath.Join(work, "theirs.pkg")

	oursArgs := append([]string{"build", root, ours,
		"--identifier", identifier, "--version", "2.4.1",
		"--install-location", "/", "--source-date-epoch", epoch}, extra...)
	mustRun(t, oursArgs...)

	theirsArgs := append([]string{"--quiet", "--root", root,
		"--identifier", identifier, "--version", "2.4.1",
		"--install-location", "/", "--ownership", "recommended"}, extra...)
	hostTool(t, "pkgbuild", append(theirsArgs, theirs)...)
	return ours, theirs
}

// ------------------------------------------------------------------- tests

// TestPackageInfoBytesMatchPkgbuild pins the PackageInfo document: pkgbuild's
// attribute order, its script timeouts and its lack of a trailing newline.
func TestPackageInfoBytesMatchPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "xar")
	root, scripts := sourceTree(t)
	stampTree(t, root, scripts)

	work := t.TempDir()
	ours := filepath.Join(work, "ours.pkg")
	theirs := filepath.Join(work, "theirs.pkg")
	mustRun(t, parityBuildArgs(root, scripts, ours)...)
	hostTool(t, "pkgbuild", "--quiet", "--root", root,
		"--identifier", "com.deploymenttheory.acceptance", "--version", "1.0.0",
		"--scripts", scripts, "--ownership", "recommended", theirs)

	requireSameBytes(t, "PackageInfo", xarEntry(t, theirs, "PackageInfo"), xarEntry(t, ours, "PackageInfo"))
}

// TestProductTreePackageInfoMatchesPkgbuild is the real-world case: an
// application with a framework and an extension inside it, a preference pane
// beside it, and ordinary payload around them. It pins every rule that
// decides which bundle list a bundle is referenced from.
func TestProductTreePackageInfoMatchesPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "xar")
	root := productTree(t)
	stampTree(t, root)

	ours, theirs := buildBothWays(t, root, "com.deploymenttheory.product")
	pi := xarEntry(t, ours, "PackageInfo")
	requireSameBytes(t, "PackageInfo (product tree)",
		sortBundleRuns(xarEntry(t, theirs, "PackageInfo")), sortBundleRuns(pi))

	got := string(pi)

	// Every bundle is described, the framework by its own name because it
	// has the Resources link a real framework has.
	for _, want := range []string{
		`<bundle path="./Applications/Example.app" id="com.example.Example" CFBundleShortVersionString="2.4.1" CFBundleVersion="2410"/>`,
		`<bundle path="./Applications/Example.app/Contents/Frameworks/Shared.framework" id="com.example.Shared"`,
		`<bundle path="./Applications/Example.app/Contents/PlugIns/Helper.appex" id="com.example.Helper"`,
		`<bundle path="./Library/PreferencePanes/Example.prefPane" id="com.example.pref"`,
	} {
		assert.Containsf(t, got, want, "PackageInfo should describe %s", want)
	}

	// pkgbuild writes the identifier once, as id.
	assert.NotContains(t, got, "CFBundleIdentifier=",
		"pkgbuild never writes CFBundleIdentifier on a bundle element")

	element := func(name string) string {
		_, rest, ok := strings.Cut(got, "<"+name+">")
		if !ok {
			return "" // self-closed, so empty
		}
		body, _, _ := strings.Cut(rest, "</"+name+">")
		return body
	}

	// Only the top-level bundles are referenced at all: a framework or an
	// extension inside the application is installed as part of it.
	for _, list := range []string{"bundle-version", "upgrade-bundle", "strict-identifier", "relocate"} {
		body := element(list)
		assert.NotContainsf(t, body, "com.example.Shared", "%s should not reference a nested framework", list)
		assert.NotContainsf(t, body, "com.example.Helper", "%s should not reference a nested extension", list)
	}

	// Everything top-level is version-checked and upgraded.
	for _, list := range []string{"bundle-version", "upgrade-bundle"} {
		body := element(list)
		assert.Containsf(t, body, "com.example.Example", "%s should reference the application", list)
		assert.Containsf(t, body, "com.example.pref", "%s should reference the preference pane", list)
	}

	// Only the application is relocated and strictly identified.
	for _, list := range []string{"strict-identifier", "relocate"} {
		body := element(list)
		assert.Containsf(t, body, "com.example.Example", "%s should reference the application", list)
		assert.NotContainsf(t, body, "com.example.pref", "%s should not reference a preference pane", list)
	}

	// Nothing was routed to update-bundle without being asked.
	assert.Empty(t, element("update-bundle"), "update-bundle should be empty without a component property list")
	assert.Empty(t, element("atomic-update-bundle"), "atomic-update-bundle is not reachable from pkgbuild's inputs")
}

// TestProductTreePayloadMatchesPkgbuild checks the payload of the same tree,
// so the symbolic links, the executables and the framework's link farm are
// all packaged the way pkgbuild packages them.
func TestProductTreePayloadMatchesPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "pkgutil", "lsbom")
	root := productTree(t)
	stampTree(t, root)

	ours, theirs := buildBothWays(t, root, "com.deploymenttheory.product")
	assert.Equal(t, payloadPaths(t, theirs), payloadPaths(t, ours), "payload listings differ")

	lsbom := func(pkg string) []string {
		dir := t.TempDir()
		hostTool(t, "pkgutil", "--expand", pkg, filepath.Join(dir, "x"))
		lines := nonEmptyLines(hostTool(t, "lsbom", "-p", "fmugsc", filepath.Join(dir, "x", "Bom")))
		sort.Strings(lines)
		return lines
	}
	assert.Equal(t, lsbom(theirs), lsbom(ours), "bill of materials differs")
	attest(t, "product tree: payload and bill of materials agree with pkgbuild")
}

// TestAnalyzeMatchesPkgbuild pins the component property list, which is the
// document pkgbuild --analyze writes and --component-plist reads back.
func TestAnalyzeMatchesPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild")
	root := productTree(t)
	stampTree(t, root)

	work := t.TempDir()
	ours := filepath.Join(work, "ours.plist")
	theirs := filepath.Join(work, "theirs.plist")
	mustRun(t, "build", root, ours, "--analyze")
	hostTool(t, "pkgbuild", "--analyze", "--root", root, theirs)

	apple, err := os.ReadFile(theirs)
	require.NoError(t, err)
	mine, err := os.ReadFile(ours)
	require.NoError(t, err)

	// Both list the same top-level bundles, with the framework and the
	// extension under the application's ChildBundles. The order is Apple's
	// hash order, so compare the parsed entries rather than the bytes.
	require.Equal(t, parseComponentPlist(t, apple), parseComponentPlist(t, mine),
		"component property list differs from pkgbuild's")
	assert.Contains(t, string(mine), "<key>ChildBundles</key>",
		"the nested framework and extension belong under ChildBundles")
	attest(t, "pkgbuild --analyze output matches pkgbuild's, entry for entry")
}

// parseComponentPlist decodes a component property list and sorts every
// array of bundles by path, so two lists that differ only in Apple's hash
// order compare equal. The parser is a third-party one rather than ours, so
// this stays an independent check.
func parseComponentPlist(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var list []map[string]any
	_, err := plist.Unmarshal(data, &list)
	require.NoError(t, err, "parsing a component property list")
	sortEntries(list)
	return list
}

// sortEntries orders a bundle list by path, recursing into ChildBundles.
func sortEntries(list []map[string]any) {
	sort.Slice(list, func(i, j int) bool {
		a, _ := list[i]["RootRelativeBundlePath"].(string)
		b, _ := list[j]["RootRelativeBundlePath"].(string)
		return a < b
	})
	for _, e := range list {
		children, ok := e["ChildBundles"].([]any)
		if !ok {
			continue
		}
		typed := make([]map[string]any, 0, len(children))
		for _, c := range children {
			if m, ok := c.(map[string]any); ok {
				typed = append(typed, m)
			}
		}
		sortEntries(typed)
		out := make([]any, len(typed))
		for i, m := range typed {
			out[i] = m
		}
		e["ChildBundles"] = out
	}
}

// TestComponentPlistMatchesPkgbuild pins how a component property list maps
// onto the PackageInfo bundle lists and scripts. The mapping is orthogonal:
// each key drives exactly one element, and BundleOverwriteAction chooses
// between upgrade-bundle and update-bundle.
func TestComponentPlistMatchesPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "xar")
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeBundle(t, root, "com.example.one", "Applications/One.app/Contents/Info.plist")
	writeBundle(t, root, "com.example.two", "Applications/Two.app/Contents/Info.plist")
	scripts := filepath.Join(base, "scripts")
	for _, n := range []string{"preinstall", "postinstall", "bundlepre", "bundlepost"} {
		writeFile(t, filepath.Join(scripts, n), "#!/bin/sh\nexit 0\n", 0o755)
	}

	// One.app is an update-only delivery with both of its own scripts and
	// an explicit timeout. Two.app is a normal upgrade, relocated and
	// strictly identified, with one script and no timeout, so it takes the
	// long bundle-script default.
	plist := filepath.Join(base, "components.plist")
	writeFile(t, plist, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<array>
	<dict>
		<key>BundleInstallScriptTimeout</key>
		<integer>1200</integer>
		<key>BundleIsVersionChecked</key>
		<true/>
		<key>BundleOverwriteAction</key>
		<string>update</string>
		<key>BundlePostInstallScriptPath</key>
		<string>bundlepost</string>
		<key>BundlePreInstallScriptPath</key>
		<string>bundlepre</string>
		<key>RootRelativeBundlePath</key>
		<string>Applications/One.app</string>
	</dict>
	<dict>
		<key>BundleHasStrictIdentifier</key>
		<true/>
		<key>BundleIsRelocatable</key>
		<true/>
		<key>BundleIsVersionChecked</key>
		<true/>
		<key>BundleOverwriteAction</key>
		<string>upgrade</string>
		<key>BundlePreInstallScriptPath</key>
		<string>bundlepre</string>
		<key>RootRelativeBundlePath</key>
		<string>Applications/Two.app</string>
	</dict>
</array>
</plist>
`, 0o644)
	stampTree(t, root, scripts)

	ours, theirs := buildBothWays(t, root, "com.deploymenttheory.cp",
		"--scripts", scripts, "--component-plist", plist)

	pi := xarEntry(t, ours, "PackageInfo")
	requireSameBytes(t, "PackageInfo (component plist)",
		sortBundleRuns(xarEntry(t, theirs, "PackageInfo")), sortBundleRuns(pi))

	got := string(pi)
	// The bundle-specific scripts come first, each naming its bundle, then
	// the package's own. A bundle with no timeout of its own gets six
	// hours where the package's own scripts get ten minutes.
	for _, want := range []string{
		`<preinstall file="./bundlepre" component-id="com.example.one" timeout="1200"/>`,
		`<preinstall file="./bundlepre" component-id="com.example.two" timeout="21600"/>`,
		`<preinstall file="./preinstall" timeout="600"/>`,
		`<postinstall file="./bundlepost" component-id="com.example.one" timeout="1200"/>`,
		`<postinstall file="./postinstall" timeout="600"/>`,
	} {
		assert.Contains(t, got, want)
	}
	assert.Contains(t, got, `<update-bundle>`, "BundleOverwriteAction update should reach update-bundle")
}

// TestComponentPlistIsExhaustive pins the rule that a component property
// list replaces bundle discovery rather than adding to it: a bundle the
// list does not name is not described at all.
func TestComponentPlistIsExhaustive(t *testing.T) {
	requireTools(t, "pkgbuild", "xar")
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeBundle(t, root, "com.example.one", "Applications/One.app/Contents/Info.plist")
	writeBundle(t, root, "com.example.two", "Applications/Two.app/Contents/Info.plist")
	plist := filepath.Join(base, "components.plist")
	writeFile(t, plist, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<array>
	<dict>
		<key>BundleIsVersionChecked</key>
		<true/>
		<key>BundleOverwriteAction</key>
		<string>upgrade</string>
		<key>RootRelativeBundlePath</key>
		<string>Applications/One.app</string>
	</dict>
</array>
</plist>
`, 0o644)
	stampTree(t, root)

	ours, theirs := buildBothWays(t, root, "com.deploymenttheory.cpx", "--component-plist", plist)

	pi := xarEntry(t, ours, "PackageInfo")
	requireSameBytes(t, "PackageInfo (exhaustive list)", xarEntry(t, theirs, "PackageInfo"), pi)
	assert.NotContains(t, string(pi), "com.example.two",
		"a bundle the component property list does not name should not be described")
}

// ------------------------------------------------------------ filter tests

// filterTree writes a root holding everything pkgbuild's default filters
// are supposed to catch and, just as importantly, the near misses they must
// not: CVSdir, notCVS.txt and .DS_Store_dir are all kept.
func filterTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	write := func(rel string) { writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), "x\n", 0o644) }
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
	stampTree(t, root)

	ours, theirs := buildBothWays(t, root, "com.deploymenttheory.filters")
	got := payloadPaths(t, ours)
	require.Equal(t, payloadPaths(t, theirs), got, "default filters differ from pkgbuild's")

	for _, gone := range []string{"./a/.svn", "./a/CVS", "./CVS", "./.DS_Store", "./plain/CVS", "./plain/.svn"} {
		assert.NotContainsf(t, got, gone, "%s should have been filtered out", gone)
	}
	// plain/ held nothing but filtered entries, so pkgbuild drops it too.
	assert.NotContains(t, got, "./plain", "a directory the filters emptied should be dropped")
	for _, kept := range []string{"./CVSdir", "./notCVS.txt", "./a/.svnfile", "./.DS_Store_dir"} {
		assert.Containsf(t, got, kept, "%s only resembles a filtered name", kept)
	}
	attest(t, "default filters agree with pkgbuild on %d payload entries", len(got))
}

// TestFilterInhibitsDefaults pins the other half of pkgbuild's rule: naming
// even one filter replaces the defaults rather than adding to them.
func TestFilterInhibitsDefaults(t *testing.T) {
	requireTools(t, "pkgbuild", "pkgutil")
	root := filterTree(t)
	stampTree(t, root)

	ours, theirs := buildBothWays(t, root, "com.deploymenttheory.filters", "--filter", "/keep$")
	got := payloadPaths(t, ours)
	require.Equal(t, payloadPaths(t, theirs), got, "--filter differs from pkgbuild's")

	assert.Contains(t, got, "./.DS_Store", "a named filter should inhibit the default .DS_Store filter")
	assert.NotContains(t, got, "./keep", "--filter /keep$ should have dropped ./keep")
	attest(t, "a named filter replaces the defaults, as pkgbuild does (%d entries)", len(got))
}

// TestDistributionBytesMatchProductbuild pins the synthesised Distribution
// against the one productbuild embeds, which is not the same document
// productbuild --synthesize writes to a file.
func TestDistributionBytesMatchProductbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "productbuild", "xar")
	root, scripts := sourceTree(t)
	stampTree(t, root, scripts)

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
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "productbuild --distribution: %s", out)

	ours := filepath.Join(work, "ours.pkg")
	mustRun(t, "product", ours, "--package", first, "--package", second, "--source-date-epoch", epoch)

	requireSameBytes(t, "Distribution", xarEntry(t, theirs, "Distribution"), xarEntry(t, ours, "Distribution"))
}

// TestComponentModeMatchesPkgbuild covers pkgbuild's third mode, where the
// payload is a bundle rather than a destination root and the package's
// identity is read out of the bundle's Info.plist.
func TestComponentModeMatchesPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "xar", "pkgutil")
	base := t.TempDir()
	root := filepath.Join(base, "root")
	app := filepath.Join(root, "Example.app")
	writeFile(t, filepath.Join(app, "Contents", "Info.plist"), infoPlist("com.example.Example", "3.2.1", "3210"), 0o644)
	writeFile(t, filepath.Join(app, "Contents", "MacOS", "Example"), "#!/bin/sh\necho example\n", 0o755)
	writeFile(t, filepath.Join(app, "Contents", "Resources", "big.bin"), strings.Repeat("a", 307200), 0o644)
	stampTree(t, root)

	work := t.TempDir()
	ours := filepath.Join(work, "ours.pkg")
	theirs := filepath.Join(work, "theirs.pkg")
	runIn(t, root, "pkgbuild", "--quiet", "--component", "Example.app", theirs)
	mustRun(t, "build", ours, "--component", app, "--source-date-epoch", epoch)

	apple := string(xarEntry(t, theirs, "PackageInfo"))
	mine := string(xarEntry(t, ours, "PackageInfo"))

	// A component build reports different payload numbers from a root
	// build of the same tree: it counts the bundle's entries rather than
	// the archive's, so the AppleDouble sidecars and the directory sizes
	// are left out. Build it the other way too, and require that the two
	// really do disagree, so this stays a test of the rule rather than of
	// nothing.
	asRoot := filepath.Join(work, "asroot.pkg")
	hostTool(t, "pkgbuild", "--quiet", "--root", root,
		"--identifier", "com.example.Example", "--version", "3.2.1",
		"--ownership", "recommended", asRoot)
	require.NotEqual(t, payloadElement(t, string(xarEntry(t, asRoot, "PackageInfo"))), payloadElement(t, apple),
		"a component build should not report the same payload numbers as a root build")
	assert.Equal(t, payloadElement(t, apple), payloadElement(t, mine))

	// Identity is inferred from the Info.plist.
	assert.Contains(t, mine, `identifier="com.example.Example"`)
	assert.Contains(t, mine, `version="3.2.1"`)

	// The install location is the directory holding the bundle. Compare it
	// resolved: under /tmp and /var the two tools spell one directory
	// differently, because those are symbolic links into /private and
	// macOS shows the shorter form to Apple's own path APIs.
	assert.Equal(t, resolved(t, installLocation(t, apple)), resolved(t, installLocation(t, mine)))

	// Only the named bundle is packaged, and the payload is pkgbuild's.
	assert.Equal(t, payloadPaths(t, theirs), payloadPaths(t, ours))

	// Everything else in the document still matches byte for byte.
	normalise := func(s string) string { return installLocationAttr.ReplaceAllString(s, `install-location="L"`) }
	requireSameBytes(t, "PackageInfo (component mode)", []byte(normalise(apple)), []byte(normalise(mine)))
}

// installLocationAttr matches the attribute the component-mode comparison
// normalises, for the /private reason above.
var installLocationAttr = regexp.MustCompile(`install-location="[^"]*"`)

// payloadElementRe matches the payload element and its counts.
var payloadElementRe = regexp.MustCompile(`<payload[^/]*/>`)

// payloadElement pulls the payload element out of a PackageInfo.
func payloadElement(t *testing.T, packageInfo string) string {
	t.Helper()
	m := payloadElementRe.FindString(packageInfo)
	require.NotEmpty(t, m, "PackageInfo carries no payload element")
	return m
}

// installLocation reads the attribute out of a PackageInfo.
func installLocation(t *testing.T, packageInfo string) string {
	t.Helper()
	m := installLocationAttr.FindString(packageInfo)
	require.NotEmpty(t, m, "PackageInfo carries no install-location")
	return strings.TrimSuffix(strings.TrimPrefix(m, `install-location="`), `"`)
}

// resolved follows symbolic links, so /tmp and /private/tmp compare equal.
func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return r
}

// runIn runs a host tool with a working directory, which --component needs
// because pkgbuild records the directory holding the bundle.
func runIn(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "%s: %s", name, out)
}

// TestPriorMatchesPkgbuild covers --prior, which takes the identifier and
// install location from a previous build and increments its version.
func TestPriorMatchesPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "xar")
	root := productTree(t)
	stampTree(t, root)

	// The version is converted to an integer and incremented, so a prior
	// 1.0.0 becomes 2 and a prior 9.9.9 becomes 10.
	for _, tc := range []struct{ prior, want string }{
		{"1.0.0", "2"},
		{"3", "4"},
		{"2.7", "3"},
		{"9.9.9", "10"},
	} {
		t.Run("prior_"+tc.prior, func(t *testing.T) {
			work := t.TempDir()
			first := filepath.Join(work, "first.pkg")
			hostTool(t, "pkgbuild", "--quiet", "--root", root,
				"--identifier", "com.deploymenttheory.prior", "--version", tc.prior,
				"--install-location", "/opt/here", "--ownership", "recommended", first)

			ours := filepath.Join(work, "ours.pkg")
			theirs := filepath.Join(work, "theirs.pkg")
			mustRun(t, "build", root, ours, "--prior", first, "--source-date-epoch", epoch)
			hostTool(t, "pkgbuild", "--quiet", "--root", root, "--prior", first,
				"--ownership", "recommended", theirs)

			mine := string(xarEntry(t, ours, "PackageInfo"))
			assert.Contains(t, mine, `identifier="com.deploymenttheory.prior"`)
			assert.Contains(t, mine, `version="`+tc.want+`"`)
			assert.Contains(t, mine, `install-location="/opt/here"`)
			requireSameBytes(t, "PackageInfo (prior)",
				sortBundleRuns(xarEntry(t, theirs, "PackageInfo")), sortBundleRuns([]byte(mine)))
		})
	}
}

// TestLargePayloadMatchesPkgbuild covers pkgbuild --large-payload, which is
// not a different container at all on current macOS: the payload is the same
// gzip cpio, carried under a different entry name and marked in the
// PackageInfo, and only macOS 12 and later will read it.
func TestLargePayloadMatchesPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "xar", "pkgutil")
	root, _ := sourceTree(t)
	stampTree(t, root)

	work := t.TempDir()
	ours := filepath.Join(work, "ours.pkg")
	theirs := filepath.Join(work, "theirs.pkg")
	mustRun(t, "build", root, ours, "--identifier", "com.deploymenttheory.large",
		"--version", "1.0", "--install-location", "/", "--large-payload",
		"--min-os-version", "12.0", "--source-date-epoch", epoch)
	hostTool(t, "pkgbuild", "--quiet", "--root", root,
		"--identifier", "com.deploymenttheory.large", "--version", "1.0",
		"--install-location", "/", "--ownership", "recommended",
		"--large-payload", "--min-os-version", "12.0", theirs)

	requireSameBytes(t, "PackageInfo (large payload)",
		xarEntry(t, theirs, "PackageInfo"), xarEntry(t, ours, "PackageInfo"))

	// The entry is renamed, and the content is still a gzip cpio.
	entries := nonEmptyLines(hostTool(t, "xar", "-tf", ours))
	assert.Contains(t, entries, "LargeSegmentedPayload")
	assert.NotContains(t, entries, "Payload")
	payload := xarEntry(t, ours, "LargeSegmentedPayload")
	require.GreaterOrEqual(t, len(payload), 2)
	assert.Equal(t, []byte{0x1f, 0x8b}, payload[:2], "the payload should still be gzip")

	// Apple's own reader takes it.
	assert.Equal(t, payloadPaths(t, theirs), payloadPaths(t, ours))
}

// TestLargePayloadNeedsMinOSVersion pins pkgbuild's precondition. Getting it
// wrong would produce a package that builds and then fails to install on
// macOS 11, so it is refused as a usage error rather than built.
func TestLargePayloadNeedsMinOSVersion(t *testing.T) {
	root, _ := sourceTree(t)
	out := filepath.Join(t.TempDir(), "out.pkg")
	for _, minOS := range []string{"", "11.0"} {
		args := []string{"build", root, out, "--identifier", "com.deploymenttheory.large",
			"--version", "1.0", "--large-payload"}
		if minOS != "" {
			args = append(args, "--min-os-version", minOS)
		}
		_, stderr, code := run(t, args...)
		assert.Equalf(t, 2, code, "--large-payload with min-os-version %q should be a usage error", minOS)
		assert.Contains(t, stderr, "--min-os-version 12.0 or later")
	}
}

// TestSynthesizeMatchesProductbuild pins the document productbuild
// --synthesize writes to a file, which is not the one it embeds.
func TestSynthesizeMatchesProductbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "productbuild")
	first, second, work := twoComponents(t)

	ours := filepath.Join(work, "ours.xml")
	theirs := filepath.Join(work, "theirs.xml")
	mustRun(t, "product", ours, "--package", first, "--package", second, "--synthesize")
	hostTool(t, "productbuild", "--quiet", "--synthesize", "--package", first, "--package", second, theirs)

	apple, err := os.ReadFile(theirs)
	require.NoError(t, err)
	mine, err := os.ReadFile(ours)
	require.NoError(t, err)
	require.Equal(t, string(apple), string(mine), "synthesised Distribution differs")

	// The file form is the one without standalone, without the sizes, and
	// naming each package as a file rather than an archive entry.
	assert.NotContains(t, string(mine), `standalone="yes"`)
	assert.NotContains(t, string(mine), "installKBytes=")
	assert.NotContains(t, string(mine), ">#")
	attest(t, "productbuild --synthesize output is byte-identical (%d bytes)", len(apple))
}

// TestSuppliedDistributionIsRewritten pins what productbuild does to a
// distribution as it embeds one: the references stop naming files beside the
// document and start naming entries inside the archive.
func TestSuppliedDistributionIsRewritten(t *testing.T) {
	requireTools(t, "pkgbuild", "productbuild", "xar")
	first, second, work := twoComponents(t)

	// A hand-written distribution, with choices the user can see and a
	// script carrying a bare ">" in a comparison. Neither the choices nor
	// the script should come back changed in meaning.
	dist := filepath.Join(work, "dist.xml")
	writeFile(t, dist, `<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
    <title>Fixture Custom</title>
    <options customize="allow" require-scripts="false" hostArchitectures="arm64,x86_64"/>
    <installation-check script="checkRAM()"/>
    <script>
    function checkRAM() {
        return system.sysctl('hw.memsize') > 1024;
    }
    </script>
    <choices-outline>
        <line choice="basic"/>
        <line choice="extra"/>
    </choices-outline>
    <choice id="basic" title="Basic" visible="true" start_selected="true">
        <pkg-ref id="com.deploymenttheory.acceptance.first"/>
    </choice>
    <choice id="extra" title="Extra" visible="true" start_selected="false">
        <pkg-ref id="com.deploymenttheory.acceptance.second"/>
    </choice>
    <pkg-ref id="com.deploymenttheory.acceptance.first" version="1.0.0" onConclusion="none">`+filepath.Base(first)+`</pkg-ref>
    <pkg-ref id="com.deploymenttheory.acceptance.second" version="2.1" onConclusion="none">`+filepath.Base(second)+`</pkg-ref>
</installer-gui-script>
`, 0o644)

	ours := filepath.Join(work, "ours.pkg")
	theirs := filepath.Join(work, "theirs.pkg")
	mustRun(t, "product", ours, "--distribution", dist, "--package-path", work, "--source-date-epoch", epoch)
	runIn(t, work, "productbuild", "--quiet", "--distribution", dist, "--package-path", work, theirs)

	mine := xarEntry(t, ours, "Distribution")
	requireSameBytes(t, "Distribution (supplied, rewritten)", xarEntry(t, theirs, "Distribution"), mine)

	got := string(mine)
	// The declarations now name archive entries and carry the sizes.
	assert.Contains(t, got, `standalone="yes"`)
	assert.Contains(t, got, ">#"+filepath.Base(first)+"</pkg-ref>")
	assert.Contains(t, got, `updateKBytes="0"`)
	// A stub per package, appended because the document declared none.
	assert.Contains(t, got, "<bundle-version/>")
	// The references inside a choice are untouched, since those point at a
	// package rather than declaring it.
	assert.Contains(t, got, `<choice id="basic" title="Basic" visible="true" start_selected="true">`)
	// The author's own content survives, escaping aside.
	assert.Contains(t, got, "system.sysctl('hw.memsize') &gt; 1024")
}

// TestPackagePathResolvesFromDistribution pins --package-path: the packages
// come from the names the distribution uses, found in the given directories.
func TestPackagePathResolvesFromDistribution(t *testing.T) {
	requireTools(t, "pkgbuild", "productbuild", "xar")
	first, second, work := twoComponents(t)

	synth := filepath.Join(work, "synth.xml")
	hostTool(t, "productbuild", "--quiet", "--synthesize", "--package", first, "--package", second, synth)

	// Move the packages somewhere the working directory does not reach, so
	// the search path is doing the work.
	elsewhere := t.TempDir()
	for _, p := range []string{first, second} {
		body, err := os.ReadFile(p)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(elsewhere, filepath.Base(p)), body, 0o644))
	}

	ours := filepath.Join(work, "ours.pkg")
	mustRun(t, "product", ours, "--distribution", synth, "--package-path", elsewhere, "--source-date-epoch", epoch)
	entries := nonEmptyLines(hostTool(t, "xar", "-tf", ours))
	assert.Contains(t, entries, filepath.Base(first))
	assert.Contains(t, entries, filepath.Base(second))

	// A name that is nowhere on the search path is an error naming it,
	// rather than a package with a missing entry.
	_, stderr, code := run(t, "product", filepath.Join(work, "no.pkg"),
		"--distribution", synth, "--package-path", t.TempDir())
	assert.NotEqual(t, 0, code)
	assert.Contains(t, stderr, filepath.Base(first))
}

// twoComponents builds two component packages with pkgbuild and returns them
// with the directory holding them.
func twoComponents(t *testing.T) (first, second, dir string) {
	t.Helper()
	root, scripts := sourceTree(t)
	stampTree(t, root, scripts)
	dir = t.TempDir()
	first = filepath.Join(dir, "first.pkg")
	second = filepath.Join(dir, "second.pkg")
	hostTool(t, "pkgbuild", "--quiet", "--root", root,
		"--identifier", "com.deploymenttheory.acceptance.first", "--version", "1.0.0",
		"--scripts", scripts, "--ownership", "recommended", first)
	hostTool(t, "pkgbuild", "--quiet", "--root", root,
		"--identifier", "com.deploymenttheory.acceptance.second", "--version", "2.1",
		"--install-location", "/opt/fixture", "--ownership", "recommended", second)
	return first, second, dir
}
