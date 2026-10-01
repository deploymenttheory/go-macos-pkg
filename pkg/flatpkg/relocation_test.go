package flatpkg

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func relocationRead(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func sortComponents(list []ComponentBundle) {
	sort.Slice(list, func(i, j int) bool { return list[i].RootRelativeBundlePath < list[j].RootRelativeBundlePath })
	for i := range list {
		sortComponents(list[i].ChildBundles)
	}
}
func relocationBuildInfo(t *testing.T, o ComponentOptions) *PackageInfo {
	t.Helper()
	var buf bytes.Buffer
	o.ExcludeXattr = hostNoise
	if _, e := BuildComponent(o, &buf); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(t.TempDir(), "out.pkg")
	if e := os.WriteFile(p, buf.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	pkg, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer pkg.Close()
	return pkg.Components[0].Info
}

// These bytes were produced by Apple's tool, including explicit relocation on
// a non-application bundle. Every supported host checks the same oracle.
func TestRelocationNativeFixtures(t *testing.T) {
	base := filepath.Join("..", "..", "testdata", "relocation", "macos-27")
	var manifest struct {
		Cases []struct {
			Name           string
			ComponentPlist string `json:"component_plist"`
			MinOS          string `json:"min_os"`
			NoRelocate     bool   `json:"no_relocate"`
		}
		SHA256 map[string]string
	}
	if e := json.Unmarshal(relocationRead(t, filepath.Join(base, "manifest.json")), &manifest); e != nil {
		t.Fatal(e)
	}
	for path, want := range manifest.SHA256 {
		if got := fmt.Sprintf("%x", sha256.Sum256(relocationRead(t, filepath.Join(base, filepath.FromSlash(path))))); got != want {
			t.Fatalf("fixture changed: %s", path)
		}
	}
	if len(manifest.Cases) != 8 || len(manifest.SHA256) != 22 {
		t.Fatalf("incomplete capture: %d cases, %d files", len(manifest.Cases), len(manifest.SHA256))
	}
	root := filepath.Join(base, "root")
	for _, c := range manifest.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var rules []ComponentBundle
			if c.ComponentPlist != "" {
				var e error
				rules, e = ParseComponentPlist(relocationRead(t, filepath.Join(base, "inputs", c.ComponentPlist)))
				if e != nil {
					t.Fatal(e)
				}
			}
			got := relocationBuildInfo(t, ComponentOptions{Root: root, Identifier: "org.example.relocation", Version: "1", InstallLocation: "/", ComponentPlist: rules, MinOSVersion: c.MinOS, NoBundleRelocation: c.NoRelocate && len(rules) == 0})
			var want PackageInfo
			if e := xml.Unmarshal(relocationRead(t, filepath.Join(base, c.Name, "PackageInfo")), &want); e != nil {
				t.Fatal(e)
			}
			// Apple's --no-relocate suppresses implicit defaults, but leaves explicit
			// plist rules intact. Our NoBundleRelocation is a stronger, documented
			// force override (tested separately); do not apply it to Apple's explicit rules.
			// Only ordering is unspecified by Apple's dictionary traversal; compare
			// complete bundle membership in every policy list, including empty lists.
			for _, pair := range [][2]*BundleRefs{{got.Relocate, want.Relocate}, {got.StrictIdentifier, want.StrictIdentifier}, {got.UpgradeBundle, want.UpgradeBundle}, {got.UpdateBundle, want.UpdateBundle}, {got.AtomicUpdateBundle, want.AtomicUpdateBundle}} {
				if pair[0] == nil || pair[1] == nil {
					t.Fatal("missing policy element")
				}
				for _, p := range pair {
					sort.Slice(p.Bundles, func(i, j int) bool { return p.Bundles[i].ID < p.Bundles[j].ID })
				}
				if !reflect.DeepEqual(pair[0], pair[1]) {
					t.Fatalf("policy mismatch: got %+v want %+v", pair[0], pair[1])
				}
			}
			if got.MinimumSystemVersion != want.MinimumSystemVersion || !reflect.DeepEqual(got.Relocatable, want.Relocatable) {
				t.Fatal("package-level policy changed")
			}
			fresh, e := AnalyzeBundles(root)
			if e != nil {
				t.Fatal(e)
			}
			if rules != nil {
				fresh = MergeComponentPlist(fresh, rules)
			}
			expected, e := ParseComponentPlist(relocationRead(t, filepath.Join(base, c.Name, "analyze.plist")))
			if e != nil {
				t.Fatal(e)
			}
			sortComponents(fresh)
			sortComponents(expected)
			gotXML, e := MarshalComponentPlist(fresh)
			if e != nil {
				t.Fatal(e)
			}
			wantXML, e := MarshalComponentPlist(expected)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(gotXML, wantXML) {
				t.Fatalf("analyze differs: got %+v want %+v", fresh, expected)
			}
		})
	}
}

func TestLegacyRelocationDefaultsAndPrecedence(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "relocation", "macos-27", "root")
	for _, legacy := range []bool{false, true} {
		fresh, e := AnalyzeBundlesWithOptions(root, AnalyzeOptions{LegacyBundleRelocation: legacy})
		if e != nil {
			t.Fatal(e)
		}
		for _, c := range fresh {
			want := legacy && c.RootRelativeBundlePath == "Applications/Fixture.app"
			if got := c.BundleIsRelocatable != nil && *c.BundleIsRelocatable; got != want {
				t.Fatalf("legacy=%v bundle=%s relocation=%v", legacy, c.RootRelativeBundlePath, got)
			}
			for _, child := range c.ChildBundles {
				if child.BundleIsRelocatable != nil {
					t.Fatal("nested bundle acquired independent policy")
				}
			}
		}
		for _, v := range []string{"absent", "true", "false", "omitted"} {
			for _, suppress := range []bool{false, true} {
				t.Run(fmt.Sprintf("legacy=%v/%s/suppress=%v", legacy, v, suppress), func(t *testing.T) {
					o := ComponentOptions{Root: root, Identifier: "org.example.test", Version: "1", LegacyBundleRelocation: legacy, NoBundleRelocation: suppress}
					count := 0
					if legacy {
						count = 1
					}
					if v != "absent" {
						var e error
						o.ComponentPlist, e = ParseComponentPlist(relocationRead(t, filepath.Join("..", "..", "testdata", "relocation", "macos-27", "inputs", v+".plist")))
						if e != nil {
							t.Fatal(e)
						}
						count = 0
						if v == "true" {
							count = 2
						}
					}
					if suppress {
						count = 0
					}
					got := relocationBuildInfo(t, o)
					if got.Relocate == nil || len(got.Relocate.Bundles) != count {
						t.Fatalf("got %+v want %d relocation refs", got.Relocate, count)
					}
				})
			}
		}
	}
	if _, e := AnalyzeBundles(filepath.Join(t.TempDir(), "missing")); e == nil {
		t.Fatal("missing tree accepted")
	}
}
