// Reading cpio archives, odc and newc, detected from the magic.
package cpio

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"time"
)

// Reader reads entries from a cpio stream.
type Reader struct {
	r      *bufio.Reader
	cur    io.Reader // limited reader over the current entry's data
	remain int64     // bytes of the current entry not yet consumed
	pad    int64     // alignment padding after the current entry's data
	format Format
	done   bool
}

// NewReader returns a Reader over r. The format is detected from the first
// header.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 64<<10)}
}

// Format returns the header format detected so far.
func (cr *Reader) Format() Format { return cr.format }

// Next advances to the next entry. It returns io.EOF after the trailer.
func (cr *Reader) Next() (*Header, error) {
	if cr.done {
		return nil, io.EOF
	}
	// Skip whatever the caller did not read of the previous entry.
	if cr.remain > 0 || cr.pad > 0 {
		if _, err := io.CopyN(io.Discard, cr.r, cr.remain+cr.pad); err != nil {
			return nil, fmt.Errorf("cpio: unable to skip entry data: %w", err)
		}
		cr.remain, cr.pad = 0, 0
	}

	magic, err := cr.r.Peek(6)
	if err != nil {
		if err == io.EOF {
			// A stream that simply ends without a trailer: tolerate it, as
			// the tools do, rather than call a complete extraction corrupt.
			cr.done = true
			return nil, io.EOF
		}
		return nil, fmt.Errorf("cpio: unable to read header: %w", err)
	}

	var hdr *Header
	switch string(magic) {
	case MagicODC:
		hdr, err = cr.readODC()
	case MagicNewc, MagicNewcCRC:
		hdr, err = cr.readNewc(string(magic))
	default:
		return nil, fmt.Errorf("%w: unknown magic %q", ErrHeader, magic)
	}
	if err != nil {
		return nil, err
	}
	if hdr.Name == Trailer {
		cr.done = true
		return nil, io.EOF
	}
	cr.remain = hdr.Size
	cr.cur = io.LimitReader(cr.r, hdr.Size)
	cr.format = hdr.Format
	return hdr, nil
}

// Read reads the current entry's data.
func (cr *Reader) Read(p []byte) (int, error) {
	if cr.cur == nil || cr.remain == 0 {
		return 0, io.EOF
	}
	n, err := cr.cur.Read(p)
	cr.remain -= int64(n)
	if cr.remain == 0 && err == nil {
		err = io.EOF
	}
	if err == io.EOF && cr.remain > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func (cr *Reader) readODC() (*Header, error) {
	var buf [76]byte
	if _, err := io.ReadFull(cr.r, buf[:]); err != nil {
		return nil, fmt.Errorf("%w: truncated odc header: %v", ErrHeader, err)
	}
	fields := [...]int{6, 6, 6, 6, 6, 6, 6, 6, 11, 6, 11}
	vals := make([]uint64, len(fields))
	off := 0
	for i, w := range fields {
		v, err := strconv.ParseUint(string(buf[off:off+w]), 8, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: field %d of odc header is not octal: %q", ErrHeader, i, buf[off:off+w])
		}
		vals[i] = v
		off += w
	}
	nameSize := vals[9]
	if nameSize == 0 || nameSize > 4096 {
		return nil, fmt.Errorf("%w: implausible name size %d", ErrHeader, nameSize)
	}
	name := make([]byte, nameSize)
	if _, err := io.ReadFull(cr.r, name); err != nil {
		return nil, fmt.Errorf("%w: truncated name: %v", ErrHeader, err)
	}
	return &Header{
		Name:    trimNUL(name),
		Dev:     vals[1],
		Inode:   vals[2],
		Mode:    uint32(vals[3]),
		UID:     uint32(vals[4]),
		GID:     uint32(vals[5]),
		NLink:   uint32(vals[6]),
		RDev:    vals[7],
		ModTime: time.Unix(int64(vals[8]), 0).UTC(),
		Size:    int64(vals[10]),
		Format:  FormatODC,
	}, nil
}

func (cr *Reader) readNewc(magic string) (*Header, error) {
	var buf [110]byte
	if _, err := io.ReadFull(cr.r, buf[:]); err != nil {
		return nil, fmt.Errorf("%w: truncated newc header: %v", ErrHeader, err)
	}
	// After the magic: ino, mode, uid, gid, nlink, mtime, filesize, devmajor,
	// devminor, rdevmajor, rdevminor, namesize, check — thirteen 8-char hex
	// fields.
	vals := make([]uint64, 13)
	for i := range vals {
		off := 6 + i*8
		v, err := strconv.ParseUint(string(buf[off:off+8]), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: field %d of newc header is not hex: %q", ErrHeader, i, buf[off:off+8])
		}
		vals[i] = v
	}
	nameSize := vals[11]
	if nameSize == 0 || nameSize > 4096 {
		return nil, fmt.Errorf("%w: implausible name size %d", ErrHeader, nameSize)
	}
	// The name is padded so the data starts on a 4-byte boundary, counted
	// from the start of the header (110 bytes).
	namePadded := (110 + int64(nameSize) + 3) &^ 3
	name := make([]byte, namePadded-110)
	if _, err := io.ReadFull(cr.r, name); err != nil {
		return nil, fmt.Errorf("%w: truncated name: %v", ErrHeader, err)
	}
	size := int64(vals[6])
	format := FormatNewc
	if magic == MagicNewcCRC {
		format = FormatNewcCRC
	}
	cr.pad = ((size + 3) &^ 3) - size
	return &Header{
		Name:    trimNUL(name[:nameSize]),
		Dev:     vals[7]<<32 | vals[8],
		Inode:   vals[0],
		Mode:    uint32(vals[1]),
		UID:     uint32(vals[2]),
		GID:     uint32(vals[3]),
		NLink:   uint32(vals[4]),
		RDev:    vals[9]<<32 | vals[10],
		ModTime: time.Unix(int64(vals[5]), 0).UTC(),
		Size:    size,
		Format:  format,
	}, nil
}

func trimNUL(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
