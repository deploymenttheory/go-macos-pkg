package cpio

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// segmented writes name as n consecutive entries of seg bytes each, which
// is the shape pkgbuild gives a file too large for an odc header.
func segmented(t *testing.T, name string, parts []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf)
	for i, p := range parts {
		hdr := &Header{
			Name:    name,
			Inode:   uint64(10 + i),
			Mode:    ModeRegular | 0o644,
			NLink:   1,
			ModTime: time.Unix(0, 0).UTC(),
			Size:    int64(len(p)),
		}
		if err := w.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestJoinSegmentsConcatenates is the regression test for reading a
// --large-payload package: pkgbuild splits a file over 8 GiB into
// consecutive entries under one name, and only the first was ever read.
func TestJoinSegmentsConcatenates(t *testing.T) {
	parts := []string{"alpha", "bravo", "charlie"}
	data := segmented(t, "./big", parts)

	cr := NewReader(bytes.NewReader(data))
	cr.JoinSegments(true)
	h, err := cr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if h.Name != "./big" {
		t.Fatalf("name = %q", h.Name)
	}
	got, err := io.ReadAll(cr)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(parts, ""); string(got) != want {
		t.Errorf("joined = %q, want %q", got, want)
	}
	if _, err := cr.Next(); err != io.EOF {
		t.Errorf("after the segments: %v, want EOF", err)
	}
}

// Without joining the segments stay separate, which is what a plain cpio
// stream means and what every other reader here relies on.
func TestWithoutJoiningSegmentsStaySeparate(t *testing.T) {
	parts := []string{"alpha", "bravo", "charlie"}
	cr := NewReader(bytes.NewReader(segmented(t, "./big", parts)))
	for i, want := range parts {
		if _, err := cr.Next(); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		got, err := io.ReadAll(cr)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("entry %d = %q, want %q", i, got, want)
		}
	}
	if _, err := cr.Next(); err != io.EOF {
		t.Errorf("after the entries: %v, want EOF", err)
	}
}

// Joining must not merge entries that merely follow one another, only
// those that share a name.
func TestJoinSegmentsLeavesDistinctNamesAlone(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	for _, e := range []struct{ name, data string }{{"./a", "one"}, {"./b", "two"}} {
		hdr := &Header{Name: e.name, Mode: ModeRegular | 0o644, NLink: 1, Size: int64(len(e.data)), ModTime: time.Unix(0, 0).UTC()}
		if err := w.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	cr := NewReader(bytes.NewReader(buf.Bytes()))
	cr.JoinSegments(true)
	for _, want := range []struct{ name, data string }{{"./a", "one"}, {"./b", "two"}} {
		h, err := cr.Next()
		if err != nil {
			t.Fatal(err)
		}
		if h.Name != want.name {
			t.Fatalf("name = %q, want %q", h.Name, want.name)
		}
		got, err := io.ReadAll(cr)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want.data {
			t.Errorf("%s = %q, want %q", want.name, got, want.data)
		}
	}
}
