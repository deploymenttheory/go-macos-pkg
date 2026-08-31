// Command tests for the read side: info, list, cat and inspect against the
// packages Apple's tools produced, and, where the tools exist, against what
// those tools say about the same packages.
package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/exitcode"
)

// infoJSON mirrors the parts of the info schema the tests assert on.
type infoJSON struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
	XAR  struct {
		ChecksumAlgorithm string `json:"checksumAlgorithm"`
		Entries           int    `json:"entries"`
		TOCDigestValid    bool   `json:"tocDigestValid"`
	} `json:"xar"`
	Packages []struct {
		Name                 string `json:"name"`
		Identifier           string `json:"identifier"`
		Version              string `json:"version"`
		InstallLocation      string `json:"installLocation"`
		MinimumSystemVersion string `json:"minimumSystemVersion"`
		Payload              *struct {
			Entry          string `json:"entry"`
			NumberOfFiles  int    `json:"numberOfFiles"`
			InstallKBytes  int    `json:"installKBytes"`
			Encoding       string `json:"encoding"`
			LargeSegmented bool   `json:"largeSegmented"`
		} `json:"payload"`
		Scripts []string `json:"scripts"`
		Bundles []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"bundles"`
	} `json:"packages"`
	Distribution *struct {
		Title             string   `json:"title"`
		HostArchitectures []string `json:"hostArchitectures"`
		Choices           []struct {
			ID string `json:"id"`
		} `json:"choices"`
		Resources []string `json:"resources"`
	} `json:"distribution"`
	Signature struct {
		Signed bool `json:"signed"`
	} `json:"signature"`
	Staple struct {
		Present bool `json:"present"`
	} `json:"staple"`
}

type listLine struct {
	Path      string `json:"path"`
	Type      string `json:"type"`
	Mode      string `json:"mode"`
	UID       int    `json:"uid"`
	GID       int    `json:"gid"`
	Size      int64  `json:"size"`
	Checksum  string `json:"checksum"`
	Target    string `json:"target"`
	Component string `json:"component"`
}

func listJSON(t *testing.T, args ...string) []listLine {
	t.Helper()
	stdout := mustRun(t, append([]string{"-o", "json", "list"}, args...)...)
	var lines []listLine
	for _, l := range nonEmptyLines(stdout) {
		var line listLine
		if err := json.Unmarshal([]byte(l), &line); err != nil {
			t.Fatalf("list printed invalid JSON line %q: %v", l, err)
		}
		lines = append(lines, line)
	}
	return lines
}

func TestInfoComponentMatchesPkgbuild(t *testing.T) {
	for _, name := range []string{"component-basic.pkg", "component-noscripts.pkg", "component-pbzx.pkg", "component-latest-26.0.pkg", "component-large-payload.pkg", "component-bundle.pkg"} {
		t.Run(name, func(t *testing.T) {
			path, want := fixture(t, name)
			var got infoJSON
			mustRunJSON(t, &got, "info", path)
			if got.Kind != "component" || len(got.Packages) != 1 {
				t.Fatalf("kind %q, %d packages", got.Kind, len(got.Packages))
			}
			c := got.Packages[0]
			if c.Identifier != want.Identifier || c.Version != want.Version || c.InstallLocation != want.InstallLocation {
				t.Errorf("identity = %s %s %s, pkgbuild wrote %s %s %s", c.Identifier, c.Version, c.InstallLocation, want.Identifier, want.Version, want.InstallLocation)
			}
			if c.Payload == nil {
				t.Fatal("no payload reported")
			}
			if c.Payload.NumberOfFiles != want.NumberOfFiles || c.Payload.InstallKBytes != want.InstallKBytes {
				t.Errorf("payload = %d files %d KB, pkgbuild wrote %d files %d KB", c.Payload.NumberOfFiles, c.Payload.InstallKBytes, want.NumberOfFiles, want.InstallKBytes)
			}
			if c.Payload.Encoding != want.PayloadEncoding {
				t.Errorf("payload encoding = %s, fixture is %s", c.Payload.Encoding, want.PayloadEncoding)
			}
			if !equalStrings(c.Scripts, want.Scripts) {
				t.Errorf("scripts = %v, want %v", c.Scripts, want.Scripts)
			}
			if !got.XAR.TOCDigestValid {
				t.Error("TOC digest reported invalid")
			}
			if got.Signature.Signed || got.Staple.Present {
				t.Error("unsigned fixture reported signed or stapled")
			}
			if name == "component-large-payload.pkg" && (c.Payload.Entry != "LargeSegmentedPayload" || !c.Payload.LargeSegmented) {
				t.Errorf("large payload: entry %q largeSegmented %v", c.Payload.Entry, c.Payload.LargeSegmented)
			}
			if name == "component-bundle.pkg" {
				if len(c.Bundles) != 1 || c.Bundles[0].ID != "com.deploymenttheory.fixture.app" {
					t.Errorf("bundles = %+v", c.Bundles)
				}
			}
			st, _ := os.Stat(path)
			if got.Size != st.Size() {
				t.Errorf("size = %d, file is %d", got.Size, st.Size())
			}
			attest(t, "%s: %d files, %d KB, %s payload", name, c.Payload.NumberOfFiles, c.Payload.InstallKBytes, c.Payload.Encoding)
		})
	}
}

func TestInfoText(t *testing.T) {
	path, want := fixture(t, "component-basic.pkg")
	stdout := mustRun(t, "info", path)
	for _, s := range []string{"component package", want.Identifier, want.Version, "preinstall, postinstall", "Signature:  none", "Staple:     none", "gzip-cpio"} {
		if !strings.Contains(stdout, s) {
			t.Errorf("info text lacks %q:\n%s", s, stdout)
		}
	}
}

func TestInfoProduct(t *testing.T) {
	path, want := fixture(t, "product-custom-dist.pkg")
	var got infoJSON
	mustRunJSON(t, &got, "info", path)
	if got.Kind != "product" || got.Distribution == nil {
		t.Fatalf("kind %q, distribution %v", got.Kind, got.Distribution != nil)
	}
	if got.Distribution.Title != want.Title {
		t.Errorf("title = %q, want %q", got.Distribution.Title, want.Title)
	}
	var choices []string
	for _, c := range got.Distribution.Choices {
		choices = append(choices, c.ID)
	}
	if !equalStrings(choices, want.Choices) {
		t.Errorf("choices = %v, want %v", choices, want.Choices)
	}
	if !equalStrings(got.Distribution.HostArchitectures, []string{"arm64", "x86_64"}) {
		t.Errorf("architectures = %v", got.Distribution.HostArchitectures)
	}
	var names []string
	for _, c := range got.Packages {
		names = append(names, c.Name)
	}
	var wantNames []string
	for n := range want.Components {
		wantNames = append(wantNames, n)
	}
	sort.Strings(wantNames)
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	if !equalStrings(sorted, wantNames) {
		t.Errorf("components = %v, want %v", names, wantNames)
	}
	// Components are listed in the order the Distribution references them.
	if len(names) == 2 && names[0] != "component-basic.pkg" {
		t.Errorf("component order = %v, Distribution lists component-basic.pkg first", names)
	}

	path, want = fixture(t, "product-basic.pkg")
	mustRunJSON(t, &got, "info", path)
	sort.Strings(got.Distribution.Resources)
	if !equalStrings(got.Distribution.Resources, want.Resources) {
		t.Errorf("resources = %v, want %v", got.Distribution.Resources, want.Resources)
	}
	stdout := mustRun(t, "info", path)
	if !strings.Contains(stdout, "product archive") || !strings.Contains(stdout, "Resource:") {
		t.Errorf("product info text:\n%s", stdout)
	}
}

func TestListMatchesBom(t *testing.T) {
	path, want := fixture(t, "component-basic.pkg")
	lines := listJSON(t, path)
	got := map[string]listLine{}
	for _, l := range lines {
		got[l.Path] = l
	}
	if len(got) != len(want.Files) {
		t.Errorf("list has %d entries, lsbom saw %d", len(got), len(want.Files))
	}
	for p, f := range want.Files {
		l, ok := got[p]
		if !ok {
			t.Errorf("%s missing from list", p)
			continue
		}
		if l.Mode != f.Mode || l.UID != f.UID || l.GID != f.GID {
			t.Errorf("%s = %s %d/%d, lsbom says %s %d/%d", p, l.Mode, l.UID, l.GID, f.Mode, f.UID, f.GID)
		}
		// The manifest's type is derived from the mode's type bits, which
		// is all lsbom prints. For AppleDouble ._ entries pkgbuild copies
		// the original's mode bits onto what is really a file, so only the
		// record type (ours) is meaningful there.
		if !isAppleDouble(p) && l.Type != f.Type {
			t.Errorf("%s type = %s, lsbom says %s", p, l.Type, f.Type)
		}
		if f.Type == "file" && l.Size != f.Size {
			t.Errorf("%s size = %d, lsbom says %d", p, l.Size, f.Size)
		}
		if f.Type == "link" && l.Target != f.Target {
			t.Errorf("%s target = %q, lsbom says %q", p, l.Target, f.Target)
		}
	}
	// Text output: one path per line, symlinks as "path -> target".
	stdout := mustRun(t, "list", path)
	text := nonEmptyLines(stdout)
	if len(text) != len(want.Files) {
		t.Errorf("text list has %d lines, want %d", len(text), len(want.Files))
	}
	found := false
	for _, line := range text {
		if line == "./usr/local/fixture/link -> hello.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("symlink line missing from:\n%s", stdout)
	}
	long := mustRun(t, "list", "-l", path)
	if !strings.Contains(long, "-rwxr-xr-x 0/0") || !strings.Contains(long, "./usr/local/fixture/bin/tool") {
		t.Errorf("long listing:\n%s", long)
	}
}

func TestListProductComponents(t *testing.T) {
	path, want := fixture(t, "product-basic.pkg")
	lines := listJSON(t, path)
	seen := map[string]int{}
	for _, l := range lines {
		if l.Component == "" {
			t.Fatal("product listing lines must name their component")
		}
		seen[l.Component]++
	}
	for name, c := range want.Components {
		if seen[name] != c.NumberOfFiles {
			t.Errorf("%s: listed %d files, PackageInfo says %d", name, seen[name], c.NumberOfFiles)
		}
	}
	only := listJSON(t, "--component", "component-basic.pkg", path)
	for _, l := range only {
		if l.Component != "component-basic.pkg" {
			t.Fatalf("--component leaked %s", l.Component)
		}
	}
	if len(only) != seen["component-basic.pkg"] {
		t.Errorf("--component listed %d, want %d", len(only), seen["component-basic.pkg"])
	}
	_, stderr, code := run(t, "list", "--component", "nope.pkg", path)
	if code != exitcode.Usage {
		t.Errorf("unknown component: exit %d, want %d\n%s", code, exitcode.Usage, stderr)
	}
}

func TestListArchiveAndScripts(t *testing.T) {
	path, want := fixture(t, "product-basic.pkg")
	stdout := mustRun(t, "list", "--archive", path)
	var entries []string
	for _, l := range nonEmptyLines(stdout) {
		entries = append(entries, strings.TrimSuffix(l, "/"))
	}
	sort.Strings(entries)
	if !equalStrings(entries, want.Entries) {
		t.Errorf("--archive = %v\nxar -tf = %v", entries, want.Entries)
	}

	path, want = fixture(t, "component-basic.pkg")
	stdout = mustRun(t, "list", "--scripts", path)
	var scripts []string
	for _, l := range nonEmptyLines(stdout) {
		if !strings.HasPrefix(filepath.Base(l), "._") {
			scripts = append(scripts, l)
		}
	}
	if !equalStrings(scripts, []string{".", "./postinstall", "./preinstall"}) {
		t.Errorf("--scripts = %v", scripts)
	}
}

func TestCat(t *testing.T) {
	path, want := fixture(t, "component-basic.pkg")
	stdout := mustRun(t, "cat", path, "PackageInfo")
	if !strings.HasPrefix(stdout, "<?xml") || !strings.Contains(stdout, `identifier="`+want.Identifier+`"`) {
		t.Errorf("cat PackageInfo:\n%s", stdout)
	}
	if got := mustRun(t, "cat", path, "--payload", "./usr/local/fixture/hello.txt"); got != "hello, world\n" {
		t.Errorf("cat --payload = %q", got)
	}
	if got := mustRun(t, "cat", path, "--payload", "usr/local/fixture/hello.txt"); got != "hello, world\n" {
		t.Errorf("cat --payload without ./ = %q", got)
	}
	_, _, code := run(t, "cat", path, "NoSuchEntry")
	if code != exitcode.BadPackage {
		t.Errorf("missing entry: exit %d, want %d", code, exitcode.BadPackage)
	}
	_, _, code = run(t, "cat", path, "--payload", "./nope")
	if code != exitcode.BadPackage {
		t.Errorf("missing payload file: exit %d, want %d", code, exitcode.BadPackage)
	}

	path, _ = fixture(t, "product-custom-dist.pkg")
	if got := mustRun(t, "cat", path, "Distribution"); !strings.Contains(got, "<installer-gui-script") {
		t.Errorf("cat Distribution:\n%s", got)
	}
	if got := mustRun(t, "cat", path, "component-noscripts.pkg/PackageInfo"); !strings.Contains(got, "fixture.noscripts") {
		t.Errorf("cat nested PackageInfo:\n%s", got)
	}
	_, _, code = run(t, "cat", path, "--payload", "./usr/local/fixture/hello.txt")
	if code != exitcode.Usage {
		t.Errorf("--payload on a product without --component: exit %d, want %d", code, exitcode.Usage)
	}
	if got := mustRun(t, "cat", path, "--component", "component-basic.pkg", "--payload", "./usr/local/fixture/hello.txt"); got != "hello, world\n" {
		t.Errorf("cat --component --payload = %q", got)
	}

	path, _ = fixture(t, "component-pbzx.pkg")
	stdout = mustRun(t, "cat", path, "--payload", "./usr/local/fixture/huge.bin")
	if len(stdout) != 20<<20 {
		t.Errorf("pbzx payload file = %d bytes, want %d", len(stdout), 20<<20)
	}
}

func TestInspect(t *testing.T) {
	path, want := fixture(t, "component-basic.pkg")
	toc := mustRun(t, "inspect", path, "toc")
	if !strings.Contains(toc, "<toc>") || !strings.Contains(toc, `<checksum style="`) {
		t.Errorf("inspect toc:\n%s", toc)
	}
	header := mustRun(t, "inspect", path, "header")
	if !strings.Contains(header, "magic:") || !strings.Contains(header, "toc digest:") {
		t.Errorf("inspect header:\n%s", header)
	}
	pi := mustRun(t, "inspect", path, "packageinfo")
	if !strings.Contains(pi, want.Identifier) {
		t.Errorf("inspect packageinfo:\n%s", pi)
	}
	if got := mustRun(t, "inspect", path, "signature"); strings.TrimSpace(got) != "unsigned" {
		t.Errorf("inspect signature = %q", got)
	}
	_, _, code := run(t, "inspect", path, "distribution")
	if code != exitcode.BadPackage {
		t.Errorf("inspect distribution on a component: exit %d", code)
	}
	_, _, code = run(t, "inspect", path, "ticket")
	if code != exitcode.Signature {
		t.Errorf("inspect ticket on an unstapled package: exit %d, want %d", code, exitcode.Signature)
	}
	_, _, code = run(t, "inspect", path, "frobnicate")
	if code != exitcode.Usage {
		t.Errorf("unknown verb: exit %d", code)
	}

	// The plain xar is not a package, but header and toc still work on it.
	plain := filepath.Join(repoRoot, "testdata", "xar", "plain.xar")
	if _, err := os.Stat(plain); err == nil {
		mustRun(t, "inspect", plain, "header")
		_, _, code := run(t, "inspect", plain, "packageinfo")
		if code != exitcode.BadPackage {
			t.Errorf("packageinfo on a plain xar: exit %d", code)
		}
	}
}

// TestInspectBomMatchesLsbom is an oracle test: our bill-of-materials
// reader against Apple's lsbom on the same Bom.
func TestInspectBomMatchesLsbom(t *testing.T) {
	requireTools(t, "lsbom", "pkgutil")
	path, _ := fixture(t, "component-basic.pkg")
	expanded := filepath.Join(t.TempDir(), "expanded")
	hostTool(t, "pkgutil", "--expand", path, expanded)
	want := hostTool(t, "lsbom", "-p", "fmugsc", filepath.Join(expanded, "Bom"))

	got := mustRun(t, "inspect", path, "bom")
	// lsbom prints "path mode uid gid size crc"; ours prints uid/gid in
	// one column. Normalize both to the same shape before comparing.
	norm := func(s string, ours bool) []string {
		var out []string
		for _, line := range nonEmptyLines(s) {
			cols := strings.Split(line, "\t")
			if ours && len(cols) >= 3 {
				ug := strings.SplitN(cols[2], "/", 2)
				cols = append(append(append([]string{}, cols[:2]...), ug...), cols[3:]...)
			}
			// Drop empty trailing columns lsbom prints for directories.
			for len(cols) > 0 && cols[len(cols)-1] == "" {
				cols = cols[:len(cols)-1]
			}
			// A symlink line ends with the target in ours; lsbom -p has no
			// link column here, so drop ours for comparison.
			if len(cols) == 7 {
				cols = cols[:6]
			}
			out = append(out, strings.Join(cols, "\t"))
		}
		sort.Strings(out)
		return out
	}
	w, g := norm(want, false), norm(got, true)
	if !equalStrings(w, g) {
		t.Errorf("bom differs from lsbom\nlsbom:\n%s\nours:\n%s", strings.Join(w, "\n"), strings.Join(g, "\n"))
	}
	attest(t, "inspect bom agrees with lsbom on %d entries", len(w))
}

// TestListArchiveMatchesXar is an oracle test against xar -tf.
func TestListArchiveMatchesXar(t *testing.T) {
	requireTools(t, "xar")
	path, _ := fixture(t, "product-basic.pkg")
	want := nonEmptyLines(hostTool(t, "xar", "-tf", path))
	var got []string
	for _, l := range nonEmptyLines(mustRun(t, "list", "--archive", path)) {
		got = append(got, strings.TrimSuffix(l, "/"))
	}
	sort.Strings(want)
	sort.Strings(got)
	if !equalStrings(want, got) {
		t.Errorf("--archive = %v\nxar -tf = %v", got, want)
	}
}

// TestPayloadMatchesPkgutil compares every payload file against pkgutil
// --expand-full's extraction of the same package.
func TestPayloadMatchesPkgutil(t *testing.T) {
	requireTools(t, "pkgutil")
	path, want := fixture(t, "component-basic.pkg")
	expanded := filepath.Join(t.TempDir(), "full")
	hostTool(t, "pkgutil", "--expand-full", path, expanded)
	checked := 0
	for p, f := range want.Files {
		// pkgutil folds ._ AppleDouble entries back into extended
		// attributes rather than writing them as files.
		if f.Type != "file" || isAppleDouble(p) {
			continue
		}
		theirs, err := os.ReadFile(filepath.Join(expanded, "Payload", p))
		if err != nil {
			t.Errorf("pkgutil did not extract %s: %v", p, err)
			continue
		}
		ours := mustRun(t, "cat", path, "--payload", p)
		if ours != string(theirs) {
			t.Errorf("%s differs from pkgutil's extraction (%d vs %d bytes)", p, len(ours), len(theirs))
		}
		checked++
	}
	attest(t, "%d payload files byte-identical to pkgutil --expand-full", checked)
}

func TestNotAPackageExit3(t *testing.T) {
	for _, path := range []string{
		filepath.Join(repoRoot, "go.mod"),
		filepath.Join(repoRoot, "testdata", "xar", "plain.xar"),
		filepath.Join(repoRoot, "does-not-exist.pkg"),
	} {
		if strings.Contains(path, "plain.xar") {
			if _, err := os.Stat(path); err != nil {
				continue
			}
		}
		for _, cmd := range []string{"info", "list"} {
			_, stderr, code := run(t, cmd, path)
			if code != exitcode.BadPackage {
				t.Errorf("%s %s: exit %d, want %d\n%s", cmd, filepath.Base(path), code, exitcode.BadPackage, stderr)
			}
		}
	}
}

func TestOutputFlagValidation(t *testing.T) {
	path, _ := fixture(t, "component-basic.pkg")
	_, stderr, code := run(t, "info", "-o", "xml", path)
	if code != exitcode.Usage || !strings.Contains(stderr, "--output") {
		t.Errorf("-o xml: exit %d\n%s", code, stderr)
	}
	// The environment variable form is honoured, and the flag beats it.
	stdout, _, code := runEnv(t, []string{"MACOSPKG_OUTPUT=json"}, "info", path)
	if code != 0 || !strings.HasPrefix(stdout, "{") {
		t.Errorf("MACOSPKG_OUTPUT=json ignored: exit %d\n%s", code, stdout)
	}
	stdout, _, _ = runEnv(t, []string{"MACOSPKG_OUTPUT=json"}, "info", "-o", "text", path)
	if strings.HasPrefix(stdout, "{") {
		t.Error("flag did not override MACOSPKG_OUTPUT")
	}
	_, _, code = run(t, "info")
	if code != exitcode.Usage {
		t.Errorf("info without args: exit %d", code)
	}
}

// isAppleDouble reports whether a payload path is a ._ sidecar that
// pkgbuild writes for a file carrying extended attributes.
func isAppleDouble(p string) bool {
	return strings.HasPrefix(filepath.Base(p), "._")
}

func equalStrings(a, b []string) bool {
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
