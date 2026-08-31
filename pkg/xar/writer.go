// Writing xar archives.
//
// A xar file is header, compressed table of contents, then heap; but the
// table of contents records where each entry's bytes sit in the heap, so
// the entries must be laid out before the TOC can be written. The writer
// therefore streams every entry to a scratch file first, remembering its
// offset, length and checksums, and assembles the file when Close is called.
//
// The first bytes of the heap are the digest of the compressed TOC, and a
// signed archive keeps its signatures immediately after that digest. Both
// are accounted for when the entry offsets are finalized, so a Signer can
// be attached before Close without the layout changing under it.

package xar

import (
	"compress/zlib"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FileHeader carries the metadata written for an entry.
type FileHeader struct {
	// Mode holds the Unix permission bits (and setuid/setgid/sticky).
	// Type bits are not needed: the entry type is given by the Add method.
	Mode uint32
	UID  int
	GID  int
	// User and Group are the symbolic owner names. Apple's pkgbuild writes
	// "root" and "wheel"; leave empty to omit the elements.
	User  string
	Group string
	// Times default to the writer's creation time when zero.
	ModTime time.Time
	CTime   time.Time
	ATime   time.Time
	// Inode is recorded when non-zero.
	Inode uint64
}

// Signer supplies signatures over the table-of-contents digest. The writer
// reserves space in the heap for them, records their locations and
// certificates in the TOC, then asks for the signature bytes once the TOC
// is final. Implemented in pkg/pkgsign.
type Signer interface {
	// Elements returns the <signature> (RSA) and <x-signature> (CMS)
	// elements to place in the TOC, with Style, Size and KeyInfo set. Either
	// may be nil. The writer assigns their offsets.
	Elements() (rsa, cms *Signature)
	// Sign returns the RSA signature and CMS blob over digest. Each must be
	// no longer than the size its element declared; shorter is padded.
	Sign(digest []byte) (rsa, cms []byte, err error)
}

// WriterOptions configures a Writer. The zero value is a sensible archive:
// SHA-256 TOC digest, SHA-256 entry checksums, entries compressed unless
// told otherwise, creation time now.
type WriterOptions struct {
	// ChecksumAlg digests the table of contents. Default SHA-256, which is
	// what Apple's tools write today; SHA-1 was the historical default.
	ChecksumAlg ChecksumAlg
	// FileChecksumAlg digests entries. Default: the same as ChecksumAlg.
	FileChecksumAlg ChecksumAlg
	// CreationTime is written as <creation-time> and used for entry times
	// that are not set. Default now; set it for reproducible output.
	CreationTime time.Time
	// TempDir holds the scratch heap file. Default os.TempDir(); a caller
	// writing a large package should point it at the output's file system.
	TempDir string
	// Signer, if set, signs the archive as it is closed.
	Signer Signer
}

// entry is a queued TOC entry with its heap placement.
type entry struct {
	file     *File
	children map[string]*entry
	names    []string // child names in insertion order
}

// Writer assembles a xar archive.
type Writer struct {
	dst     io.Writer
	opts    WriterOptions
	scratch *os.File
	heapLen int64
	root    *entry
	count   int
	closed  bool
}

// NewWriter returns a Writer that writes the finished archive to dst when
// Close is called.
func NewWriter(dst io.Writer, opts WriterOptions) (*Writer, error) {
	if opts.ChecksumAlg == ChecksumNone {
		opts.ChecksumAlg = ChecksumSHA256
	}
	if _, err := opts.ChecksumAlg.New(); err != nil {
		return nil, err
	}
	if opts.FileChecksumAlg == ChecksumNone {
		opts.FileChecksumAlg = opts.ChecksumAlg
	}
	if opts.CreationTime.IsZero() {
		opts.CreationTime = time.Now()
	}
	opts.CreationTime = opts.CreationTime.UTC().Truncate(time.Second)
	scratch, err := os.CreateTemp(opts.TempDir, "xar-heap-*")
	if err != nil {
		return nil, fmt.Errorf("xar: unable to create scratch file: %w", err)
	}
	return &Writer{
		dst:     dst,
		opts:    opts,
		scratch: scratch,
		root:    &entry{children: map[string]*entry{}},
	}, nil
}

// AddDir adds a directory. Missing parents are created with mode 0755.
func (w *Writer) AddDir(path string, hdr FileHeader) error {
	e, err := w.place(path)
	if err != nil {
		return err
	}
	e.file.Type = typeElem{Value: TypeDirectory}
	w.fill(e.file, hdr)
	return nil
}

// AddFile adds a regular file whose content is read from r and stored with
// the given encoding (EncodingNone or EncodingGzip).
func (w *Writer) AddFile(path string, hdr FileHeader, encoding string, r io.Reader) error {
	e, err := w.place(path)
	if err != nil {
		return err
	}
	e.file.Type = typeElem{Value: TypeFile}
	w.fill(e.file, hdr)
	data, err := w.store(r, encoding)
	if err != nil {
		return fmt.Errorf("xar: %s: %w", path, err)
	}
	e.file.Data = data
	return nil
}

// AddSymlink adds a symbolic link to target.
func (w *Writer) AddSymlink(path string, hdr FileHeader, target string) error {
	e, err := w.place(path)
	if err != nil {
		return err
	}
	e.file.Type = typeElem{Value: TypeSymlink}
	// libarchive writes type="broken" unconditionally because it cannot
	// know what the target is on the reading machine; neither can we.
	e.file.Link = &linkElem{Value: target, Type: "broken"}
	w.fill(e.file, hdr)
	return nil
}

// place creates the entry for path (and any missing parent directories),
// returning it. Re-adding a path replaces its metadata but keeps its
// position and children.
func (w *Writer) place(path string) (*entry, error) {
	if w.closed {
		return nil, fmt.Errorf("xar: write after close")
	}
	clean := strings.Trim(path, "/")
	if clean == "" || clean == "." {
		return nil, fmt.Errorf("xar: empty entry path")
	}
	cur := w.root
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("xar: invalid entry path %q", path)
		}
		child, ok := cur.children[part]
		if !ok {
			w.count++
			child = &entry{
				file:     &File{ID: w.count},
				children: map[string]*entry{},
			}
			child.file.SetName(part)
			if i < len(parts)-1 {
				// An implicit parent: a directory with default metadata.
				child.file.Type = typeElem{Value: TypeDirectory}
				w.fill(child.file, FileHeader{Mode: 0o755})
			}
			cur.children[part] = child
			cur.names = append(cur.names, part)
		}
		cur = child
	}
	return cur, nil
}

// fill writes the metadata elements for hdr into f.
func (w *Writer) fill(f *File, hdr FileHeader) {
	f.Mode = fmt.Sprintf("%04o", hdr.Mode&0o7777)
	f.UID = strconv.Itoa(hdr.UID)
	f.GID = strconv.Itoa(hdr.GID)
	f.User = hdr.User
	f.Group = hdr.Group
	f.Inode = ""
	if hdr.Inode != 0 {
		f.Inode = strconv.FormatUint(hdr.Inode, 10)
	}
	or := func(t time.Time) string {
		if t.IsZero() {
			t = w.opts.CreationTime
		}
		return FormatFileTime(t)
	}
	f.CTime = or(hdr.CTime)
	f.MTime = or(hdr.ModTime)
	f.ATime = or(hdr.ATime)
}

// store streams r to the scratch heap, encoding as asked, and returns the
// Data element with the heap-relative offset (before the digest and
// signature prefix, which Close adds).
func (w *Writer) store(r io.Reader, encoding string) (*Data, error) {
	archived, err := w.opts.FileChecksumAlg.New()
	if err != nil {
		return nil, err
	}
	extracted, err := w.opts.FileChecksumAlg.New()
	if err != nil {
		return nil, err
	}
	counter := &countingWriter{w: io.MultiWriter(w.scratch, archived)}

	var sink io.Writer
	var closer io.Closer
	switch encoding {
	case EncodingNone, "":
		encoding = EncodingNone
		sink = counter
	case EncodingGzip, EncodingZlib:
		// The style says gzip; the bytes are zlib. See EncodingGzip.
		zw, err := zlib.NewWriterLevel(counter, zlib.DefaultCompression)
		if err != nil {
			return nil, err
		}
		sink, closer = zw, zw
		encoding = EncodingGzip
	default:
		return nil, fmt.Errorf("%w for writing: %q", ErrUnsupportedEncoding, encoding)
	}

	size, err := io.Copy(io.MultiWriter(sink, extracted), r)
	if err != nil {
		return nil, err
	}
	if closer != nil {
		if err := closer.Close(); err != nil {
			return nil, err
		}
	}
	d := &Data{
		Length:            counter.n,
		Offset:            w.heapLen,
		Size:              size,
		Encoding:          Encoding{Style: encoding},
		ArchivedChecksum:  &Digest{Style: w.opts.FileChecksumAlg.String(), Value: hex.EncodeToString(archived.Sum(nil))},
		ExtractedChecksum: &Digest{Style: w.opts.FileChecksumAlg.String(), Value: hex.EncodeToString(extracted.Sum(nil))},
	}
	w.heapLen += counter.n
	return d, nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// Close finalizes the table of contents, writes the archive to dst and
// removes the scratch file.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	defer func() {
		_ = w.scratch.Close()
		_ = os.Remove(w.scratch.Name())
	}()

	digestSize := int64(w.opts.ChecksumAlg.Size())
	toc := &TOC{
		Checksum:     &Checksum{Style: w.opts.ChecksumAlg.String(), Offset: 0, Size: digestSize},
		CreationTime: FormatCreationTime(w.opts.CreationTime),
	}

	// Signature space comes right after the digest; entry data after that.
	prefix := digestSize
	var rsaEl, cmsEl *Signature
	if w.opts.Signer != nil {
		rsaEl, cmsEl = w.opts.Signer.Elements()
		if rsaEl != nil {
			rsaEl.Offset = prefix
			prefix += rsaEl.Size
			toc.Signature = rsaEl
		}
		if cmsEl != nil {
			cmsEl.Offset = prefix
			prefix += cmsEl.Size
			toc.XSignature = cmsEl
		}
	}

	toc.Files = w.finalize(w.root, prefix)

	raw, err := marshalTOC(toc)
	if err != nil {
		return err
	}
	compressed, err := compressTOC(raw)
	if err != nil {
		return err
	}
	h, _ := w.opts.ChecksumAlg.New()
	h.Write(compressed)
	digest := h.Sum(nil)

	hdr := Header{
		Size:            HeaderSize,
		Version:         Version,
		TOCCompressed:   uint64(len(compressed)),
		TOCUncompressed: uint64(len(raw)),
		ChecksumAlg:     w.opts.ChecksumAlg,
	}
	hdrBytes, _ := hdr.MarshalBinary()

	out := &errWriter{w: w.dst}
	out.Write(hdrBytes)
	out.Write(compressed)
	out.Write(digest)
	if w.opts.Signer != nil {
		rsaSig, cmsSig, err := w.opts.Signer.Sign(digest)
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
	if _, err := w.scratch.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("xar: unable to rewind scratch file: %w", err)
	}
	if _, err := io.Copy(w.dst, w.scratch); err != nil {
		return fmt.Errorf("xar: unable to write heap: %w", err)
	}
	return nil
}

// finalize converts the entry tree to TOC files, shifting every data offset
// by prefix and ordering children by insertion, which is the order the
// caller walked its source tree.
func (w *Writer) finalize(e *entry, prefix int64) []*File {
	sort.SliceStable(e.names, func(i, j int) bool { return e.children[e.names[i]].file.ID < e.children[e.names[j]].file.ID })
	files := make([]*File, 0, len(e.names))
	for _, name := range e.names {
		child := e.children[name]
		if child.file.Data != nil {
			child.file.Data.Offset += prefix
		}
		child.file.Children = w.finalize(child, prefix)
		files = append(files, child.file)
	}
	return files
}

// compressTOC zlib-compresses the TOC XML at level 6, as libarchive does.
func compressTOC(raw []byte) ([]byte, error) {
	var buf strings.Builder
	zw, err := zlib.NewWriterLevel(&buf, 6)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

func padTo(b []byte, size int64) []byte {
	if int64(len(b)) >= size {
		return b
	}
	out := make([]byte, size)
	copy(out, b)
	return out
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) {
	if e.err != nil {
		return
	}
	_, e.err = e.w.Write(p)
}
