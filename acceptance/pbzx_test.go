package acceptance

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/exitcode"
)

// pbzxArgs builds the acceptance tree with a pbzx payload.
func pbzxArgs(root, scripts, out string, extra ...string) []string {
	return append(append(buildArgs(root, scripts, out), "--compression", "pbzx"), extra...)
}

// TestBuildPBZX builds with --compression pbzx on every platform and
// checks the package reads back, extracts, verifies and reproduces.
func TestBuildPBZX(t *testing.T) {
	root, scripts := sourceTree(t)
	out := filepath.Join(t.TempDir(), "out.pkg")
	var rep buildJSON
	mustRunJSON(t, &rep, pbzxArgs(root, scripts, out)...)

	var info infoJSON
	mustRunJSON(t, &info, "info", out)
	c := info.Packages[0]
	if c.Payload.Encoding != "pbzx-cpio" {
		t.Errorf("encoding = %s, want pbzx-cpio", c.Payload.Encoding)
	}
	if c.MinimumSystemVersion != "12.0" {
		t.Errorf("minimum system version = %q, want 12.0 (pkgbuild's floor for --compression latest)", c.MinimumSystemVersion)
	}
	if c.Payload.NumberOfFiles != rep.NumberOfFiles {
		t.Errorf("info %d files, build reported %d", c.Payload.NumberOfFiles, rep.NumberOfFiles)
	}

	// The container itself: pbzx magic, pkgbuild's 16 MiB block size, and
	// the Scripts archive still gzip.
	payload := mustRun(t, "cat", out, "Payload")
	if !strings.HasPrefix(payload, "pbzx") {
		t.Fatalf("Payload starts %x, want pbzx", payload[:4])
	}
	if bs := binary.BigEndian.Uint64([]byte(payload[4:12])); bs != 16<<20 {
		t.Errorf("block size = %d, want %d", bs, 16<<20)
	}
	if sc := mustRun(t, "cat", out, "Scripts"); !strings.HasPrefix(sc, "\x1f\x8b") {
		t.Errorf("Scripts starts %x, want gzip", sc[:2])
	}

	dir := filepath.Join(t.TempDir(), "x")
	mustRun(t, "extract", "--verify", out, dir)
	for _, rel := range []string{"usr/local/fixture/hello.txt", "usr/local/fixture/big.bin", "usr/local/fixture/sub/nested/deep.txt"} {
		a, _ := fileSHA256(filepath.Join(root, filepath.FromSlash(rel)))
		b, err := fileSHA256(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || a != b {
			t.Errorf("%s: extracted %s, source %s (%v)", rel, b, a, err)
		}
	}

	// Reproducible, like the gzip build.
	again := filepath.Join(t.TempDir(), "again.pkg")
	mustRun(t, pbzxArgs(root, scripts, again)...)
	a, _ := os.ReadFile(out)
	b, _ := os.ReadFile(again)
	if !bytes.Equal(a, b) {
		t.Error("two pbzx builds of the same tree differ")
	}

	// A higher minimum is kept; a lower one is refused, as pkgbuild does.
	higher := filepath.Join(t.TempDir(), "higher.pkg")
	var rep2 buildJSON
	mustRunJSON(t, &rep2, pbzxArgs(root, scripts, higher, "--min-os-version", "14.0")...)
	if rep2.MinimumSystemVersion != "14.0" {
		t.Errorf("minimum system version = %q, want 14.0", rep2.MinimumSystemVersion)
	}
	_, stderr, code := run(t, pbzxArgs(root, scripts, filepath.Join(t.TempDir(), "old.pkg"), "--min-os-version", "11.0")...)
	if code != exitcode.Usage || !strings.Contains(stderr, "12.0") {
		t.Errorf("--min-os-version 11.0 with pbzx: exit %d, stderr %q", code, stderr)
	}
	_, _, code = run(t, append(buildArgs(root, scripts, filepath.Join(t.TempDir(), "bad.pkg")), "--compression", "bzip2")...)
	if code != exitcode.Usage {
		t.Errorf("--compression bzip2: exit %d", code)
	}
	attest(t, "built pbzx package: %d files, sha256 %s", rep.NumberOfFiles, rep.SHA256[:12])
}

// TestBuildPBZXFromManifest takes the compression from a build manifest.
func TestBuildPBZXFromManifest(t *testing.T) {
	root, scripts := sourceTree(t)
	m := filepath.Join(t.TempDir(), "build-info.yaml")
	os.WriteFile(m, []byte("identifier: com.deploymenttheory.manifest\nversion: 2.0\ncompression: latest\n"), 0o644)
	out := filepath.Join(t.TempDir(), "out.pkg")
	args := []string{"build", root, out, "--manifest", m, "--scripts", scripts}
	if runtime.GOOS == "windows" {
		args = append(args, "--executable", `bin/tool$`)
	}
	var rep buildJSON
	mustRunJSON(t, &rep, args...)
	if rep.PayloadEncoding != "pbzx-cpio" || rep.MinimumSystemVersion != "12.0" {
		t.Errorf("report: encoding %s, min OS %s", rep.PayloadEncoding, rep.MinimumSystemVersion)
	}
}

// TestPBZXFixturesMatchPkgbuild reads every pkgbuild --compression latest
// fixture and checks the container's parameters against what the
// generator recorded.
func TestPBZXFixturesMatchPkgbuild(t *testing.T) {
	m := manifest
	n := 0
	for name, want := range m.Packages {
		if !strings.HasPrefix(name, "component-latest-") && name != "component-pbzx.pkg" {
			continue
		}
		n++
		t.Run(name, func(t *testing.T) {
			path, _ := fixture(t, name)
			var info infoJSON
			mustRunJSON(t, &info, "info", path)
			if got := info.Packages[0].Payload.Encoding; got != want.PayloadEncoding {
				t.Errorf("encoding %s, fixture is %s", got, want.PayloadEncoding)
			}
			payload := mustRun(t, "cat", path, "Payload")
			if bs := binary.BigEndian.Uint64([]byte(payload[4:12])); bs != want.PayloadBlockSize {
				t.Errorf("block size %d, fixture is %d", bs, want.PayloadBlockSize)
			}
			if chunks := countPBZChunks([]byte(payload)); chunks != want.PayloadChunks {
				t.Errorf("%d chunks, fixture is %d", chunks, want.PayloadChunks)
			}
			if want.ScriptsEncoding != "" {
				sc := mustRun(t, "cat", path, "Scripts")
				if want.ScriptsEncoding == "gzip-cpio" && !strings.HasPrefix(sc, "\x1f\x8b") {
					t.Errorf("Scripts starts %x, fixture says gzip", sc[:2])
				}
			}
			dir := filepath.Join(t.TempDir(), "x")
			mustRun(t, "extract", "--verify", path, dir)
		})
	}
	if n == 0 {
		t.Fatal("no pbzx fixtures in the manifest")
	}
	for ver, enc := range m.Generator.CompressionLatest {
		if enc != "pbzx-cpio" {
			t.Errorf("pkgbuild --compression latest --min-os-version %s wrote %s: update flatpkg.CompressionLatest", ver, enc)
		}
	}
}

func sortedLines(s string) string {
	lines := nonEmptyLines(s)
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// countPBZChunks walks a pbz* container's chunk headers.
func countPBZChunks(b []byte) int {
	n := 0
	for off := 12; off+16 <= len(b); {
		deflated := binary.BigEndian.Uint64(b[off+8 : off+16])
		off += 16 + int(deflated)
		n++
	}
	return n
}

// --- oracle tests -------------------------------------------------------

// TestBuildPBZXParityWithPkgbuild builds the same tree with pkgbuild
// --compression latest and compares the containers and Apple's readings
// of both.
func TestBuildPBZXParityWithPkgbuild(t *testing.T) {
	requireTools(t, "pkgbuild", "pkgutil", "lsbom", "xar")
	root, scripts := sourceTree(t)
	hostTool(t, "sh", "-c", `find "$1" "$2" -exec touch -h -t 202401020304.05 {} +`, "sh", root, scripts)

	ours := filepath.Join(t.TempDir(), "ours.pkg")
	theirs := filepath.Join(t.TempDir(), "theirs.pkg")
	mustRun(t, append(parityBuildArgs(root, scripts, ours), "--compression", "pbzx")...)
	hostTool(t, "pkgbuild", "--quiet", "--root", root, "--identifier", "com.deploymenttheory.acceptance", "--version", "1.0.0", "--scripts", scripts, "--ownership", "recommended", "--compression", "latest", "--min-os-version", "12.0", theirs)

	oursPayload := []byte(mustRun(t, "cat", ours, "Payload"))
	theirsPayload := []byte(mustRun(t, "cat", theirs, "Payload"))
	if !bytes.Equal(oursPayload[:12], theirsPayload[:12]) {
		t.Errorf("container header ours %x, pkgbuild %x", oursPayload[:12], theirsPayload[:12])
	}
	if a, b := countPBZChunks(oursPayload), countPBZChunks(theirsPayload); a != b {
		t.Errorf("chunks ours %d, pkgbuild %d", a, b)
	}
	// Every chunk is one xz stream with no integrity check.
	for _, p := range [][]byte{oursPayload, theirsPayload} {
		if !bytes.HasPrefix(p[28:], []byte{0xfd, '7', 'z', 'X', 'Z', 0, 0, 0}) {
			t.Errorf("first chunk starts %x, want an xz stream with check none", p[28:36])
		}
	}

	// What pkgutil sees when it unpacks each payload.
	oursDir := filepath.Join(t.TempDir(), "ours")
	theirsDir := filepath.Join(t.TempDir(), "theirs")
	hostTool(t, "pkgutil", "--expand-full", ours, oursDir)
	hostTool(t, "pkgutil", "--expand-full", theirs, theirsDir)
	oursList := hostTool(t, "sh", "-c", `cd "$1" && find Payload | LC_ALL=C sort`, "sh", oursDir)
	theirsList := hostTool(t, "sh", "-c", `cd "$1" && find Payload | LC_ALL=C sort`, "sh", theirsDir)
	if oursList != theirsList {
		t.Errorf("pkgutil --expand-full trees differ:\nours:\n%s\ntheirs:\n%s", oursList, theirsList)
	}
	for _, rel := range []string{"Payload/usr/local/fixture/hello.txt", "Payload/usr/local/fixture/big.bin"} {
		a, _ := fileSHA256(filepath.Join(oursDir, rel))
		b, _ := fileSHA256(filepath.Join(theirsDir, rel))
		if a != b {
			t.Errorf("%s differs after pkgutil --expand-full", rel)
		}
	}
	// pkgbuild writes the cpio in readdir order and we sort, so compare
	// the sets.
	oursFiles := sortedLines(hostTool(t, "pkgutil", "--payload-files", ours))
	theirsFiles := sortedLines(hostTool(t, "pkgutil", "--payload-files", theirs))
	if oursFiles != theirsFiles {
		t.Errorf("pkgutil --payload-files differ:\nours:\n%s\ntheirs:\n%s", oursFiles, theirsFiles)
	}
	attest(t, "pbzx payload: ours %d bytes in %d chunks, pkgbuild %d bytes in %d chunks", len(oursPayload), countPBZChunks(oursPayload), len(theirsPayload), countPBZChunks(theirsPayload))
}

// TestInstallerInstallsOurPBZXPackage is the end-to-end proof for pbzx:
// Apple's installer installs it.
func TestInstallerInstallsOurPBZXPackage(t *testing.T) {
	requireTools(t, "installer", "hdiutil", "sudo", "pkgutil")
	requireInstallerOptIn(t)
	root, scripts := sourceTree(t)
	ours := filepath.Join(t.TempDir(), "ours.pkg")
	mustRun(t, pbzxArgs(root, scripts, ours)...)

	dmg := filepath.Join(t.TempDir(), "target.dmg")
	hostTool(t, "hdiutil", "create", "-quiet", "-size", "64m", "-fs", "HFS+", "-volname", "MacospkgPBZX", dmg)
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
	pkgs := hostTool(t, "pkgutil", "--volume", mount, "--pkgs")
	if !strings.Contains(pkgs, "com.deploymenttheory.acceptance") {
		t.Errorf("receipt not recorded:\n%s", pkgs)
	}
	attest(t, "installer installed our pbzx package onto %s", mount)
}
