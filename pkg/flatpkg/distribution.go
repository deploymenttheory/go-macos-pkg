// Distribution: the XML script of a product archive, which the Installer
// runs to decide what to show and what to install.
//
// The document is parsed into a model for inspection, but the raw bytes are
// kept alongside: a Distribution carries JavaScript, localisations and
// elements this model does not enumerate, and everything that copies one
// must copy it whole.
package flatpkg

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// Distribution mirrors <installer-gui-script>.
type Distribution struct {
	XMLName xml.Name `xml:"installer-gui-script"`

	MinSpecVersion string `xml:"minSpecVersion,attr,omitempty"`

	Title          string          `xml:"title,omitempty"`
	Options        *DistOptions    `xml:"options"`
	Domains        *DistDomains    `xml:"domains"`
	Product        *DistProduct    `xml:"product"`
	Welcome        *DistResource   `xml:"welcome"`
	Readme         *DistResource   `xml:"readme"`
	License        *DistResource   `xml:"license"`
	Conclusion     *DistResource   `xml:"conclusion"`
	Background     *DistBackground `xml:"background"`
	BackgroundDark *DistBackground `xml:"background-darkAqua"`

	VolumeCheck       *DistCheck `xml:"volume-check"`
	InstallationCheck *DistCheck `xml:"installation-check"`

	ChoicesOutline *ChoicesOutline `xml:"choices-outline"`
	Choices        []Choice        `xml:"choice"`
	PkgRefs        []PkgRef        `xml:"pkg-ref"`
	Scripts        []string        `xml:"script"`

	// Raw is the document as read.
	Raw []byte `xml:"-"`
}

// DistOptions is <options>.
type DistOptions struct {
	Customize            string `xml:"customize,attr,omitempty"` // never | always | allow
	RequireScripts       string `xml:"require-scripts,attr,omitempty"`
	HostArchitectures    string `xml:"hostArchitectures,attr,omitempty"`
	RootVolumeOnly       string `xml:"rootVolumeOnly,attr,omitempty"`
	AllowExternalScripts string `xml:"allow-external-scripts,attr,omitempty"`
	MPKG                 string `xml:"mpkg,attr,omitempty"`
}

// Architectures splits hostArchitectures.
func (o *DistOptions) Architectures() []string {
	if o == nil || o.HostArchitectures == "" {
		return nil
	}
	var out []string
	for _, a := range strings.Split(o.HostArchitectures, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// DistDomains is <domains>.
type DistDomains struct {
	EnableAnywhere        string `xml:"enable_anywhere,attr,omitempty"`
	EnableCurrentUserHome string `xml:"enable_currentUserHome,attr,omitempty"`
	EnableLocalSystem     string `xml:"enable_localSystem,attr,omitempty"`
}

// DistProduct is <product>.
type DistProduct struct {
	ID      string `xml:"id,attr"`
	Version string `xml:"version,attr,omitempty"`
}

// DistResource is a welcome/readme/license/conclusion element.
type DistResource struct {
	File     string `xml:"file,attr,omitempty"`
	MimeType string `xml:"mime-type,attr,omitempty"`
	UTI      string `xml:"uti,attr,omitempty"`
	Language string `xml:"language,attr,omitempty"`
}

// DistBackground is <background> or <background-darkAqua>.
type DistBackground struct {
	File      string `xml:"file,attr,omitempty"`
	MimeType  string `xml:"mime-type,attr,omitempty"`
	UTI       string `xml:"uti,attr,omitempty"`
	Alignment string `xml:"alignment,attr,omitempty"`
	Scaling   string `xml:"scaling,attr,omitempty"`
}

// DistCheck is <volume-check> or <installation-check>.
type DistCheck struct {
	Script            string             `xml:"script,attr,omitempty"`
	AllowedOSVersions *AllowedOSVersions `xml:"allowed-os-versions"`
}

// AllowedOSVersions is <allowed-os-versions>.
type AllowedOSVersions struct {
	Versions []OSVersion `xml:"os-version"`
}

// OSVersion is <os-version min="..." before="..."/>.
type OSVersion struct {
	Min    string `xml:"min,attr,omitempty"`
	Before string `xml:"before,attr,omitempty"`
}

// ChoicesOutline is <choices-outline>: the tree of lines the Installer
// shows on its customise pane.
type ChoicesOutline struct {
	Lines []OutlineLine `xml:"line"`
}

// OutlineLine is one <line choice="..."> with optional nested lines.
type OutlineLine struct {
	Choice string        `xml:"choice,attr"`
	Lines  []OutlineLine `xml:"line"`
}

// Choice is <choice>.
type Choice struct {
	ID            string   `xml:"id,attr"`
	Title         string   `xml:"title,attr,omitempty"`
	Description   string   `xml:"description,attr,omitempty"`
	Visible       string   `xml:"visible,attr,omitempty"`
	Enabled       string   `xml:"enabled,attr,omitempty"`
	Selected      string   `xml:"selected,attr,omitempty"`
	StartSelected string   `xml:"start_selected,attr,omitempty"`
	StartEnabled  string   `xml:"start_enabled,attr,omitempty"`
	StartVisible  string   `xml:"start_visible,attr,omitempty"`
	PkgRefs       []PkgRef `xml:"pkg-ref"`
}

// PkgRef is <pkg-ref>. Inside a choice it only carries an id; at the top
// level it carries the package's attributes and, as its text, the package
// path within the archive (for example "#base.pkg").
type PkgRef struct {
	ID                string      `xml:"id,attr"`
	Version           string      `xml:"version,attr,omitempty"`
	Auth              string      `xml:"auth,attr,omitempty"`
	OnConclusion      string      `xml:"onConclusion,attr,omitempty"`
	InstallKBytes     string      `xml:"installKBytes,attr,omitempty"`
	PackageIdentifier string      `xml:"packageIdentifier,attr,omitempty"`
	Path              string      `xml:",chardata"`
	MustClose         *MustClose  `xml:"must-close"`
	BundleVersion     *BundleList `xml:"bundle-version"`
}

// MustClose lists applications that must quit before installing.
type MustClose struct {
	Apps []struct {
		ID string `xml:"id,attr"`
	} `xml:"app"`
}

// ParseDistribution decodes a Distribution document. Legacy PackageMaker
// files use <installer-script> as the root; it is accepted.
func ParseDistribution(data []byte) (*Distribution, error) {
	var d Distribution
	trimmed := data
	if i := strings.Index(string(data), "<installer-script"); i >= 0 && !strings.Contains(string(data), "<installer-gui-script") {
		s := strings.Replace(string(data), "<installer-script", "<installer-gui-script", 1)
		s = strings.Replace(s, "</installer-script>", "</installer-gui-script>", 1)
		trimmed = []byte(s)
	}
	if err := xml.Unmarshal(trimmed, &d); err != nil {
		return nil, fmt.Errorf("flatpkg: unable to parse Distribution: %w", err)
	}
	d.Raw = append([]byte(nil), data...)
	return &d, nil
}

// PackagePaths returns the archive-relative package paths referenced by
// the top-level pkg-refs, with the leading "#" removed and any file: prefix
// stripped, in document order and without duplicates.
func (d *Distribution) PackagePaths() []string {
	seen := map[string]bool{}
	var out []string
	for _, ref := range d.PkgRefs {
		p := strings.TrimSpace(ref.Path)
		if p == "" {
			continue
		}
		p = strings.TrimPrefix(p, "#")
		p = strings.TrimPrefix(p, "file:./")
		p = strings.TrimPrefix(p, "file:")
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// ChoiceIDs returns the ids of every choice in document order.
func (d *Distribution) ChoiceIDs() []string {
	out := make([]string, 0, len(d.Choices))
	for _, c := range d.Choices {
		out = append(out, c.ID)
	}
	return out
}

// Marshal encodes the Distribution with an XML declaration and four-space
// indentation, for documents this tool synthesises.
func (d *Distribution) Marshal() ([]byte, error) {
	body, err := xml.MarshalIndent(d, "", "    ")
	if err != nil {
		return nil, fmt.Errorf("flatpkg: unable to encode Distribution: %w", err)
	}
	return append(append([]byte(`<?xml version="1.0" encoding="utf-8"?>`+"\n"), selfClose(body)...), '\n'), nil
}
