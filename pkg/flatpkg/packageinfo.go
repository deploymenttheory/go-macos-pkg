// PackageInfo: the XML manifest of a component package.
//
// The element and attribute names are Apple's, kebab-case and camelCase
// mixed exactly as pkgbuild writes them; the Go names are the same words.
package flatpkg

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"regexp"
)

// PackageInfo mirrors the <pkg-info> document.
type PackageInfo struct {
	XMLName xml.Name `xml:"pkg-info"`

	// Field order is the order pkgbuild writes the attributes in, because
	// encoding/xml emits attributes in field order and the documents are
	// compared byte for byte against pkgbuild's. Every fixture in
	// testdata/cli and both real-world oracles agree on it, across
	// InstallCmds 834, 860.14 and 864.12.
	OverwritePermissions *bool  `xml:"overwrite-permissions,attr,omitempty"`
	Relocatable          *bool  `xml:"relocatable,attr,omitempty"`
	Identifier           string `xml:"identifier,attr"`
	PostinstallAction    string `xml:"postinstall-action,attr,omitempty"` // none | logout | restart | shutdown
	Version              string `xml:"version,attr"`
	FormatVersion        int    `xml:"format-version,attr"`
	GeneratorVersion     string `xml:"generator-version,attr,omitempty"`
	InstallLocation      string `xml:"install-location,attr,omitempty"`
	Auth                 string `xml:"auth,attr,omitempty"` // none | root
	MinimumSystemVersion string `xml:"minimumSystemVersion,attr,omitempty"`

	// No fixture carries either of these, so their position is ours.
	PreserveXattr         *bool `xml:"preserve-xattr,attr,omitempty"`
	UseHFSPlusCompression *bool `xml:"useHFSPlusCompression,attr,omitempty"`

	// Element order is pkgbuild's: payload, bundle, the bundle lists,
	// then scripts.
	Payload *Payload `xml:"payload"`

	// Bundles describes each bundle in the payload (path, identifier,
	// versions); the lists below refer to them by id. Older pkgbuild
	// versions put the details inside bundle-version instead, which the
	// reader accepts too.
	Bundles []Bundle `xml:"bundle"`

	BundleVersion      *BundleList `xml:"bundle-version"`
	UpgradeBundle      *BundleRefs `xml:"upgrade-bundle"`
	UpdateBundle       *BundleRefs `xml:"update-bundle"`
	AtomicUpdateBundle *BundleRefs `xml:"atomic-update-bundle"`
	StrictIdentifier   *BundleRefs `xml:"strict-identifier"`
	Relocate           *BundleRefs `xml:"relocate"`

	Scripts *Scripts `xml:"scripts"`

	// Raw is the document as read, kept so an expand writes what it saw.
	Raw []byte `xml:"-"`
}

// Payload summarises the payload: how many entries and how many kilobytes
// they occupy once installed.
type Payload struct {
	// LargeSegmented marks a --large-payload package, whose archive entry
	// is LargeSegmentedPayload rather than Payload. pkgbuild writes it
	// first, before the counts, so it is declared first here.
	LargeSegmented string `xml:"large-segmented,attr,omitempty"`

	NumberOfFiles int `xml:"numberOfFiles,attr"`
	InstallKBytes int `xml:"installKBytes,attr"`
}

// Scripts names the install scripts carried in the Scripts archive.
type Scripts struct {
	Preinstall  *Script `xml:"preinstall"`
	Postinstall *Script `xml:"postinstall"`
	Preflight   *Script `xml:"preflight"`
	Postflight  *Script `xml:"postflight"`
	Preupgrade  *Script `xml:"preupgrade"`
	Postupgrade *Script `xml:"postupgrade"`
}

// Script is one script reference: the file is relative to the Scripts
// archive root, conventionally "./preinstall".
type Script struct {
	File        string `xml:"file,attr"`
	ComponentID string `xml:"component-id,attr,omitempty"`
	// Timeout is written by recent pkgbuild versions (seconds).
	Timeout string `xml:"timeout,attr,omitempty"`
}

// Names lists the scripts present, in Apple's canonical order.
func (s *Scripts) Names() []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, p := range []struct {
		name string
		s    *Script
	}{
		{"preflight", s.Preflight}, {"preinstall", s.Preinstall}, {"preupgrade", s.Preupgrade},
		{"postinstall", s.Postinstall}, {"postupgrade", s.Postupgrade}, {"postflight", s.Postflight},
	} {
		if p.s != nil {
			out = append(out, p.name)
		}
	}
	return out
}

// BundleList is the <bundle-version> element: the bundles the payload
// contains, which the Installer uses for version checks and relocation.
type BundleList struct {
	Bundles []Bundle `xml:"bundle"`
}

// Bundle describes one bundle in the payload. Attribute order is
// pkgbuild's.
type Bundle struct {
	Path                       string `xml:"path,attr,omitempty"`
	ID                         string `xml:"id,attr"`
	CFBundleIdentifier         string `xml:"CFBundleIdentifier,attr,omitempty"`
	CFBundleShortVersionString string `xml:"CFBundleShortVersionString,attr,omitempty"`
	CFBundleVersion            string `xml:"CFBundleVersion,attr,omitempty"`
	SearchPath                 string `xml:"search,attr,omitempty"`
}

// BundleRefs is a list of <bundle id="..."/> references.
type BundleRefs struct {
	Bundles []BundleRef `xml:"bundle"`
}

// BundleRef refers to a bundle in BundleList by id.
type BundleRef struct {
	ID string `xml:"id,attr"`
}

// ParsePackageInfo decodes a PackageInfo document.
func ParsePackageInfo(data []byte) (*PackageInfo, error) {
	var info PackageInfo
	if err := xml.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("flatpkg: unable to parse PackageInfo: %w", err)
	}
	info.Raw = append([]byte(nil), data...)
	return &info, nil
}

// Marshal encodes the PackageInfo as pkgbuild would: an XML declaration,
// four-space indentation, empty elements self-closed.
func (p *PackageInfo) Marshal() ([]byte, error) {
	body, err := xml.MarshalIndent(p, "", "    ")
	if err != nil {
		return nil, fmt.Errorf("flatpkg: unable to encode PackageInfo: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	buf.Write(selfClose(body))
	// No trailing newline: pkgbuild's PackageInfo ends at the closing tag,
	// and these documents are compared with its output byte for byte.
	return buf.Bytes(), nil
}

// emptyElement matches <tag attrs></tag> with nothing between.
var emptyElement = regexp.MustCompile(`<([A-Za-z][\w.-]*)((?:\s[^<>]*)?)></([A-Za-z][\w.-]*)>`)

// selfClose rewrites empty elements as <tag attrs/>, which is what
// Apple's tools write and what encoding/xml never does.
func selfClose(xml []byte) []byte {
	return emptyElement.ReplaceAllFunc(xml, func(m []byte) []byte {
		sub := emptyElement.FindSubmatch(m)
		if string(sub[1]) != string(sub[3]) {
			return m
		}
		return []byte("<" + string(sub[1]) + string(sub[2]) + "/>")
	})
}

// AllBundles returns the bundles the PackageInfo describes, with path and
// version details: from the top-level bundle elements when present, else
// from bundle-version (the older layout).
func (p *PackageInfo) AllBundles() []Bundle {
	byID := map[string]Bundle{}
	var order []string
	add := func(b Bundle) {
		if b.ID == "" {
			return
		}
		if have, ok := byID[b.ID]; ok {
			if have.Path == "" {
				have.Path = b.Path
			}
			if have.CFBundleShortVersionString == "" {
				have.CFBundleShortVersionString = b.CFBundleShortVersionString
			}
			if have.CFBundleVersion == "" {
				have.CFBundleVersion = b.CFBundleVersion
			}
			byID[b.ID] = have
			return
		}
		byID[b.ID] = b
		order = append(order, b.ID)
	}
	for _, b := range p.Bundles {
		add(b)
	}
	if p.BundleVersion != nil {
		for _, b := range p.BundleVersion.Bundles {
			add(b)
		}
	}
	out := make([]Bundle, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// BoolPtr returns a pointer to b, for the optional boolean attributes.
func BoolPtr(b bool) *bool { return &b }
