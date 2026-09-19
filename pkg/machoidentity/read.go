package machoidentity

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"debug/macho"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalid     = errors.New("machoidentity: malformed executable or code signature")
	ErrUnsupported = errors.New("machoidentity: unsupported format or hash algorithm")
	ErrLimit       = errors.New("machoidentity: metadata exceeds limit")
)

const (
	maxArchitectures = 64
	maxMetadata      = 16 << 20
	maxBlobs         = 256
	maxString        = 4096
)

// CodeDirectory is one hash algorithm's observed signing identity. CDHash is
// truncated to twenty bytes; FullHash retains the algorithm's entire digest.
type CodeDirectory struct {
	HashType  uint8  `json:"hashType"`
	Algorithm string `json:"algorithm"`
	CDHash    string `json:"cdhash"`
	FullHash  string `json:"fullHash"`
	SigningID string `json:"signingID"`
	TeamID    string `json:"teamID,omitempty"`
	Flags     uint32 `json:"flags"`
	Version   uint32 `json:"version"`
}

// Architecture describes one image. CPU and Subtype retain Apple's numeric
// values, including capability bits. Name is a display label. A nil directory
// list means no embedded signature was found; no trust check is implied.
type Architecture struct {
	Name            string          `json:"name"`
	CPU             uint32          `json:"cpu"`
	Subtype         uint32          `json:"subtype"`
	CodeDirectories []CodeDirectory `json:"codeDirectories"`
}

// Read reads every architecture from exactly size bytes. It checks context
// between bounded reads. The source must remain immutable during the call.
// All results are discarded on error, including unsupported signature forms.
func Read(ctx context.Context, r io.ReaderAt, size int64) ([]Architecture, error) {
	if r == nil || size < 4 {
		return nil, ErrInvalid
	}
	s := source{ctx: ctx, r: r, size: size}
	head, err := s.read(0, 4)
	if err != nil {
		return nil, err
	}
	magic := binary.BigEndian.Uint32(head)
	var order binary.ByteOrder = binary.BigEndian
	wide := false
	switch magic {
	case 0xcafebabe, 0xcafebabf:
		wide = magic == 0xcafebabf
	case 0xbebafeca, 0xbfbafeca:
		order = binary.LittleEndian
		wide = magic == 0xbfbafeca
	default:
		a, err := s.image()
		if err != nil {
			return nil, err
		}
		return []Architecture{a}, nil
	}
	head, err = s.read(4, 4)
	if err != nil {
		return nil, err
	}
	count := order.Uint32(head)
	if count == 0 {
		return nil, ErrInvalid
	}
	if count > maxArchitectures {
		return nil, ErrLimit
	}
	stride := int64(20)
	if wide {
		stride = 32
	}
	tableEnd := 8 + int64(count)*stride
	table, err := s.read(8, int64(count)*stride)
	if err != nil {
		return nil, err
	}
	var result []Architecture
	type extent struct{ start, end int64 }
	var extents []extent
	seen := map[[2]uint32]bool{}
	for i := uint32(0); i < count; i++ {
		e := table[int64(i)*stride : int64(i+1)*stride]
		cpu, sub := order.Uint32(e), order.Uint32(e[4:])
		offset, length := uint64(order.Uint32(e[8:])), uint64(order.Uint32(e[12:]))
		if wide {
			offset, length = order.Uint64(e[8:]), order.Uint64(e[16:])
		}
		if offset < uint64(tableEnd) || offset > uint64(size) || length > uint64(size)-offset || length < 4 {
			return nil, ErrInvalid
		}
		key := [2]uint32{cpu, sub}
		if seen[key] {
			return nil, ErrInvalid
		}
		seen[key] = true
		start, end := int64(offset), int64(offset+length)
		for _, previous := range extents {
			if start < previous.end && end > previous.start {
				return nil, ErrInvalid
			}
		}
		extents = append(extents, extent{start, end})
		a, err := (source{ctx: ctx, r: io.NewSectionReader(r, start, int64(length)), size: int64(length)}).image()
		if err != nil {
			return nil, err
		}
		if a.CPU != cpu || a.Subtype != sub {
			return nil, ErrInvalid
		}
		result = append(result, a)
	}
	return result, nil
}

type source struct {
	ctx  context.Context
	r    io.ReaderAt
	size int64
}

func (s source) read(offset, length int64) ([]byte, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if offset < 0 || length < 0 || offset > s.size || length > s.size-offset {
		return nil, ErrInvalid
	}
	if length > maxMetadata {
		return nil, ErrLimit
	}
	b := make([]byte, int(length))
	if _, err := io.ReadFull(io.NewSectionReader(s.r, offset, length), b); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return b, nil
}

func (s source) image() (Architecture, error) {
	var a Architecture
	h, err := s.read(0, 28)
	if err != nil {
		return a, err
	}
	var order binary.ByteOrder = binary.LittleEndian
	headerSize := int64(28)
	switch binary.BigEndian.Uint32(h) {
	case 0xfeedface:
		order = binary.BigEndian
	case 0xfeedfacf:
		order = binary.BigEndian
		headerSize = 32
	case 0xcefaedfe:
	case 0xcffaedfe:
		headerSize = 32
	default:
		return a, ErrUnsupported
	}
	a.CPU, a.Subtype = order.Uint32(h[4:]), order.Uint32(h[8:])
	a.Name = architectureName(a.CPU, a.Subtype)
	count, length := order.Uint32(h[16:]), order.Uint32(h[20:])
	if uint64(count)*8 > uint64(length) {
		return a, ErrInvalid
	}
	commands, err := s.read(headerSize, int64(length))
	if err != nil {
		return a, err
	}
	found := false
	for i := uint32(0); i < count; i++ {
		if len(commands) < 8 {
			return a, ErrInvalid
		}
		kind, n := order.Uint32(commands), order.Uint32(commands[4:])
		if n < 8 || uint64(n) > uint64(len(commands)) || n%4 != 0 {
			return a, ErrInvalid
		}
		if kind == 0x1d {
			if found || n != 16 {
				return a, ErrInvalid
			}
			found = true
			offset, size := order.Uint32(commands[8:]), order.Uint32(commands[12:])
			if int64(offset) < headerSize+int64(length) {
				return a, ErrInvalid
			}
			blob, err := s.read(int64(offset), int64(size))
			if err != nil {
				return a, err
			}
			a.CodeDirectories, err = directories(blob)
			if err != nil {
				return a, err
			}
		}
		commands = commands[n:]
	}
	if len(commands) != 0 {
		return a, ErrInvalid
	}
	return a, nil
}

func architectureName(cpu, sub uint32) string {
	base := sub & 0x00ffffff
	switch macho.Cpu(cpu) {
	case macho.CpuAmd64:
		if base == 3 {
			return "x86_64"
		}
		if base == 8 {
			return "x86_64h"
		}
	case macho.CpuArm64:
		if base == 0 {
			return "arm64"
		}
		if base == 2 {
			return "arm64e"
		}
	case macho.Cpu386:
		if base == 3 {
			return "i386"
		}
	}
	return fmt.Sprintf("%s:%d", macho.Cpu(cpu), sub)
}

func directories(blob []byte) ([]CodeDirectory, error) {
	be := binary.BigEndian
	if len(blob) < 12 || be.Uint32(blob) != 0xfade0cc0 {
		return nil, ErrInvalid
	}
	length, count := be.Uint32(blob[4:]), be.Uint32(blob[8:])
	if count > maxBlobs {
		return nil, ErrLimit
	}
	if uint64(length) > uint64(len(blob)) || uint64(length) < 12+uint64(count)*8 {
		return nil, ErrInvalid
	}
	blob = blob[:length]
	seen := map[uint32]bool{}
	var result []CodeDirectory
	var ranges [][2]uint32
	for i := uint32(0); i < count; i++ {
		entry := blob[12+i*8 : 20+i*8]
		kind, off := be.Uint32(entry), be.Uint32(entry[4:])
		if seen[kind] || off < 12+count*8 || uint64(off)+8 > uint64(len(blob)) {
			return nil, ErrInvalid
		}
		seen[kind] = true
		n := be.Uint32(blob[off+4:])
		if n < 8 || uint64(off)+uint64(n) > uint64(len(blob)) {
			return nil, ErrInvalid
		}
		for _, previous := range ranges {
			if off < previous[1] && off+n > previous[0] {
				return nil, ErrInvalid
			}
		}
		ranges = append(ranges, [2]uint32{off, off + n})
		if kind != 0 && (kind < 0x1000 || kind > 0x1005) {
			continue
		}
		cd, err := directory(blob[off : off+n])
		if err != nil {
			return nil, err
		}
		result = append(result, cd)
	}
	if !seen[0] || len(result) == 0 {
		return nil, ErrInvalid
	}
	return result, nil
}

func directory(b []byte) (CodeDirectory, error) {
	var d CodeDirectory
	be := binary.BigEndian
	if len(b) < 44 || be.Uint32(b) != 0xfade0c02 || uint64(be.Uint32(b[4:])) != uint64(len(b)) {
		return d, ErrInvalid
	}
	d.Version, d.Flags = be.Uint32(b[8:]), be.Uint32(b[12:])
	minimum := 44
	for _, version := range []struct {
		value uint32
		size  int
	}{{0x20100, 48}, {0x20200, 52}, {0x20300, 64}, {0x20400, 88}, {0x20500, 96}, {0x20600, 108}} {
		if d.Version >= version.value {
			minimum = version.size
		}
	}
	if len(b) < minimum {
		return d, ErrInvalid
	}
	if d.Version < 0x20000 || d.Version > 0x20600 {
		return d, ErrUnsupported
	}
	var digest []byte
	d.HashType = b[37]
	expectedSize := byte(0)
	switch d.HashType {
	case 1:
		sum := sha1.Sum(b)
		digest = sum[:]
		d.Algorithm = "sha1"
		expectedSize = 20
	case 2, 3:
		sum := sha256.Sum256(b)
		digest = sum[:]
		d.Algorithm = "sha256"
		expectedSize = 32
		if d.HashType == 3 {
			expectedSize = 20
		}
	case 4:
		sum := sha512.Sum384(b)
		digest = sum[:]
		d.Algorithm = "sha384"
		expectedSize = 48
	default:
		return d, ErrUnsupported
	}
	if b[36] != expectedSize {
		return d, ErrInvalid
	}
	hashOff, special, slots := uint64(be.Uint32(b[16:])), uint64(be.Uint32(b[24:])), uint64(be.Uint32(b[28:]))
	if hashOff > uint64(len(b)) || special*uint64(expectedSize) > hashOff || slots*uint64(expectedSize) > uint64(len(b))-hashOff {
		return d, ErrInvalid
	}
	stringsEnd := hashOff - special*uint64(expectedSize)
	if stringsEnd < uint64(minimum) {
		return d, ErrInvalid
	}
	var err error
	d.SigningID, err = identifier(b[:stringsEnd], be.Uint32(b[20:]), minimum)
	if err != nil || d.SigningID == "" {
		return d, ErrInvalid
	}
	if d.Version >= 0x20200 && be.Uint32(b[48:]) != 0 {
		d.TeamID, err = identifier(b[:stringsEnd], be.Uint32(b[48:]), minimum)
		if err != nil {
			return d, err
		}
	}
	d.CDHash, d.FullHash = hex.EncodeToString(digest[:20]), hex.EncodeToString(digest)
	return d, nil
}

func identifier(b []byte, offset uint32, minimum int) (string, error) {
	if uint64(offset) >= uint64(len(b)) || uint64(offset) < uint64(minimum) {
		return "", ErrInvalid
	}
	value := b[offset:]
	if len(value) > maxString {
		value = value[:maxString]
	}
	n := strings.IndexByte(string(value), 0)
	if n < 0 || !utf8.Valid(value[:n]) {
		return "", ErrInvalid
	}
	return string(value[:n]), nil
}
