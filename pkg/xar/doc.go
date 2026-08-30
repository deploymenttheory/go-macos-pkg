// Package xar reads and writes the eXtensible ARchive container that a
// macOS flat package is built from.
//
// An archive is a 28-byte header, a zlib-compressed XML table of contents
// describing every entry, and a heap holding the entry data. The table of
// contents carries each entry's offset and length into the heap, its
// checksums before and after encoding, and its metadata; the heap itself
// has no structure of its own. A signed archive reserves space at the
// front of the heap for the digest and the signatures, which is why
// signing can rewrite the table of contents and copy the heap through
// untouched.
//
// Reading takes an io.ReaderAt and pulls entries lazily, so a
// multi-gigabyte package is never held in memory. Input is treated as
// hostile: the table of contents is size-bounded, and every offset and
// length is checked against the file before it is used.
//
//	r, err := xar.Open(f, size)
//	for _, e := range r.Files {
//	    rc, err := r.Open(e)
//	}
//
// The format is Apple's, and where Apple's own tool and the widely used
// forks disagree, this package follows Apple. The differences are noted
// where they arise, and docs/formats/xar.md records the byte layout.
package xar
