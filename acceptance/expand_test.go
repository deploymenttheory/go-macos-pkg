// Command tests for expand and extract: against the manifest everywhere,
// and against pkgutil --expand / --expand-full where pkgutil exists.
package acceptance

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/exitcode"
)

type extractJSON struct {
	Partial    bool `json:"partial"`
	Components []struct {
		Component string   `json:"component"`
		Dir       string   `json:"dir"`
		Encoding  string   `json:"encoding"`
		Files     int      `json:"files"`
		Symlinks  int      `json:"symlinks"`
		HardLinks int      `json:"hardLinks"`
		Xattrs    int      `json:"xattrs"`
		Skipped   []string `json:"skipped"`
	} `json:"components"`
}

// checkTree compares an extracted payload against the manifest: every
// regular file's bytes, every symlink's target, and (on Unix) the mode.
func checkTree(t *testing.T, dir string, files map[string]manifestFile) int {
	t.Helper()
	checked := 0
	for p, f := range files {
		if isAppleDouble(p) {
			continue // ._ sidecars are written too, but have no manifest hash
		}
		target := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(p, "./")))
		switch f.Type {
		case "file":
			sum, err := fileSHA256(target)
			if err != nil {
				t.Errorf("%s: %v", p, err)
				continue
			}
			if f.SHA256 != "" && sum != f.SHA256 {
				t.Errorf("%s: sha256 %s, want %s", p, sum, f.SHA256)
			}
			if runtime.GOOS != "windows" {
				st, _ := os.Stat(target)
				if want := f.Mode; want != "" && st != nil {
					if got := strings.TrimLeft(strings.ToLower(octal(st.Mode().Perm())), "0"); got != strings.TrimLeft(want, "0") {
						t.Errorf("%s: mode %s, want %s", p, got, want)
					}
				}
			}
			checked++
		case "link":
			if runtime.GOOS == "windows" {
				continue
			}
			got, err := os.Readlink(target)
			if err != nil || got != f.Target {
				t.Errorf("%s: link %q (%v), want %q", p, got, err, f.Target)
			}
			checked++
		case "dir":
			if st, err := os.Stat(target); err != nil || !st.IsDir() {
				t.Errorf("%s: not a directory (%v)", p, err)
			}
		}
	}
	return checked
}

func octal(m os.FileMode) string {
	const digits = "01234567"
	v := uint32(m)
	out := ""
	for i := 0; i < 4; i++ {
		out = string(digits[v&7]) + out
		v >>= 3
	}
	return out
}

func TestExtractMatchesManifest(t *testing.T) {
	for _, name := range []string{"component-basic.pkg", "component-pbzx.pkg", "component-large-payload.pkg"} {
		t.Run(name, func(t *testing.T) {
			path, want := fixture(t, name)
			dir := filepath.Join(t.TempDir(), "out")
			var rep extractJSON
			mustRunJSON(t, &rep, "extract", "--verify", path, dir)
			if rep.Partial || len(rep.Components) != 1 {
				t.Fatalf("report: %+v", rep)
			}
			if rep.Components[0].Encoding != want.PayloadEncoding {
				t.Errorf("encoding %s, want %s", rep.Components[0].Encoding, want.PayloadEncoding)
			}
			n := checkTree(t, dir, want.Files)
			attest(t, "%s: %d files and links match the manifest after extract --verify", name, n)
		})
	}
}

func TestExtractProductComponents(t *testing.T) {
	path, want := fixture(t, "product-basic.pkg")
	dir := filepath.Join(t.TempDir(), "out")
	var rep extractJSON
	mustRunJSON(t, &rep, "extract", path, dir)
	if len(rep.Components) != len(want.Components) {
		t.Fatalf("extracted %d components, want %d", len(rep.Components), len(want.Components))
	}
	for name := range want.Components {
		if _, err := os.Stat(filepath.Join(dir, name, "usr", "local", "fixture", "hello.txt")); err != nil {
			t.Errorf("%s payload not under its own directory: %v", name, err)
		}
	}
	single := filepath.Join(t.TempDir(), "one")
	mustRunJSON(t, &rep, "extract", "--component", "component-basic.pkg", path, single)
	if _, err := os.Stat(filepath.Join(single, "usr", "local", "fixture", "hello.txt")); err != nil {
		t.Errorf("--component payload not extracted directly into DIR: %v", err)
	}
	basic, _ := fixture(t, "component-basic.pkg")
	_ = basic
	checkTree(t, single, manifest.Packages["component-basic.pkg"].Files)
}

func TestExtractPatternAndScripts(t *testing.T) {
	path, _ := fixture(t, "component-basic.pkg")
	dir := filepath.Join(t.TempDir(), "out")
	var rep extractJSON
	mustRunJSON(t, &rep, "extract", "--regexp", `bin/tool$`, path, dir)
	if rep.Components[0].Files != 1 {
		t.Errorf("pattern extracted %d files, want 1", rep.Components[0].Files)
	}
	if _, err := os.Stat(filepath.Join(dir, "usr", "local", "fixture", "hello.txt")); err == nil {
		t.Error("pattern did not filter hello.txt")
	}
	_, _, code := run(t, "extract", "--regexp", "nomatch", path, filepath.Join(t.TempDir(), "none"))
	if code != exitcode.Partial {
		t.Errorf("no match: exit %d, want %d", code, exitcode.Partial)
	}
	_, _, code = run(t, "extract", "--regexp", "(", path, filepath.Join(t.TempDir(), "bad"))
	if code != exitcode.Usage {
		t.Errorf("bad regexp: exit %d, want %d", code, exitcode.Usage)
	}

	scripts := filepath.Join(t.TempDir(), "scripts")
	mustRun(t, "extract", "--scripts", path, scripts)
	for _, s := range []string{"preinstall", "postinstall"} {
		got, err := os.ReadFile(filepath.Join(scripts, s))
		if err != nil || !strings.HasPrefix(string(got), "#!/bin/sh") {
			t.Errorf("%s: %q %v", s, got, err)
		}
		if runtime.GOOS != "windows" {
			if st, _ := os.Stat(filepath.Join(scripts, s)); st.Mode().Perm()&0o111 == 0 {
				t.Errorf("%s is not executable", s)
			}
		}
	}
}

func TestExtractSymlinkModes(t *testing.T) {
	path, _ := fixture(t, "component-basic.pkg")
	dir := filepath.Join(t.TempDir(), "file")
	mustRun(t, "extract", "--symlinks", "file", path, dir)
	link := filepath.Join(dir, "usr", "local", "fixture", "link")
	st, err := os.Lstat(link)
	if err != nil || st.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("--symlinks file: %v %v", st, err)
	}
	if got, _ := os.ReadFile(link); string(got) != "hello.txt" {
		t.Errorf("link file = %q", got)
	}
	_, _, code := run(t, "extract", "--symlinks", "sideways", path, filepath.Join(t.TempDir(), "x"))
	if code != exitcode.Usage {
		t.Errorf("bad --symlinks: exit %d", code)
	}
}

func TestExpandLayout(t *testing.T) {
	path, want := fixture(t, "product-basic.pkg")
	dir := filepath.Join(t.TempDir(), "expanded")
	mustRun(t, "expand", path, dir)
	for _, e := range want.Entries {
		if e == "component-basic.pkg/Scripts" {
			continue // unpacked into a directory, checked below
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(e))); err != nil {
			t.Errorf("entry %s not written: %v", e, err)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "component-basic.pkg", "Scripts")); err != nil || !st.IsDir() {
		t.Error("Scripts was not unpacked into a directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "component-basic.pkg", "Scripts", "postinstall")); err != nil {
		t.Error("Scripts/postinstall missing")
	}
	// Payload is left as the gzip stream without --full.
	head := make([]byte, 3)
	f, _ := os.Open(filepath.Join(dir, "component-basic.pkg", "Payload"))
	f.Read(head)
	f.Close()
	if head[0] != 0x1f || head[1] != 0x8b {
		t.Error("Payload was unpacked without --full")
	}
	// The written PackageInfo is byte-identical to cat's.
	got, _ := os.ReadFile(filepath.Join(dir, "component-basic.pkg", "PackageInfo"))
	if string(got) != mustRun(t, "cat", path, "component-basic.pkg/PackageInfo") {
		t.Error("expanded PackageInfo differs from cat")
	}
	_, stderr, code := run(t, "expand", path, dir)
	if code != exitcode.Error || !strings.Contains(stderr, "already exists") {
		t.Errorf("expand into an existing directory: exit %d\n%s", code, stderr)
	}

	full := filepath.Join(t.TempDir(), "full")
	mustRun(t, "expand", "--full", "--verify", path, full)
	checkTree(t, filepath.Join(full, "component-basic.pkg", "Payload"), manifest.Packages["component-basic.pkg"].Files)
}

// TestExpandMatchesPkgutil is an oracle test: our expansion against
// pkgutil's, file by file.
func TestExpandMatchesPkgutil(t *testing.T) {
	requireTools(t, "pkgutil")
	for _, name := range []string{"component-basic.pkg", "product-basic.pkg"} {
		for _, full := range []bool{false, true} {
			t.Run(name+fullLabel(full), func(t *testing.T) {
				path, _ := fixture(t, name)
				ours := filepath.Join(t.TempDir(), "ours")
				theirs := filepath.Join(t.TempDir(), "theirs")
				if full {
					mustRun(t, "expand", "--full", path, ours)
					hostTool(t, "pkgutil", "--expand-full", path, theirs)
				} else {
					mustRun(t, "expand", path, ours)
					hostTool(t, "pkgutil", "--expand", path, theirs)
				}
				ourFiles := walkFiles(t, ours)
				theirFiles := walkFiles(t, theirs)
				// pkgutil turns ._ sidecars back into extended attributes;
				// we write them as files. Compare everything else.
				if !equalStrings(ourFiles, theirFiles) {
					t.Errorf("file sets differ\nours:   %v\ntheirs: %v", ourFiles, theirFiles)
				}
				compared := 0
				for _, rel := range theirFiles {
					a, _ := os.ReadFile(filepath.Join(ours, rel))
					b, _ := os.ReadFile(filepath.Join(theirs, rel))
					if string(a) != string(b) {
						t.Errorf("%s differs from pkgutil's copy (%d vs %d bytes)", rel, len(a), len(b))
					}
					compared++
				}
				attest(t, "%s%s: %d files byte-identical to pkgutil", name, fullLabel(full), compared)
			})
		}
	}
}

func fullLabel(full bool) string {
	if full {
		return "/full"
	}
	return "/expand"
}

// walkFiles lists regular files under dir, relative, sorted, without ._
// sidecars.
func walkFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && !isAppleDouble(p) {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}
