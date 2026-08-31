// Building a product archive from component packages: what productbuild
// does.
package flatpkg

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// ProductOptions configures BuildProduct.
type ProductOptions struct {
	// Packages are the component packages to embed, in order. Each is
	// placed in the archive under its base name.
	Packages []string
	// Distribution is a Distribution XML document to use; nil synthesises
	// one that installs every package, as productbuild --synthesize does.
	Distribution []byte
	// Requirements is the pre-install requirements property list that
	// productbuild reads with --product. It shapes the synthesised
	// Distribution's architectures, domains, volume-check and
	// installation-check.
	Requirements *ProductRequirements
	// Output is the archive's own path. Only the one-step modes need it,
	// to name the component they build after it, as productbuild does.
	Output string
	// GeneratorVersion is written into any component built here.
	GeneratorVersion string
	// Root is a destination root to package as a component and embed, as
	// productbuild --root does, installing at RootInstallPath.
	Root            string
	RootInstallPath string
	// Content is a directory to package as a component and embed, as
	// productbuild --content does.
	Content string
	// Components are bundles to package as components and embed, as
	// productbuild --component does.
	Components []ProductComponent
	// UI names the interface a synthesised choices-outline is for, as
	// productbuild --ui does. "mas" marks an outline meant for the Mac App
	// Store. A distribution may carry several outlines and pick between
	// them by this attribute.
	//
	// It renames as well as labels: the top choice becomes the interface's
	// own name rather than "default", and every package's choice and
	// reference is prefixed with it, so two outlines in one document do not
	// collide.
	UI string
	// Scripts is a directory embedded as the archive's Scripts entry, for
	// the system.run() commands a Distribution can invoke. It is not a
	// component's scripts: nothing here runs at install time on its own.
	Scripts string
	// Plugins is a directory embedded as the archive's PlugIns entry, for
	// the Installer's plug-in mechanism. It normally holds an
	// InstallerSections.plist and one or more plug-in bundles.
	Plugins string
	// Resources is a directory whose contents are embedded under
	// Resources/ (welcome, license, background files the Distribution
	// refers to); optional.
	Resources string
	// Title, MinOSVersion and HostArchitectures shape a synthesised
	// Distribution; ignored when one is supplied.
	Title             string
	MinOSVersion      string
	HostArchitectures []string
	// ProductID and ProductVersion set the <product> element of a
	// synthesised Distribution; optional.
	ProductID      string
	ProductVersion string

	Epoch   time.Time
	TempDir string
	Signer  xar.Signer

	Progress func(path string)
}

// ProductResult reports what BuildProduct wrote.
type ProductResult struct {
	Components   []string
	Distribution []byte
	Resources    []string
}

// BuildProduct writes a product archive to out.
func BuildProduct(o ProductOptions, out io.Writer) (*ProductResult, error) {
	archiveTime := time.Now()
	if !o.Epoch.IsZero() {
		archiveTime = o.Epoch
	}

	// The one-step modes build their component first, then carry on as if
	// it had been given with --package.
	built, locations, cleanup, err := buildInlineComponents(&o, archiveTime)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	o.Packages = append(append([]string{}, built...), o.Packages...)
	if len(o.Packages) == 0 {
		return nil, fmt.Errorf("flatpkg: at least one component package is required")
	}

	// Open every component first: the Distribution needs their identity.
	type component struct {
		name string
		pkg  *Package
	}
	var components []component
	seen := map[string]bool{}
	for _, path := range o.Packages {
		p, err := Open(path)
		if err != nil {
			return nil, fmt.Errorf("flatpkg: %s: %w", path, err)
		}
		defer p.Close()
		if p.Kind != KindComponent {
			return nil, fmt.Errorf("flatpkg: %s is a product archive; only component packages can be embedded", path)
		}
		name := filepath.Base(path)
		if seen[name] {
			return nil, fmt.Errorf("flatpkg: two packages are both named %s", name)
		}
		seen[name] = true
		components = append(components, component{name: name, pkg: p})
	}

	// Scratch space for the Scripts and PlugIns archives, made only if
	// there is something to put in it.
	tmp := ""
	res := &ProductResult{}
	var refs []synthRef
	for _, c := range components {
		info := c.pkg.Components[0].Info
		r := synthRef{ID: info.Identifier, Version: info.Version, Path: c.name}
		if info.Payload != nil {
			r.InstallKBytes = info.Payload.InstallKBytes
		}
		if loc, ok := locations[c.name]; ok {
			r.CustomLocation = loc.installPath
			r.Bundle = loc.bundle
			r.ShortVersion = loc.shortVersion
			r.Title = loc.title
		}
		refs = append(refs, r)
	}
	dist := o.Distribution
	if dist == nil {
		dist = synthesiseDistribution(o, refs, true)
	} else {
		// A distribution written for productbuild names its packages as
		// files beside it; embedding rewrites those references to name
		// entries inside the archive.
		dist = embedDistribution(dist, refs)
	}
	res.Distribution = dist

	w, err := xar.NewWriter(out, xar.WriterOptions{
		ChecksumAlg:  xar.ChecksumSHA256,
		CreationTime: archiveTime,
		TempDir:      o.TempDir,
		Signer:       o.Signer,
	})
	if err != nil {
		return nil, err
	}
	hdr := xar.FileHeader{Mode: 0o644, User: "root", Group: "wheel", ModTime: archiveTime, CTime: archiveTime, ATime: archiveTime}
	dirHdr := xar.FileHeader{Mode: 0o755, User: "root", Group: "wheel", ModTime: archiveTime, CTime: archiveTime, ATime: archiveTime}

	// productbuild's order: Distribution, Resources, then the packages.
	if err := w.AddFile(EntryDistribution, hdr, xar.EncodingGzip, bytes.NewReader(dist)); err != nil {
		return nil, err
	}
	if o.Resources != "" {
		names, err := addResources(w, o.Resources, hdr, dirHdr, o.Progress)
		if err != nil {
			return nil, err
		}
		res.Resources = names
	}
	for _, spec := range []struct{ dir, entry string }{
		{o.Scripts, EntryScripts},
		{o.Plugins, EntryPlugins},
	} {
		if spec.dir == "" {
			continue
		}
		if tmp == "" {
			scratch := o.TempDir
			if scratch == "" {
				scratch = os.TempDir()
			}
			tmp, err = os.MkdirTemp(scratch, "macospkg-product-*")
			if err != nil {
				return nil, err
			}
			defer func() { _ = os.RemoveAll(tmp) }()
		}
		archive := filepath.Join(tmp, spec.entry)
		if err := writeArchivedDir(spec.dir, archive, ComponentOptions{}, archiveTime, false); err != nil {
			return nil, err
		}
		if err := addFileEntry(w, spec.entry, hdr, xar.EncodingNone, archive); err != nil {
			return nil, err
		}
		if o.Progress != nil {
			o.Progress(spec.entry)
		}
	}
	for _, c := range components {
		if err := w.AddDir(c.name, dirHdr); err != nil {
			return nil, err
		}
		// Copy the component's entries as they are: metadata entries are
		// re-encoded, the payload and scripts streamed through unchanged.
		for _, f := range c.pkg.XAR.Files() {
			if f.IsDir() || f.Data == nil {
				continue
			}
			encoding := xar.EncodingGzip
			if f.Name() == EntryPayload || f.Name() == EntryLargePayload || f.Name() == EntryScripts {
				encoding = xar.EncodingNone
			}
			rc, err := c.pkg.XAR.Open(f)
			if err != nil {
				return nil, fmt.Errorf("flatpkg: %s/%s: %w", c.name, f.Path(), err)
			}
			err = w.AddFile(c.name+"/"+f.Path(), hdr, encoding, rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
			if o.Progress != nil {
				o.Progress(c.name + "/" + f.Path())
			}
		}
		res.Components = append(res.Components, c.name)
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return res, nil
}

// addResources embeds a resources directory under Resources/.
func addResources(w *xar.Writer, dir string, hdr, dirHdr xar.FileHeader, progress func(string)) ([]string, error) {
	var names []string
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("flatpkg: resources: %w", err)
	}
	sort.Strings(paths)
	for _, p := range paths {
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			if err := w.AddDir(EntryResources, dirHdr); err != nil {
				return nil, err
			}
			continue
		}
		name := EntryResources + "/" + filepath.ToSlash(rel)
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		switch {
		case fi.IsDir():
			if err := w.AddDir(name, dirHdr); err != nil {
				return nil, err
			}
		case fi.Mode().IsRegular():
			f, err := os.Open(p)
			if err != nil {
				return nil, err
			}
			err = w.AddFile(name, hdr, xar.EncodingGzip, f)
			f.Close()
			if err != nil {
				return nil, err
			}
			names = append(names, name)
			if progress != nil {
				progress(name)
			}
		default:
			return nil, fmt.Errorf("flatpkg: resources: %s is not a regular file", rel)
		}
	}
	return names, nil
}

// DefaultHostArchitectures is what productbuild writes into a synthesised
// Distribution when it is given no architectures: both, x86_64 first. It has
// done so since Big Sur.
var DefaultHostArchitectures = []string{"x86_64", "arm64"}

// ProductComponent is one bundle to package in place and embed.
type ProductComponent struct {
	Path string
	// InstallPath is where the bundle installs, and becomes the choice's
	// customLocation. Empty leaves the component's own location to stand.
	InstallPath string
}

// synthRef is what a synthesised Distribution needs to know per package.
type synthRef struct {
	ID            string
	Version       string
	Path          string
	InstallKBytes int
	// CustomLocation overrides where the package installs, and is what
	// the install-path argument of --root or --component becomes.
	CustomLocation string
	// Bundle, when the package was built from a single bundle, is written
	// into the reference's bundle-version so the Installer can version
	// check it without opening the payload.
	Bundle *Bundle
	// ShortVersion is the bundle's CFBundleShortVersionString, unpadded,
	// which is what the product element and the default choice's versStr
	// carry. Version alongside it is the padded three-part form.
	ShortVersion string
	// Title is the bundle's CFBundleName, which titles both the product
	// and the choices when a product is built straight from a bundle.
	Title string
}

// synthesiseDistribution writes the Distribution that productbuild puts
// inside a product archive when it is not given one: one hidden choice per
// package, all selected, no customisation.
//
// The shape is productbuild's, byte for byte, and it is worth being precise
// about which shape that is. "productbuild --synthesize" writes a document
// to a file with bare <pkg-ref id/> stubs, bare package paths and no size
// attributes. Synthesising straight into an archive instead fills the stubs
// in with a bundle-version, names each package as an entry with a leading
// "#", and adds installKBytes and updateKBytes. Neither declares standalone;
// only a document productbuild re-serialises does, which is embedDistribution's
// job.
//
// Two attributes are deliberately absent because productbuild does not write
// them either: auth, which every component's PackageInfo carries but which
// never reaches the Distribution, and a trailing newline.
func synthesiseDistribution(o ProductOptions, refs []synthRef, embedded bool) []byte {
	var b strings.Builder
	attr := func(v string) string { return `"` + xmlEscape(v) + `"` }

	// With --ui the ids are namespaced by the interface, so a document can
	// carry an outline for each without their choices colliding.
	topChoice := "default"
	choiceID := func(id string) string { return id }
	if o.UI != "" {
		topChoice = o.UI
		choiceID = func(id string) string { return o.UI + "-" + id }
	}

	// No standalone, whichever form this is. productbuild declares it only
	// when it re-serialises a document somebody else wrote, which is the
	// --distribution path and is handled in embedDistribution. A document
	// productbuild synthesises straight into an archive does not carry it.
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	fmt.Fprintf(&b, "<installer-gui-script minSpecVersion=%s>\n", attr(o.Requirements.MinSpecVersion(o.MinOSVersion)))
	// The stubs productbuild writes ahead of <options>, one per package.
	// Embedding fills each one in with a bundle-version element.
	for _, r := range refs {
		switch {
		case !embedded:
			fmt.Fprintf(&b, "    <pkg-ref id=%s/>\n", attr(choiceID(r.ID)))
		case r.Bundle != nil:
			// A component built from one bundle carries that bundle's
			// details, so the Installer can version check without
			// opening the payload.
			fmt.Fprintf(&b, "    <pkg-ref id=%s>\n        <bundle-version>\n            <bundle", attr(choiceID(r.ID)))
			if v := r.Bundle.CFBundleShortVersionString; v != "" {
				fmt.Fprintf(&b, " CFBundleShortVersionString=%s", attr(v))
			}
			if v := r.Bundle.CFBundleVersion; v != "" {
				fmt.Fprintf(&b, " CFBundleVersion=%s", attr(v))
			}
			fmt.Fprintf(&b, " id=%s path=%s/>\n        </bundle-version>\n    </pkg-ref>\n",
				attr(r.Bundle.ID), attr(r.Bundle.Path))
		default:
			fmt.Fprintf(&b, "    <pkg-ref id=%s>\n        <bundle-version/>\n    </pkg-ref>\n", attr(choiceID(r.ID)))
		}
	}

	// productbuild writes the product element after the stubs, and takes
	// its identity from the bundle where one component is a bundle.
	productID, productVersion := o.ProductID, o.ProductVersion
	for _, r := range refs {
		if r.Bundle != nil && productID == "" {
			productID, productVersion = r.Bundle.ID, r.ShortVersion
		}
	}
	if productID != "" {
		fmt.Fprintf(&b, "    <product id=%s", attr(productID))
		if productVersion != "" {
			fmt.Fprintf(&b, " version=%s", attr(productVersion))
		}
		b.WriteString("/>\n")
	}

	// The title comes after the product element, and a product built from
	// a bundle takes the bundle's own name where none was given.
	title := o.Title
	for _, r := range refs {
		if title == "" {
			title = r.Title
		}
	}
	if title != "" {
		fmt.Fprintf(&b, "    <title>%s</title>\n", xmlEscape(title))
	}

	archs := o.HostArchitectures
	if len(archs) == 0 {
		archs = o.Requirements.Architectures()
	}
	if len(archs) == 0 {
		archs = DefaultHostArchitectures
	}
	fmt.Fprintf(&b, "    <options customize=\"never\" require-scripts=\"false\" hostArchitectures=%s/>\n",
		attr(strings.Join(archs, ",")))

	// productbuild's order: domains, then what the volume must be, then
	// what the machine must have.
	b.WriteString(o.Requirements.DomainsElement())
	volumeCheck, trailingVolumeCheck := o.Requirements.VolumeCheckElement(o.MinOSVersion)
	b.WriteString(volumeCheck)
	b.WriteString(o.Requirements.InstallationCheckElement())

	// One default line wrapping every package, not one wrapper per package.
	b.WriteString("    <choices-outline")
	if o.UI != "" {
		fmt.Fprintf(&b, " ui=%s", attr(o.UI))
	}
	fmt.Fprintf(&b, ">\n        <line choice=%s>\n", attr(topChoice))
	for _, r := range refs {
		fmt.Fprintf(&b, "            <line choice=%s/>\n", attr(choiceID(r.ID)))
	}
	b.WriteString("        </line>\n    </choices-outline>\n")
	versStr := ""
	for _, r := range refs {
		if r.ShortVersion != "" && versStr == "" {
			versStr = r.ShortVersion
		}
	}
	fmt.Fprintf(&b, "    <choice id=%s", attr(topChoice))
	if title != "" {
		fmt.Fprintf(&b, " title=%s", attr(title))
	}
	if versStr != "" {
		fmt.Fprintf(&b, " versStr=%s", attr(versStr))
	}
	b.WriteString("/>\n")

	// Each choice is followed by its own pkg-ref, interleaved. Only an
	// embedded document carries the sizes and the "#" that names an entry
	// inside the archive; a synthesised file names the package itself.
	for _, r := range refs {
		fmt.Fprintf(&b, "    <choice id=%s", attr(choiceID(r.ID)))
		if r.Title != "" {
			fmt.Fprintf(&b, " title=%s", attr(r.Title))
		}
		b.WriteString(` visible="false"`)
		if r.CustomLocation != "" {
			fmt.Fprintf(&b, " customLocation=%s", attr(r.CustomLocation))
		}
		fmt.Fprintf(&b, ">\n        <pkg-ref id=%s/>\n    </choice>\n", attr(choiceID(r.ID)))
		fmt.Fprintf(&b, "    <pkg-ref id=%s version=%s onConclusion=\"none\"", attr(choiceID(r.ID)), attr(r.Version))
		if embedded {
			fmt.Fprintf(&b, " installKBytes=%s updateKBytes=\"0\"", attr(strconv.Itoa(r.InstallKBytes)))
			fmt.Fprintf(&b, ">#%s</pkg-ref>\n", xmlEscape(r.Path))
		} else {
			fmt.Fprintf(&b, ">%s</pkg-ref>\n", xmlEscape(r.Path))
		}
	}

	b.WriteString(trailingVolumeCheck)
	b.WriteString("</installer-gui-script>")
	return []byte(b.String())
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SynthesizeDistribution writes the Distribution that productbuild
// --synthesize writes to a file, for the packages named in o.Packages.
//
// It is not the document a product archive carries. productbuild rewrites a
// distribution when it embeds one: the embedded form declares standalone,
// fills the leading pkg-ref stubs in with a bundle-version element, names
// each package as an entry inside the archive with a leading "#", and adds
// installKBytes and updateKBytes. The file form does none of that, and
// names each package by its base name however the path was spelled.
func SynthesizeDistribution(o ProductOptions) ([]byte, error) {
	if len(o.Packages) == 0 {
		return nil, fmt.Errorf("flatpkg: at least one component package is required")
	}
	refs, err := synthRefsFor(o.Packages)
	if err != nil {
		return nil, err
	}
	return synthesiseDistribution(o, refs, false), nil
}

// synthRefsFor reads the identity of each component package.
func synthRefsFor(paths []string) ([]synthRef, error) {
	refs := make([]synthRef, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		p, err := Open(path)
		if err != nil {
			return nil, fmt.Errorf("flatpkg: %s: %w", path, err)
		}
		if p.Kind != KindComponent {
			p.Close()
			return nil, fmt.Errorf("flatpkg: %s is a product archive; only component packages can be embedded", path)
		}
		name := filepath.Base(path)
		if seen[name] {
			p.Close()
			return nil, fmt.Errorf("flatpkg: two packages are both named %s", name)
		}
		seen[name] = true
		info := p.Components[0].Info
		r := synthRef{ID: info.Identifier, Version: info.Version, Path: name}
		if info.Payload != nil {
			r.InstallKBytes = info.Payload.InstallKBytes
		}
		p.Close()
		refs = append(refs, r)
	}
	return refs, nil
}

// ResolvePackagePaths finds the component packages a Distribution names.
//
// A distribution refers to its packages by file name, as "#Foo.pkg", and
// productbuild looks for each in the directories given with --package-path
// and in the working directory. This does the same, in that order, and says
// which name it could not find rather than failing later on a missing entry.
func ResolvePackagePaths(dist []byte, searchPaths []string) ([]string, error) {
	d, err := ParseDistribution(dist)
	if err != nil {
		return nil, err
	}
	dirs := append(append([]string{}, searchPaths...), ".")
	var out []string
	for _, name := range d.PackagePaths() {
		found := ""
		for _, dir := range dirs {
			candidate := filepath.Join(dir, name)
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
				found = candidate
				break
			}
		}
		if found == "" {
			return nil, fmt.Errorf("flatpkg: the distribution names %s, which is not in %s", name, strings.Join(dirs, ", "))
		}
		out = append(out, found)
	}
	return out, nil
}

// xmlDeclLine matches the declaration a Distribution starts with.
var xmlDeclLine = regexp.MustCompile(`^<\?xml[^>]*\?>`)

// choiceElement matches a <choice> and everything in it. The pkg-ref
// elements inside a choice refer to a package; the ones outside declare it.
// Only the declarations are rewritten.
var choiceElement = regexp.MustCompile(`(?s)<choice\b[^>]*>.*?</choice>`)

// namingPkgRef matches a pkg-ref that carries a package's path as its text.
var namingPkgRef = regexp.MustCompile(`(?s)<pkg-ref\s+id="([^"]*)"([^>]*)>([^<]*)</pkg-ref>`)

// emptyPkgRef matches the bare stub a synthesised distribution declares each
// package with, before productbuild fills it in.
var emptyPkgRef = regexp.MustCompile(`<pkg-ref\s+id="([^"]*)"\s*/>`)

// embedDistribution turns a Distribution written for productbuild into the
// one a product archive carries, which is not the same document.
//
// productbuild rewrites a distribution as it embeds it, and the rewrite is
// small and mechanical:
//
//   - the XML declaration gains standalone="yes";
//   - every pkg-ref that names a package gains installKBytes and
//     updateKBytes, and its path gains a leading "#", which is what turns
//     "component-basic.pkg" from a file beside the distribution into an
//     entry inside the archive;
//   - every package ends up declared by a stub carrying a bundle-version.
//     A synthesised document already has a bare <pkg-ref id="X"/> for each,
//     which is filled in where it stands; a hand-written one usually has
//     none, and the stubs are appended at the end.
//
// The pkg-ref elements inside a choice are left alone: those refer to a
// package the document declares elsewhere, and filling them in would change
// what the choice installs.
//
// Two more things follow from productbuild re-serialising the document
// rather than editing it, and both are reproduced so the result compares
// equal: a bare ">" in character data comes back escaped, and the trailing
// newline goes. Neither changes what the Installer reads. Everything else
// is left exactly as the author wrote it.
func embedDistribution(dist []byte, refs []synthRef) []byte {
	// Keyed by the package's file name, not by the pkg-ref's id. The two
	// need not agree: Google's Go installer declares
	// id="org.golang.go.pkg" for a package whose identifier is
	// org.golang.go, and gives the identifier separately as
	// packageIdentifier. The name in the element's text is what actually
	// ties a reference to a package.
	byPath := make(map[string]synthRef, len(refs))
	for _, r := range refs {
		byPath[r.Path] = r
	}
	// A document that already names archive entries is one this tool wrote
	// or read out of a package, not one written for productbuild. Rewriting
	// it again would change it: Apple's own distributions do not all carry
	// updateKBytes, and adding one would break the round trip of an
	// existing package. Leave it exactly as it is.
	if alreadyEmbedded(string(dist), byPath) {
		return dist
	}

	out := string(dist)
	if loc := xmlDeclLine.FindStringIndex(out); loc != nil {
		out = `<?xml version="1.0" encoding="utf-8" standalone="yes"?>` + out[loc[1]:]
	}

	// The ids the document uses to declare its packages, in document
	// order. A stub is written for each, using the id the document chose
	// rather than the package's own identifier. Gathering them first is
	// what lets the rewrite tell a declaration's own stub from a bare
	// reference inside a choice that happens to share the id.
	declared := declaredIDs(out, byPath)
	stubbed := map[string]bool{}
	rewrite := func(segment string) string {
		segment = namingPkgRef.ReplaceAllStringFunc(segment, func(m string) string {
			sub := namingPkgRef.FindStringSubmatch(m)
			id, attrs, body := sub[1], sub[2], sub[3]
			path := strings.TrimSpace(body)
			r, ok := byPath[strings.TrimPrefix(path, "#")]
			if !ok || path == "" {
				return m
			}
			if strings.HasPrefix(path, "#") {
				// Already an archive entry, so already rewritten.
				return m
			}
			path = "#" + path
			if !strings.Contains(attrs, "installKBytes=") {
				attrs += ` installKBytes="` + strconv.Itoa(r.InstallKBytes) + `"`
			}
			if !strings.Contains(attrs, "updateKBytes=") {
				attrs += ` updateKBytes="0"`
			}
			return `<pkg-ref id="` + id + `"` + attrs + `>` + path + `</pkg-ref>`
		})
		return emptyPkgRef.ReplaceAllStringFunc(segment, func(m string) string {
			id := emptyPkgRef.FindStringSubmatch(m)[1]
			if !declaredID(declared, id) {
				return m
			}
			stubbed[id] = true
			return `<pkg-ref id="` + id + `">` + "\n        <bundle-version/>\n    </pkg-ref>"
		})
	}

	out = outsideChoices(out, rewrite)

	// A stub already written as <pkg-ref id="X"><bundle-version/></pkg-ref>
	// counts too, so a document embedded twice does not grow.
	for _, id := range declared {
		if stubbed[id] {
			continue
		}
		existing := regexp.MustCompile(`(?s)<pkg-ref\s+id="` + regexp.QuoteMeta(id) + `"\s*>\s*<bundle-version\s*/>`)
		if existing.MatchString(out) {
			stubbed[id] = true
		}
	}
	var b strings.Builder
	for _, id := range declared {
		if stubbed[id] {
			continue
		}
		fmt.Fprintf(&b, "    <pkg-ref id=%q>\n        <bundle-version/>\n    </pkg-ref>\n", id)
	}
	if b.Len() > 0 {
		const closing = "</installer-gui-script>"
		if i := strings.LastIndex(out, closing); i >= 0 {
			out = out[:i] + b.String() + out[i:]
		}
	}
	return []byte(strings.TrimRight(escapeTextGT(out), "\n"))
}

// escapeTextGT escapes a bare ">" wherever it appears in character data.
//
// It is legal unescaped, and a hand-written distribution often has one in a
// JavaScript comparison, but Apple's serialiser always writes "&gt;" and
// these documents are compared with its output. Tags, comments and CDATA
// sections are copied through untouched: inside them a ">" is structure or
// is already exempt.
func escapeTextGT(doc string) string {
	var b strings.Builder
	b.Grow(len(doc))
	for i := 0; i < len(doc); {
		switch {
		case strings.HasPrefix(doc[i:], "<!--"):
			i += copyUntil(&b, doc[i:], "-->")
		case strings.HasPrefix(doc[i:], "<![CDATA["):
			i += copyUntil(&b, doc[i:], "]]>")
		case doc[i] == '<':
			i += copyUntil(&b, doc[i:], ">")
		case doc[i] == '>':
			b.WriteString("&gt;")
			i++
		default:
			b.WriteByte(doc[i])
			i++
		}
	}
	return b.String()
}

// copyUntil writes s up to and including the first end marker, or all of s
// when there is none, and reports how much it wrote.
func copyUntil(b *strings.Builder, s, end string) int {
	n := strings.Index(s, end)
	if n < 0 {
		b.WriteString(s)
		return len(s)
	}
	n += len(end)
	b.WriteString(s[:n])
	return n
}

// outsideChoices applies transform to the parts of a document that are not
// inside a <choice> element, and leaves the choices untouched.
func outsideChoices(doc string, transform func(string) string) string {
	spans := choiceElement.FindAllStringIndex(doc, -1)
	var b strings.Builder
	prev := 0
	for _, span := range spans {
		b.WriteString(transform(doc[prev:span[0]]))
		b.WriteString(doc[span[0]:span[1]])
		prev = span[1]
	}
	b.WriteString(transform(doc[prev:]))
	return b.String()
}

// alreadyEmbedded reports whether a Distribution already names its packages
// as entries inside an archive, which is what embedding produces.
//
// It is true only when every package the archive carries is declared that
// way, so a document part-way between the two forms is still rewritten.
func alreadyEmbedded(doc string, byPath map[string]synthRef) bool {
	found := map[string]bool{}
	for _, m := range namingPkgRef.FindAllStringSubmatch(doc, -1) {
		body := strings.TrimSpace(m[3])
		name := strings.TrimPrefix(body, "#")
		if _, ok := byPath[name]; !ok || body == "" {
			continue
		}
		if !strings.HasPrefix(body, "#") {
			return false
		}
		found[name] = true
	}
	return len(found) == len(byPath)
}

// declaredIDs returns the ids the document declares its packages with, in
// document order and without repeats. A pkg-ref declares a package when its
// text names one; the same id appearing inside a choice only refers to it.
func declaredIDs(doc string, byPath map[string]synthRef) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range namingPkgRef.FindAllStringSubmatch(doc, -1) {
		id, path := m[1], strings.TrimSpace(m[3])
		if path == "" || seen[id] {
			continue
		}
		if _, ok := byPath[strings.TrimPrefix(path, "#")]; !ok {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// declaredID reports whether id is one the document uses to declare a
// package, rather than one that only appears inside a choice.
func declaredID(declared []string, id string) bool {
	for _, d := range declared {
		if d == id {
			return true
		}
	}
	return false
}

// inlineLocation is what a one-step mode knows about the component it built
// that reading the component back would not tell the Distribution.
type inlineLocation struct {
	installPath  string
	bundle       *Bundle
	shortVersion string
	title        string
}

// buildInlineComponents packages whatever --root, --content and --component
// named, and returns the component packages, what the Distribution needs to
// say about each, and a function to clean up the scratch files.
//
// productbuild names each of these components for itself: a root or a
// content directory takes the output's own base name, and a bundle takes its
// identifier. The version is 0 for a root or a directory, since there is
// nothing to read one from, and the bundle's own for a bundle.
func buildInlineComponents(o *ProductOptions, archiveTime time.Time) (paths []string, locations map[string]inlineLocation, cleanup func(), err error) {
	locations = map[string]inlineLocation{}
	cleanup = func() {}
	if o.Root == "" && o.Content == "" && len(o.Components) == 0 {
		return nil, locations, cleanup, nil
	}

	scratch := o.TempDir
	if scratch == "" {
		scratch = os.TempDir()
	}
	dir, err := os.MkdirTemp(scratch, "macospkg-inline-*")
	if err != nil {
		return nil, nil, cleanup, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	build := func(co ComponentOptions, name string, loc inlineLocation) error {
		out := filepath.Join(dir, name)
		f, ferr := os.Create(out)
		if ferr != nil {
			return ferr
		}
		defer f.Close()
		co.Epoch = o.Epoch
		co.TempDir = o.TempDir
		co.GeneratorVersion = o.GeneratorVersion
		if _, berr := BuildComponent(co, f); berr != nil {
			return berr
		}
		paths = append(paths, out)
		locations[name] = loc
		return nil
	}

	// A root and a content directory are the same build with a different
	// default install location, and both are named after the output.
	for _, m := range []struct {
		dir, installLocation string
		custom               bool
	}{
		{o.Root, o.RootInstallPath, true},
		{o.Content, "", false},
	} {
		if m.dir == "" {
			continue
		}
		// Both take the source directory's own name, not the archive's.
		name := filepath.Base(filepath.Clean(m.dir))
		loc := inlineLocation{}
		if m.custom {
			loc.installPath = m.installLocation
		}
		if err = build(ComponentOptions{
			Root:            m.dir,
			Identifier:      name,
			Version:         "0",
			InstallLocation: m.installLocation,
		}, name+".pkg", loc); err != nil {
			return nil, nil, cleanup, err
		}
	}

	for _, c := range o.Components {
		id, ierr := InferFromBundle(c.Path)
		if ierr != nil {
			return nil, nil, cleanup, ierr
		}
		bundles, berr := findBundles(filepath.Dir(filepath.Clean(c.Path)))
		if berr != nil {
			return nil, nil, cleanup, berr
		}
		var b *Bundle
		for i := range bundles {
			if rootRelative(bundles[i].Path) == filepath.Base(filepath.Clean(c.Path)) {
				// The Distribution names the bundle by its path within
				// the payload, with no leading "./".
				copyOf := bundles[i]
				copyOf.Path = rootRelative(copyOf.Path)
				b = &copyOf
				break
			}
		}
		loc := inlineLocation{installPath: c.InstallPath, bundle: b, title: id.Name}
		if b != nil {
			loc.shortVersion = b.CFBundleShortVersionString
		}
		if err = build(ComponentOptions{
			Components:      []string{c.Path},
			Identifier:      id.Identifier,
			Version:         id.Version,
			InstallLocation: c.InstallPath,
		}, id.Identifier+".pkg", loc); err != nil {
			return nil, nil, cleanup, err
		}
	}
	return paths, locations, cleanup, nil
}
