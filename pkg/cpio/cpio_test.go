package cpio

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestODCRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	mtime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := []struct {
		hdr  Header
		data string
	}{
		{Header{Name: ".", Mode: ModeDir | 0o755, NLink: 1, ModTime: mtime, Inode: 1}, ""},
		{Header{Name: "./a", Mode: ModeDir | 0o755, UID: 0, GID: 80, NLink: 1, ModTime: mtime, Inode: 2}, ""},
		{Header{Name: "./a/hello.txt", Mode: ModeRegular | 0o644, UID: 0, GID: 80, NLink: 1, ModTime: mtime, Inode: 3, Size: 6}, "hello\n"},
		{Header{Name: "./a/link", Mode: ModeSymlink | 0o755, NLink: 1, ModTime: mtime, Inode: 4, Size: 9}, "hello.txt"},
	}
	for _, e := range entries {
		if err := w.WriteHeader(&e.hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("070707")) {
		t.Fatal("no odc magic")
	}
	// A header is 76 bytes of fixed-width octal; check the first one.
	if got := string(buf.Bytes()[:76]); got != "0707070000000000010407550000000000000000010000001454467644500000200000000000" {
		t.Errorf("first header = %q", got)
	}

	r := NewReader(bytes.NewReader(buf.Bytes()))
	for i, e := range entries {
		hdr, err := r.Next()
		if err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		if hdr.Name != e.hdr.Name || hdr.Mode != e.hdr.Mode || hdr.UID != e.hdr.UID || hdr.GID != e.hdr.GID ||
			hdr.Size != e.hdr.Size || !hdr.ModTime.Equal(mtime) || hdr.Inode != e.hdr.Inode || hdr.Format != FormatODC {
			t.Errorf("entry %d = %+v, want %+v", i, *hdr, e.hdr)
		}
		data, _ := io.ReadAll(r)
		if string(data) != e.data {
			t.Errorf("entry %d data = %q, want %q", i, data, e.data)
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("after trailer: %v, want EOF", err)
	}
	if r.Format() != FormatODC {
		t.Error("format not recorded")
	}
}

func TestReaderSkipsUnreadData(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.WriteHeader(&Header{Name: "./big", Mode: ModeRegular | 0o644, Size: 5000})
	w.Write(bytes.Repeat([]byte("x"), 5000))
	w.WriteHeader(&Header{Name: "./small", Mode: ModeRegular | 0o644, Size: 1})
	w.Write([]byte("y"))
	w.Close()
	r := NewReader(&buf)
	if h, _ := r.Next(); h.Name != "./big" {
		t.Fatal("first entry")
	}
	h, err := r.Next()
	if err != nil || h.Name != "./small" {
		t.Fatalf("second entry: %v %v", h, err)
	}
	data, _ := io.ReadAll(r)
	if string(data) != "y" {
		t.Errorf("second data = %q", data)
	}
}

func TestWriterRejectsBadSizes(t *testing.T) {
	w := NewWriter(io.Discard)
	w.WriteHeader(&Header{Name: "./f", Size: 2})
	if _, err := w.Write([]byte("abc")); err == nil {
		t.Error("overlong write accepted")
	}
	w = NewWriter(io.Discard)
	w.WriteHeader(&Header{Name: "./f", Size: 2})
	if err := w.Close(); err == nil {
		t.Error("short entry accepted at close")
	}
}

// newc vector built by hand: one file "a" containing "hi\n".
func TestNewcHandMade(t *testing.T) {
	var buf bytes.Buffer
	hdr := func(name string, size int, mode uint32) {
		fields := []uint64{1, uint64(mode), 0, 0, 1, 1700000000, uint64(size), 0, 0, 0, 0, uint64(len(name) + 1), 0}
		buf.WriteString("070701")
		for _, f := range fields {
			buf.WriteString(strings.ToUpper(padHex(f)))
		}
		buf.WriteString(name)
		buf.WriteByte(0)
		for buf.Len()%4 != 0 {
			buf.WriteByte(0)
		}
	}
	hdr("a", 3, ModeRegular|0o644)
	buf.WriteString("hi\n")
	buf.WriteByte(0) // pad to 4
	hdr(Trailer, 0, 0)

	r := NewReader(&buf)
	h, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if h.Name != "a" || h.Size != 3 || h.Mode != ModeRegular|0o644 || h.Format != FormatNewc || h.ModTime.Unix() != 1700000000 {
		t.Errorf("header = %+v", *h)
	}
	data, _ := io.ReadAll(r)
	if string(data) != "hi\n" {
		t.Errorf("data = %q", data)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("after trailer: %v", err)
	}
}

func padHex(v uint64) string {
	s := strings.ToUpper(strings.TrimLeft(strings.Repeat("0", 8)+hexOf(v), ""))
	return s[len(s)-8:]
}

func hexOf(v uint64) string {
	const d = "0123456789abcdef"
	if v == 0 {
		return "0"
	}
	var out []byte
	for v > 0 {
		out = append([]byte{d[v&0xf]}, out...)
		v >>= 4
	}
	return string(out)
}

// TestReadsAppleCPIO parses archives macOS's cpio wrote, if committed.
func TestReadsAppleCPIO(t *testing.T) {
	for _, name := range []string{"odc.cpio", "newc.cpio"} {
		path := filepath.Join("..", "..", "testdata", "cpio", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Skipf("%s not committed", name)
		}
		r := NewReader(bytes.NewReader(data))
		var names []string
		for {
			h, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			names = append(names, h.Name)
			if h.UID != 0 || h.GID != 80 {
				t.Errorf("%s: %s owner %d:%d, want 0:80", name, h.Name, h.UID, h.GID)
			}
			if h.Name == "./usr/local/fixture/hello.txt" {
				data, _ := io.ReadAll(r)
				if string(data) != "hello, world\n" {
					t.Errorf("%s: hello.txt = %q", name, data)
				}
			}
			if h.Name == "./usr/local/fixture/link" {
				if !h.IsSymlink() {
					t.Errorf("%s: link is not a symlink (mode %o)", name, h.Mode)
				}
				target, _ := io.ReadAll(r)
				if string(target) != "hello.txt" {
					t.Errorf("%s: link target = %q", name, target)
				}
			}
		}
		if len(names) < 8 || names[0] != "." {
			t.Errorf("%s: entries %v", name, names)
		}
	}
}
