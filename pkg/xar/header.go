// xar archive header: the 28 bytes that locate the table of contents.
//
// Layout (all fields big-endian), from Apple's xar/include/xar.h.in:
//
//	struct xar_header {
//		uint32_t magic;                  // 0x78617221 "xar!"
//		uint16_t size;                   // header size, 28
//		uint16_t version;                // 1
//		uint64_t toc_length_compressed;
//		uint64_t toc_length_uncompressed;
//		uint32_t cksum_alg;
//	};
//
// The checksum algorithm numbering is Apple's: 3 means SHA-256 and 4 SHA-512.
// The upstream (mackyle) fork instead uses 3 to mean "other" and appends the
// algorithm's name to a header longer than 28 bytes; ReadHeader honours the
// size field so both are read correctly, but only Apple's form is written.
package xar

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
)

const (
	// Magic is the four-byte signature at the start of every xar archive:
	// the ASCII bytes "xar!".
	Magic uint32 = 0x78617221

	// HeaderSize is the size of the standard header. Apple's tools never
	// write anything else, and libarchive refuses to read anything else.
	HeaderSize = 28

	// maxHeaderSize bounds the extended header the upstream fork can write.
	// 7-Zip uses the same bound to keep false positives down.
	maxHeaderSize = 64

	// Version is the only table-of-contents version ever defined.
	Version = 1
)

// ChecksumAlg identifies the digest algorithm of the table of contents, as
// numbered in the header and named in the TOC's <checksum style="..."> element.
type ChecksumAlg uint32

// Checksum algorithms, numbered as in Apple's xar fork.
const (
	ChecksumNone   ChecksumAlg = 0
	ChecksumSHA1   ChecksumAlg = 1
	ChecksumMD5    ChecksumAlg = 2
	ChecksumSHA256 ChecksumAlg = 3
	ChecksumSHA512 ChecksumAlg = 4
)

// String returns the algorithm's name as written in the TOC's style attribute.
func (a ChecksumAlg) String() string {
	switch a {
	case ChecksumNone:
		return "none"
	case ChecksumSHA1:
		return "sha1"
	case ChecksumMD5:
		return "md5"
	case ChecksumSHA256:
		return "sha256"
	case ChecksumSHA512:
		return "sha512"
	default:
		return fmt.Sprintf("unknown(%d)", uint32(a))
	}
}

// Size returns the digest length in bytes, or 0 for none or unknown.
func (a ChecksumAlg) Size() int {
	switch a {
	case ChecksumSHA1:
		return sha1.Size
	case ChecksumMD5:
		return md5.Size
	case ChecksumSHA256:
		return sha256.Size
	case ChecksumSHA512:
		return sha512.Size
	default:
		return 0
	}
}

// New returns a fresh hash for the algorithm.
func (a ChecksumAlg) New() (hash.Hash, error) {
	switch a {
	case ChecksumSHA1:
		return sha1.New(), nil
	case ChecksumMD5:
		return md5.New(), nil
	case ChecksumSHA256:
		return sha256.New(), nil
	case ChecksumSHA512:
		return sha512.New(), nil
	default:
		return nil, fmt.Errorf("xar: unsupported checksum algorithm %s", a)
	}
}

// ParseChecksumStyle maps a TOC style attribute to an algorithm. Old archives
// wrote the names in upper case, so the comparison is case-insensitive.
func ParseChecksumStyle(style string) (ChecksumAlg, error) {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "none", "":
		return ChecksumNone, nil
	case "sha1":
		return ChecksumSHA1, nil
	case "md5":
		return ChecksumMD5, nil
	case "sha256":
		return ChecksumSHA256, nil
	case "sha512":
		return ChecksumSHA512, nil
	default:
		return 0, fmt.Errorf("xar: unknown checksum style %q", style)
	}
}

// Header is the parsed archive header.
type Header struct {
	Size            uint16
	Version         uint16
	TOCCompressed   uint64
	TOCUncompressed uint64
	ChecksumAlg     ChecksumAlg
}

// ErrNotXar reports that the input does not begin with the xar magic.
var ErrNotXar = errors.New("xar: not a xar archive")

// ReadHeader parses the header at the start of r.
func ReadHeader(r io.ReaderAt) (*Header, error) {
	var buf [HeaderSize]byte
	if _, err := r.ReadAt(buf[:], 0); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrNotXar
		}
		return nil, fmt.Errorf("xar: unable to read header: %w", err)
	}
	if binary.BigEndian.Uint32(buf[0:4]) != Magic {
		return nil, ErrNotXar
	}
	h := &Header{
		Size:            binary.BigEndian.Uint16(buf[4:6]),
		Version:         binary.BigEndian.Uint16(buf[6:8]),
		TOCCompressed:   binary.BigEndian.Uint64(buf[8:16]),
		TOCUncompressed: binary.BigEndian.Uint64(buf[16:24]),
		ChecksumAlg:     ChecksumAlg(binary.BigEndian.Uint32(buf[24:28])),
	}
	if h.Size < HeaderSize || h.Size > maxHeaderSize {
		return nil, fmt.Errorf("xar: implausible header size %d", h.Size)
	}
	// Apple's writer only ever wrote version 1, and 7-Zip notes some very old
	// archives carry 0. Anything else is a format we do not know.
	if h.Version > Version {
		return nil, fmt.Errorf("xar: unsupported version %d", h.Version)
	}
	if h.Size > HeaderSize {
		// The upstream fork's extended header: when cksum_alg is 3 ("other")
		// the algorithm's name follows, NUL-terminated. Apple's fork never
		// writes this, and reads 3 as SHA-256; the name disambiguates.
		extra := make([]byte, h.Size-HeaderSize)
		if _, err := r.ReadAt(extra, HeaderSize); err != nil {
			return nil, fmt.Errorf("xar: unable to read extended header: %w", err)
		}
		if h.ChecksumAlg == 3 {
			name := extra
			if i := bytes.IndexByte(extra, 0); i >= 0 {
				name = extra[:i]
			}
			if alg, err := ParseChecksumStyle(string(name)); err == nil && alg != ChecksumNone {
				h.ChecksumAlg = alg
			}
		}
	}
	return h, nil
}

// HeapOffset is the file offset of the first heap byte: the header and the
// compressed table of contents precede it. Every offset in the TOC is
// relative to this point.
func (h *Header) HeapOffset() int64 {
	return int64(h.Size) + int64(h.TOCCompressed)
}

// MarshalBinary encodes a standard 28-byte header.
func (h *Header) MarshalBinary() ([]byte, error) {
	buf := make([]byte, HeaderSize)
	binary.BigEndian.PutUint32(buf[0:4], Magic)
	binary.BigEndian.PutUint16(buf[4:6], HeaderSize)
	binary.BigEndian.PutUint16(buf[6:8], Version)
	binary.BigEndian.PutUint64(buf[8:16], h.TOCCompressed)
	binary.BigEndian.PutUint64(buf[16:24], h.TOCUncompressed)
	binary.BigEndian.PutUint32(buf[24:28], uint32(h.ChecksumAlg))
	return buf, nil
}
