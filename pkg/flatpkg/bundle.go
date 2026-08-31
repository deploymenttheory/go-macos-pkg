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
		info, bundleRoot, ok := readBundleInfo(p)
		if !ok || info.CFBundleIdentifier == "" {
			return nil
		}
		rel, err := filepath.Rel(root, bundleRoot)
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

// readBundleInfo finds and parses a bundle's Info.plist and reports the
// directory pkgbuild treats as the bundle. Applications keep the plist in
// Contents/; frameworks in Resources/, which in a well-formed framework is
// a symbolic link to Versions/Current/Resources; flat bundles at the top
// level.
//
// The order matters, and so does the root each candidate implies. A
// well-formed framework is found through its top-level Resources link and
// is reported as the framework itself, which is what pkgbuild reports. A
// framework missing that link is found inside Versions instead, and
// pkgbuild then names the version directory rather than the framework:
// "Inner.framework/Versions/A", not "Inner.framework".
func readBundleInfo(dir string) (bundleInfo, string, bool) {
	candidates := []struct{ plistPath, bundleRoot string }{
		{filepath.Join(dir, "Contents", "Info.plist"), dir},
		{filepath.Join(dir, "Resources", "Info.plist"), dir},
		{filepath.Join(dir, "Versions", "Current", "Resources", "Info.plist"), filepath.Join(dir, "Versions", "Current")},
		{filepath.Join(dir, "Versions", "A", "Resources", "Info.plist"), filepath.Join(dir, "Versions", "A")},
		{filepath.Join(dir, "Info.plist"), dir},
	}
	for _, c := range candidates {
		data, err := os.ReadFile(c.plistPath)
		if err != nil {
			continue
		}
		var info bundleInfo
		if _, err := plist.Unmarshal(data, &info); err != nil {
			continue
		}
		return info, c.bundleRoot, true
	}
	return bundleInfo{}, "", false
}

// isApplicationBundle reports whether a bundle is an application, which is
// the only kind pkgbuild treats as relocatable.
//
// pkgbuild --analyze writes BundleIsRelocatable and BundleHasStrictIdentifier
// for a .app and omits both, which is to say false, for a .framework,
// .bundle, .plugin, .kext, .appex, .xpc, .prefPane, .qlgenerator, .saver and
// .mdimporter alike. That is the whole rule: only an application can be
// moved by the user and then found again at its new home, so only an
// application is relocated or matched on a strict identifier. Everything
// else is installed where the package puts it.
func isApplicationBundle(path string) bool {
	return strings.HasSuffix(path, ".app")
}

// topLevelBundles returns the bundles that no other bundle contains.
//
// pkgbuild describes every bundle it finds in its own <bundle> element, but
// references only the top-level ones from bundle-version, upgrade-bundle,
// strict-identifier and relocate. A nested bundle, a framework inside an
// application say, is installed and versioned as part of the bundle that
// contains it, which is the same thing a component property list says by
// putting it under the parent's ChildBundles rather than in the top-level
// array.
func topLevelBundles(all []Bundle) []Bundle {
	var out []Bundle
	for _, b := range all {
		nested := false
		for _, other := range all {
			if other.Path != b.Path && strings.HasPrefix(b.Path, other.Path+"/") {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, b)
		}
	}
	return out
}
