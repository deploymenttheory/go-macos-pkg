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

	// join makes Read continue across consecutive entries that share a
	// name, which is how a --large-payload package carries a file too
	// large for an odc header: pkgbuild splits it into segments under the
	// one path and the Installer joins them again.
	join    bool
	name    string  // the logical entry being read
	pending *Header // read ahead while joining, not yet returned by Next
}

// JoinSegments makes the reader treat consecutive entries that share a
// name as one file, concatenating their data. The header Next returns
// describes the first segment, so its Size is that segment's, not the
// whole file's; the bill of materials carries the true size.
func (cr *Reader) JoinSegments(on bool) { cr.join = on }

// NewReader returns a Reader over r. The format is detected from the first
// header.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 64<<10)}
}

// Format returns the header format detected so far.
func (cr *Reader) Format() Format { return cr.format }

// Next advances to the next entry. It returns io.EOF after the trailer.
func (cr *Reader) Next() (*Header, error) {
	if cr.pending != nil {
		hdr := cr.pending
		cr.pending = nil
		cr.begin(hdr)
		return hdr, nil
	}
	hdr, err := cr.nextHeader()
	if err != nil {
		return nil, err
	}
	cr.begin(hdr)
	return hdr, nil
}

// begin makes hdr the current entry.
func (cr *Reader) begin(hdr *Header) {
	cr.remain = hdr.Size
	cr.cur = io.LimitReader(cr.r, hdr.Size)
	cr.format = hdr.Format
	cr.name = hdr.Name
}

// nextHeader skips whatever is left of the current entry and reads the
// header that follows. It returns io.EOF at the trailer.
func (cr *Reader) nextHeader() (*Header, error) {
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
	return hdr, nil
}

// Read reads the current entry's data, continuing into the entries that
// follow when they are segments of the same file and JoinSegments is on.
func (cr *Reader) Read(p []byte) (int, error) {
	if cr.cur == nil {
		return 0, io.EOF
	}
	if cr.remain == 0 {
		if !cr.join {
			return 0, io.EOF
		}
		if err := cr.nextSegment(); err != nil {
			return 0, err
		}
	}
	n, err := cr.cur.Read(p)
	cr.remain -= int64(n)
	if err == io.EOF && cr.remain > 0 {
		return n, io.ErrUnexpectedEOF
	}
	if cr.remain == 0 {
		// The segment is done. Without joining that is the end of the
		// entry; with it, the next Read decides.
		if cr.join {
			if err == io.EOF {
				err = nil
			}
		} else if err == nil {
			err = io.EOF
		}
	}
	return n, err
}

// nextSegment advances to the entry that follows when it continues the
// current one. It returns io.EOF when the file is complete, keeping the
// header it read for Next.
func (cr *Reader) nextSegment() error {
	hdr, err := cr.nextHeader()
	if err != nil {
		return io.EOF
	}
	if hdr.Name != cr.name {
		cr.pending = hdr
		return io.EOF
	}
	cr.remain = hdr.Size
	cr.cur = io.LimitReader(cr.r, hdr.Size)
	return nil
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
	// devminor, rdevmajor, rdevminor, namesize, check: thirteen 8-char hex
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
