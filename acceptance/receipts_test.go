// receipts: reading what a volume records about the packages installed on
// it, which is pkgutil's receipt database side.
//
// These run against the machine's own receipts, so they assert relationships
// with pkgutil rather than fixed values: which package is installed on a
// given Mac is not something a test can know.
package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReceiptsListIsASubsetOfPkgutil pins the boundary of what a directory
// reader can see.
//
// Every identifier in <volume>/var/db/receipts is one pkgutil reports, but
// not the other way round: the packages that make up macOS itself are held
// in a sealed database pkgutil reaches through a private interface, and no
// directory lists them. Reading the directory is what makes this work
// against a mounted volume from any operating system, and this is the price.
func TestReceiptsListIsASubsetOfPkgutil(t *testing.T) {
	requireTools(t, "pkgutil")
	ours := receiptIDs(t)
	if len(ours) == 0 {
		t.Skip("this machine has no receipts under /var/db/receipts")
	}
	theirs := map[string]bool{}
	for _, id := range nonEmptyLines(hostTool(t, "pkgutil", "--pkgs")) {
		theirs[id] = true
	}
	for _, id := range ours {
		assert.Truef(t, theirs[id], "pkgutil should know %s", id)
	}
	attest(t, "all %d receipts on this volume are ones pkgutil reports, of %d it knows", len(ours), len(theirs))
}

// TestReceiptsInfoMatchesPkgutil compares the fields pkgutil prints.
func TestReceiptsInfoMatchesPkgutil(t *testing.T) {
	requireTools(t, "pkgutil")
	id := someReceipt(t)

	var ours struct {
		PackageID       string `json:"packageId"`
		Version         string `json:"version"`
		Volume          string `json:"volume"`
		InstallLocation string `json:"installLocation"`
		InstallTime     int64  `json:"installTime"`
	}
	mustRunJSON(t, &ours, "receipts", "info", id)

	// pkgutil prints "key: value" lines.
	theirs := map[string]string{}
	for _, l := range nonEmptyLines(hostTool(t, "pkgutil", "--pkg-info", id)) {
		k, v, ok := strings.Cut(l, ": ")
		if ok {
			theirs[k] = v
		}
	}
	assert.Equal(t, theirs["package-id"], ours.PackageID)
	assert.Equal(t, theirs["version"], ours.Version)
	assert.Equal(t, theirs["volume"], ours.Volume)
	assert.Equal(t, theirs["location"], ours.InstallLocation)
	if v, ok := theirs["install-time"]; ok {
		assert.Equal(t, v, strconv.FormatInt(ours.InstallTime, 10))
	}
	attest(t, "receipts info agrees with pkgutil --pkg-info for %s", id)
}

// TestReceiptsFilesMatchPkgutil compares the whole file listing, which is
// the substantial part of a receipt.
func TestReceiptsFilesMatchPkgutil(t *testing.T) {
	requireTools(t, "pkgutil")
	id := someReceiptWithFiles(t)

	theirs := nonEmptyLines(hostTool(t, "pkgutil", "--files", id))
	sort.Strings(theirs)
	ours := nonEmptyLines(mustRun(t, "receipts", "files", id))
	sort.Strings(ours)
	require.Equal(t, theirs, ours)

	// The two halves partition the listing, as they do for a package.
	files := nonEmptyLines(mustRun(t, "receipts", "files", "--only-files", id))
	dirs := nonEmptyLines(mustRun(t, "receipts", "files", "--only-dirs", id))
	assert.Len(t, ours, len(files)+len(dirs))
	attest(t, "receipts files agrees with pkgutil --files on %d paths for %s", len(ours), id)
}

// fakeVolume builds a volume with a receipt database of its own, so the
// reader can be exercised anywhere rather than only on a Mac that happens
// to have something installed.
//
// A receipt is a property list and a bill of materials named for the
// package. The bill of materials is borrowed from a fixture, since it is
// the same format a package carries.
func fakeVolume(t *testing.T) (volume, id string) {
	t.Helper()
	volume = t.TempDir()
	id = "com.deploymenttheory.example"
	dir := filepath.Join(volume, "var", "db", "receipts")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	writeFile(t, filepath.Join(dir, id+".plist"), `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>InstallPrefixPath</key><string>usr/local</string>
	<key>InstallProcessName</key><string>installer</string>
	<key>PackageFileName</key><string>example.pkg</string>
	<key>PackageIdentifier</key><string>`+id+`</string>
	<key>PackageVersion</key><string>4.2.1</string>
</dict>
</plist>
`, 0o644)

	pkg, _ := fixture(t, "component-basic.pkg")
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".bom"),
		[]byte(mustRun(t, "cat", pkg, "Bom")), 0o644))
	return volume, id
}

// TestReceiptsReadAVolume exercises the reader against a volume built for
// the purpose, so it runs on every platform rather than only where a Mac
// happens to have something installed.
func TestReceiptsReadAVolume(t *testing.T) {
	volume, id := fakeVolume(t)

	assert.Equal(t, []string{id}, nonEmptyLines(mustRun(t, "receipts", "list", "--volume", volume)))

	var info struct {
		PackageID       string `json:"packageId"`
		Version         string `json:"version"`
		Volume          string `json:"volume"`
		InstallLocation string `json:"installLocation"`
		InstalledBy     string `json:"installedBy"`
		PackageFileName string `json:"packageFileName"`
		HasFiles        bool   `json:"hasFiles"`
	}
	mustRunJSON(t, &info, "receipts", "info", id, "--volume", volume)
	assert.Equal(t, id, info.PackageID)
	assert.Equal(t, "4.2.1", info.Version)
	assert.Equal(t, "usr/local", info.InstallLocation)
	assert.Equal(t, "installer", info.InstalledBy)
	assert.Equal(t, "example.pkg", info.PackageFileName)
	assert.True(t, info.HasFiles)
	assert.Equal(t, volume, info.Volume)

	// The paths come out relative to the install location, without the
	// "./" the bill of materials carries and without the root entry.
	files := nonEmptyLines(mustRun(t, "receipts", "files", id, "--volume", volume))
	assert.NotEmpty(t, files)
	for _, f := range files {
		assert.NotEqual(t, ".", f)
		assert.False(t, strings.HasPrefix(f, "./"), "%s should be relative to the install location", f)
	}
	// And the two halves partition the listing.
	onlyFiles := nonEmptyLines(mustRun(t, "receipts", "files", "--only-files", id, "--volume", volume))
	onlyDirs := nonEmptyLines(mustRun(t, "receipts", "files", "--only-dirs", id, "--volume", volume))
	assert.Len(t, files, len(onlyFiles)+len(onlyDirs))
}

// TestReceiptsRejectsWhatIsNotThere covers the errors: an unknown package,
// a volume with no receipt database, and an identifier trying to reach out
// of the directory.
func TestReceiptsRejectsWhatIsNotThere(t *testing.T) {
	volume, _ := fakeVolume(t)

	_, stderr, code := run(t, "receipts", "info", "no.such.package.exists", "--volume", volume)
	assert.Equal(t, 3, code, "an unknown package is a missing-package error")
	assert.Contains(t, stderr, "no.such.package.exists")

	_, stderr, code = run(t, "receipts", "list", "--volume", t.TempDir())
	assert.Equal(t, 3, code, "a volume with no receipt database is one too")
	assert.Contains(t, stderr, "var/db/receipts")

	// An identifier that tries to reach out of the directory is refused
	// rather than followed.
	_, _, code = run(t, "receipts", "info", filepath.Join("..", "..", "etc", "passwd"), "--volume", volume)
	assert.NotEqual(t, 0, code)
}

// receiptIDs lists the identifiers on this machine's boot volume.
func receiptIDs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, l := range nonEmptyLines(mustRun(t, "-o", "json", "receipts", "list")) {
		var e struct {
			PackageID string `json:"packageId"`
		}
		require.NoError(t, json.Unmarshal([]byte(l), &e))
		out = append(out, e.PackageID)
	}
	return out
}

// someReceipt picks one to compare, or skips where there is none.
func someReceipt(t *testing.T) string {
	t.Helper()
	ids := receiptIDs(t)
	if len(ids) == 0 {
		t.Skip("this machine has no receipts under /var/db/receipts")
	}
	return ids[0]
}

// someReceiptWithFiles picks one that installed something, since a
// scripts-only package leaves no bill of materials.
func someReceiptWithFiles(t *testing.T) string {
	t.Helper()
	for _, id := range receiptIDs(t) {
		var info struct {
			HasFiles bool `json:"hasFiles"`
		}
		mustRunJSON(t, &info, "receipts", "info", id)
		if info.HasFiles {
			return id
		}
	}
	t.Skip("no receipt on this machine carries a bill of materials")
	return ""
}
