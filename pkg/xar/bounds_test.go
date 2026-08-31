package xar

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestHeapSectionBoundsSurviveOverflow covers the range check on entry data.
//
// Both the offset and the length come from the table of contents, so both
// are chosen by whoever made the file. Checking them as
// heapOffset+offset+length overflows int64 for large values and wraps to a
// negative number, which passes any upper-bound test: an entry declaring a
// length of 2^63-1 was accepted and read to the end of the file, well past
// the data it owns.
func TestHeapSectionBoundsSurviveOverflow(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "cli", "component-basic.pkg")
	f, err := os.Open(path)
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	x, err := Open(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a real range still works", func(t *testing.T) {
		sr, err := x.HeapSection(0, 10)
		if err != nil {
			t.Fatalf("a range inside the heap was refused: %v", err)
		}
		if n, err := io.Copy(io.Discard, sr); err != nil || n != 10 {
			t.Errorf("read %d bytes (%v), want 10", n, err)
		}
	})

	const maxInt64 = 1<<63 - 1
	for _, tc := range []struct {
		name           string
		offset, length int64
	}{
		{"length runs to the end of int64", 1, maxInt64},
		{"offset and length each half of it", 1 << 62, 1 << 62},
		{"offset at the top of int64", maxInt64, 1},
		{"negative length", 0, -1},
		{"negative offset", -1, 1},
		{"honest range past the end", st.Size(), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := x.HeapSection(tc.offset, tc.length); err == nil {
				t.Errorf("offset %d length %d was accepted; it is not inside the file",
					tc.offset, tc.length)
			}
		})
	}
}
