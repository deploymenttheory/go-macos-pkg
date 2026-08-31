// Bundle detection for PackageInfo's bundle-version element.
//
// pkgbuild records every bundle in the payload (applications, frameworks,
// plug-ins, kernel extensions) so the Installer can check versions and
// relocate a bundle the user has moved. Detection is by directory name
// suffix plus an Info.plist in the place the bundle type keeps it.
package flatpkg

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"howett.net/plist"
)

// bundleSuffixes are the directory suffixes pkgbuild treats as bundles.
var bundleSuffixes = []string{".app", ".framework", ".bundle", ".plugin", ".kext", ".appex", ".xpc", ".prefPane", ".qlgenerator", ".saver", ".mdimporter"}

// isBundleDir reports whether a directory name looks like a bundle.
func isBundleDir(name string) bool {
	for _, s := range bundleSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// bundleInfo is what we read from an Info.plist.
type bundleInfo struct {
	CFBundleIdentifier         string `plist:"CFBundleIdentifier"`
	CFBundleShortVersionString string `plist:"CFBundleShortVersionString"`
	CFBundleVersion            string `plist:"CFBundleVersion"`
}

// findBundles walks root and returns a Bundle for every bundle directory
// with a readable Info.plist, paths relative to root and prefixed "./",
// sorted by path. Nested bundles (a framework inside an app) are reported
// too, as pkgbuild does.
func findBundles(root string) ([]Bundle, error) {
	var out []Bundle
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || !isBundleDir(d.Name()) {
			return nil
		}
		info, ok := readBundleInfo(p)
		if !ok || info.CFBundleIdentifier == "" {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		// CFBundleIdentifier is deliberately left unset: pkgbuild writes
		// the identifier once, as id, and a second CFBundleIdentifier
		// attribute would break a byte comparison against its output.
		// The reader still accepts the attribute where a package has it.
		out = append(out, Bundle{
			ID:                         info.CFBundleIdentifier,
			Path:                       "./" + filepath.ToSlash(rel),
			CFBundleShortVersionString: info.CFBundleShortVersionString,
			CFBundleVersion:            info.CFBundleVersion,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// readBundleInfo finds and parses a bundle's Info.plist. Applications keep
// it in Contents/; frameworks in Versions/Current/Resources/ or Resources/;
// flat bundles at the top level.
func readBundleInfo(dir string) (bundleInfo, bool) {
	candidates := []string{
		filepath.Join(dir, "Contents", "Info.plist"),
		filepath.Join(dir, "Resources", "Info.plist"),
		filepath.Join(dir, "Versions", "Current", "Resources", "Info.plist"),
		filepath.Join(dir, "Versions", "A", "Resources", "Info.plist"),
		filepath.Join(dir, "Info.plist"),
	}
	for _, c := range candidates {
		data, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		var info bundleInfo
		if _, err := plist.Unmarshal(data, &info); err != nil {
			continue
		}
		return info, true
	}
	return bundleInfo{}, false
}
