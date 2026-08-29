// Round-trip tests: a real, published package is expanded with macospkg,
// rebuilt with macospkg from what came out, and the two are compared
// entry for entry. This is the writers' hardest test: every rule
// pkgbuild and productbuild followed must be reproduced for the rebuilt
// package to match — and on macOS, Apple's own readers judge both.
package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// roundTrip expands the real package and rebuilds it, returning the
// expanded directory, the rebuilt product archive, the component names,
// and the rebuilt component packages. The rebuilt component is given the original's identity; modes
// are taken from the extracted tree, except that the original's
// executables are named explicitly so hosts without execute bits
// (Windows) rebuild the same package as everyone else.
func roundTrip(t *testing.T) (expanded, rebuilt string, components, packages []string) {
	t.Helper()
	pkg := realPackage(t)
	base := t.TempDir()
	expanded = filepath.Join(base, "expanded")
	mustRun(t, "expand", "--full", "--verify", pkg, expanded)

	var info infoJSON
	mustRunJSON(t, &info, "info", pkg)
	if info.Kind != "product" {
		t.Skipf("%s is a %s; the round trip expects a product archive", filepath.Base(pkg), info.Kind)
	}
	rebuiltDir := filepath.Join(base, "rebuilt")
	os.MkdirAll(rebuiltDir, 0o755)
	for _, c := range info.Packages {
		compDir := filepath.Join(expanded, filepath.FromSlash(c.Name))
		out := filepath.Join(rebuiltDir, c.Name) // the archive directory name must match the Distribution's #ref
		args := []string{"build", filepath.Join(compDir, "Payload"), out, "--identifier", c.Identifier, "--version", c.Version}
		if c.InstallLocation != "" {
			args = append(args, "--install-location", c.InstallLocation)
		}
		if st, err := os.Stat(filepath.Join(compDir, "Scripts")); err == nil && st.IsDir() {
			args = append(args, "--scripts", filepath.Join(compDir, "Scripts"))
		}
		if pat := executablePattern(t, pkg, c.Name); pat != "" {
			args = append(args, "--executable", pat)
		}
		mustRun(t, args...)
		packages = append(packages, out)
		components = append(components, c.Name)
	}
	rebuilt = filepath.Join(rebuiltDir, "rebuilt.pkg")
	args := []string{"product", rebuilt, "--distribution", filepath.Join(expanded, "Distribution")}
	if st, err := os.Stat(filepath.Join(expanded, "Resources")); err == nil && st.IsDir() {
		args = append(args, "--resources", filepath.Join(expanded, "Resources"))
	}
	for _, p := range packages {
		args = append(args, "--package", p)
	}
	mustRun(t, args...)
	return expanded, rebuilt, components, packages
}

// executablePattern builds an anchored alternation of the original's
// executable payload paths for --executable.
func executablePattern(t *testing.T, pkg, component string) string {
	t.Helper()
	var paths []string
	for _, l := range listJSON(t, "--component", component, pkg) {
		if l.Type == "file" && strings.HasSuffix(l.Mode, "55") || l.Type == "file" && strings.Contains(l.Mode, "7") {
			paths = append(paths, regexp.QuoteMeta(l.Path))
		}
	}
	if len(paths) == 0 {
		return ""
	}
	return "^(" + strings.Join(paths, "|") + ")$"
}

func TestRealPackageRoundTrip(t *testing.T) {
	pkg := realPackage(t)
	expanded, rebuilt, components, _ := roundTrip(t)

	// Identity and payload numbers, as PackageInfo records them.
	var orig, ours infoJSON
	mustRunJSON(t, &orig, "info", pkg)
	mustRunJSON(t, &ours, "info", rebuilt)
	if ours.Kind != "product" || len(ours.Packages) != len(orig.Packages) {
		t.Fatalf("rebuilt: kind %s, %d components (original %d)", ours.Kind, len(ours.Packages), len(orig.Packages))
	}
	for i := range orig.Packages {
		o, r := orig.Packages[i], ours.Packages[i]
		if o.Name != r.Name || o.Identifier != r.Identifier || o.Version != r.Version {
			t.Errorf("component %d identity: %s %s %s vs %s %s %s", i, r.Name, r.Identifier, r.Version, o.Name, o.Identifier, o.Version)
		}
		if o.Payload.NumberOfFiles != r.Payload.NumberOfFiles {
			t.Errorf("%s numberOfFiles: rebuilt %d, original %d", o.Name, r.Payload.NumberOfFiles, o.Payload.NumberOfFiles)
		}
		if o.Payload.InstallKBytes != r.Payload.InstallKBytes {
			t.Errorf("%s installKBytes: rebuilt %d, original %d", o.Name, r.Payload.InstallKBytes, o.Payload.InstallKBytes)
		}
		if !equalStrings(o.Scripts, r.Scripts) {
			t.Errorf("%s scripts: rebuilt %v, original %v", o.Name, r.Scripts, o.Scripts)
		}
		if o.Payload.Encoding != r.Payload.Encoding {
			t.Errorf("%s payload encoding: rebuilt %s, original %s", o.Name, r.Payload.Encoding, o.Payload.Encoding)
		}
		attest(t, "%s: %d files, %d KB installed — rebuilt identically", o.Name, r.Payload.NumberOfFiles, r.Payload.InstallKBytes)
	}
	if orig.Distribution.Title != ours.Distribution.Title || !equalStrings(orig.Distribution.Resources, ours.Distribution.Resources) {
		t.Errorf("distribution: rebuilt %+v, original %+v", ours.Distribution, orig.Distribution)
	}

	// Distribution and resources byte for byte.
	if a, b := mustRun(t, "cat", pkg, "Distribution"), mustRun(t, "cat", rebuilt, "Distribution"); a != b {
		t.Error("Distribution differs after the round trip")
	}
	for _, res := range orig.Distribution.Resources {
		if a, b := mustRun(t, "cat", pkg, res), mustRun(t, "cat", rebuilt, res); a != b {
			t.Errorf("%s differs after the round trip", res)
		}
	}
	// The archive holds the same entries.
	entries := func(p string) []string {
		var out []string
		for _, l := range nonEmptyLines(mustRun(t, "list", "--archive", p)) {
			out = append(out, strings.TrimSuffix(l, "/"))
		}
		sort.Strings(out)
		return out
	}
	if a, b := entries(pkg), entries(rebuilt); !equalStrings(a, b) {
		t.Errorf("archive entries: rebuilt %v, original %v", b, a)
	}

	// Every bill-of-materials entry: type, mode, owner, size, checksum,
	// link target and modification time.
	for _, comp := range components {
		o := listJSON(t, "--component", comp, pkg)
		r := listJSON(t, "--component", comp, rebuilt)
		byPath := map[string]listLine{}
		for _, l := range r {
			byPath[l.Path] = l
		}
		if len(o) != len(r) {
			t.Errorf("%s: rebuilt bom has %d entries, original %d", comp, len(r), len(o))
		}
		mismatches := 0
		for _, ol := range o {
			rl, ok := byPath[ol.Path]
			if !ok {
				t.Errorf("%s: %s missing from the rebuilt package", comp, ol.Path)
				continue
			}
			if ol.Type != rl.Type || ol.Mode != rl.Mode || ol.UID != rl.UID || ol.GID != rl.GID || ol.Size != rl.Size || ol.Checksum != rl.Checksum || ol.Target != rl.Target {
				if mismatches < 20 {
					t.Errorf("%s: %s\n  original: %+v\n  rebuilt:  %+v", comp, ol.Path, ol, rl)
				}
				mismatches++
			}
		}
		if mismatches > 20 {
			t.Errorf("%s: %d entries differ in total", comp, mismatches)
		}
		attest(t, "%s: %d bill-of-materials entries identical (type, mode, owner, size, checksum)", comp, len(o)-mismatches)
	}

	// Payload bytes: extract the rebuilt package and compare with what
	// came out of the original.
	for _, comp := range components {
		dir := filepath.Join(t.TempDir(), "x")
		mustRun(t, "extract", "--verify", "--component", comp, rebuilt, dir)
		src := filepath.Join(expanded, filepath.FromSlash(comp), "Payload")
		checked := 0
		err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
			if err != nil || !fi.Mode().IsRegular() {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			a, _ := fileSHA256(p)
			b, err := fileSHA256(filepath.Join(dir, rel))
			if err != nil || a != b {
				t.Errorf("%s: %s differs after the round trip (%v)", comp, rel, err)
			}
			checked++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		attest(t, "%s: %d payload files byte-identical after the round trip", comp, checked)
	}
}

// TestRealPackageRoundTripApple lets Apple's readers compare the two.
func TestRealPackageRoundTripApple(t *testing.T) {
	requireTools(t, "pkgutil", "lsbom", "xar")
	pkg := realPackage(t)
	_, rebuilt, components, _ := roundTrip(t)

	pf := func(p string) []string {
		out := nonEmptyLines(hostTool(t, "pkgutil", "--payload-files", p))
		sort.Strings(out)
		return out
	}
	if a, b := pf(pkg), pf(rebuilt); !equalStrings(a, b) {
		t.Errorf("pkgutil --payload-files: %d entries vs %d", len(a), len(b))
	}
	oe := filepath.Join(t.TempDir(), "orig")
	re := filepath.Join(t.TempDir(), "rebuilt")
	hostTool(t, "pkgutil", "--expand", pkg, oe)
	hostTool(t, "pkgutil", "--expand", rebuilt, re)
	for _, comp := range components {
		a := nonEmptyLines(hostTool(t, "lsbom", "-p", "fmugsc", filepath.Join(oe, comp, "Bom")))
		b := nonEmptyLines(hostTool(t, "lsbom", "-p", "fmugsc", filepath.Join(re, comp, "Bom")))
		sort.Strings(a)
		sort.Strings(b)
		if !equalStrings(a, b) {
			diff := 0
			for i := range a {
				if i < len(b) && a[i] != b[i] && diff < 10 {
					t.Errorf("lsbom differs:\n  original: %s\n  rebuilt:  %s", a[i], b[i])
					diff++
				}
			}
			if len(a) != len(b) {
				t.Errorf("lsbom: %d entries vs %d", len(a), len(b))
			}
		} else {
			attest(t, "%s: lsbom lists %d identical entries for the original and the rebuilt Bom", comp, len(a))
		}
	}
	xdir := t.TempDir()
	cmd := exec.Command("xar", "-xf", rebuilt)
	cmd.Dir = xdir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("xar -x on the rebuilt package: %v\n%s", err, out)
	}
}

// installedPath maps a payload path to where installer puts it on a
// target volume: /etc, /var and /tmp are symlinks into /private on a
// boot volume, and installer writes under private/ on any volume rather
// than creating the symlinks.
func installedPath(rel string) string {
	slash := filepath.ToSlash(rel)
	for _, top := range []string{"etc/", "var/", "tmp/"} {
		if strings.HasPrefix(slash, top) || slash == strings.TrimSuffix(top, "/") {
			return filepath.FromSlash("private/" + slash)
		}
	}
	return rel
}

// TestRealPackageRoundTripInstalls installs the rebuilt component
// packages with macOS's installer onto a scratch volume and compares the
// result with the original payload. The components, not the product
// archive: a Distribution decides where it may be installed (Go's allows
// the boot volume only) and runs its own checks, whereas a component
// package installs wherever installer is pointed.
func TestRealPackageRoundTripInstalls(t *testing.T) {
	requireTools(t, "installer", "hdiutil", "sudo")
	if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
		t.Skip("passwordless sudo is not available; installer needs root")
	}
	expanded, _, components, packages := roundTrip(t)

	dmg := filepath.Join(t.TempDir(), "target.dmg")
	hostTool(t, "hdiutil", "create", "-quiet", "-size", "1g", "-fs", "HFS+", "-volname", "MacospkgRoundTrip", dmg)
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

	for _, p := range packages {
		out, err := exec.Command("sudo", "-n", "installer", "-pkg", p, "-target", mount, "-verboseR").CombinedOutput()
		if err != nil {
			t.Fatalf("installer failed on the rebuilt %s: %v\n%s", filepath.Base(p), err, out)
		}
	}
	checked := 0
	for _, comp := range components {
		src := filepath.Join(expanded, filepath.FromSlash(comp), "Payload")
		filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
			if err != nil || !fi.Mode().IsRegular() {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			a, _ := fileSHA256(p)
			b, err := fileSHA256(filepath.Join(mount, installedPath(rel)))
			if err != nil || a != b {
				if checked < 20 {
					t.Errorf("%s: installed %s differs from the payload (%v)", comp, rel, err)
				}
			}
			checked++
			return nil
		})
	}
	attest(t, "installer installed the rebuilt package: %d files checked against the original payload", checked)
}
