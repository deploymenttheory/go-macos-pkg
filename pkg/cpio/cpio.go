// Package cpio reads and writes the cpio archives that carry a flat
// package's Payload and Scripts.
//
// Apple's pkgbuild writes the portable ASCII ("odc", SUSv2) format: a
// 76-byte header of fixed-width octal ASCII fields, the NUL-terminated name,
// then the data, with no alignment, ended by an entry named TRAILER!!!.
// The reader also understands the SVR4 "newc" format (070701, and 070702
// with checksums), which other tools produce and which appears inside some
// Apple payloads.
//
// Layout of an odc header, from the cpio(5) portable format:
//
//	magic     6  "070707"
//	dev       6
//	ino       6
//	mode      6
//	uid       6
//	gid       6
//	nlink     6
//	rdev      6
//	mtime    11
//	namesize  6  including the terminating NUL
//	filesize 11
package cpio

import (
	"errors"
	"io/fs"
	"time"
)

// Format identifies a cpio header format.
type Format int

// Supported formats.
const (
	FormatUnknown Format = iota
	// FormatODC is the portable ASCII format pkgbuild writes.
	FormatODC
	// FormatNewc is the SVR4 format with 8-character hex fields.
	FormatNewc
	// FormatNewcCRC is newc with a per-file checksum (070702).
	FormatNewcCRC
)

func (f Format) String() string {
	switch f {
	case FormatODC:
		return "odc"
	case FormatNewc:
		return "newc"
	case FormatNewcCRC:
		return "newc-crc"
	default:
		return "unknown"
	}
}

// Magic strings.
const (
	MagicODC     = "070707"
	MagicNewc    = "070701"
	MagicNewcCRC = "070702"

	// Trailer is the name of the entry that ends an archive.
	Trailer = "TRAILER!!!"
)

// Mode type bits, as in <sys/stat.h>; kept here so the package has no
// dependency on syscall and behaves identically on Windows.
const (
	ModeTypeMask = 0o170000
	ModeSocket   = 0o140000
	ModeSymlink  = 0o120000
	ModeRegular  = 0o100000
	ModeBlockDev = 0o060000
	ModeDir      = 0o040000
	ModeCharDev  = 0o020000
	ModeFIFO     = 0o010000
	ModeSetUID   = 0o004000
	ModeSetGID   = 0o002000
	ModeSticky   = 0o001000
	ModePerm     = 0o000777
)

// Header describes one archive entry.
type Header struct {
	Name    string
	Dev     uint64
	Inode   uint64
	Mode    uint32 // type bits and permissions, Unix st_mode
	UID     uint32
	GID     uint32
	NLink   uint32
	RDev    uint64
	ModTime time.Time
	Size    int64
	// Format records the header format the entry was read in; the writer
	// ignores it.
	Format Format
}

// IsDir reports whether the entry is a directory.
func (h *Header) IsDir() bool { return h.Mode&ModeTypeMask == ModeDir }

// IsRegular reports whether the entry is a regular file.
func (h *Header) IsRegular() bool { return h.Mode&ModeTypeMask == ModeRegular }

// IsSymlink reports whether the entry is a symbolic link; its target is the
// entry's data.
func (h *Header) IsSymlink() bool { return h.Mode&ModeTypeMask == ModeSymlink }

// FileMode converts the Unix mode to an fs.FileMode.
func (h *Header) FileMode() fs.FileMode {
	m := fs.FileMode(h.Mode & ModePerm)
	switch h.Mode & ModeTypeMask {
	case ModeDir:
		m |= fs.ModeDir
	case ModeSymlink:
		m |= fs.ModeSymlink
	case ModeCharDev:
		m |= fs.ModeDevice | fs.ModeCharDevice
	case ModeBlockDev:
		m |= fs.ModeDevice
	case ModeFIFO:
		m |= fs.ModeNamedPipe
	case ModeSocket:
		m |= fs.ModeSocket
	}
	if h.Mode&ModeSetUID != 0 {
		m |= fs.ModeSetuid
	}
	if h.Mode&ModeSetGID != 0 {
		m |= fs.ModeSetgid
	}
	if h.Mode&ModeSticky != 0 {
		m |= fs.ModeSticky
	}
	return m
}

// ErrHeader reports a malformed header.
var ErrHeader = errors.New("cpio: invalid header")
