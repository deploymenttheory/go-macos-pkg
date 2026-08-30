// macospkg cat PKG ENTRY: one archive entry or payload file to stdout.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/deploymenttheory/go-macos-pkg/pkg/cpio"
	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
	"github.com/spf13/cobra"
)

var (
	catPayload   string
	catComponent string
	catRaw       bool
)

var catCmd = &cobra.Command{
	Use:   "cat PKG [ENTRY]",
	Short: "Write one archive entry or payload file to stdout",
	Long: `Write an archive entry to stdout, decoded: PackageInfo, Distribution, a
nested component's PackageInfo (foo.pkg/PackageInfo), a Distribution resource
(Resources/en.lproj/welcome.html), or Payload itself (the gzip cpio stream).

--payload PATH instead writes one file from inside the payload, by the path
the bill of materials uses (./usr/local/bin/tool). Use --component to pick
the component of a product archive.

Examples:
  macospkg cat Foo.pkg PackageInfo
  macospkg cat Product.pkg Distribution
  macospkg cat Product.pkg foo.pkg/PackageInfo
  macospkg cat Foo.pkg --payload ./usr/local/bin/tool > tool
  macospkg cat Foo.pkg Payload | gunzip | cpio -itv`,
	Args: rangeArgs(1, 2, "PKG [ENTRY]"),
	RunE: runCat,
}

func init() {
	catCmd.Flags().StringVar(&catPayload, "payload", "", "write this file from inside the payload (path as in the bill of materials)")
	catCmd.Flags().StringVar(&catComponent, "component", "", "component of a product archive to read the payload from")
	catCmd.Flags().BoolVar(&catRaw, "raw", false, "write the entry's stored bytes without decoding its encoding")
}

func runCat(cmd *cobra.Command, args []string) error {
	p, err := openPackage(args[0])
	if err != nil {
		return err
	}
	defer p.Close()

	if catPayload != "" {
		if len(args) > 1 {
			return usageErrorf("--payload takes no ENTRY argument")
		}
		return catPayloadFile(p, catPayload)
	}
	if len(args) < 2 {
		return usageErrorf("expected PKG ENTRY, or PKG --payload PATH")
	}
	entry := strings.Trim(args[1], "/")
	f := p.XAR.Lookup(entry)
	if f == nil {
		return withCode(ExitBadPackage, fmt.Errorf("no entry %q in %s (try: macospkg list --archive %s)", entry, p.Path, p.Path))
	}
	if f.IsDir() {
		return withCode(ExitBadPackage, fmt.Errorf("%q is a directory", entry))
	}
	if catRaw {
		rc, err := p.XAR.OpenRaw(f)
		if err != nil {
			return err
		}
		_, err = io.Copy(os.Stdout, rc)
		return err
	}
	rc, err := p.XAR.Open(f)
	if err != nil {
		return withCode(ExitUnsupported, err)
	}
	defer rc.Close()
	if _, err := io.Copy(os.Stdout, rc); err != nil {
		return fmt.Errorf("unable to read %s: %w", entry, err)
	}
	return nil
}

// catPayloadFile streams one file out of the payload cpio.
func catPayloadFile(p *flatpkg.Package, want string) error {
	components, err := selectComponents(p, catComponent)
	if err != nil {
		return err
	}
	if len(components) > 1 {
		return usageErrorf("%s has %d components; choose one with --component", p.Path, len(components))
	}
	c := components[0]
	if !c.HasPayload() {
		return withCode(ExitBadPackage, fmt.Errorf("%s has no payload", componentLabel(c)))
	}
	want = normalisePayloadPath(want)
	cr, _, closer, err := c.OpenPayloadCPIO()
	if err != nil {
		return payloadOpenError(err)
	}
	defer closer.Close()
	for {
		h, err := cr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("payload: %w", err)
		}
		if normalisePayloadPath(h.Name) != want {
			continue
		}
		if !h.IsRegular() {
			return withCode(ExitBadPackage, fmt.Errorf("%s is a %s, not a file", want, cpioType(h)))
		}
		_, err = io.Copy(os.Stdout, cr)
		return err
	}
	return withCode(ExitBadPackage, fmt.Errorf("no file %s in the payload (try: macospkg list %s)", want, p.Path))
}

// normalisePayloadPath makes "./a", "a" and "/a" compare equal.
func normalisePayloadPath(s string) string {
	s = strings.TrimPrefix(s, "./")
	s = strings.Trim(s, "/")
	if s == "" || s == "." {
		return "."
	}
	return "./" + s
}

// payloadOpenError maps an unsupported payload container to exit 5.
func payloadOpenError(err error) error {
	if errors.Is(err, flatpkg.ErrUnsupportedPayload) {
		return withCode(ExitUnsupported, err)
	}
	return err
}

func cpioType(h *cpio.Header) string {
	switch {
	case h.IsDir():
		return "dir"
	case h.IsSymlink():
		return "link"
	case h.IsRegular():
		return "file"
	default:
		return "other"
	}
}
