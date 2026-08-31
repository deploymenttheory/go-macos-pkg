// Package flatpkg reads and writes macOS flat installer packages: a xar
// archive holding, for a component package, PackageInfo, Bom, Payload and
// Scripts at its root, or, for a product archive, a Distribution with the
// component packages nested in directories beneath it.
//
// The names in this package follow Apple's tools: "component package" is
// what pkgbuild makes, "product archive" what productbuild makes, and the
// file that scripts the latter is the Distribution.
package flatpkg

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// Kind is the flavour of flat package.
type Kind string

// Package kinds.
const (
	// KindComponent is a single component package: pkgbuild's output.
	KindComponent Kind = "component"
	// KindProduct is a product archive with a Distribution: productbuild's
	// output.
	KindProduct Kind = "product"
)

// Entry names Apple uses inside the archive.
const (
	EntryPackageInfo = "PackageInfo"
	EntryBom         = "Bom"
	EntryPayload     = "Payload"
	// EntryLargePayload is what pkgbuild --large-payload writes instead of
	// Payload: the same gzip cpio, allowed to exceed 8 GiB, with
	// large-segmented="true" on the PackageInfo payload element.
	EntryLargePayload = "LargeSegmentedPayload"
	EntryScripts      = "Scripts"
	EntryDistribution = "Distribution"
	EntryResources    = "Resources"
	// EntryPlugins is what productbuild --plugins writes: a gzip cpio of
	// the Installer plug-in bundles and their InstallerSections.plist.
	EntryPlugins = "PlugIns"
)

// ErrNotPackage reports a xar archive that is not a flat package.
var ErrNotPackage = errors.New("flatpkg: xar archive is not a flat installer package")

// Package is an opened flat package.
type Package struct {
	Path string
	Kind Kind
	XAR  *xar.Reader

	// Components are the component packages, in archive order. A component
	// package has exactly one, named "".
	Components []*Component

	// Distribution is set for a product archive.
	Distribution *Distribution
}

// Component is one component package inside the archive.
type Component struct {
	// Name is the directory holding the component ("foo.pkg"), or "" for
	// the root of a component package.
	Name string
	Info *PackageInfo

	packageInfo *xar.File
	bomEntry    *xar.File
	payload     *xar.File
	scripts     *xar.File

	pkg *Package
	bom *bom.BOM
}

// Open opens the package at path.
func Open(path string) (*Package, error) {
	x, err := xar.OpenFile(path)
	if err != nil {
		if errors.Is(err, xar.ErrNotXar) || os.IsNotExist(err) {
			return nil, err
		}
		return nil, err
	}
	p, err := FromXAR(x)
	if err != nil {
		x.Close()
		return nil, err
	}
	p.Path = path
	return p, nil
}

// FromXAR interprets an opened xar archive as a flat package.
func FromXAR(x *xar.Reader) (*Package, error) {
	p := &Package{XAR: x}

	// A product archive is recognised by its Distribution; anything else
	// must be a component package with a PackageInfo at the root.
	if dist := x.Lookup(EntryDistribution); dist != nil && !dist.IsDir() {
		p.Kind = KindProduct
		data, err := readEntry(x, dist)
		if err != nil {
			return nil, err
		}
		d, err := ParseDistribution(data)
		if err != nil {
			return nil, err
		}
		p.Distribution = d
		// Components live in top-level directories, conventionally named
		// *.pkg. Take every top-level directory that holds a PackageInfo,
		// ordered as the Distribution references them, then the rest.
		var names []string
		seen := map[string]bool{}
		for _, ref := range d.PackagePaths() {
			ref = strings.Trim(ref, "/")
			if f := x.Lookup(ref + "/" + EntryPackageInfo); f != nil && !seen[ref] {
				seen[ref] = true
				names = append(names, ref)
			}
		}
		var extra []string
		for _, f := range x.Files() {
			if f.IsDir() && !strings.Contains(f.Path(), "/") && !seen[f.Path()] {
				if x.Lookup(f.Path()+"/"+EntryPackageInfo) != nil {
					seen[f.Path()] = true
					extra = append(extra, f.Path())
				}
			}
		}
		sort.Strings(extra)
		names = append(names, extra...)
		for _, name := range names {
			c, err := p.component(name)
			if err != nil {
				return nil, err
			}
			p.Components = append(p.Components, c)
		}
		return p, nil
	}

	if x.Lookup(EntryPackageInfo) == nil {
		return nil, ErrNotPackage
	}
	p.Kind = KindComponent
	c, err := p.component("")
	if err != nil {
		return nil, err
	}
	p.Components = []*Component{c}
	return p, nil
}

// Close releases the archive.
func (p *Package) Close() error { return p.XAR.Close() }

// Component returns the component with the given name, or nil.
func (p *Package) Component(name string) *Component {
	for _, c := range p.Components {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (p *Package) component(name string) (*Component, error) {
	prefix := ""
	if name != "" {
		prefix = name + "/"
	}
	c := &Component{
		Name:        name,
		pkg:         p,
		packageInfo: p.XAR.Lookup(prefix + EntryPackageInfo),
		bomEntry:    p.XAR.Lookup(prefix + EntryBom),
		payload:     p.XAR.Lookup(prefix + EntryPayload),
		scripts:     p.XAR.Lookup(prefix + EntryScripts),
	}
	if c.payload == nil {
		c.payload = p.XAR.Lookup(prefix + EntryLargePayload)
	}
	if c.packageInfo == nil {
		return nil, fmt.Errorf("flatpkg: component %q has no PackageInfo", name)
	}
	data, err := readEntry(p.XAR, c.packageInfo)
	if err != nil {
		return nil, err
	}
	info, err := ParsePackageInfo(data)
	if err != nil {
		return nil, fmt.Errorf("flatpkg: component %q: %w", name, err)
	}
	c.Info = info
	return c, nil
}

// Entry returns the xar entry for one of the component's files
// (EntryPackageInfo, EntryBom, EntryPayload or EntryScripts), or nil.
func (c *Component) Entry(name string) *xar.File {
	switch name {
	case EntryPackageInfo:
		return c.packageInfo
	case EntryBom:
		return c.bomEntry
	case EntryPayload, EntryLargePayload:
		return c.payload
	case EntryScripts:
		return c.scripts
	}
	return nil
}

// HasPayload reports whether the component carries a Payload archive
// (pkgbuild --nopayload omits it).
func (c *Component) HasPayload() bool { return c.payload != nil }

// PayloadEntryName returns the archive entry name of the payload: Payload,
// or LargeSegmentedPayload for a --large-payload package.
func (c *Component) PayloadEntryName() string {
	if c.payload == nil {
		return ""
	}
	return c.payload.Name()
}

// HasScripts reports whether the component carries a Scripts archive.
func (c *Component) HasScripts() bool { return c.scripts != nil }

// Bom parses and caches the component's bill of materials.
func (c *Component) Bom() (*bom.BOM, error) {
	if c.bom != nil {
		return c.bom, nil
	}
	if c.bomEntry == nil {
		return nil, fmt.Errorf("flatpkg: component %q has no Bom", c.Name)
	}
	rc, err := c.pkg.XAR.Open(c.bomEntry)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := bom.Read(rc)
	if err != nil {
		return nil, fmt.Errorf("flatpkg: component %q: %w", c.Name, err)
	}
	c.bom = b
	return b, nil
}

// OpenPayload returns the decoded Payload bytes: a cpio stream, still
// wrapped in whatever compression pkgbuild applied (gzip or pbzx).
func (c *Component) OpenPayload() (io.ReadCloser, error) {
	if c.payload == nil {
		return nil, fmt.Errorf("flatpkg: component %q has no Payload", c.Name)
	}
	return c.pkg.XAR.Open(c.payload)
}

// OpenScripts returns the decoded Scripts bytes, a gzip cpio stream.
func (c *Component) OpenScripts() (io.ReadCloser, error) {
	if c.scripts == nil {
		return nil, fmt.Errorf("flatpkg: component %q has no Scripts", c.Name)
	}
	return c.pkg.XAR.Open(c.scripts)
}

// readEntry decodes a whole entry into memory. Only used for the small
// metadata entries; payloads are streamed.
func readEntry(x *xar.Reader, f *xar.File) ([]byte, error) {
	rc, err := x.Open(f)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	const limit = 64 << 20
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("flatpkg: unable to read %s: %w", f.Path(), err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("flatpkg: %s is implausibly large", f.Path())
	}
	return data, nil
}

// ReadEntry decodes a named archive entry into memory.
func (p *Package) ReadEntry(path string) ([]byte, error) {
	f := p.XAR.Lookup(path)
	if f == nil {
		return nil, fmt.Errorf("flatpkg: no entry %q", path)
	}
	if f.IsDir() {
		return nil, fmt.Errorf("flatpkg: %q is a directory", path)
	}
	return readEntry(p.XAR, f)
}
