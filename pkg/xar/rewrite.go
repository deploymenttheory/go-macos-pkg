// Rewriting an existing archive with a new table of contents: what
// signing an already-built package needs.
//
// The entries' bytes stay exactly where they are relative to each other;
// only the prefix of the heap (the digest and the signatures) changes
// size, so every entry offset shifts by the same amount and the heap can
// be copied through untouched. That is what lets a multi-gigabyte package
// be signed without re-encoding anything.
package xar

import (
	"fmt"
	"io"
	"time"
)

// RewriteOptions configures Rewrite.
type RewriteOptions struct {
	// ChecksumAlg for the new table of contents; zero keeps the archive's.
	ChecksumAlg ChecksumAlg
	// Signer signs the new archive; nil strips any existing signature.
	Signer Signer
	// CreationTime replaces the TOC creation time when set.
	CreationTime time.Time
}

// Rewrite copies x to dst with a fresh table of contents: the archive's
// existing signatures are dropped, the Signer's (if any) are added, and
// the heap is copied through.
func Rewrite(x *Reader, dst io.Writer, o RewriteOptions) error {
	alg := o.ChecksumAlg
	if alg == ChecksumNone {
		alg = x.Header().ChecksumAlg
	}
	if _, err := alg.New(); err != nil {
		return err
	}

	old := x.TOC()
	// The old heap prefix: digest plus signatures, in whatever order and
	// with whatever gaps the writer left. The entry data begins after the
	// largest of them; everything from there to the heap's end is copied.
	oldPrefix := int64(0)
	if old.Checksum != nil {
		oldPrefix = max(oldPrefix, old.Checksum.Offset+old.Checksum.Size)
	}
	for _, s := range []*Signature{old.Signature, old.XSignature} {
		if s != nil {
			oldPrefix = max(oldPrefix, s.Offset+s.Size)
		}
	}
	// Sanity: no entry may begin inside the prefix.
	for _, f := range x.Files() {
		if f.Data != nil && f.Data.Offset < oldPrefix {
			return fmt.Errorf("xar: entry %s begins inside the signature area; cannot rewrite", f.Path())
		}
	}
	// The heap range to copy, measured before any offset is shifted.
	heapStart := x.HeapOffset() + oldPrefix
	heapEnd := x.HeapEnd()
	if heapEnd < heapStart {
		return fmt.Errorf("xar: heap end %d precedes its data start %d", heapEnd, heapStart)
	}

	toc := &TOC{
		Checksum:     &Checksum{Style: alg.String(), Offset: 0, Size: int64(alg.Size())},
		CreationTime: old.CreationTime,
		Files:        old.Files,
	}
	if !o.CreationTime.IsZero() {
		toc.CreationTime = FormatCreationTime(o.CreationTime)
	}
	if toc.CreationTime == "" {
		toc.CreationTime = FormatCreationTime(time.Now())
	}
	newPrefix := int64(alg.Size())
	var rsaEl, cmsEl *Signature
	if o.Signer != nil {
		rsaEl, cmsEl = o.Signer.Elements()
		if rsaEl != nil {
			rsaEl.Offset = newPrefix
			newPrefix += rsaEl.Size
			toc.Signature = rsaEl
		}
		if cmsEl != nil {
			cmsEl.Offset = newPrefix
			newPrefix += cmsEl.Size
			toc.XSignature = cmsEl
		}
	}
	delta := newPrefix - oldPrefix
	shift(toc.Files, delta)
	// The File structs are shared with the reader; undo the shift on the
	// way out so x stays usable.
	defer shift(toc.Files, -delta)

	raw, err := marshalTOC(toc)
	if err != nil {
		return err
	}
	compressed, err := compressTOC(raw)
	if err != nil {
		return err
	}
	h, _ := alg.New()
	h.Write(compressed)
	digest := h.Sum(nil)

	hdr := Header{Size: HeaderSize, Version: Version, TOCCompressed: uint64(len(compressed)), TOCUncompressed: uint64(len(raw)), ChecksumAlg: alg}
	hdrBytes, _ := hdr.MarshalBinary()
	out := &errWriter{w: dst}
	out.Write(hdrBytes)
	out.Write(compressed)
	out.Write(digest)
	if o.Signer != nil {
		rsaSig, cmsSig, err := o.Signer.Sign(digest)
		if err != nil {
			return fmt.Errorf("xar: signing failed: %w", err)
		}
		if rsaEl != nil {
			if int64(len(rsaSig)) > rsaEl.Size {
				return fmt.Errorf("xar: RSA signature is %d bytes, %d reserved", len(rsaSig), rsaEl.Size)
			}
			out.Write(padTo(rsaSig, rsaEl.Size))
		}
		if cmsEl != nil {
			if int64(len(cmsSig)) > cmsEl.Size {
				return fmt.Errorf("xar: CMS signature is %d bytes, %d reserved", len(cmsSig), cmsEl.Size)
			}
			out.Write(padTo(cmsSig, cmsEl.Size))
		}
	}
	if out.err != nil {
		return fmt.Errorf("xar: unable to write archive: %w", out.err)
	}
	if _, err := io.Copy(dst, io.NewSectionReader(x.r, heapStart, heapEnd-heapStart)); err != nil {
		return fmt.Errorf("xar: unable to copy heap: %w", err)
	}
	return nil
}

// shift moves every data and extended-attribute offset by delta.
func shift(files []*File, delta int64) {
	for _, f := range files {
		if f.Data != nil {
			f.Data.Offset += delta
		}
		for _, ea := range f.EAs {
			ea.Offset += delta
		}
		shift(f.Children, delta)
	}
}
