// Payload container detection and opening.
//
// A Payload is a cpio archive, but pkgbuild wraps it in one of two ways
// depending on its flags: gzip (the default and the only one every macOS
// reads) or a pbz* block-compression container (--compression latest,
// which has meant pbzx (xz chunks) on every macOS from 12 to 26).
// --large-payload keeps gzip but names the entry LargeSegmentedPayload.
// Every pbz* container is decoded, LZBITMAP included (see pkg/lzbitmap).
// The first bytes tell them apart; anything else is unsupported.
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
// pbzz zlib, pbzb LZBITMAP.
const (
	PayloadGzip    PayloadEncoding = "gzip-cpio"
	PayloadPBZX    PayloadEncoding = "pbzx-cpio"
	PayloadPBZE    PayloadEncoding = "pbze-cpio"
	PayloadPBZ4    PayloadEncoding = "pbz4-cpio"
	PayloadPBZZ    PayloadEncoding = "pbzz-cpio"
	PayloadPBZB    PayloadEncoding = "pbzb-cpio"
	PayloadCPIO    PayloadEncoding = "cpio"
	PayloadUnknown PayloadEncoding = "unknown"
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
// decode. Every container macOS will install is decoded, so in practice
// this means a Payload no packaging tool produces.
var ErrUnsupportedPayload = errors.New("flatpkg: unsupported payload encoding")

var gzipMagic = []byte{0x1f, 0x8b, 0x08}

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

// maxPayloadNesting bounds how many compression layers OpenCPIO will peel.
// A real payload is a plain cpio, or one gzip or pbzx layer around it; a
// deeply nested gzip chain is a decompression bomb, since each layer
// compresses to a few tens of bytes.
const maxPayloadNesting = 8

// OpenCPIO unwraps a Payload or Scripts stream to its cpio entries,
// whatever container it is in.
func OpenCPIO(r io.Reader) (*cpio.Reader, PayloadEncoding, error) {
	return openCPIO(r, 0)
}

func openCPIO(r io.Reader, depth int) (*cpio.Reader, PayloadEncoding, error) {
	if depth > maxPayloadNesting {
		return nil, PayloadUnknown, fmt.Errorf("%w: payload nested more than %d layers deep", ErrUnsupportedPayload, maxPayloadNesting)
	}
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
		inner, innerEnc, err := openCPIO(gz, depth+1)
		if err != nil {
			return nil, enc, err
		}
		if innerEnc.IsPBZ() {
			enc = innerEnc
		}
		return inner, enc, nil
	case PayloadPBZX, PayloadPBZE, PayloadPBZ4, PayloadPBZZ, PayloadPBZB:
		pr, err := pbzx.NewReader(br)
		if err != nil {
			return nil, enc, err
		}
		return joined(cpio.NewReader(pr)), enc, nil
	case PayloadCPIO:
		return joined(cpio.NewReader(br)), enc, nil
	default:
		return nil, enc, fmt.Errorf("%w: unrecognized payload container", ErrUnsupportedPayload)
	}
}

// joined turns on segment joining, so a file a --large-payload package
// split across consecutive entries reads back as the one file it is. A
// payload that carries no segments is unaffected, since the joining only
// takes effect where two entries in a row share a name.
func joined(cr *cpio.Reader) *cpio.Reader {
	cr.JoinSegments(true)
	return cr
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
