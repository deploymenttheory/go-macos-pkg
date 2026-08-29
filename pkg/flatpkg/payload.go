// Payload container detection and opening.
//
// A Payload is a cpio archive, but pkgbuild wraps it in one of two ways
// depending on its flags: gzip (the default and the only one every macOS
// reads) or a pbz* block-compression container (--compression latest,
// which has meant pbzx — xz chunks — on every macOS from 12 to 26).
// --large-payload keeps gzip but names the entry LargeSegmentedPayload.
// Apple Archive is recognised so that an .aar handed to the tool is named
// correctly; the Installer itself never reads it. The first bytes tell
// them apart.
package flatpkg

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/deploymenttheory/go-macos-pkg/pkg/cpio"
	"github.com/deploymenttheory/go-macos-pkg/pkg/pbzx"
)

// PayloadEncoding names how a Payload's cpio stream is wrapped.
type PayloadEncoding string

// Payload encodings, as reported by info and list. The pbz* names carry
// the container's algorithm letter: pbzx is xz, pbze LZFSE, pbz4 LZ4,
// pbzz zlib, pbzb LZBITMAP (detected, not decodable).
const (
	PayloadGzip         PayloadEncoding = "gzip-cpio"
	PayloadPBZX         PayloadEncoding = "pbzx-cpio"
	PayloadPBZE         PayloadEncoding = "pbze-cpio"
	PayloadPBZ4         PayloadEncoding = "pbz4-cpio"
	PayloadPBZZ         PayloadEncoding = "pbzz-cpio"
	PayloadPBZB         PayloadEncoding = "pbzb-cpio"
	PayloadCPIO         PayloadEncoding = "cpio"
	PayloadAppleArchive PayloadEncoding = "apple-archive"
	PayloadUnknown      PayloadEncoding = "unknown"
)

// IsPBZ reports whether the encoding is one of the pbz* containers.
func (e PayloadEncoding) IsPBZ() bool {
	return strings.HasPrefix(string(e), "pbz")
}

// pbzEncoding names the encoding for a container algorithm.
func pbzEncoding(a pbzx.Algorithm) PayloadEncoding {
	return PayloadEncoding("pbz" + string(rune(a)) + "-cpio")
}

// ErrUnsupportedPayload reports a payload container this tool cannot
// decode: Apple Archive (which the Installer does not read either) and
// pbzb, whose LZBITMAP compression has no public specification.
var ErrUnsupportedPayload = errors.New("flatpkg: unsupported payload encoding")

var (
	gzipMagic         = []byte{0x1f, 0x8b, 0x08}
	appleArchiveMagic = [][]byte{[]byte("AA01"), []byte("YAA1"), []byte("AEA1")}
)

// SniffPayload identifies the container from the first bytes.
func SniffPayload(head []byte) PayloadEncoding {
	switch {
	case bytes.HasPrefix(head, gzipMagic):
		return PayloadGzip
	case pbzx.IsPBZX(head):
		a, _ := pbzx.Sniff(head)
		return pbzEncoding(a)
	case bytes.HasPrefix(head, []byte(cpio.MagicODC)),
		bytes.HasPrefix(head, []byte(cpio.MagicNewc)),
		bytes.HasPrefix(head, []byte(cpio.MagicNewcCRC)):
		return PayloadCPIO
	}
	for _, m := range appleArchiveMagic {
		if bytes.HasPrefix(head, m) {
			return PayloadAppleArchive
		}
	}
	return PayloadUnknown
}

// PayloadEncoding reports how the component's Payload is wrapped, reading
// only its first bytes.
func (c *Component) PayloadEncoding() (PayloadEncoding, error) {
	if c.payload == nil {
		return PayloadUnknown, fmt.Errorf("flatpkg: component %q has no Payload", c.Name)
	}
	return sniffEntry(c.pkg, c.payload.Path())
}

// ScriptsEncoding reports how the component's Scripts archive is wrapped.
func (c *Component) ScriptsEncoding() (PayloadEncoding, error) {
	if c.scripts == nil {
		return PayloadUnknown, fmt.Errorf("flatpkg: component %q has no Scripts", c.Name)
	}
	return sniffEntry(c.pkg, c.scripts.Path())
}

func sniffEntry(p *Package, path string) (PayloadEncoding, error) {
	f := p.XAR.Lookup(path)
	rc, err := p.XAR.Open(f)
	if err != nil {
		return PayloadUnknown, err
	}
	defer rc.Close()
	head := make([]byte, 8)
	n, err := io.ReadFull(rc, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return PayloadUnknown, err
	}
	return SniffPayload(head[:n]), nil
}

// OpenCPIO unwraps a Payload or Scripts stream to its cpio entries,
// whatever container it is in. Apple Archive returns ErrUnsupportedPayload.
func OpenCPIO(r io.Reader) (*cpio.Reader, PayloadEncoding, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	head, err := br.Peek(8)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, PayloadUnknown, err
	}
	enc := SniffPayload(head)
	switch enc {
	case PayloadGzip:
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, enc, fmt.Errorf("flatpkg: payload is not gzip: %w", err)
		}
		// pkgbuild gzips a plain cpio, but be safe: a gzip around pbzx
		// would be unusual, not impossible.
		inner, innerEnc, err := OpenCPIO(gz)
		if err != nil {
			return nil, enc, err
		}
		if innerEnc.IsPBZ() {
			enc = innerEnc
		}
		return inner, enc, nil
	case PayloadPBZX, PayloadPBZE, PayloadPBZ4, PayloadPBZZ:
		pr, err := pbzx.NewReader(br)
		if err != nil {
			return nil, enc, err
		}
		return cpio.NewReader(pr), enc, nil
	case PayloadPBZB:
		return nil, enc, fmt.Errorf("%w: pbzb (LZBITMAP has no public specification)", ErrUnsupportedPayload)
	case PayloadCPIO:
		return cpio.NewReader(br), enc, nil
	case PayloadAppleArchive:
		return nil, enc, fmt.Errorf("%w: Apple Archive (the Installer does not read it either)", ErrUnsupportedPayload)
	default:
		return nil, enc, fmt.Errorf("%w: unrecognised payload container", ErrUnsupportedPayload)
	}
}

// OpenPayloadCPIO opens the component's Payload as cpio entries.
func (c *Component) OpenPayloadCPIO() (*cpio.Reader, PayloadEncoding, io.Closer, error) {
	rc, err := c.OpenPayload()
	if err != nil {
		return nil, PayloadUnknown, nil, err
	}
	cr, enc, err := OpenCPIO(rc)
	if err != nil {
		rc.Close()
		return nil, enc, nil, err
	}
	return cr, enc, rc, nil
}

// OpenScriptsCPIO opens the component's Scripts archive as cpio entries.
func (c *Component) OpenScriptsCPIO() (*cpio.Reader, PayloadEncoding, io.Closer, error) {
	rc, err := c.OpenScripts()
	if err != nil {
		return nil, PayloadUnknown, nil, err
	}
	cr, enc, err := OpenCPIO(rc)
	if err != nil {
		rc.Close()
		return nil, enc, nil, err
	}
	return cr, enc, rc, nil
}
