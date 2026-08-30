package flatpkg

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/cpio"
)

// TestExtractionDoesNotFollowSymlinks is the traversal case SafeRelPath
// cannot see. Every name in this payload is lexically innocent: none is
// absolute and none climbs with "..". The escape is made of two entries,
// a symlink pointing out of the destination and a file whose path goes
// through it, and only the second one is dangerous, and only because the
// first already landed.
func TestExtractionDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	witness := filepath.Join(outside, "witness.txt")
	if err := os.WriteFile(witness, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		link string
	}{
		{"absolute link", outside},
		{"climbing link", "../" + filepath.Base(outside)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			cw := cpio.NewWriter(&buf)
			write := func(h *cpio.Header, data []byte) {
				h.NLink, h.Size = 1, int64(len(data))
				if err := cw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
				cw.Write(data)
			}
			write(&cpio.Header{Name: ".", Inode: 1, Mode: cpio.ModeDir | 0o755}, nil)
			write(&cpio.Header{Name: "./escape", Inode: 2, Mode: cpio.ModeSymlink | 0o777}, []byte(tc.link))
			write(&cpio.Header{Name: "./escape/witness.txt", Inode: 3, Mode: cpio.ModeRegular | 0o644}, []byte("overwritten\n"))
			if err := cw.Close(); err != nil {
				t.Fatal(err)
			}

			dir := t.TempDir()
			// An error is acceptable; writing through the link is not.
			_, _ = ExtractCPIO(cpio.NewReader(bytes.NewReader(buf.Bytes())), dir, ExtractOptions{Symlinks: SymlinkReal})

			got, err := os.ReadFile(witness)
			if err != nil {
				t.Fatalf("witness disappeared: %v", err)
			}
			if string(got) != "original\n" {
				t.Errorf("extraction wrote through the symlink: witness now %q", got)
			}
		})
	}
}

// TestExtractionDoesNotFollowAnExistingSymlink covers a link already in
// the destination rather than one the payload creates.
func TestExtractionDoesNotFollowAnExistingSymlink(t *testing.T) {
	outside := t.TempDir()
	witness := filepath.Join(outside, "witness.txt")
	if err := os.WriteFile(witness, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(witness, filepath.Join(dir, "witness.txt")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}

	var buf bytes.Buffer
	cw := cpio.NewWriter(&buf)
	data := []byte("overwritten\n")
	h := &cpio.Header{Name: "./witness.txt", Inode: 1, Mode: cpio.ModeRegular | 0o644, NLink: 1, Size: int64(len(data))}
	if err := cw.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	cw.Write(data)
	cw.Close()

	_, _ = ExtractCPIO(cpio.NewReader(bytes.NewReader(buf.Bytes())), dir, ExtractOptions{})
	got, err := os.ReadFile(witness)
	if err != nil {
		t.Fatalf("witness disappeared: %v", err)
	}
	if string(got) != "original\n" {
		t.Errorf("extraction wrote through a pre-existing symlink: witness now %q", got)
	}
}
