package bom

import (
	"encoding/binary"
	"testing"
)

// TestFillRecordLinkLenOverflow guards the width-mismatch overflow in
// fillRecord: a symlink record whose declared link length is near 2^32 used
// to pass a uint32-wrapped bound and then slice far past the record, panicking.
func TestFillRecordLinkLenOverflow(t *testing.T) {
	for _, linkLen := range []uint32{0xFFFFFFFF, 0xFFFFFFE1, 0xFFFFFFF0} {
		rec := make([]byte, 40)
		rec[0] = byte(TypeLink)
		binary.BigEndian.PutUint32(rec[27:31], linkLen)
		b := &BOM{data: rec, blocks: []block{{Address: 0, Length: uint32(len(rec))}}}
		var e Entry
		if err := b.fillRecord(&e, 0); err != nil {
			t.Fatalf("linkLen %#x: unexpected error %v", linkLen, err)
		}
		if e.LinkTarget != "" {
			t.Fatalf("linkLen %#x: out-of-range link target should be ignored, got %q", linkLen, e.LinkTarget)
		}
	}
}

// TestFillRecordLinkLenValid confirms a well-formed link target still decodes.
func TestFillRecordLinkLenValid(t *testing.T) {
	target := "usr/bin/sh"
	rec := make([]byte, pathRecordSize+len(target)+1)
	rec[0] = byte(TypeLink)
	binary.BigEndian.PutUint32(rec[27:31], uint32(len(target)+1))
	copy(rec[pathRecordSize:], target)
	b := &BOM{data: rec, blocks: []block{{Address: 0, Length: uint32(len(rec))}}}
	var e Entry
	if err := b.fillRecord(&e, 0); err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if e.LinkTarget != target {
		t.Fatalf("link target = %q, want %q", e.LinkTarget, target)
	}
}
