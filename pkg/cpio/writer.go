// Writing cpio archives in the portable ASCII (odc) format pkgbuild uses.
package cpio

import (
	"bufio"
	"fmt"
	"io"
)

// Writer writes an odc cpio archive.
type Writer struct {
	w      *bufio.Writer
	remain int64 // data bytes still owed for the current entry
	closed bool
	err    error
}

// NewWriter returns a Writer that writes odc entries to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: bufio.NewWriterSize(w, 64<<10)}
}

// WriteHeader begins a new entry. The caller must then write exactly
// hdr.Size bytes of data before the next WriteHeader or Close.
func (cw *Writer) WriteHeader(hdr *Header) error {
	if cw.err != nil {
		return cw.err
	}
	if cw.closed {
		return fmt.Errorf("cpio: write after close")
	}
	if cw.remain > 0 {
		return fmt.Errorf("cpio: previous entry is short by %d bytes", cw.remain)
	}
	if err := cw.writeODC(hdr); err != nil {
		cw.err = err
		return err
	}
	cw.remain = hdr.Size
	return nil
}

// Write writes data for the current entry.
func (cw *Writer) Write(p []byte) (int, error) {
	if cw.err != nil {
		return 0, cw.err
	}
	if int64(len(p)) > cw.remain {
		cw.err = fmt.Errorf("cpio: entry data exceeds declared size")
		return 0, cw.err
	}
	n, err := cw.w.Write(p)
	cw.remain -= int64(n)
	if err != nil {
		cw.err = err
	}
	return n, err
}

// Close writes the trailer and flushes. It does not close the underlying
// writer.
func (cw *Writer) Close() error {
	if cw.err != nil {
		return cw.err
	}
	if cw.closed {
		return nil
	}
	if cw.remain > 0 {
		cw.err = fmt.Errorf("cpio: last entry is short by %d bytes", cw.remain)
		return cw.err
	}
	// The trailer is an ordinary header with nlink 1 and no data, as cpio
	// itself writes it; readers stop at the name.
	if err := cw.writeODC(&Header{Name: Trailer, NLink: 1}); err != nil {
		cw.err = err
		return err
	}
	cw.closed = true
	return cw.w.Flush()
}

// writeODC emits one 76-byte odc header and the NUL-terminated name.
//
// Every field is zero-padded octal. The 6-character fields hold 18 bits and
// the 11-character ones 33, which bounds what an odc archive can describe:
// an inode over 262143 or a file over 8 GiB does not fit. pkgbuild writes
// small sequential inodes for this reason, and so does the caller.
func (cw *Writer) writeODC(hdr *Header) error {
	name := hdr.Name
	nameSize := len(name) + 1
	if nameSize > 0o777777 {
		return fmt.Errorf("cpio: name too long: %q", name)
	}
	if hdr.Size < 0 || hdr.Size > 0o77777777777 {
		return fmt.Errorf("cpio: %s: size %d does not fit an odc header", name, hdr.Size)
	}
	// The 6-character fields hold 18 bits. Masking an over-large value
	// writes a different number silently, and the bill of materials records
	// the real one, so the payload and the Bom would disagree about who owns
	// the file with nothing to show for it. uid and gid are the ones that
	// reach this from real input: a Mac bound to a directory service issues
	// uids in the millions, and --ownership preserve passes them straight
	// through.
	for _, f := range [...]struct {
		name  string
		value uint64
	}{
		{"uid", uint64(hdr.UID)},
		{"gid", uint64(hdr.GID)},
		{"inode", hdr.Inode},
		{"link count", uint64(hdr.NLink)},
		{"device", hdr.Dev},
		{"rdev", hdr.RDev},
	} {
		if f.value > 0o777777 {
			return fmt.Errorf("cpio: %s: %s %d does not fit an odc header (max %d)", name, f.name, f.value, 0o777777)
		}
	}
	var mtime int64
	if !hdr.ModTime.IsZero() {
		mtime = hdr.ModTime.Unix()
		if mtime < 0 {
			mtime = 0
		}
	}
	_, err := fmt.Fprintf(cw.w, "%s%06o%06o%06o%06o%06o%06o%06o%011o%06o%011o%s\x00",
		MagicODC,
		hdr.Dev,
		hdr.Inode,
		hdr.Mode&0o777777,
		hdr.UID,
		hdr.GID,
		hdr.NLink,
		hdr.RDev,
		mtime&0o77777777777,
		nameSize,
		hdr.Size,
		name,
	)
	return err
}
