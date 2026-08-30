// The xar table of contents: an XML document, zlib-compressed, that names
// every entry and says where its bytes live in the heap.
//
// The model below mirrors what Apple's xar and libarchive write, in the order
// they write it, so that marshalling a TOC we built produces the same shape.
// Reading is more forgiving than writing: Apple's own tools emit duplicate
// <name> elements and unknown children, and both are tolerated.

package xar

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Encoding style strings for heap entries, as written in <encoding style="">.
const (
	// EncodingNone stores bytes verbatim.
	EncodingNone = "application/octet-stream"
	// EncodingGzip is the name Apple and libarchive give to zlib-framed
	// deflate data. Despite the name the bytes are RFC 1950 zlib, not RFC
	// 1952 gzip; every reader in the wild calls inflate on them.
	EncodingGzip = "application/x-gzip"
	// EncodingZlib is the upstream fork's honest name for the same thing.
	EncodingZlib = "application/zlib"
	// EncodingBzip2 is bzip2 data.
	EncodingBzip2 = "application/x-bzip2"
	// EncodingLZMA is raw LZMA data (libarchive).
	EncodingLZMA = "application/x-lzma"
	// EncodingXZ is xz data (libarchive).
	EncodingXZ = "application/x-xz"
)

// Entry types, as written in <type>.
const (
	TypeFile      = "file"
	TypeDirectory = "directory"
	TypeSymlink   = "symlink"
	TypeHardlink  = "hardlink"
	TypeFIFO      = "fifo"
	TypeSocket    = "socket"
	TypeCharDev   = "character special"
	TypeBlockDev  = "block special"
)

// Signature styles, as written in <signature style=""> and <x-signature>.
const (
	SignatureRSA = "RSA"
	SignatureCMS = "CMS"
)

// Timestamp layouts. Apple writes <creation-time> without a zone suffix and
// the per-file times with a trailing Z; both are UTC. libarchive and 7-Zip
// require exactly these shapes, so the writer emits them exactly.
const (
	creationTimeLayout = "2006-01-02T15:04:05"
	fileTimeLayout     = "2006-01-02T15:04:05Z"
)

// document is the XML root: <xar><toc>...</toc></xar>. It has no namespace
// and no attributes; 7-Zip insists <xar> has exactly one child.
type document struct {
	XMLName xml.Name `xml:"xar"`
	TOC     TOC      `xml:"toc"`
}

// TOC is the table of contents. Field order is marshalling order and matches
// Apple's writer: checksum, creation-time, signatures, then files.
type TOC struct {
	Checksum     *Checksum  `xml:"checksum"`
	CreationTime string     `xml:"creation-time,omitempty"`
	Signature    *Signature `xml:"signature"`
	XSignature   *Signature `xml:"x-signature"`
	Files        []*File    `xml:"file"`
}

// Checksum locates the TOC digest in the heap. Offset is always 0 in
// practice and Size is the digest length.
type Checksum struct {
	Style  string `xml:"style,attr"`
	Offset int64  `xml:"offset"`
	Size   int64  `xml:"size"`
}

// Signature locates a signature over the TOC digest in the heap and carries
// the signer's certificate chain. Apple writes an RSA one in <signature> and
// a CMS one in <x-signature>.
type Signature struct {
	Style   string   `xml:"style,attr"`
	Offset  int64    `xml:"offset"`
	Size    int64    `xml:"size"`
	KeyInfo *KeyInfo `xml:"KeyInfo"`
}

// KeyInfo is the XML-DSig KeyInfo element holding the certificate chain.
type KeyInfo struct {
	XMLNS    string   `xml:"xmlns,attr,omitempty"`
	X509Data X509Data `xml:"X509Data"`
}

// X509Data holds base64 DER certificates, leaf first.
type X509Data struct {
	Certificates []string `xml:"X509Certificate"`
}

// XMLDSigNamespace is the namespace Apple's productsign puts on KeyInfo.
const XMLDSigNamespace = "http://www.w3.org/2000/09/xmldsig#"

// File is one archive entry. Directories nest their children as <file>
// elements of their own.
type File struct {
	ID int `xml:"id,attr"`

	// Names holds every <name> element seen. Apple's tools have been observed
	// to write the same name two or three times; Name() resolves it.
	Names []nameElem `xml:"name"`

	Type   typeElem  `xml:"type"`
	Link   *linkElem `xml:"link"`
	Data   *Data     `xml:"data"`
	EAs    []*EA     `xml:"ea"`
	Device *Device   `xml:"device"`

	// Metadata is kept as strings: the formats are Apple's (octal modes,
	// ISO-8601 times) and a package tool has no business re-interpreting a
	// value it merely copies. Typed accessors parse on demand.
	Inode    string `xml:"inode,omitempty"`
	DeviceNo string `xml:"deviceno,omitempty"`
	Mode     string `xml:"mode,omitempty"`
	UID      string `xml:"uid,omitempty"`
	User     string `xml:"user,omitempty"`
	GID      string `xml:"gid,omitempty"`
	Group    string `xml:"group,omitempty"`
	CTime    string `xml:"ctime,omitempty"`
	MTime    string `xml:"mtime,omitempty"`
	ATime    string `xml:"atime,omitempty"`

	Children []*File `xml:"file"`

	// path is the slash-joined path from the archive root, set when the
	// reader flattens the tree.
	path string
}

// nameElem is a <name> element, optionally base64-encoded when the name is
// not representable in the document encoding.
type nameElem struct {
	Value   string `xml:",chardata"`
	EncType string `xml:"enctype,attr,omitempty"`
}

// typeElem is the <type> element; hard links carry the original's id (or
// "original") in a link attribute.
type typeElem struct {
	Value string `xml:",chardata"`
	Link  string `xml:"link,attr,omitempty"`
}

// linkElem is the <link> element of a symlink: the target, with an
// informational type attribute describing the referent.
type linkElem struct {
	Value string `xml:",chardata"`
	Type  string `xml:"type,attr,omitempty"`
}

// Device is the <device> element of a character or block special file.
type Device struct {
	Major int `xml:"major"`
	Minor int `xml:"minor"`
}

// Data locates an entry's bytes in the heap. Length is the stored size,
// Size the extracted size; the checksums are over the stored and extracted
// bytes respectively. Field order is marshalling order.
type Data struct {
	Length            int64    `xml:"length"`
	Offset            int64    `xml:"offset"`
	Size              int64    `xml:"size"`
	Encoding          Encoding `xml:"encoding"`
	ArchivedChecksum  *Digest  `xml:"archived-checksum"`
	ExtractedChecksum *Digest  `xml:"extracted-checksum"`
}

// EA is an extended attribute: a Data block plus the attribute's name.
type EA struct {
	ID                int      `xml:"id,attr"`
	Length            int64    `xml:"length"`
	Offset            int64    `xml:"offset"`
	Size              int64    `xml:"size"`
	Encoding          Encoding `xml:"encoding"`
	ArchivedChecksum  *Digest  `xml:"archived-checksum"`
	ExtractedChecksum *Digest  `xml:"extracted-checksum"`
	Name              string   `xml:"name"`
}

// Encoding is the empty <encoding style="..."/> element.
type Encoding struct {
	Style string `xml:"style,attr"`
}

// Digest is a <archived-checksum style="sha1">hex</archived-checksum> element.
type Digest struct {
	Style string `xml:"style,attr"`
	Value string `xml:",chardata"`
}

// Name returns the entry's name. When several <name> elements are present
// the last one wins, which is what Apple's reader does.
func (f *File) Name() string {
	if len(f.Names) == 0 {
		return ""
	}
	n := f.Names[len(f.Names)-1]
	if strings.EqualFold(n.EncType, "base64") {
		if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(n.Value)); err == nil {
			return string(decoded)
		}
	}
	return n.Value
}

// SetName replaces every <name> element with one.
func (f *File) SetName(name string) {
	f.Names = []nameElem{{Value: name}}
}

// Path returns the slash-separated path from the archive root, or the bare
// name for a File that did not come from a Reader.
func (f *File) Path() string {
	if f.path != "" {
		return f.path
	}
	return f.Name()
}

// IsDir reports whether the entry is a directory.
func (f *File) IsDir() bool { return f.Type.Value == TypeDirectory }

// IsRegular reports whether the entry carries data of its own: a file, or
// the original member of a hard-link set.
func (f *File) IsRegular() bool {
	return f.Type.Value == TypeFile || (f.Type.Value == TypeHardlink && f.Type.Link == "original")
}

// HardlinkTarget returns the id of the entry a hard link refers to, or -1
// when the entry is not a hard link or is the original.
func (f *File) HardlinkTarget() int {
	if f.Type.Value != TypeHardlink || f.Type.Link == "" || f.Type.Link == "original" {
		return -1
	}
	id, err := strconv.Atoi(f.Type.Link)
	if err != nil {
		return -1
	}
	return id
}

// SymlinkTarget returns a symlink's target, or "" for other entries.
func (f *File) SymlinkTarget() string {
	if f.Type.Value != TypeSymlink || f.Link == nil {
		return ""
	}
	return f.Link.Value
}

// ModeBits parses the octal <mode> element (permission bits only, as Apple
// writes them, with setuid/setgid/sticky when present). Zero if absent.
func (f *File) ModeBits() uint32 {
	if f.Mode == "" {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(f.Mode), 8, 32)
	if err != nil {
		return 0
	}
	return uint32(v)
}

// UIDValue and GIDValue parse the numeric owner fields; -1 when absent.
func (f *File) UIDValue() int { return parseIntOr(f.UID, -1) }
func (f *File) GIDValue() int { return parseIntOr(f.GID, -1) }

func parseIntOr(s string, def int) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return v
}

// ModTime parses <mtime>; the zero time when absent or malformed.
func (f *File) ModTime() time.Time { return parseFileTime(f.MTime) }

// parseFileTime accepts the per-file layout and, leniently, the
// creation-time layout without a Z, since both are UTC.
func parseFileTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(fileTimeLayout, s); err == nil {
		return t
	}
	if t, err := time.Parse(creationTimeLayout, s); err == nil {
		return t
	}
	return time.Time{}
}

// FormatFileTime renders a time as the per-file <mtime> layout.
func FormatFileTime(t time.Time) string { return t.UTC().Format(fileTimeLayout) }

// FormatCreationTime renders a time as the <creation-time> layout.
func FormatCreationTime(t time.Time) string { return t.UTC().Format(creationTimeLayout) }

// CreationTimeValue parses the TOC's creation time; zero when absent.
func (t *TOC) CreationTimeValue() time.Time { return parseFileTime(t.CreationTime) }

// parseTOC decodes an uncompressed TOC document.
func parseTOC(data []byte) (*TOC, error) {
	var doc document
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("xar: unable to parse table of contents: %w", err)
	}
	return &doc.TOC, nil
}

// marshalTOC encodes a TOC as the XML document Apple's tools write: an XML
// declaration, four-space indentation, no namespace.
func marshalTOC(toc *TOC) ([]byte, error) {
	body, err := xml.MarshalIndent(&document{TOC: *toc}, "", "    ")
	if err != nil {
		return nil, fmt.Errorf("xar: unable to encode table of contents: %w", err)
	}
	return append([]byte(xml.Header), body...), nil
}
