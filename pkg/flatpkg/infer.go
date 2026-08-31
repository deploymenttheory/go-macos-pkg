// Inferring a package's identity from what it contains, as pkgbuild does
// when it is given --component or --prior rather than an identifier and a
// version.
package flatpkg

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// NormalizeBundleVersion turns a CFBundleShortVersionString into the version
// pkgbuild writes for a --component build: exactly three numeric components.
//
// pkgbuild takes the leading integer of each of the first three dot-separated
// parts, pads what is missing with zeros and drops the rest, so 4 and 4.0
// both become 4.0.0, 4.0.1.2 becomes 4.0.1, and 4.0b1 becomes 4.0.0.
func NormalizeBundleVersion(s string) string {
	parts := strings.Split(s, ".")
	out := make([]string, 3)
	for i := 0; i < 3; i++ {
		out[i] = "0"
		if i >= len(parts) {
			continue
		}
		if n := leadingInt(parts[i]); n >= 0 {
			out[i] = strconv.Itoa(n)
		}
	}
	return strings.Join(out, ".")
}

// NextVersionAfter is what pkgbuild --prior writes: the leading integer of
// the prior package's version, incremented. A version with no leading digit
// counts as zero, so the result is 1.
func NextVersionAfter(version string) string {
	n := leadingInt(version)
	if n < 0 {
		n = 0
	}
	return strconv.Itoa(n + 1)
}

// leadingInt reads the digits at the start of s, or -1 if there are none.
func leadingInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return -1
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return -1
	}
	return n
}

// BundleIdentity is what a single bundle tells pkgbuild about the package
// built from it.
type BundleIdentity struct {
	Identifier string
	Version    string
	// InstallLocation is the absolute path of the directory holding the
	// bundle, which is what pkgbuild infers. It is rarely what you want
	// for a package you intend to ship, so pass --install-location.
	//
	// Under /tmp, /var or /etc the two tools can spell one directory
	// differently: those are symbolic links into /private, and macOS
	// presents the shorter form to Apple's own path APIs while the kernel
	// reports the resolved one. Anywhere else they agree.
	InstallLocation string
	// Name is CFBundleName, which productbuild titles a product with when
	// it builds one straight from a bundle.
	Name string
}

// InferFromBundle reads a bundle's Info.plist and returns the identity
// pkgbuild derives from it in --component mode.
func InferFromBundle(bundlePath string) (BundleIdentity, error) {
	clean := filepath.Clean(bundlePath)
	info, _, ok := readBundleInfo(clean)
	if !ok {
		return BundleIdentity{}, fmt.Errorf("flatpkg: %s has no readable Info.plist; only bundles can be given as a component", bundlePath)
	}
	if info.CFBundleIdentifier == "" {
		return BundleIdentity{}, fmt.Errorf("flatpkg: %s has no CFBundleIdentifier", bundlePath)
	}
	abs, err := filepath.Abs(filepath.Dir(clean))
	if err != nil {
		return BundleIdentity{}, err
	}
	return BundleIdentity{
		Identifier:      info.CFBundleIdentifier,
		Version:         NormalizeBundleVersion(info.CFBundleShortVersionString),
		InstallLocation: abs,
		Name:            info.CFBundleName,
	}, nil
}

// InferFromPrior reads a previously built component package and returns the
// identity pkgbuild --prior derives from it: the same identifier and install
// location, and the next version.
func InferFromPrior(path string) (BundleIdentity, error) {
	p, err := Open(path)
	if err != nil {
		return BundleIdentity{}, err
	}
	defer p.Close()
	if len(p.Components) == 0 || p.Components[0].Info == nil {
		return BundleIdentity{}, fmt.Errorf("flatpkg: %s carries no PackageInfo to take an identity from", path)
	}
	info := p.Components[0].Info
	return BundleIdentity{
		Identifier:      info.Identifier,
		Version:         NextVersionAfter(info.Version),
		InstallLocation: info.InstallLocation,
	}, nil
}

// resolveComponents turns a list of bundle paths into the payload root they
// share and a filter that keeps only those bundles.
//
// pkgbuild packages a bundle in place: the root is the directory holding it
// and the payload is the bundle alone, which is why the inferred install
// location is that directory. Bundles that do not share one directory have
// no such root, and pkgbuild's own answer there is undocumented, so this
// says so rather than guessing.
func resolveComponents(components []string) (root string, keep func(string) bool, err error) {
	if len(components) == 0 {
		return "", nil, nil
	}
	var dir string
	names := make(map[string]bool, len(components))
	for _, c := range components {
		clean := filepath.Clean(c)
		abs, err := filepath.Abs(clean)
		if err != nil {
			return "", nil, err
		}
		parent := filepath.Dir(abs)
		if dir == "" {
			dir = parent
		} else if parent != dir {
			return "", nil, fmt.Errorf("flatpkg: components must share one directory, but %s is not in %s", c, dir)
		}
		names[filepath.Base(abs)] = true
	}
	keep = func(rel string) bool {
		// rel is the "./"-prefixed path collectPayload walks with.
		name := strings.TrimPrefix(rel, "./")
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[:i]
		}
		return names[name]
	}
	return dir, keep, nil
}

// MinOSVersionAtLeast reports whether a minimumSystemVersion string names a
// major version of at least major. An empty or unparsable version is not
// enough, which is what makes it usable as a precondition.
func MinOSVersionAtLeast(version string, major int) bool {
	if version == "" {
		return false
	}
	return leadingInt(strings.SplitN(version, ".", 2)[0]) >= major
}
