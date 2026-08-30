// Package pbzx reads and writes the pbz* block-compression containers Apple
// wraps around payloads: pkgbuild's pbzx (xz) for --compression latest,
// and the siblings libParallelCompression and the aa tool produce.
//
// Layout, integers big-endian:
//
//	magic      4  "pbz" + an algorithm letter
//	blockSize  8  the writer's block size
//	chunks, until end of input:
//	  inflated 8  size of the chunk once decoded
//	  deflated 8  size of the chunk as stored
//	  data     deflated bytes: compressed, or the plain bytes when the
//	           chunk did not shrink (deflated == inflated)
//
// There is no trailer. Algorithms: 'x' xz (LZMA2), 'e' LZFSE, '4' LZ4 in
// Apple's bv4* framing, 'z' zlib, 'b' LZBITMAP (see pkg/lzbitmap).
// What pkgbuild writes, from its own output: 16 MiB blocks, one
// xz stream per chunk with no integrity check and an 8 MiB dictionary.
// The same rules hold for every variant; pkgbuild has used only pbzx for
// --compression latest on every --min-os-version from 12.0 to 26.0.
package pbzx

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/deploymenttheory/go-macos-pkg/pkg/lzbitmap"
	"github.com/go-compressions/lzfse"
	"github.com/ulikunitz/xz"
)

// Algorithm is the letter after "pbz" in the magic.
type Algorithm byte

// Algorithms.
const (
	XZ       Algorithm = 'x'
	LZFSE    Algorithm = 'e'
	LZ4      Algorithm = '4'
	Zlib     Algorithm = 'z'
	LZBitmap Algorithm = 'b'
)

func (a Algorithm) String() string {
	switch a {
	case XZ:
		return "xz"
	case LZFSE:
		return "lzfse"
	case LZ4:
		return "lz4"
	case Zlib:
		return "zlib"
	case LZBitmap:
		return "lzbitmap"
	}
	return fmt.Sprintf("unknown(%q)", byte(a))
}

// Magic returns the four-byte container magic for the algorithm.
func (a Algorithm) Magic() []byte { return []byte{'p', 'b', 'z', byte(a)} }

// DefaultBlockSize is what pkgbuild uses.
const DefaultBlockSize = 16 << 20

// maxBufferedChunk bounds chunks for the algorithms decoded in memory.
const maxBufferedChunk = 1 << 30

// Errors.
var (
	ErrNotPBZ               = errors.New("pbzx: not a pbz* container")
	ErrUnsupportedAlgorithm = errors.New("pbzx: unsupported algorithm")
)

// Magic is the pbzx magic, kept for callers that only know that variant.
var Magic = []byte("pbzx")

var xzMagic = []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}

// Sniff reports the algorithm of a pbz* header and whether head is one.
func Sniff(head []byte) (Algorithm, bool) {
	if len(head) < 4 || head[0] != 'p' || head[1] != 'b' || head[2] != 'z' {
		return 0, false
	}
	switch Algorithm(head[3]) {
	case XZ, LZFSE, LZ4, Zlib, LZBitmap:
		return Algorithm(head[3]), true
	}
	return 0, false
}

// IsPBZX reports whether head begins with any pbz* magic.
func IsPBZX(head []byte) bool {
	_, ok := Sniff(head)
	return ok
}

// Reader decodes a pbz* stream chunk by chunk.
type Reader struct {
	r         io.Reader
	algo      Algorithm
	blockSize uint64
	chunk     io.Reader // decoder over the current chunk, nil between chunks
	stored    io.Reader // the current chunk's stored bytes, to drain when done
	left      int64     // decoded bytes still expected from the current chunk
	err       error
}

// NewReader validates the header and returns a streaming decoder.
func NewReader(r io.Reader) (*Reader, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:4]); err != nil {
		return nil, ErrNotPBZ
	}
	algo, ok := Sniff(hdr[:4])
	if !ok {
		return nil, ErrNotPBZ
	}
	if _, err := io.ReadFull(r, hdr[4:]); err != nil {
		return nil, fmt.Errorf("pbzx: unable to read header: %w", err)
	}
	return &Reader{r: r, algo: algo, blockSize: binary.BigEndian.Uint64(hdr[4:12])}, nil
}

// Algorithm returns the container's compression algorithm.
func (pr *Reader) Algorithm() Algorithm { return pr.algo }

// BlockSize returns the block size the writer used.
func (pr *Reader) BlockSize() uint64 { return pr.blockSize }

// Flags is the old name of BlockSize.
//
// Deprecated: use BlockSize.
func (pr *Reader) Flags() uint64 { return pr.blockSize }

// Read decodes into p.
func (pr *Reader) Read(p []byte) (int, error) {
	for {
		if pr.err != nil {
			return 0, pr.err
		}
		if pr.chunk == nil {
			if err := pr.nextChunk(); err != nil {
				pr.err = err
				return 0, err
			}
		}
		n, err := pr.chunk.Read(p)
		pr.left -= int64(n)
		if err == io.EOF {
			if pr.left != 0 {
				pr.err = fmt.Errorf("pbzx: chunk decoded short by %d bytes", pr.left)
				return n, pr.err
			}
			// A streaming decoder may stop before the stream's trailer
			// (xz index and footer); drain what is left of the stored
			// chunk so the next header is read from the right place.
			if _, err := io.Copy(io.Discard, pr.stored); err != nil {
				pr.err = fmt.Errorf("pbzx: unable to skip chunk trailer: %w", err)
				return n, pr.err
			}
			pr.chunk = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (pr *Reader) nextChunk() error {
	var hdr [16]byte
	if _, err := io.ReadFull(pr.r, hdr[:]); err != nil {
		if err == io.EOF {
			return io.EOF
		}
		return fmt.Errorf("pbzx: unable to read chunk header: %w", err)
	}
	inflated := binary.BigEndian.Uint64(hdr[0:8])
	deflated := binary.BigEndian.Uint64(hdr[8:16])
	if inflated > 1<<40 || deflated > 1<<40 {
		return fmt.Errorf("pbzx: implausible chunk sizes (%d, %d)", inflated, deflated)
	}
	if deflated > inflated {
		return fmt.Errorf("pbzx: chunk grew (%d stored for %d decoded)", deflated, inflated)
	}
	stored := io.LimitReader(pr.r, int64(deflated))
	pr.stored = stored
	pr.left = int64(inflated)

	// A chunk that did not compress is stored as is, and its sizes agree.
	if inflated == deflated {
		pr.chunk = stored
		return nil
	}
	switch pr.algo {
	case XZ:
		br := &peekReader{r: stored}
		if head, err := br.peek(6); err == nil && !bytes.Equal(head, xzMagic) {
			return fmt.Errorf("pbzx: chunk is not an xz stream")
		}
		xr, err := xz.NewReader(br)
		if err != nil {
			return fmt.Errorf("pbzx: bad xz chunk: %w", err)
		}
		pr.chunk = io.LimitReader(xr, int64(inflated))
	case Zlib:
		zr, err := zlib.NewReader(stored)
		if err != nil {
			return fmt.Errorf("pbzx: bad zlib chunk: %w", err)
		}
		pr.chunk = io.LimitReader(zr, int64(inflated))
	case LZFSE, LZ4, LZBitmap:
		if inflated > maxBufferedChunk {
			return fmt.Errorf("pbzx: %s chunk of %d bytes exceeds the %d-byte limit", pr.algo, inflated, maxBufferedChunk)
		}
		data, err := io.ReadAll(stored)
		if err != nil {
			return fmt.Errorf("pbzx: unable to read chunk: %w", err)
		}
		var out []byte
		switch pr.algo {
		case LZFSE:
			out, err = lzfse.Decompress(data)
		case LZBitmap:
			out, err = lzbitmap.Decompress(data)
		default:
			out, err = decodeLZ4Frames(data, int(inflated))
		}
		if err != nil {
			return fmt.Errorf("pbzx: bad %s chunk: %w", pr.algo, err)
		}
		if uint64(len(out)) != inflated {
			return fmt.Errorf("pbzx: %s chunk decoded to %d bytes, header says %d", pr.algo, len(out), inflated)
		}
		pr.chunk = bytes.NewReader(out)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedAlgorithm, pr.algo)
	}
	return nil
}

// peekReader lets nextChunk look at the first bytes of a chunk without
// consuming them from the underlying limited reader.
type peekReader struct {
	r    io.Reader
	head []byte
}

func (p *peekReader) peek(n int) ([]byte, error) {
	buf := make([]byte, n)
	got, err := io.ReadFull(p.r, buf)
	p.head = buf[:got]
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	return p.head, err
}

func (p *peekReader) Read(b []byte) (int, error) {
	if len(p.head) > 0 {
		n := copy(b, p.head)
		p.head = p.head[n:]
		return n, nil
	}
	return p.r.Read(b)
}

// Writer encodes a pbz* stream.
type Writer struct {
	w         io.Writer
	algo      Algorithm
	blockSize int
	buf       []byte
	started   bool
	closed    bool
}

// NewWriter returns a Writer producing the container for algo with the
// given block size (0 selects pkgbuild's 16 MiB).
func NewWriter(w io.Writer, algo Algorithm, blockSize uint64) (*Writer, error) {
	switch algo {
	case XZ, LZFSE, LZ4, Zlib, LZBitmap:
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, byte(algo))
	}
	if blockSize == 0 {
		blockSize = DefaultBlockSize
	}
	if blockSize > maxBufferedChunk {
		return nil, fmt.Errorf("pbzx: block size %d exceeds the %d-byte limit", blockSize, maxBufferedChunk)
	}
	return &Writer{w: w, algo: algo, blockSize: int(blockSize), buf: make([]byte, 0, blockSize)}, nil
}

func (pw *Writer) header() error {
	if pw.started {
		return nil
	}
	pw.started = true
	hdr := make([]byte, 12)
	copy(hdr, pw.algo.Magic())
	binary.BigEndian.PutUint64(hdr[4:], uint64(pw.blockSize))
	_, err := pw.w.Write(hdr)
	return err
}

// Write buffers p, emitting a chunk whenever a full block is available.
func (pw *Writer) Write(p []byte) (int, error) {
	if pw.closed {
		return 0, errors.New("pbzx: write after close")
	}
	if err := pw.header(); err != nil {
		return 0, err
	}
	written := 0
	for len(p) > 0 {
		room := pw.blockSize - len(pw.buf)
		n := min(room, len(p))
		pw.buf = append(pw.buf, p[:n]...)
		p = p[n:]
		written += n
		if len(pw.buf) == pw.blockSize {
			if err := pw.flush(); err != nil {
				return written, err
			}
		}
	}
	return written, nil
}

// flush compresses and writes the buffered block.
func (pw *Writer) flush() error {
	if len(pw.buf) == 0 {
		return nil
	}
	compressed, err := compressChunk(pw.algo, pw.buf)
	if err != nil {
		return err
	}
	data := compressed
	if len(compressed) >= len(pw.buf) {
		data = pw.buf // incompressible: stored, sizes equal
	}
	var hdr [16]byte
	binary.BigEndian.PutUint64(hdr[0:8], uint64(len(pw.buf)))
	binary.BigEndian.PutUint64(hdr[8:16], uint64(len(data)))
	if _, err := pw.w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := pw.w.Write(data); err != nil {
		return err
	}
	pw.buf = pw.buf[:0]
	return nil
}

// Close writes the final, possibly short, chunk. An empty input still
// gets a header.
func (pw *Writer) Close() error {
	if pw.closed {
		return nil
	}
	pw.closed = true
	if err := pw.header(); err != nil {
		return err
	}
	return pw.flush()
}

// compressChunk compresses one block with the algorithm's Apple-matching
// parameters.
func compressChunk(algo Algorithm, block []byte) ([]byte, error) {
	var out bytes.Buffer
	switch algo {
	case XZ:
		// One xz stream per chunk, no integrity check, 8 MiB LZMA2
		// dictionary: what pkgbuild writes (stream flags 0x0000, LZMA2
		// property 0x16), the equivalent of xz -6.
		cfg := xz.WriterConfig{DictCap: 8 << 20, NoCheckSum: true}
		zw, err := cfg.NewWriter(&out)
		if err != nil {
			return nil, err
		}
		if _, err := zw.Write(block); err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
	case Zlib:
		zw := zlib.NewWriter(&out)
		if _, err := zw.Write(block); err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
	case LZFSE:
		data, err := lzfse.Compress(block)
		if err != nil {
			return nil, err
		}
		return data, nil
	case LZ4:
		return encodeLZ4Frames(block), nil
	case LZBitmap:
		return lzbitmap.Compress(block)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedAlgorithm, algo)
	}
	return out.Bytes(), nil
}
