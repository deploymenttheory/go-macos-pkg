package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
)

func TestRelocationBuildAndAnalyzeOptions(t *testing.T) {
	flag := buildCmd.Flags().Lookup("legacy-bundle-relocation")
	oldChanged, oldValue := flag.Changed, buildLegacyBundleRelocation
	oldManifest, oldIdentifier, oldVersion := buildManifest, buildIdentifier, buildVersion
	defer func() {
		flag.Changed = oldChanged
		buildLegacyBundleRelocation = oldValue
		buildManifest = oldManifest
		buildIdentifier = oldIdentifier
		buildVersion = oldVersion
	}()
	root := filepath.Join("..", "..", "testdata", "relocation", "macos-27", "root")
	for _, tc := range []struct {
		name                       string
		legacy, manifest, explicit bool
		want                       int
	}{
		{"default", false, false, false, 0},
		{"legacy", true, false, true, 1},
		{"manifest", false, true, false, 1},
		{"override-manifest", false, true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buildLegacyBundleRelocation = tc.legacy
			flag.Changed = tc.explicit
			buildIdentifier = "org.example.relocation"
			buildVersion = "1"
			buildManifest = ""
			if tc.manifest {
				project := t.TempDir()
				if e := os.CopyFS(filepath.Join(project, "payload"), os.DirFS(root)); e != nil {
					t.Fatal(e)
				}
				buildManifest = filepath.Join(project, "build-info.json")
				if e := os.WriteFile(buildManifest, []byte(`{"legacy_bundle_relocation":true}`), 0600); e != nil {
					t.Fatal(e)
				}
			}
			out := filepath.Join(t.TempDir(), "out.pkg")
			if e := runBuild(buildCmd, []string{root, out}); e != nil {
				t.Fatal(e)
			}
			pkg, e := flatpkg.Open(out)
			if e != nil {
				t.Fatal(e)
			}
			defer pkg.Close()
			if got := len(pkg.Components[0].Info.Relocate.Bundles); got != tc.want {
				t.Fatalf("relocation references: got %d want %d", got, tc.want)
			}
			if tc.manifest {
				return
			} // analyze has no build-manifest input
			plist := filepath.Join(t.TempDir(), "analyze.plist")
			if e := runAnalyze(root, plist); e != nil {
				t.Fatal(e)
			}
			data, e := os.ReadFile(plist)
			if e != nil {
				t.Fatal(e)
			}
			list, e := flatpkg.ParseComponentPlist(data)
			if e != nil {
				t.Fatal(e)
			}
			got := 0
			for _, b := range list {
				if b.BundleIsRelocatable != nil && *b.BundleIsRelocatable {
					got++
				}
			}
			if got != tc.want {
				t.Fatalf("analyze relocations: got %d want %d", got, tc.want)
			}
		})
	}
}
