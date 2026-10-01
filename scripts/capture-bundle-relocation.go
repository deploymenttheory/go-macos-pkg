//go:build ignore

// Capture independent pkgbuild relocation evidence. No package code is imported.
// Run on macOS: go run scripts/capture-bundle-relocation.go -out testdata/relocation/macos-27
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func write(path string, data []byte) {
	must(os.MkdirAll(filepath.Dir(path), 0755))
	must(os.WriteFile(path, data, 0644))
}
func read(path string) []byte { b, e := os.ReadFile(path); must(e); return b }
func hash(b []byte) string    { return fmt.Sprintf("%x", sha256.Sum256(b)) }

type capture struct {
	Name           string     `json:"name"`
	ComponentPlist string     `json:"component_plist,omitempty"`
	MinOS          string     `json:"min_os,omitempty"`
	NoRelocate     bool       `json:"no_relocate,omitempty"`
	Commands       [][]string `json:"commands"`
}

func main() {
	out := flag.String("out", "", "new evidence directory")
	flag.Parse()
	if runtime.GOOS != "darwin" || *out == "" {
		panic("requires macOS and -out")
	}
	if _, err := os.Stat(*out); !os.IsNotExist(err) {
		panic("output must not exist")
	}
	work, err := os.MkdirTemp("", "pkg-relocation-")
	must(err)
	defer os.RemoveAll(work)
	run := func(dir, exe string, args ...string) []byte {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Dir = dir
		b, err := cmd.CombinedOutput()
		if err != nil {
			panic(fmt.Sprintf("%s %v: %v\n%s", exe, args, err, b))
		}
		return b
	}
	root := filepath.Join(work, "root")
	sources := map[string]string{}
	for _, b := range []struct{ path, id, kind string }{
		{"Applications/Fixture.app", "org.example.fixture", "APPL"},
		{"Applications/Fixture.app/Contents/PlugIns/Child.plugin", "org.example.child", "BNDL"},
		{"Library/Fixture.bundle", "org.example.bundle", "BNDL"},
	} {
		p := b.path + "/Contents/Info.plist"
		body := fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><dict><key>CFBundleIdentifier</key><string>%s</string><key>CFBundlePackageType</key><string>%s</string><key>CFBundleVersion</key><string>1</string></dict></plist>\n", b.id, b.kind)
		sources[p] = body
		write(filepath.Join(root, p), []byte(body))
		write(filepath.Join(*out, "root", p), []byte(body))
	}
	cases := []capture{{Name: "default"}, {Name: "minimum-26", MinOS: "26.0"}, {Name: "minimum-27", MinOS: "27.0"}, {Name: "explicit-true", ComponentPlist: "true.plist"}, {Name: "explicit-false", ComponentPlist: "false.plist"}, {Name: "explicit-omitted", ComponentPlist: "omitted.plist"}, {Name: "suppressed", NoRelocate: true}, {Name: "explicit-true-suppressed", ComponentPlist: "true.plist", NoRelocate: true}}
	for _, v := range []string{"true", "false", "omitted"} {
		body := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><array>"
		for _, p := range []string{"Applications/Fixture.app", "Library/Fixture.bundle"} {
			body += "<dict><key>RootRelativeBundlePath</key><string>" + p + "</string><key>BundleIsVersionChecked</key><true/><key>BundleOverwriteAction</key><string>upgrade</string>"
			if p == "Applications/Fixture.app" {
				body += "<key>BundleHasStrictIdentifier</key><true/>"
			}
			if v != "omitted" {
				body += "<key>BundleIsRelocatable</key><" + v + "/>"
			}
			body += "</dict>"
		}
		body += "</array></plist>\n"
		write(filepath.Join(work, v+".plist"), []byte(body))
		write(filepath.Join(*out, "inputs", v+".plist"), []byte(body))
	}
	for i := range cases {
		c := &cases[i]
		d := filepath.Join(work, c.Name)
		must(os.MkdirAll(d, 0755))
		args := []string{"--root", root, "--identifier", "org.example.relocation", "--version", "1", "--install-location", "/"}
		if c.ComponentPlist != "" {
			args = append(args, "--component-plist", filepath.Join(work, c.ComponentPlist))
		}
		if c.MinOS != "" {
			args = append(args, "--min-os-version", c.MinOS)
		}
		if c.NoRelocate {
			args = append(args, "--no-relocate")
		}
		args = append(args, filepath.Join(d, "out.pkg"))
		run(work, "/usr/bin/pkgbuild", args...)
		run(d, "/usr/bin/xar", "-xf", filepath.Join(d, "out.pkg"), "PackageInfo")
		write(filepath.Join(*out, c.Name, "PackageInfo"), read(filepath.Join(d, "PackageInfo")))
		c.Commands = append(c.Commands, append([]string{"/usr/bin/pkgbuild"}, args...))
		analyze := []string{"--analyze", "--root", root}
		if c.ComponentPlist != "" {
			analyze = append(analyze, "--component-plist", filepath.Join(work, c.ComponentPlist))
		}
		analyze = append(analyze, filepath.Join(d, "analyze.plist"))
		run(work, "/usr/bin/pkgbuild", analyze...)
		write(filepath.Join(*out, c.Name, "analyze.plist"), read(filepath.Join(d, "analyze.plist")))
		c.Commands = append(c.Commands, append([]string{"/usr/bin/pkgbuild"}, analyze...))
		for j := range c.Commands {
			for k := range c.Commands[j] {
				c.Commands[j][k] = strings.ReplaceAll(c.Commands[j][k], work, "$WORK")
			}
		}
	}
	hashes := map[string]string{}
	must(filepath.WalkDir(*out, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, e := filepath.Rel(*out, path)
			must(e)
			hashes[filepath.ToSlash(rel)] = hash(read(path))
		}
		return nil
	}))
	manifest := struct {
		OS           string            `json:"os"`
		ToolSHA256   string            `json:"pkgbuild_sha256"`
		ManualSHA256 string            `json:"manual_sha256"`
		Captured     string            `json:"captured_utc"`
		Cases        []capture         `json:"cases"`
		SHA256       map[string]string `json:"sha256"`
	}{string(run(work, "/usr/bin/sw_vers")), hash(read("/usr/bin/pkgbuild")), hash(read("/usr/share/man/man1/pkgbuild.1")), time.Now().UTC().Format(time.RFC3339), cases, hashes}
	data, err := json.MarshalIndent(manifest, "", "  ")
	must(err)
	write(filepath.Join(*out, "manifest.json"), append(data, '\n'))
	fmt.Printf("Captured %d unsigned native build/analyze cases in %s\n", len(cases), *out)
}
