// Package pbzx decodes the pbzx container Apple wraps around xz-compressed
// payloads (pkgbuild --compression latest, and OTA archives).
//
// Layout, all integers big-endian:
//
//	magic      4  "pbzx"
//	flags      8  the chunk size the writer used (informational)
//	chunks:
//	  inflated 8  size of the chunk once decoded
//	  deflated 8  size of the chunk as stored
//	  data     deflated bytes: an xz stream, or raw when deflated == inflated
//
// The stream ends at end of input; there is no trailer.
package pbzx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/ulikunitz/xz"
)

// Magic is the four-byte signature "pbzx".
var Magic = []byte("pbzx")

var xzMagic = []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}

// IsPBZX reports whether head begins with the pbzx magic.
func IsPBZX(head []byte) bool { return bytes.HasPrefix(head, Magic) }

// Reader decodes a pbzx stream chunk by chunk.
type Reader struct {
	r      io.Reader
	chunk  io.Reader // decoder over the current chunk, nil between chunks
	stored io.Reader // the current chunk's stored bytes, to drain when done
	left   int64     // decoded bytes still expected from the current chunk
	flags  uint64
	err    error
}

// NewReader validates the magic and returns a streaming decoder.
func NewReader(r io.Reader) (*Reader, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("pbzx: unable to read header: %w", err)
	}
	if !bytes.Equal(hdr[:4], Magic) {
		return nil, errors.New("pbzx: bad magic")
	}
	return &Reader{r: r, flags: binary.BigEndian.Uint64(hdr[4:12])}, nil
}

// Flags returns the header's flags word, which records the chunk size.
func (pr *Reader) Flags() uint64 { return pr.flags }

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
			// The decoder stops once it has produced the chunk's bytes,
			// which leaves the xz index and footer unread; drain them so
			// the next chunk header is read from the right place.
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
	stored := io.LimitReader(pr.r, int64(deflated))
	pr.stored = stored
	pr.left = int64(inflated)

	// A chunk that did not compress is stored raw and its sizes agree.
	// Otherwise it is an xz stream. Peek at the magic rather than trust the
	// arithmetic, since a raw chunk of exactly the xz magic is a stretch and
	// a compressed chunk that happens to be the same size is not.
	br := &peekReader{r: stored}
	head, err := br.peek(6)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return fmt.Errorf("pbzx: unable to read chunk: %w", err)
	}
	if bytes.Equal(head, xzMagic) {
		xr, err := xz.NewReader(br)
		if err != nil {
			return fmt.Errorf("pbzx: bad xz chunk: %w", err)
		}
		pr.chunk = io.LimitReader(xr, int64(inflated))
		return nil
	}
	if inflated != deflated {
		return fmt.Errorf("pbzx: chunk is neither xz nor stored (%d in, %d out)", deflated, inflated)
	}
	pr.chunk = br
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
