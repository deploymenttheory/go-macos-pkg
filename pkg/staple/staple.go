// Attaching and removing tickets on package files.
package staple

import (
	"fmt"
	"io"
	"os"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// Has reports whether path carries a stapled ticket, returning it.
func Has(path string) (*Ticket, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return Read(f, st.Size())
}

// Staple writes src to dst with ticket attached, replacing any ticket
// already there. src and dst may be the same path.
func Staple(src, dst string, ticket []byte) error {
	return rewrite(src, dst, func(archiveEnd int64, in *os.File, out io.Writer) error {
		if _, err := io.Copy(out, io.NewSectionReader(in, 0, archiveEnd)); err != nil {
			return err
		}
		_, err := out.Write(Encode(ticket))
		return err
	})
}

// Unstaple writes src to dst without its ticket. It is not an error for
// src to be unstapled.
func Unstaple(src, dst string) error {
	return rewrite(src, dst, func(archiveEnd int64, in *os.File, out io.Writer) error {
		_, err := io.Copy(out, io.NewSectionReader(in, 0, archiveEnd))
		return err
	})
}

// rewrite finds where the archive proper ends, then lets fn write the
// output through a temporary file beside dst.
func rewrite(src, dst string, fn func(archiveEnd int64, in *os.File, out io.Writer) error) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	x, err := xar.Open(in, st.Size())
	if err != nil {
		return err
	}
	// The archive ends where its heap does; anything after is a staple
	// (or junk) and is not carried over.
	archiveEnd := x.HeapEnd()
	if t, err := Read(in, st.Size()); err == nil && t.Offset < archiveEnd {
		archiveEnd = t.Offset
	}
	tmp, err := os.CreateTemp(dirOf(dst), ".staple-*")
	if err != nil {
		return err
	}
	if err := fn(archiveEnd, in, tmp); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("staple: unable to write %s: %w", dst, err)
	}
	return nil
}

func dirOf(path string) string {
	if i := lastSlash(path); i >= 0 {
		return path[:i+1]
	}
	return "."
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' || s[i] == os.PathSeparator {
			return i
		}
	}
	return -1
}
