// flatten: reassembling an expanded package, which is pkgutil --flatten.
package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlattenRoundTrip is the property that matters: expanding a package and
// flattening it again gives back what went in.
//
// Every entry compares byte for byte except Scripts, which is a cpio built
// afresh from the unpacked directory. pkgutil's own flatten does the same,
// so the archives differ in their gzip framing while holding the same paths
// with the same modes, and that is what is checked instead.
func TestFlattenRoundTrip(t *testing.T) {
	for _, name := range []string{"component-basic.pkg", "product-basic.pkg", "component-bundle.pkg"} {
		t.Run(name, func(t *testing.T) {
			pkg, _ := fixture(t, name)
			work := t.TempDir()
			expanded := filepath.Join(work, "expanded")
			rebuilt := filepath.Join(work, "rebuilt.pkg")

			// --xattrs file keeps the "._" sidecars as the files they are
			// in the archive, so the round trip carries them either way.
			mustRun(t, "expand", "--xattrs", "file", pkg, expanded)
			mustRun(t, "flatten", expanded, rebuilt)

			assert.Equal(t, archiveEntries(t, pkg), archiveEntries(t, rebuilt))

			// Decoded, not raw: the entries are gzipped afresh, so their
			// stored bytes differ while their contents do not. That is
			// true of pkgutil --flatten as well.
			for _, entry := range archiveFileEntries(t, rebuilt) {
				if strings.HasSuffix(entry, "Scripts") {
					continue // rebuilt from the directory; covered below
				}
				before := mustRun(t, "cat", pkg, entry)
				after := mustRun(t, "cat", rebuilt, entry)
				assert.Equalf(t, before, after, "%s should have come back unchanged", entry)
			}

			// The Scripts archives are built afresh, so compare what they
			// hold rather than their bytes: the same paths with the same
			// modes, which is all a package's scripts amount to.
			for _, entry := range archiveFileEntries(t, rebuilt) {
				if !strings.HasSuffix(entry, "Scripts") {
					continue
				}
				component := strings.TrimSuffix(strings.TrimSuffix(entry, "Scripts"), "/")
				args := []string{"list", "--scripts", "-l"}
				if component != "" {
					args = append(args, "--component", component)
				}
				before := scriptListing(t, append(args, pkg))
				after := scriptListing(t, append(args, rebuilt))
				assert.Equalf(t, before, after, "%s should hold the same scripts", entry)
			}
			attest(t, "%s: flattened back to the same %d entries", name, len(archiveEntries(t, rebuilt)))
		})
	}
}

// archiveEntry is one line of "list --archive -o json"; the archive listing
// names its column "entry" rather than "path", since these are entries in
// the container and not files a package installs.
type archiveEntry struct {
	Entry string `json:"entry"`
	Type  string `json:"type"`
}

// archiveEntries lists a package's archive entries in a stable order, using
// our own reader so the test runs on every platform.
func archiveEntries(t *testing.T, pkg string) []string {
	t.Helper()
	return archiveEntriesOfType(t, pkg, "")
}

// archiveFileEntries lists only the entries that hold bytes. The directories
// a product archive uses to group its components have nothing to compare.
func archiveFileEntries(t *testing.T, pkg string) []string {
	t.Helper()
	return archiveEntriesOfType(t, pkg, "file")
}

// archiveEntriesOfType lists the entries of one type, or all where the type
// is empty.
func archiveEntriesOfType(t *testing.T, pkg, kind string) []string {
	t.Helper()
	var out []string
	for _, l := range nonEmptyLines(mustRun(t, "-o", "json", "list", "--archive", pkg)) {
		var e archiveEntry
		require.NoErrorf(t, json.Unmarshal([]byte(l), &e), "list --archive printed invalid JSON: %s", l)
		if kind != "" && e.Type != kind {
			continue
		}
		out = append(out, e.Entry)
	}
	sort.Strings(out)
	return out
}

// TestFlattenMatchesPkgutil checks the result against pkgutil, both that it
// produces the same entries and that Apple's own reader takes ours.
func TestFlattenMatchesPkgutil(t *testing.T) {
	requireTools(t, "pkgutil", "xar", "lsbom")
	pkg, _ := fixture(t, "product-basic.pkg")
	work := t.TempDir()

	// Expanded by each tool, flattened by each tool.
	oursDir := filepath.Join(work, "ours-expanded")
	theirsDir := filepath.Join(work, "theirs-expanded")
	mustRun(t, "expand", "--xattrs", "file", pkg, oursDir)
	hostTool(t, "pkgutil", "--expand", pkg, theirsDir)

	ours := filepath.Join(work, "ours.pkg")
	theirs := filepath.Join(work, "theirs.pkg")
	mustRun(t, "flatten", oursDir, ours)
	hostTool(t, "pkgutil", "--flatten", theirsDir, theirs)

	sorted := func(pkg string) []string {
		e := nonEmptyLines(hostTool(t, "xar", "-tf", pkg))
		sort.Strings(e)
		return e
	}
	assert.Equal(t, sorted(theirs), sorted(ours))

	// What the package installs is unchanged, and Apple's reader accepts
	// what we wrote.
	assert.Equal(t, payloadPaths(t, pkg), payloadPaths(t, ours))
	back := filepath.Join(work, "back")
	hostTool(t, "pkgutil", "--expand", ours, back)
	attest(t, "pkgutil expands what flatten writes, and the payload is unchanged")
}

// TestFlattenCarriesAnEdit is what flatten is for: changing one thing in a
// package without rebuilding it.
func TestFlattenCarriesAnEdit(t *testing.T) {
	pkg, _ := fixture(t, "component-basic.pkg")
	work := t.TempDir()
	expanded := filepath.Join(work, "expanded")
	mustRun(t, "expand", "--xattrs", "file", pkg, expanded)

	info := filepath.Join(expanded, "PackageInfo")
	before, err := os.ReadFile(info)
	require.NoError(t, err)
	edited := strings.Replace(string(before), `version="1.0.0"`, `version="9.9.9"`, 1)
	require.NotEqual(t, string(before), edited, "the fixture should carry the version being edited")
	require.NoError(t, os.WriteFile(info, []byte(edited), 0o644))

	rebuilt := filepath.Join(work, "rebuilt.pkg")
	mustRun(t, "flatten", expanded, rebuilt)

	var report infoJSON
	mustRunJSON(t, &report, "info", rebuilt)
	require.NotEmpty(t, report.Packages)
	assert.Equal(t, "9.9.9", report.Packages[0].Version,
		"the edited PackageInfo should be the one carried through")
}

// TestFlattenRefusesWhatIsNotAPackage keeps flatten from quietly producing an
// archive out of any directory at all.
func TestFlattenRefusesWhatIsNotAPackage(t *testing.T) {
	work := t.TempDir()
	empty := filepath.Join(work, "empty")
	require.NoError(t, os.MkdirAll(empty, 0o755))
	_, stderr, code := run(t, "flatten", empty, filepath.Join(work, "out.pkg"))
	assert.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "nothing to flatten")

	_, stderr, code = run(t, "flatten", filepath.Join(work, "PackageInfo-that-is-a-file"), filepath.Join(work, "out.pkg"))
	assert.NotEqual(t, 0, code)
	assert.NotEmpty(t, stderr)
}

// scriptListing lists a Scripts archive as mode, size and path.
//
// Ownership is left out on purpose. The archive a package arrives with
// records the uid of whoever built it, and pkgutil --flatten records the
// uid of whoever expanded it; macospkg records root:wheel, so that
// flattening the same directory on two machines gives the same package. It
// makes no difference to installation, where the Installer runs the scripts
// as root whatever the archive says.
func scriptListing(t *testing.T, args []string) []string {
	t.Helper()
	var out []string
	for _, l := range nonEmptyLines(mustRun(t, args...)) {
		fields := strings.Fields(l)
		if len(fields) < 4 {
			continue
		}
		out = append(out, fields[0]+" "+fields[2]+" "+fields[3])
	}
	sort.Strings(out)
	return out
}

// TestListFilters checks the two halves of a listing against lsbom, which
// can make the same split, and pins that they are halves: every entry is in
// exactly one of them.
func TestListFilters(t *testing.T) {
	pkg, _ := fixture(t, "component-basic.pkg")

	all := listPaths(t, pkg)
	files := listPaths(t, pkg, "--only-files")
	dirs := listPaths(t, pkg, "--only-dirs")

	assert.Len(t, all, len(files)+len(dirs), "every entry should be a file or a directory, and not both")
	assert.NotEmpty(t, files)
	assert.NotEmpty(t, dirs)
	for _, d := range dirs {
		assert.NotContains(t, files, d)
	}

	// A regular expression narrows the listing to what it matches.
	matched := listPaths(t, pkg, "--regexp", `bin/`)
	assert.NotEmpty(t, matched)
	for _, p := range matched {
		assert.Contains(t, p, "bin/")
	}

	// Asking for both halves at once is a usage error rather than an
	// empty listing, since it can only be a mistake.
	_, stderr, code := run(t, "list", "--only-files", "--only-dirs", pkg)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "--only-files and --only-dirs")
}

// TestListDirsMatchLsbom compares the directory half against Apple's own
// reader, which is the independent check.
func TestListDirsMatchLsbom(t *testing.T) {
	requireTools(t, "lsbom", "xar")
	pkg, _ := fixture(t, "component-basic.pkg")

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Bom"), xarEntry(t, pkg, "Bom"), 0o644))
	theirs := nonEmptyLines(hostTool(t, "lsbom", "-d", "-p", "f", filepath.Join(dir, "Bom")))
	sort.Strings(theirs)

	ours := listPaths(t, pkg, "--only-dirs")
	assert.Equal(t, theirs, ours)
	attest(t, "--only-dirs agrees with lsbom -d on %d directories", len(ours))
}

// listPaths lists a package's payload paths, in a stable order.
func listPaths(t *testing.T, pkg string, extra ...string) []string {
	t.Helper()
	args := append(append([]string{"-o", "json", "list"}, extra...), pkg)
	var out []string
	for _, l := range nonEmptyLines(mustRun(t, args...)) {
		var e struct {
			Path string `json:"path"`
		}
		require.NoError(t, json.Unmarshal([]byte(l), &e))
		out = append(out, e.Path)
	}
	sort.Strings(out)
	return out
}

// TestCatBomFeedsLsbom pins the documented stand-in for pkgutil --bom:
// there is no command whose only job is to write the bill of materials to a
// file, because cat already does it. Not with --raw, which would write the
// gzip the entry is stored as.
func TestCatBomFeedsLsbom(t *testing.T) {
	requireTools(t, "lsbom", "xar")
	pkg, _ := fixture(t, "component-basic.pkg")
	dir := t.TempDir()

	written := filepath.Join(dir, "Bom")
	require.NoError(t, os.WriteFile(written, []byte(mustRun(t, "cat", pkg, "Bom")), 0o644))
	extracted := filepath.Join(dir, "Bom-xar")
	require.NoError(t, os.WriteFile(extracted, xarEntry(t, pkg, "Bom"), 0o644))

	assert.Equal(t, hostTool(t, "lsbom", extracted), hostTool(t, "lsbom", written),
		"lsbom should read what cat writes")
}
