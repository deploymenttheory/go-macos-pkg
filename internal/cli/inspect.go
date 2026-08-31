// macospkg inspect PKG VERB: low-level structural inspection.
package cli

import (
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
	"github.com/deploymenttheory/go-macos-pkg/pkg/staple"
	"github.com/spf13/cobra"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect PKG VERB [NAME]",
	Short: "Low-level structural inspection (header, TOC, bom, signature)",
	Long: `Dump one structure of the package as stored:

  header               the 28-byte xar header, decoded
  toc                  the table of contents XML, exactly as stored
  packageinfo [NAME]   a component's PackageInfo (NAME picks a component of
                       a product archive, e.g. foo.pkg)
  distribution         the Distribution of a product archive
  bom [NAME]           the bill of materials, one line per path: path, mode,
                       uid/gid, size, 32-bit CRC checksum
  signature            the signature elements and the certificate chain (PEM)
  cms                  the raw CMS signature (DER), to stdout
  rsa                  the raw RSA signature, to stdout
  digest               the table-of-contents digest the signatures cover, raw
  ticket               the stapled notarization ticket, raw, to stdout

Examples:
  macospkg inspect Foo.pkg toc
  macospkg inspect Product.pkg bom foo.pkg
  macospkg inspect Foo.pkg signature`,
	Args: rangeArgs(2, 3, "PKG VERB [NAME]"),
	RunE: runInspect,
}

func runInspect(cmd *cobra.Command, args []string) error {
	verb := strings.ToLower(args[1])
	name := ""
	if len(args) > 2 {
		name = args[2]
	}

	// header and toc work on any xar, package or not.
	switch verb {
	case "header", "toc":
		x, err := openXAR(args[0])
		if err != nil {
			return err
		}
		defer x.Close()
		if verb == "toc" {
			_, err := os.Stdout.Write(x.RawTOC())
			return err
		}
		h := x.Header()
		if structured() {
			return jsonOut(map[string]any{
				"magic": "xar!", "headerSize": h.Size, "version": h.Version,
				"tocCompressedLength": h.TOCCompressed, "tocLength": h.TOCUncompressed,
				"checksumAlgorithm": h.ChecksumAlg.String(), "checksumAlgorithmID": uint32(h.ChecksumAlg),
				"heapOffset": x.HeapOffset(), "heapEnd": x.HeapEnd(), "fileSize": x.Size(),
				"tocDigestValid": x.TOCDigestValid(),
			})
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "magic:\txar!\n")
		fmt.Fprintf(w, "header size:\t%d\n", h.Size)
		fmt.Fprintf(w, "version:\t%d\n", h.Version)
		fmt.Fprintf(w, "toc length (compressed):\t%d\n", h.TOCCompressed)
		fmt.Fprintf(w, "toc length (uncompressed):\t%d\n", h.TOCUncompressed)
		fmt.Fprintf(w, "checksum algorithm:\t%s (%d)\n", h.ChecksumAlg, uint32(h.ChecksumAlg))
		fmt.Fprintf(w, "heap offset:\t%d\n", x.HeapOffset())
		fmt.Fprintf(w, "heap end:\t%d\n", x.HeapEnd())
		fmt.Fprintf(w, "file size:\t%d\n", x.Size())
		fmt.Fprintf(w, "toc digest:\t%s\n", validLabel(x.TOCDigestValid()))
		return w.Flush()
	}

	p, err := openPackage(args[0])
	if err != nil {
		return err
	}
	defer p.Close()

	switch verb {
	case "packageinfo":
		components, err := selectComponents(p, name)
		if err != nil {
			return err
		}
		if len(components) > 1 {
			return usageErrorf("%s has %d components; name one (inspect PKG packageinfo NAME)", p.Path, len(components))
		}
		_, err = os.Stdout.Write(components[0].Info.Raw)
		return err
	case "distribution":
		if p.Distribution == nil {
			return withCode(ExitBadPackage, fmt.Errorf("%s is a component package and has no Distribution", p.Path))
		}
		_, err := os.Stdout.Write(p.Distribution.Raw)
		return err
	case "bom":
		components, err := selectComponents(p, name)
		if err != nil {
			return err
		}
		if len(components) > 1 {
			return usageErrorf("%s has %d components; name one (inspect PKG bom NAME)", p.Path, len(components))
		}
		return inspectBom(components[0])
	case "signature":
		return inspectSignature(p)
	case "cms", "rsa":
		toc := p.XAR.TOC()
		el := toc.XSignature
		if verb == "rsa" {
			el = toc.Signature
		}
		if el == nil {
			return withCode(ExitSignature, fmt.Errorf("%s has no %s signature", p.Path, strings.ToUpper(verb)))
		}
		sec, err := p.XAR.HeapSection(el.Offset, el.Size)
		if err != nil {
			return err
		}
		buf := make([]byte, el.Size)
		if _, err := sec.ReadAt(buf, 0); err != nil && err != io.EOF {
			return err
		}
		// Written as stored, padding included: a BER-encoded CMS ends in
		// zero bytes that belong to it.
		_, err = os.Stdout.Write(buf)
		return err
	case "digest":
		d, err := p.XAR.ComputeTOCDigest()
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(d)
		return err
	case "ticket":
		f, err := os.Open(p.Path)
		if err != nil {
			return err
		}
		defer f.Close()
		st, _ := f.Stat()
		t, err := staple.Read(f, st.Size())
		if err != nil {
			return withCode(ExitSignature, err)
		}
		_, err = os.Stdout.Write(t.Data)
		return err
	default:
		return usageErrorf("unknown inspect verb %q: want header, toc, packageinfo, distribution, bom, signature, cms, rsa, digest or ticket", verb)
	}
}

// inspectBom prints the bill of materials in lsbom's default column order:
// path, mode (octal, with type bits), uid/gid, then size and CRC-32 for
// files, or the link target for symlinks.
func inspectBom(c *flatpkg.Component) error {
	b, err := c.Bom()
	if err != nil {
		return err
	}
	entries, err := b.Paths()
	if err != nil {
		return err
	}
	if structured() {
		for _, e := range entries {
			if err := jsonOut(payloadEntryOf(e, c, false)); err != nil {
				return err
			}
		}
		return nil
	}
	for _, e := range entries {
		switch e.Type {
		case bom.TypeFile:
			fmt.Printf("%s\t%o\t%d/%d\t%d\t%d\n", e.Path, e.Mode, e.UID, e.GID, e.Size, e.Checksum)
		case bom.TypeLink:
			fmt.Printf("%s\t%o\t%d/%d\t%d\t%d\t%s\n", e.Path, e.Mode, e.UID, e.GID, e.Size, e.Checksum, e.LinkTarget)
		default:
			fmt.Printf("%s\t%o\t%d/%d\n", e.Path, e.Mode, e.UID, e.GID)
		}
	}
	return nil
}

func inspectSignature(p *flatpkg.Package) error {
	s := summariseSignature(p.XAR)
	if structured() {
		toc := p.XAR.TOC()
		out := map[string]any{"signature": s}
		if toc.Signature != nil {
			out["rsa"] = map[string]any{"offset": toc.Signature.Offset, "size": toc.Signature.Size}
		}
		if toc.XSignature != nil {
			out["cms"] = map[string]any{"offset": toc.XSignature.Offset, "size": toc.XSignature.Size}
		}
		return jsonOut(out)
	}
	if !s.Signed {
		fmt.Println("unsigned")
		return nil
	}
	toc := p.XAR.TOC()
	for _, sig := range []struct {
		label string
		el    interface{}
	}{} {
		_ = sig
	}
	if toc.Signature != nil {
		fmt.Printf("signature:   style=%s offset=%d size=%d\n", toc.Signature.Style, toc.Signature.Offset, toc.Signature.Size)
	}
	if toc.XSignature != nil {
		fmt.Printf("x-signature: style=%s offset=%d size=%d\n", toc.XSignature.Style, toc.XSignature.Offset, toc.XSignature.Size)
	}
	var chain []string
	if toc.Signature != nil && toc.Signature.KeyInfo != nil {
		chain = toc.Signature.KeyInfo.X509Data.Certificates
	} else if toc.XSignature != nil && toc.XSignature.KeyInfo != nil {
		chain = toc.XSignature.KeyInfo.X509Data.Certificates
	}
	for i, c := range s.Certificates {
		fmt.Printf("certificate %d: %s\n  issuer:  %s\n  expires: %s\n  sha256:  %s\n", i, c.Subject, c.Issuer, c.NotAfter, c.SHA256)
	}
	for _, b64 := range chain {
		der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(b64), ""))
		if err != nil {
			continue
		}
		if err := pem.Encode(os.Stdout, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			return err
		}
	}
	return nil
}
