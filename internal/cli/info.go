// macospkg info PKG — package summary.
package cli

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
	"github.com/deploymenttheory/go-macos-pkg/pkg/staple"
	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
	"github.com/spf13/cobra"
)

var infoCmd = &cobra.Command{
	Use:   "info PKG",
	Short: "Package summary: kind, components, payload, signature, staple",
	Long: `Summarise a flat package: whether it is a component package or a product
archive, each component's PackageInfo, the Distribution of a product archive,
the payload container, and whether the archive is signed and stapled.

The signature block reports what the archive carries; use verify to check it.

Examples:
  macospkg info Foo.pkg
  macospkg info -o json Foo.pkg | jq .packages[0].identifier`,
	Args: exactArgs(1, "PKG"),
	RunE: runInfo,
}

// packageSummary is the JSON schema for macospkg info.
type packageSummary struct {
	Path         string             `json:"path"`
	Kind         string             `json:"kind"`
	Size         int64              `json:"size"`
	XAR          xarSummary         `json:"xar"`
	Packages     []componentSummary `json:"packages"`
	Distribution *distSummary       `json:"distribution,omitempty"`
	Signature    signatureSummary   `json:"signature"`
	Staple       stapleSummary      `json:"staple"`
}

type xarSummary struct {
	HeaderSize          int    `json:"headerSize"`
	Version             int    `json:"version"`
	ChecksumAlgorithm   string `json:"checksumAlgorithm"`
	TOCCompressedLength int64  `json:"tocCompressedLength"`
	TOCLength           int64  `json:"tocLength"`
	Entries             int    `json:"entries"`
	CreationTime        string `json:"creationTime,omitempty"`
	TOCDigestValid      bool   `json:"tocDigestValid"`
}

type componentSummary struct {
	Name                 string          `json:"name"`
	Identifier           string          `json:"identifier"`
	Version              string          `json:"version"`
	FormatVersion        int             `json:"formatVersion"`
	InstallLocation      string          `json:"installLocation,omitempty"`
	Auth                 string          `json:"auth,omitempty"`
	OverwritePermissions *bool           `json:"overwritePermissions,omitempty"`
	Relocatable          *bool           `json:"relocatable,omitempty"`
	GeneratorVersion     string          `json:"generatorVersion,omitempty"`
	PostinstallAction    string          `json:"postinstallAction,omitempty"`
	MinimumSystemVersion string          `json:"minimumSystemVersion,omitempty"`
	Payload              *payloadSummary `json:"payload,omitempty"`
	Scripts              []string        `json:"scripts"`
	Bundles              []bundleSummary `json:"bundles"`
}

type payloadSummary struct {
	Entry          string `json:"entry"`
	NumberOfFiles  int    `json:"numberOfFiles"`
	InstallKBytes  int    `json:"installKBytes"`
	Encoding       string `json:"encoding"`
	LargeSegmented bool   `json:"largeSegmented,omitempty"`
}

type bundleSummary struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
}

type distSummary struct {
	Title             string       `json:"title,omitempty"`
	MinSpecVersion    string       `json:"minSpecVersion,omitempty"`
	Customize         string       `json:"customize,omitempty"`
	RequireScripts    string       `json:"requireScripts,omitempty"`
	HostArchitectures []string     `json:"hostArchitectures"`
	RootVolumeOnly    string       `json:"rootVolumeOnly,omitempty"`
	Choices           []choiceInfo `json:"choices"`
	PkgRefs           []pkgRefInfo `json:"pkgRefs"`
	Resources         []string     `json:"resources"`
	AllowedOSVersions []string     `json:"allowedOSVersions"`
}

type choiceInfo struct {
	ID      string   `json:"id"`
	Title   string   `json:"title,omitempty"`
	Visible string   `json:"visible,omitempty"`
	PkgRefs []string `json:"pkgRefs"`
}

type pkgRefInfo struct {
	ID            string `json:"id"`
	Version       string `json:"version,omitempty"`
	Path          string `json:"path,omitempty"`
	InstallKBytes string `json:"installKBytes,omitempty"`
	Auth          string `json:"auth,omitempty"`
	OnConclusion  string `json:"onConclusion,omitempty"`
}

// signatureSummary describes the signature elements present. Validity is
// verify's job; here "signed" means the TOC carries a signature.
type signatureSummary struct {
	Signed       bool          `json:"signed"`
	Digest       string        `json:"digest,omitempty"`
	Styles       []string      `json:"styles"`
	Certificates []certSummary `json:"certificates"`
	TeamID       string        `json:"teamId,omitempty"`
}

type certSummary struct {
	Subject   string `json:"subject"`
	Issuer    string `json:"issuer"`
	TeamID    string `json:"teamId,omitempty"`
	NotBefore string `json:"notBefore"`
	NotAfter  string `json:"notAfter"`
	SHA256    string `json:"sha256"`
}

type stapleSummary struct {
	Present bool `json:"present"`
	Length  int  `json:"length,omitempty"`
}

func runInfo(cmd *cobra.Command, args []string) error {
	p, err := openPackage(args[0])
	if err != nil {
		return err
	}
	defer p.Close()

	summary, err := collectSummary(p)
	if err != nil {
		return err
	}
	if opts.Output == "json" {
		return jsonOut(summary)
	}
	printSummary(summary)
	return nil
}

func collectSummary(p *flatpkg.Package) (*packageSummary, error) {
	x := p.XAR
	hdr := x.Header()
	s := &packageSummary{
		Path: p.Path,
		Kind: string(p.Kind),
		Size: x.Size(),
		XAR: xarSummary{
			HeaderSize:          int(hdr.Size),
			Version:             int(hdr.Version),
			ChecksumAlgorithm:   hdr.ChecksumAlg.String(),
			TOCCompressedLength: int64(hdr.TOCCompressed),
			TOCLength:           int64(hdr.TOCUncompressed),
			Entries:             len(x.Files()),
			CreationTime:        x.TOC().CreationTime,
			TOCDigestValid:      x.TOCDigestValid(),
		},
		Packages: []componentSummary{},
	}
	for _, c := range p.Components {
		s.Packages = append(s.Packages, summariseComponent(c))
	}
	if d := p.Distribution; d != nil {
		s.Distribution = summariseDistribution(p, d)
	}
	s.Signature = summariseSignature(x)
	s.Staple = summariseStaple(p)
	return s, nil
}

func summariseComponent(c *flatpkg.Component) componentSummary {
	info := c.Info
	cs := componentSummary{
		Name:                 c.Name,
		Identifier:           info.Identifier,
		Version:              info.Version,
		FormatVersion:        info.FormatVersion,
		InstallLocation:      info.InstallLocation,
		Auth:                 info.Auth,
		OverwritePermissions: info.OverwritePermissions,
		Relocatable:          info.Relocatable,
		GeneratorVersion:     info.GeneratorVersion,
		PostinstallAction:    info.PostinstallAction,
		MinimumSystemVersion: info.MinimumSystemVersion,
		Scripts:              []string{},
		Bundles:              []bundleSummary{},
	}
	if names := info.Scripts.Names(); names != nil {
		cs.Scripts = names
	}
	if c.HasPayload() {
		ps := &payloadSummary{Entry: c.PayloadEntryName()}
		if info.Payload != nil {
			ps.NumberOfFiles = info.Payload.NumberOfFiles
			ps.InstallKBytes = info.Payload.InstallKBytes
			ps.LargeSegmented = info.Payload.LargeSegmented == "true"
		}
		if enc, err := c.PayloadEncoding(); err == nil {
			ps.Encoding = string(enc)
		}
		cs.Payload = ps
	}
	if info.BundleVersion != nil {
		for _, b := range info.BundleVersion.Bundles {
			v := b.CFBundleShortVersionString
			if v == "" {
				v = b.CFBundleVersion
			}
			cs.Bundles = append(cs.Bundles, bundleSummary{ID: b.ID, Path: b.Path, Version: v})
		}
	}
	return cs
}

func summariseDistribution(p *flatpkg.Package, d *flatpkg.Distribution) *distSummary {
	ds := &distSummary{
		Title:             d.Title,
		MinSpecVersion:    d.MinSpecVersion,
		HostArchitectures: []string{},
		Choices:           []choiceInfo{},
		PkgRefs:           []pkgRefInfo{},
		Resources:         []string{},
		AllowedOSVersions: []string{},
	}
	if o := d.Options; o != nil {
		ds.Customize = o.Customize
		ds.RequireScripts = o.RequireScripts
		ds.RootVolumeOnly = o.RootVolumeOnly
		if a := o.Architectures(); a != nil {
			ds.HostArchitectures = a
		}
	}
	for _, c := range d.Choices {
		ci := choiceInfo{ID: c.ID, Title: c.Title, Visible: c.Visible, PkgRefs: []string{}}
		for _, r := range c.PkgRefs {
			ci.PkgRefs = append(ci.PkgRefs, r.ID)
		}
		ds.Choices = append(ds.Choices, ci)
	}
	for _, r := range d.PkgRefs {
		if strings.TrimSpace(r.Path) == "" && r.Version == "" && r.InstallKBytes == "" {
			continue // a bare reference inside a choice, already listed
		}
		ds.PkgRefs = append(ds.PkgRefs, pkgRefInfo{
			ID: r.ID, Version: r.Version, Path: strings.TrimSpace(r.Path),
			InstallKBytes: r.InstallKBytes, Auth: r.Auth, OnConclusion: r.OnConclusion,
		})
	}
	for _, f := range p.XAR.Files() {
		if strings.HasPrefix(f.Path(), flatpkg.EntryResources+"/") && !f.IsDir() {
			ds.Resources = append(ds.Resources, f.Path())
		}
	}
	if vc := d.VolumeCheck; vc != nil && vc.AllowedOSVersions != nil {
		for _, v := range vc.AllowedOSVersions.Versions {
			s := v.Min
			if v.Before != "" {
				s += " <" + v.Before
			}
			ds.AllowedOSVersions = append(ds.AllowedOSVersions, s)
		}
	}
	return ds
}

// summariseSignature reports the signature elements and decodes the
// certificate chain. It does not verify anything.
func summariseSignature(x *xar.Reader) signatureSummary {
	ss := signatureSummary{Styles: []string{}, Certificates: []certSummary{}}
	toc := x.TOC()
	var chain []string
	for _, sig := range []*xar.Signature{toc.Signature, toc.XSignature} {
		if sig == nil {
			continue
		}
		ss.Signed = true
		ss.Styles = append(ss.Styles, sig.Style)
		if sig.KeyInfo != nil && len(chain) == 0 {
			chain = sig.KeyInfo.X509Data.Certificates
		}
	}
	if !ss.Signed {
		return ss
	}
	ss.Digest = x.Header().ChecksumAlg.String()
	for _, b64 := range chain {
		der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(b64), ""))
		if err != nil {
			continue
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(der)
		cs := certSummary{
			Subject:   cert.Subject.String(),
			Issuer:    cert.Issuer.String(),
			NotBefore: cert.NotBefore.UTC().Format(time.RFC3339),
			NotAfter:  cert.NotAfter.UTC().Format(time.RFC3339),
			SHA256:    hex.EncodeToString(sum[:]),
		}
		if len(cert.Subject.OrganizationalUnit) > 0 {
			cs.TeamID = cert.Subject.OrganizationalUnit[0]
		}
		ss.Certificates = append(ss.Certificates, cs)
	}
	if len(ss.Certificates) > 0 {
		ss.TeamID = ss.Certificates[0].TeamID
	}
	return ss
}

func summariseStaple(p *flatpkg.Package) stapleSummary {
	f, err := os.Open(p.Path)
	if err != nil {
		return stapleSummary{}
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return stapleSummary{}
	}
	t, err := staple.Read(f, st.Size())
	if err != nil {
		if !errors.Is(err, staple.ErrNoTicket) {
			verbosef("staple: %v", err)
		}
		return stapleSummary{}
	}
	return stapleSummary{Present: true, Length: len(t.Data)}
}

func printSummary(s *packageSummary) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer func() { _ = w.Flush() }()
	fmt.Fprintf(w, "Package:\t%s\n", s.Path)
	fmt.Fprintf(w, "Kind:\t%s\n", kindLabel(s.Kind))
	fmt.Fprintf(w, "Size:\t%s (%d bytes)\n", formatSize(uint64(s.Size)), s.Size)
	fmt.Fprintf(w, "Archive:\txar v%d, %s TOC digest (%s), %d entries", s.XAR.Version, s.XAR.ChecksumAlgorithm, validLabel(s.XAR.TOCDigestValid), s.XAR.Entries)
	if s.XAR.CreationTime != "" {
		fmt.Fprintf(w, ", created %s", s.XAR.CreationTime)
	}
	fmt.Fprintln(w)
	if d := s.Distribution; d != nil {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Distribution:\t%s\n", orDash(d.Title))
		if d.MinSpecVersion != "" {
			fmt.Fprintf(w, "  minSpecVersion:\t%s\n", d.MinSpecVersion)
		}
		if len(d.HostArchitectures) > 0 {
			fmt.Fprintf(w, "  Architectures:\t%s\n", strings.Join(d.HostArchitectures, ", "))
		}
		if d.Customize != "" {
			fmt.Fprintf(w, "  Customize:\t%s\n", d.Customize)
		}
		if len(d.AllowedOSVersions) > 0 {
			fmt.Fprintf(w, "  OS versions:\t%s\n", strings.Join(d.AllowedOSVersions, ", "))
		}
		for _, c := range d.Choices {
			fmt.Fprintf(w, "  Choice %s:\t%s (%s)\n", c.ID, orDash(c.Title), strings.Join(c.PkgRefs, ", "))
		}
		for _, r := range d.Resources {
			fmt.Fprintf(w, "  Resource:\t%s\n", r)
		}
	}
	for _, c := range s.Packages {
		fmt.Fprintln(w)
		name := c.Name
		if name == "" {
			name = "(root)"
		}
		fmt.Fprintf(w, "Component:\t%s\n", name)
		fmt.Fprintf(w, "  Identifier:\t%s\n", c.Identifier)
		fmt.Fprintf(w, "  Version:\t%s\n", c.Version)
		fmt.Fprintf(w, "  Install location:\t%s\n", orDash(c.InstallLocation))
		if c.MinimumSystemVersion != "" {
			fmt.Fprintf(w, "  Minimum macOS:\t%s\n", c.MinimumSystemVersion)
		}
		if c.Payload != nil {
			fmt.Fprintf(w, "  Payload:\t%d files, %d KB installed, %s", c.Payload.NumberOfFiles, c.Payload.InstallKBytes, c.Payload.Encoding)
			if c.Payload.LargeSegmented {
				fmt.Fprint(w, " (large-segmented)")
			}
			fmt.Fprintln(w)
		} else {
			fmt.Fprintf(w, "  Payload:\tnone\n")
		}
		if len(c.Scripts) > 0 {
			fmt.Fprintf(w, "  Scripts:\t%s\n", strings.Join(c.Scripts, ", "))
		}
		for _, b := range c.Bundles {
			fmt.Fprintf(w, "  Bundle:\t%s %s (%s)\n", b.ID, b.Version, b.Path)
		}
		extras := []string{}
		if c.Auth != "" {
			extras = append(extras, "auth="+c.Auth)
		}
		if c.Relocatable != nil {
			extras = append(extras, fmt.Sprintf("relocatable=%v", *c.Relocatable))
		}
		if c.OverwritePermissions != nil {
			extras = append(extras, fmt.Sprintf("overwrite-permissions=%v", *c.OverwritePermissions))
		}
		if c.PostinstallAction != "" {
			extras = append(extras, "postinstall-action="+c.PostinstallAction)
		}
		if len(extras) > 0 {
			fmt.Fprintf(w, "  Flags:\t%s\n", strings.Join(extras, " "))
		}
		if c.GeneratorVersion != "" {
			fmt.Fprintf(w, "  Generator:\t%s\n", c.GeneratorVersion)
		}
	}
	fmt.Fprintln(w)
	if !s.Signature.Signed {
		fmt.Fprintf(w, "Signature:\tnone\n")
	} else {
		fmt.Fprintf(w, "Signature:\t%s over %s digest\n", strings.Join(s.Signature.Styles, "+"), s.Signature.Digest)
		for i, c := range s.Signature.Certificates {
			label := "  Certificate:"
			if i > 0 {
				label = "  Issued by:"
			}
			fmt.Fprintf(w, "%s\t%s (expires %s)\n", label, cn(c.Subject), c.NotAfter[:10])
		}
		if s.Signature.TeamID != "" {
			fmt.Fprintf(w, "  Team ID:\t%s\n", s.Signature.TeamID)
		}
	}
	if s.Staple.Present {
		fmt.Fprintf(w, "Staple:\tnotarization ticket present (%d bytes)\n", s.Staple.Length)
	} else {
		fmt.Fprintf(w, "Staple:\tnone\n")
	}
}

func kindLabel(k string) string {
	switch k {
	case string(flatpkg.KindComponent):
		return "component package"
	case string(flatpkg.KindProduct):
		return "product archive (distribution)"
	}
	return k
}

func validLabel(ok bool) string {
	if ok {
		return "valid"
	}
	return "INVALID"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// cn pulls the common name out of a distinguished name string for the
// text renderer; JSON carries the whole name.
func cn(dn string) string {
	for _, part := range strings.Split(dn, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "CN=") {
			return strings.TrimPrefix(part, "CN=")
		}
	}
	return dn
}
