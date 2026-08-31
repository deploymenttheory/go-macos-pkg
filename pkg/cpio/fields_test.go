package cpio

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestODCRejectsFieldsItCannotHold covers the 18-bit odc fields. Masking an
// over-large value wrote a different number and said nothing, while the bill
// of materials recorded the real one, so a package could claim one owner in
// its Bom and another in its payload.
//
// uid and gid are the ones real input reaches: a Mac bound to a directory
// service issues uids in the millions, and --ownership preserve passes them
// through untouched.
func TestODCRejectsFieldsItCannotHold(t *testing.T) {
	const max = 0o777777 // 262143

	base := func() *Header {
		return &Header{Name: "./f", Mode: ModeRegular | 0o644, NLink: 1, ModTime: time.Unix(0, 0)}
	}
	cases := []struct {
		field string
		set   func(*Header, uint64)
	}{
		{"uid", func(h *Header, v uint64) { h.UID = uint32(v) }},
		{"gid", func(h *Header, v uint64) { h.GID = uint32(v) }},
		{"inode", func(h *Header, v uint64) { h.Inode = v }},
		{"link count", func(h *Header, v uint64) { h.NLink = uint32(v) }},
		{"device", func(h *Header, v uint64) { h.Dev = v }},
		{"rdev", func(h *Header, v uint64) { h.RDev = v }},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			// The largest value the field holds must still be written, and
			// must survive a round trip unchanged.
			h := base()
			tc.set(h, max)
			var buf bytes.Buffer
			w := NewWriter(&buf)
			if err := w.WriteHeader(h); err != nil {
				t.Fatalf("%s = %d refused, but it fits: %v", tc.field, max, err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			got, err := NewReader(bytes.NewReader(buf.Bytes())).Next()
			if err != nil {
				t.Fatal(err)
			}
			var read uint64
			switch tc.field {
			case "uid":
				read = uint64(got.UID)
			case "gid":
				read = uint64(got.GID)
			case "inode":
				read = got.Inode
			case "link count":
				read = uint64(got.NLink)
			case "device":
				read = got.Dev
			case "rdev":
				read = got.RDev
			}
			if read != max {
				t.Errorf("%s round-tripped %d, want %d", tc.field, read, max)
			}

			// One more must be refused, not quietly turned into something else.
			h = base()
			tc.set(h, max+1)
			err = NewWriter(&bytes.Buffer{}).WriteHeader(h)
			if err == nil {
				t.Fatalf("%s = %d was accepted; an odc header cannot hold it", tc.field, max+1)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error does not name the field: %v", err)
			}
		})
	}
}

// A uid a directory service would issue, as a named case.
func TestODCRejectsDirectoryServiceUID(t *testing.T) {
	h := &Header{Name: "./f", Mode: ModeRegular | 0o644, NLink: 1, ModTime: time.Unix(0, 0), UID: 5000001, GID: 5000001}
	err := NewWriter(&bytes.Buffer{}).WriteHeader(h)
	if err == nil {
		t.Fatal("uid 5000001 was accepted; it masks to 19265 and the Bom would disagree")
	}
}
