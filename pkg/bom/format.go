// Package bom reads and writes Apple bill-of-materials files: the Bom entry
// of a flat package, which lists every path the payload installs with its
// mode, owner, size and CRC-32, and later becomes the receipt pkgutil
// queries.
//
// The format is undocumented by Apple. What is known comes from bomutils
// (Fabian Renn) and apple-bom (Gregory Szorc); see NOTICE. All integers are
// big-endian.
//
//	Header (32 bytes)
//	  magic           8   "BOMStore"
//	  version         4   1
//	  numberOfBlocks  4   populated blocks, not counting block 0
//	  indexOffset     4   file offset of the block table
//	  indexLength     4   its length, including the free list that follows it
//	  varsOffset      4   file offset of the variables table
//	  varsLength      4
//
//	Block table: count u32, then count x { address u32, length u32 }.
//	Block 0 is always the null block. Everything else is addressed by index.
//
//	Variables: count u32, then count x { blockIndex u32, nameLength u8, name }.
//	Apple writes BomInfo, Paths, HLIndex, VIndex and Size64.
//
//	Tree block ("tree"): magic 4, version u32, child u32 (a Paths block),
//	blockSize u32 (4096), pathCount u32, unknown u8.
//
//	Paths block: isLeaf u16, count u16, forward u32, backward u32, then
//	count x { index0 u32, index1 u32 }. In a leaf, index0 is a PathInfo
//	block and index1 a File block. In a branch, index0 is a child Paths
//	block. Leaves are chained through forward/backward.
//
//	PathInfo block: id u32, index u32 (a PathRecord block).
//
//	PathRecord block (31 bytes plus an optional link target):
//	  type 1, unknown 1, architecture 2, mode 2, user 4, group 4, mtime 4,
//	  size 4, unknown 1, checksum-or-devtype 4, linkNameLength 4, linkName.
//
//	File block: parentID u32, name (NUL-terminated).
package bom

import (
	"errors"
	"fmt"
	"io/fs"
	"time"
)

// Magic is the eight-byte file signature.
const Magic = "BOMStore"

// Well-known variable names.
const (
	VarBomInfo = "BomInfo"
	VarPaths   = "Paths"
	VarHLIndex = "HLIndex"
	VarVIndex  = "VIndex"
	VarSize64  = "Size64"
)

// treeMagic starts every tree block.
const treeMagic = "tree"

// Sizes of the fixed structures.
const (
	headerSize     = 32
	treeSize       = 21
	pathRecordSize = 31
)

// PathType classifies a path record.
type PathType uint8

// Path types.
const (
	TypeFile      PathType = 1
	TypeDirectory PathType = 2
	TypeLink      PathType = 3
	TypeDevice    PathType = 4
)

func (t PathType) String() string {
	switch t {
	case TypeFile:
		return "file"
	case TypeDirectory:
		return "dir"
	case TypeLink:
		return "link"
	case TypeDevice:
		return "dev"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(t))
	}
}

// Entry is one path in the bill of materials with its recorded metadata.
type Entry struct {
	// Path is the slash-joined path from the payload root, beginning with
	// ".": the root itself is ".", a file under it "./usr/local/bin/tool".
	Path     string
	ID       uint32
	ParentID uint32
	Type     PathType
	// Architecture is 0 for non-executables; for Mach-O files pkgbuild
	// records a CPU type summary. Its exact encoding is not fully known.
	Architecture uint16
	// Mode holds the Unix permission and type bits (st_mode).
	Mode    uint16
	UID     uint32
	GID     uint32
	ModTime time.Time
	// Size is the file size. Sizes over 4 GiB come from the Size64 tree.
	Size int64
	// Checksum is the POSIX cksum(1) CRC of a file's content (see
	// checksum.go); 0xFFFFFFFF for an empty file, 0 for directories.
	Checksum uint32
	// DevType is the device number of a device node.
	DevType uint32
	// LinkTarget is a symlink's target.
	LinkTarget string
}

// FileMode converts the recorded mode to an fs.FileMode.
func (e *Entry) FileMode() fs.FileMode {
	const (
		typeMask = 0o170000
		modeDir  = 0o040000
		modeLink = 0o120000
		modeChr  = 0o020000
		modeBlk  = 0o060000
		modeFIFO = 0o010000
		modeSock = 0o140000
	)
	m := fs.FileMode(e.Mode & 0o777)
	switch e.Mode & typeMask {
	case modeDir:
		m |= fs.ModeDir
	case modeLink:
		m |= fs.ModeSymlink
	case modeChr:
		m |= fs.ModeDevice | fs.ModeCharDevice
	case modeBlk:
		m |= fs.ModeDevice
	case modeFIFO:
		m |= fs.ModeNamedPipe
	case modeSock:
		m |= fs.ModeSocket
	}
	if e.Mode&0o4000 != 0 {
		m |= fs.ModeSetuid
	}
	if e.Mode&0o2000 != 0 {
		m |= fs.ModeSetgid
	}
	if e.Mode&0o1000 != 0 {
		m |= fs.ModeSticky
	}
	return m
}

// ErrInvalid reports a malformed bill of materials.
var ErrInvalid = errors.New("bom: invalid format")
