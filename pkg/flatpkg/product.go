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
	if len(o.Packages) == 0 {
		return nil, fmt.Errorf("flatpkg: at least one component package is required")
	}
	archiveTime := time.Now()
	if !o.Epoch.IsZero() {
		archiveTime = o.Epoch
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

	res := &ProductResult{}
	dist := o.Distribution
	if dist == nil {
		var refs []synthRef
		for _, c := range components {
			info := c.pkg.Components[0].Info
			r := synthRef{ID: info.Identifier, Version: info.Version, Path: c.name}
			if info.Payload != nil {
				r.InstallKBytes = info.Payload.InstallKBytes
			}
			refs = append(refs, r)
		}
		dist = synthesiseDistribution(o, refs)
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

// synthRef is what a synthesised Distribution needs to know per package.
type synthRef struct {
	ID            string
	Version       string
	Path          string
	InstallKBytes int
}

// synthesiseDistribution writes the Distribution that productbuild puts
// inside a product archive when it is not given one: one hidden choice per
// package, all selected, no customisation.
//
// The shape is productbuild's, byte for byte, and it is worth being precise
// about which shape that is. "productbuild --synthesize" writes a slightly
// different document to a file: no standalone attribute, bare <pkg-ref id/>
// stubs, bare package paths, and no size attributes. When productbuild then
// embeds a distribution into an archive it rewrites it, and that rewritten
// form is what a package actually carries and what this function produces:
// standalone="yes", stubs carrying <bundle-version/>, "#name.pkg" paths, and
// installKBytes plus updateKBytes on every reference.
//
// Two attributes are deliberately absent because productbuild does not write
// them either: auth, which every component's PackageInfo carries but which
// never reaches the Distribution, and a trailing newline.
func synthesiseDistribution(o ProductOptions, refs []synthRef) []byte {
	var b strings.Builder
	attr := func(v string) string { return `"` + xmlEscape(v) + `"` }

	b.WriteString(`<?xml version="1.0" encoding="utf-8" standalone="yes"?>` + "\n")
	b.WriteString(`<installer-gui-script minSpecVersion="1">` + "\n")
	if o.Title != "" {
		fmt.Fprintf(&b, "    <title>%s</title>\n", xmlEscape(o.Title))
	}
	if o.ProductID != "" {
		fmt.Fprintf(&b, "    <product id=%s", attr(o.ProductID))
		if o.ProductVersion != "" {
			fmt.Fprintf(&b, " version=%s", attr(o.ProductVersion))
		}
		b.WriteString("/>\n")
	}

	// The stubs productbuild writes ahead of <options>, one per package.
	for _, r := range refs {
		fmt.Fprintf(&b, "    <pkg-ref id=%s>\n        <bundle-version/>\n    </pkg-ref>\n", attr(r.ID))
	}

	archs := o.HostArchitectures
	if len(archs) == 0 {
		archs = DefaultHostArchitectures
	}
	fmt.Fprintf(&b, "    <options customize=\"never\" require-scripts=\"false\" hostArchitectures=%s/>\n",
		attr(strings.Join(archs, ",")))

	if o.MinOSVersion != "" {
		fmt.Fprintf(&b, "    <volume-check>\n        <allowed-os-versions>\n            <os-version min=%s/>\n        </allowed-os-versions>\n    </volume-check>\n",
			attr(o.MinOSVersion))
	}

	// One default line wrapping every package, not one wrapper per package.
	b.WriteString("    <choices-outline>\n        <line choice=\"default\">\n")
	for _, r := range refs {
		fmt.Fprintf(&b, "            <line choice=%s/>\n", attr(r.ID))
	}
	b.WriteString("        </line>\n    </choices-outline>\n")
	b.WriteString("    <choice id=\"default\"/>\n")

	// Each choice is followed by its own pkg-ref, interleaved.
	for _, r := range refs {
		fmt.Fprintf(&b, "    <choice id=%s visible=\"false\">\n        <pkg-ref id=%s/>\n    </choice>\n", attr(r.ID), attr(r.ID))
		fmt.Fprintf(&b, "    <pkg-ref id=%s version=%s onConclusion=\"none\" installKBytes=%s updateKBytes=\"0\">#%s</pkg-ref>\n",
			attr(r.ID), attr(r.Version), attr(strconv.Itoa(r.InstallKBytes)), xmlEscape(r.Path))
	}

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
