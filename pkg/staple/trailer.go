// Package staple attaches notarization tickets to flat packages and
// recognizes them.
//
// A stapled ticket is not part of the xar archive at all. It is appended
// after the archive's last heap byte as a trailer, so an unaware reader
// ignores it and a stapler-aware one finds it by looking at the end of the
// file:
//
//	[xar archive]
//	[Trailer{type=Terminator, length=0}]   16 bytes
//	[ticket bytes]                         begin "s8ch"
//	[Trailer{type=Ticket, length=len}]     16 bytes
//
// Trailer layout, little-endian (the only little-endian structure in the
// whole format; it was designed by a different team):
//
//	magic    4  "t8lr"
//	version  2  1
//	type     2  0 invalid, 1 terminator, 2 ticket
//	length   4  ticket length (ticket trailer) or 0 (terminator)
//	unused   4
package staple

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// TrailerSize is the size of one trailer record.
const TrailerSize = 16

// Trailer magic and types.
var (
	trailerMagic = []byte("t8lr")
	ticketMagic  = []byte("s8ch")
)

const (
	trailerVersion    = 1
	trailerTerminator = 1
	trailerTicket     = 2
)

// Trailer is one trailer record.
type Trailer struct {
	Version uint16
	Type    uint16
	Length  uint32
}

// marshal encodes a trailer.
func (t Trailer) marshal() []byte {
	b := make([]byte, TrailerSize)
	copy(b[0:4], trailerMagic)
	binary.LittleEndian.PutUint16(b[4:6], t.Version)
	binary.LittleEndian.PutUint16(b[6:8], t.Type)
	binary.LittleEndian.PutUint32(b[8:12], t.Length)
	return b
}

func parseTrailer(b []byte) (Trailer, bool) {
	if len(b) < TrailerSize || !bytes.Equal(b[0:4], trailerMagic) {
		return Trailer{}, false
	}
	return Trailer{
		Version: binary.LittleEndian.Uint16(b[4:6]),
		Type:    binary.LittleEndian.Uint16(b[6:8]),
		Length:  binary.LittleEndian.Uint32(b[8:12]),
	}, true
}

// Ticket is a stapled notarization ticket as found in a file.
type Ticket struct {
	// Data is the raw ticket: a CMS blob Apple signed, beginning "s8ch".
	Data []byte
	// Offset is the file offset of the terminator trailer that begins the
	// stapled region; everything from here to the end of the file is the
	// staple.
	Offset int64
}

// ErrNoTicket reports that a file carries no stapled ticket.
var ErrNoTicket = errors.New("staple: no notarization ticket is stapled")

// Read looks for a stapled ticket at the end of a file of the given size.
// It returns ErrNoTicket when the file ends in something other than a
// ticket trailer.
func Read(r io.ReaderAt, size int64) (*Ticket, error) {
	if size < 2*TrailerSize {
		return nil, ErrNoTicket
	}
	var last [TrailerSize]byte
	if _, err := r.ReadAt(last[:], size-TrailerSize); err != nil {
		return nil, fmt.Errorf("staple: unable to read trailer: %w", err)
	}
	tr, ok := parseTrailer(last[:])
	if !ok || tr.Type != trailerTicket {
		return nil, ErrNoTicket
	}
	if tr.Version != trailerVersion {
		return nil, fmt.Errorf("staple: unsupported trailer version %d", tr.Version)
	}
	// terminator | ticket | ticket trailer
	start := size - TrailerSize - int64(tr.Length) - TrailerSize
	if start < 0 {
		return nil, fmt.Errorf("staple: ticket length %d exceeds file", tr.Length)
	}
	var term [TrailerSize]byte
	if _, err := r.ReadAt(term[:], start); err != nil {
		return nil, fmt.Errorf("staple: unable to read terminator: %w", err)
	}
	if t, ok := parseTrailer(term[:]); !ok || t.Type != trailerTerminator {
		return nil, fmt.Errorf("staple: ticket is not preceded by a terminator trailer")
	}
	data := make([]byte, tr.Length)
	if _, err := r.ReadAt(data, start+TrailerSize); err != nil {
		return nil, fmt.Errorf("staple: unable to read ticket: %w", err)
	}
	if !bytes.HasPrefix(data, ticketMagic) {
		return nil, fmt.Errorf("staple: ticket does not begin with %q", ticketMagic)
	}
	return &Ticket{Data: data, Offset: start}, nil
}

// Encode returns the bytes to append to a file to staple ticket to it.
func Encode(ticket []byte) []byte {
	out := make([]byte, 0, 2*TrailerSize+len(ticket))
	out = append(out, Trailer{Version: trailerVersion, Type: trailerTerminator}.marshal()...)
	out = append(out, ticket...)
	out = append(out, Trailer{Version: trailerVersion, Type: trailerTicket, Length: uint32(len(ticket))}.marshal()...)
	return out
}
