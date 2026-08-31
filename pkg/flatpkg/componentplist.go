// Component property lists: the per-bundle rules pkgbuild --analyze writes
// and pkgbuild --component-plist reads.
//
// Without one, pkgbuild applies a fixed default to every bundle it finds.
// With one, each bundle in the payload can be version-checked or not,
// relocated or not, matched on a strict identifier or not, upgraded or
// updated, and given its own install scripts. The list is an array of
// dictionaries, one per bundle, with nested bundles under ChildBundles.
package flatpkg

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"howett.net/plist"
)

// Overwrite actions, the values BundleOverwriteAction takes.
//
// Despite the names neither checks a version: that is BundleIsVersionChecked
// alone. upgrade replaces the bundle on disk atomically, so paths that no
// longer exist are removed. update overwrites it and leaves anything the
// package does not carry in place, and installs nothing at all where there
// is no bundle on disk already, which is what an update-only package wants.
const (
	OverwriteUpgrade = "upgrade"
	OverwriteUpdate  = "update"
)

// ComponentBundle is one dictionary in a component property list.
//
// Field order is alphabetical because that is the order Apple's property
// list serialiser emits keys in, and these documents are compared with
// pkgbuild's byte for byte.
//
// The three booleans are pointers so that absent and false stay distinct:
// pkgbuild --analyze omits a false boolean rather than writing <false/>,
// and a child bundle carries none of them at all.
type ComponentBundle struct {
	BundleHasStrictIdentifier *bool `plist:"BundleHasStrictIdentifier,omitempty"`
	// BundleInstallScriptTimeout is in seconds, and is honoured only by
	// macOS 15 and later. Absent means the system default, currently ten
	// minutes.
	BundleInstallScriptTimeout *int  `plist:"BundleInstallScriptTimeout,omitempty"`
	BundleIsRelocatable        *bool `plist:"BundleIsRelocatable,omitempty"`
	BundleIsVersionChecked     *bool `plist:"BundleIsVersionChecked,omitempty"`
	// BundleOverwriteAction is always written, including as an empty
	// string on a child bundle, so it carries no omitempty.
	BundleOverwriteAction string `plist:"BundleOverwriteAction"`
	// The script paths are relative to the --scripts directory.
	BundlePostInstallScriptPath string `plist:"BundlePostInstallScriptPath,omitempty"`
	BundlePreInstallScriptPath  string `plist:"BundlePreInstallScriptPath,omitempty"`
	// ChildBundles holds the bundles nested inside this one. Their paths
	// stay relative to the destination root, not to the parent.
	ChildBundles []ComponentBundle `plist:"ChildBundles,omitempty"`
	// RootRelativeBundlePath has no leading "./", unlike Bundle.Path.
	RootRelativeBundlePath string `plist:"RootRelativeBundlePath"`
}

// bundleRules is what a component property list entry decides, resolved
// against the defaults, and is what the PackageInfo bundle lists are built
// from.
type bundleRules struct {
	versionChecked   bool
	overwriteAction  string
	strictIdentifier bool
	relocatable      bool
	preInstall       string
	postInstall      string
	scriptTimeout    *int
}

// defaultBundleRules is what pkgbuild applies to a bundle when it is given
// no component property list: version-checked and upgraded whatever the
// bundle is, relocated and strictly identified only if it is an
// application.
func defaultBundleRules(b Bundle) bundleRules {
	app := isApplicationBundle(b.Path)
	return bundleRules{
		versionChecked:   true,
		overwriteAction:  OverwriteUpgrade,
		strictIdentifier: app,
		relocatable:      app,
	}
}

// rules resolves one entry. An absent boolean is false, which is how
// pkgbuild reads a list that omits it.
func (c ComponentBundle) rules() bundleRules {
	boolOr := func(p *bool) bool { return p != nil && *p }
	return bundleRules{
		versionChecked:   boolOr(c.BundleIsVersionChecked),
		overwriteAction:  c.BundleOverwriteAction,
		strictIdentifier: boolOr(c.BundleHasStrictIdentifier),
		relocatable:      boolOr(c.BundleIsRelocatable),
		preInstall:       c.BundlePreInstallScriptPath,
		postInstall:      c.BundlePostInstallScriptPath,
		scriptTimeout:    c.BundleInstallScriptTimeout,
	}
}

// rootRelative strips the "./" that Bundle.Path carries and a component
// property list does not.
func rootRelative(bundlePath string) string {
	return strings.TrimPrefix(bundlePath, "./")
}

// AnalyzeBundles builds the component property list that pkgbuild --analyze
// writes for a destination root: one entry per top-level bundle, carrying
// the defaults, with any nested bundles listed under ChildBundles.
//
// A child entry carries only its path and an empty overwrite action, which
// is what pkgbuild writes: a nested bundle is installed as part of the one
// that contains it, so it has no rules of its own.
func AnalyzeBundles(root string) ([]ComponentBundle, error) {
	all, err := findBundles(root)
	if err != nil {
		return nil, err
	}
	top := topLevelBundles(all)
	out := make([]ComponentBundle, 0, len(top))
	for _, b := range top {
		r := defaultBundleRules(b)
		entry := ComponentBundle{
			BundleOverwriteAction:  r.overwriteAction,
			RootRelativeBundlePath: rootRelative(b.Path),
		}
		if r.versionChecked {
			entry.BundleIsVersionChecked = BoolPtr(true)
		}
		if r.relocatable {
			entry.BundleIsRelocatable = BoolPtr(true)
		}
		if r.strictIdentifier {
			entry.BundleHasStrictIdentifier = BoolPtr(true)
		}
		for _, child := range all {
			if child.Path != b.Path && strings.HasPrefix(child.Path, b.Path+"/") {
				entry.ChildBundles = append(entry.ChildBundles, ComponentBundle{
					RootRelativeBundlePath: rootRelative(child.Path),
				})
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// MergeComponentPlist carries the settings of a prior list forward onto a
// freshly analyzed one, which is what pkgbuild --analyze --component-plist
// does: bundles that still exist keep the rules they were given, bundles
// that have gone are dropped, and new ones arrive with the defaults.
//
// Matching is by RootRelativeBundlePath, the only stable name an entry has.
func MergeComponentPlist(fresh, prior []ComponentBundle) []ComponentBundle {
	byPath := make(map[string]ComponentBundle, len(prior))
	var index func([]ComponentBundle)
	index = func(list []ComponentBundle) {
		for _, e := range list {
			byPath[e.RootRelativeBundlePath] = e
			index(e.ChildBundles)
		}
	}
	index(prior)

	out := make([]ComponentBundle, 0, len(fresh))
	for _, e := range fresh {
		if old, ok := byPath[e.RootRelativeBundlePath]; ok {
			children := e.ChildBundles
			e = old
			// The tree is what the root says it is now, not what the
			// prior list remembered.
			e.ChildBundles = MergeComponentPlist(children, prior)
		}
		out = append(out, e)
	}
	return out
}

// ParseComponentPlist decodes a component property list.
func ParseComponentPlist(data []byte) ([]ComponentBundle, error) {
	var out []ComponentBundle
	if _, err := plist.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("flatpkg: unable to parse the component property list: %w", err)
	}
	for i, e := range out {
		if e.RootRelativeBundlePath == "" {
			return nil, fmt.Errorf("flatpkg: component property list entry %d has no RootRelativeBundlePath", i)
		}
	}
	return out, nil
}

// MarshalComponentPlist encodes a component property list the way Apple's
// serialiser writes one.
func MarshalComponentPlist(list []ComponentBundle) ([]byte, error) {
	if list == nil {
		list = []ComponentBundle{}
	}
	var buf bytes.Buffer
	enc := plist.NewEncoderForFormat(&buf, plist.XMLFormat)
	enc.Indent("\t")
	if err := enc.Encode(list); err != nil {
		return nil, fmt.Errorf("flatpkg: unable to encode the component property list: %w", err)
	}
	return applePlistStyle(buf.Bytes()), nil
}

// emptyPlistString matches the self-closed empty string the encoder writes.
var emptyPlistString = regexp.MustCompile(`<string/>`)

// applePlistStyle reconciles the two places our encoder and Apple's
// serialiser disagree, so the documents compare byte for byte:
//
//   - Apple puts the root container hard against the left margin, inside
//     <plist> rather than indented one level within it;
//   - Apple writes an empty string as <string></string>, never <string/>.
//
// A component property list also ends with a newline, where a PackageInfo
// does not. Both are Apple's, and both are what these files are compared
// against.
func applePlistStyle(b []byte) []byte {
	b = emptyPlistString.ReplaceAll(b, []byte(`<string></string>`))
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "\t") {
			lines[i] = l[1:]
		}
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out)
}

// DefaultBundleScriptTimeout is the timeout pkgbuild writes on a
// bundle-specific script when the component property list gives none. It is
// far longer than the ten minutes a package's own script gets, which is
// what DefaultScriptTimeout carries.
const DefaultBundleScriptTimeout = "21600"

// bundleScript is one bundle-specific script on its way into <scripts>.
type bundleScript struct {
	kind        string // preinstall or postinstall
	file        string // relative to the scripts directory
	componentID string
	timeout     string
}

// scriptsForBundle turns a bundle's rules into the script elements pkgbuild
// writes for it, preinstall before postinstall.
func scriptsForBundle(b Bundle, r bundleRules) []bundleScript {
	timeout := DefaultBundleScriptTimeout
	if r.scriptTimeout != nil {
		timeout = strconv.Itoa(*r.scriptTimeout)
	}
	var out []bundleScript
	for _, s := range []struct{ kind, file string }{
		{"preinstall", r.preInstall},
		{"postinstall", r.postInstall},
	} {
		if s.file == "" {
			continue
		}
		out = append(out, bundleScript{kind: s.kind, file: s.file, componentID: b.ID, timeout: timeout})
	}
	return out
}

// resolveBundleRules decides which bundles a package describes and how each
// behaves.
//
// Given no component property list, every bundle found is described and the
// top-level ones take the defaults. Given one, the list is exhaustive:
// pkgbuild describes only the bundles it names, together with their
// children, and drops the rest. A named bundle that no longer exists in the
// root is ignored.
func resolveBundleRules(all []Bundle, list []ComponentBundle) ([]Bundle, map[string]bundleRules) {
	if len(list) == 0 {
		rules := make(map[string]bundleRules, len(all))
		for _, b := range topLevelBundles(all) {
			rules[b.Path] = defaultBundleRules(b)
		}
		return all, rules
	}

	named := make(map[string]ComponentBundle, len(list))
	for _, e := range list {
		named[e.RootRelativeBundlePath] = e
	}
	rules := make(map[string]bundleRules, len(list))
	kept := make([]Bundle, 0, len(all))
	for _, b := range all {
		if e, ok := named[rootRelative(b.Path)]; ok {
			rules[b.Path] = e.rules()
			kept = append(kept, b)
			continue
		}
		// A child of a named bundle is still described, with no rules of
		// its own; anything else the list does not mention is dropped.
		for _, parent := range list {
			if strings.HasPrefix(rootRelative(b.Path), parent.RootRelativeBundlePath+"/") {
				kept = append(kept, b)
				break
			}
		}
	}
	return kept, rules
}
