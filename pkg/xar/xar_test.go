package xar

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildArchive assembles a raw xar from a TOC document and heap bytes the
// way a writer would, so the reader can be tested against hand-made input
// rather than only against our own writer.
func buildArchive(t *testing.T, tocXML string, alg ChecksumAlg, heap []byte) []byte {
	t.Helper()
	var comp bytes.Buffer
	zw := zlib.NewWriter(&comp)
	zw.Write([]byte(tocXML))
	zw.Close()
	h, err := alg.New()
	if err != nil {
		t.Fatal(err)
	}
	h.Write(comp.Bytes())
	hdr := Header{Size: HeaderSize, Version: 1, TOCCompressed: uint64(comp.Len()), TOCUncompressed: uint64(len(tocXML)), ChecksumAlg: alg}
	hb, _ := hdr.MarshalBinary()
	var out bytes.Buffer
	out.Write(hb)
	out.Write(comp.Bytes())
	out.Write(h.Sum(nil))
	out.Write(heap)
	return out.Bytes()
}

func TestReadHeader(t *testing.T) {
	hdr := Header{Size: 28, Version: 1, TOCCompressed: 10, TOCUncompressed: 20, ChecksumAlg: ChecksumSHA256}
	b, _ := hdr.MarshalBinary()
	got, err := ReadHeader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if *got != hdr {
		t.Errorf("round trip = %+v, want %+v", *got, hdr)
	}
	if got.HeapOffset() != 38 {
		t.Errorf("HeapOffset = %d, want 38", got.HeapOffset())
	}

	// Not xar.
	if _, err := ReadHeader(bytes.NewReader([]byte("not a xar archive at all"))); err != ErrNotXar {
		t.Errorf("garbage: err = %v, want ErrNotXar", err)
	}
	if _, err := ReadHeader(bytes.NewReader([]byte("xar"))); err != ErrNotXar {
		t.Errorf("short: err = %v, want ErrNotXar", err)
	}

	// Extended header from the upstream fork: size 36, alg 3 and a name.
	ext := make([]byte, 36)
	copy(ext, b)
	binary.BigEndian.PutUint16(ext[4:6], 36)
	binary.BigEndian.PutUint32(ext[24:28], 3)
	copy(ext[28:], "sha512\x00")
	got, err = ReadHeader(bytes.NewReader(ext))
	if err != nil {
		t.Fatal(err)
	}
	if got.ChecksumAlg != ChecksumSHA512 || got.HeapOffset() != 46 {
		t.Errorf("extended header: alg %s heap %d", got.ChecksumAlg, got.HeapOffset())
	}

	// Implausible sizes.
	binary.BigEndian.PutUint16(ext[4:6], 200)
	if _, err := ReadHeader(bytes.NewReader(ext)); err == nil {
		t.Error("header size 200 accepted")
	}
}

func TestChecksumAlg(t *testing.T) {
	cases := []struct {
		style string
		want  ChecksumAlg
		ok    bool
	}{
		{"sha1", ChecksumSHA1, true}, {"SHA1", ChecksumSHA1, true}, {"sha256", ChecksumSHA256, true},
		{"sha512", ChecksumSHA512, true}, {"md5", ChecksumMD5, true}, {"none", ChecksumNone, true},
		{"crc32", 0, false},
	}
	for _, tc := range cases {
		got, err := ParseChecksumStyle(tc.style)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("ParseChecksumStyle(%q) = %v, %v; want %v, ok=%v", tc.style, got, err, tc.want, tc.ok)
		}
	}
	if ChecksumSHA256.Size() != sha256.Size || ChecksumSHA1.Size() != sha1.Size {
		t.Error("digest sizes wrong")
	}
	if ChecksumAlg(9).String() != "unknown(9)" {
		t.Errorf("unknown alg String = %q", ChecksumAlg(9).String())
	}
}

const sampleTOC = `<?xml version="1.0" encoding="UTF-8"?>
<xar>
 <toc>
  <checksum style="sha1">
   <size>20</size>
   <offset>0</offset>
  </checksum>
  <creation-time>2026-08-29T14:00:22</creation-time>
  <file id="1">
   <name>dir</name>
   <name>dir</name>
   <type>directory</type>
   <mode>0755</mode>
   <uid>0</uid>
   <gid>80</gid>
   <mtime>2024-01-02T03:04:05Z</mtime>
   <file id="2">
    <name>hello.txt</name>
    <type>file</type>
    <mode>0644</mode>
    <data>
     <archived-checksum style="sha1">%ARCH%</archived-checksum>
     <extracted-checksum style="sha1">%EXTR%</extracted-checksum>
     <encoding style="application/octet-stream"/>
     <size>5</size>
     <offset>20</offset>
     <length>5</length>
    </data>
   </file>
   <file id="3">
    <name>link</name>
    <type>symlink</type>
    <link type="file">hello.txt</link>
   </file>
   <file id="4">
    <name>hard</name>
    <type link="2">hardlink</type>
   </file>
   <unknown-element>ignored</unknown-element>
  </file>
 </toc>
</xar>
`

func TestReaderHandMade(t *testing.T) {
	content := []byte("hello")
	sum := sha1.Sum(content)
	hexsum := strings.ToLower(bytesToHex(sum[:]))
	toc := strings.NewReplacer("%ARCH%", hexsum, "%EXTR%", hexsum).Replace(sampleTOC)
	archive := buildArchive(t, toc, ChecksumSHA1, content)

	x, err := Open(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	if !x.TOCDigestValid() {
		t.Error("TOC digest reported invalid")
	}
	if got := x.TOC().CreationTimeValue(); !got.Equal(time.Date(2026, 8, 29, 14, 0, 22, 0, time.UTC)) {
		t.Errorf("creation time = %v", got)
	}
	var paths []string
	for _, f := range x.Files() {
		paths = append(paths, f.Path())
	}
	if want := "dir dir/hello.txt dir/link dir/hard"; strings.Join(paths, " ") != want {
		t.Errorf("paths = %q, want %q", strings.Join(paths, " "), want)
	}
	dir := x.Lookup("dir")
	if dir == nil || !dir.IsDir() || dir.Name() != "dir" || len(dir.Names) != 2 {
		t.Fatalf("dir entry: %+v", dir)
	}
	if dir.ModeBits() != 0o755 || dir.UIDValue() != 0 || dir.GIDValue() != 80 {
		t.Errorf("dir metadata: mode %o uid %d gid %d", dir.ModeBits(), dir.UIDValue(), dir.GIDValue())
	}
	if !dir.ModTime().Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("dir mtime = %v", dir.ModTime())
	}
	f := x.Lookup("dir/hello.txt")
	rc, err := x.Open(f)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "hello" {
		t.Errorf("content = %q", got)
	}
	if err := x.Verify(f); err != nil {
		t.Errorf("Verify: %v", err)
	}
	if x.Lookup("dir/link").SymlinkTarget() != "hello.txt" {
		t.Error("symlink target lost")
	}
	if x.Lookup("dir/hard").HardlinkTarget() != 2 {
		t.Error("hardlink target lost")
	}
	if x.HeapEnd() != int64(len(archive)) {
		t.Errorf("HeapEnd = %d, want %d", x.HeapEnd(), len(archive))
	}

	// Trailing bytes after the heap (a notarization ticket) must not
	// disturb reading, and HeapEnd must still point at the real end.
	withTrailer := append(append([]byte(nil), archive...), []byte("t8lr trailer bytes")...)
	x2, err := Open(bytes.NewReader(withTrailer), int64(len(withTrailer)))
	if err != nil {
		t.Fatal(err)
	}
	if x2.HeapEnd() != int64(len(archive)) {
		t.Errorf("HeapEnd with trailer = %d, want %d", x2.HeapEnd(), len(archive))
	}

	// Corrupt the checksum: Verify must fail, and TOC digest must fail if
	// the compressed TOC is touched.
	bad := strings.NewReplacer("%ARCH%", hexsum, "%EXTR%", strings.Repeat("0", 40)).Replace(sampleTOC)
	archive = buildArchive(t, bad, ChecksumSHA1, content)
	x, _ = Open(bytes.NewReader(archive), int64(len(archive)))
	if err := x.Verify(x.Lookup("dir/hello.txt")); err == nil {
		t.Error("Verify accepted a wrong extracted checksum")
	}
	archive[len(archive)-6] ^= 0xff // flip a byte in the stored digest
	x, _ = Open(bytes.NewReader(archive), int64(len(archive)))
	if x.TOCDigestValid() {
		t.Error("TOC digest reported valid after corruption")
	}
}

func TestReaderRejectsBadLengths(t *testing.T) {
	archive := buildArchive(t, sampleTOC, ChecksumSHA1, nil)
	binary.BigEndian.PutUint64(archive[16:24], 5) // wrong uncompressed length
	if _, err := Open(bytes.NewReader(archive), int64(len(archive))); err == nil {
		t.Error("accepted TOC with wrong uncompressed length")
	}
	archive = buildArchive(t, sampleTOC, ChecksumSHA1, nil)
	binary.BigEndian.PutUint64(archive[8:16], 1<<40) // TOC past EOF
	if _, err := Open(bytes.NewReader(archive), int64(len(archive))); err == nil {
		t.Error("accepted TOC running past EOF")
	}
}

func TestWriterRoundTrip(t *testing.T) {
	epoch := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	build := func() []byte {
		var out bytes.Buffer
		w, err := NewWriter(&out, WriterOptions{CreationTime: epoch, TempDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		hdr := FileHeader{Mode: 0o644, UID: 0, GID: 0, User: "root", Group: "wheel", ModTime: epoch}
		if err := w.AddFile("PackageInfo", hdr, EncodingGzip, strings.NewReader("<pkg-info/>")); err != nil {
			t.Fatal(err)
		}
		if err := w.AddFile("Payload", hdr, EncodingNone, bytes.NewReader(bytes.Repeat([]byte("p"), 1000))); err != nil {
			t.Fatal(err)
		}
		if err := w.AddFile("sub.pkg/PackageInfo", hdr, EncodingGzip, strings.NewReader("<pkg-info/>")); err != nil {
			t.Fatal(err)
		}
		if err := w.AddSymlink("Resources/link", FileHeader{Mode: 0o755}, "../PackageInfo"); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	a, b := build(), build()
	if !bytes.Equal(a, b) {
		t.Error("two builds with the same inputs differ")
	}

	x, err := Open(bytes.NewReader(a), int64(len(a)))
	if err != nil {
		t.Fatal(err)
	}
	if x.Header().ChecksumAlg != ChecksumSHA256 || !x.TOCDigestValid() {
		t.Errorf("header alg %s, digest valid %v", x.Header().ChecksumAlg, x.TOCDigestValid())
	}
	if got := x.TOC().CreationTime; got != "2024-01-02T03:04:05" {
		t.Errorf("creation-time = %q", got)
	}
	var paths []string
	for _, f := range x.Files() {
		paths = append(paths, f.Path())
		if err := x.Verify(f); err != nil {
			t.Errorf("Verify %s: %v", f.Path(), err)
		}
	}
	if want := "PackageInfo Payload sub.pkg sub.pkg/PackageInfo Resources Resources/link"; strings.Join(paths, " ") != want {
		t.Errorf("paths = %q\nwant  %q", strings.Join(paths, " "), want)
	}
	pi := x.Lookup("PackageInfo")
	if pi.Data.Encoding.Style != EncodingGzip || pi.Data.Size != 11 || pi.MTime != "2024-01-02T03:04:05Z" || pi.User != "root" {
		t.Errorf("PackageInfo entry: %+v data %+v", pi, pi.Data)
	}
	rc, _ := x.Open(pi)
	got, _ := io.ReadAll(rc)
	if string(got) != "<pkg-info/>" {
		t.Errorf("PackageInfo content = %q", got)
	}
	// The "gzip" entry must be zlib-framed, as Apple's tools write it.
	raw, _ := x.OpenRaw(pi)
	head := make([]byte, 2)
	raw.ReadAt(head, 0)
	if head[0] != 0x78 {
		t.Errorf("gzip-style entry is not zlib framed: % x", head)
	}
	payload := x.Lookup("Payload")
	if payload.Data.Length != 1000 || payload.Data.Offset != 32+pi.Data.Length {
		t.Errorf("Payload placement: %+v", payload.Data)
	}
	if x.Lookup("Resources").ModeBits() != 0o755 {
		t.Error("implicit directory mode")
	}
	if x.HeapEnd() != int64(len(a)) {
		t.Errorf("HeapEnd = %d, want %d", x.HeapEnd(), len(a))
	}
	// The TOC must carry the XML declaration and Apple's element order.
	toc := string(x.RawTOC())
	if !strings.HasPrefix(toc, `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Error("TOC lacks XML declaration")
	}
	if strings.Index(toc, "<checksum") > strings.Index(toc, "<creation-time>") {
		t.Error("checksum must precede creation-time")
	}
	if strings.Contains(toc, "xmlns") {
		t.Error("TOC must not carry a namespace")
	}
}

// fakeSigner reserves space and writes recognizable bytes, to prove the
// writer lays signatures out where the TOC says they are.
type fakeSigner struct{ digest []byte }

func (s *fakeSigner) Elements() (*Signature, *Signature) {
	return &Signature{Style: SignatureRSA, Size: 256, KeyInfo: &KeyInfo{X509Data: X509Data{Certificates: []string{"AAAA"}}}},
		&Signature{Style: SignatureCMS, Size: 1024}
}

func (s *fakeSigner) Sign(digest []byte) ([]byte, []byte, error) {
	s.digest = digest
	return bytes.Repeat([]byte{0xAA}, 100), bytes.Repeat([]byte{0xBB}, 300), nil
}

func TestWriterSignerLayout(t *testing.T) {
	var out bytes.Buffer
	signer := &fakeSigner{}
	w, err := NewWriter(&out, WriterOptions{ChecksumAlg: ChecksumSHA1, TempDir: t.TempDir(), Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	w.AddFile("PackageInfo", FileHeader{Mode: 0o644}, EncodingNone, strings.NewReader("x"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := out.Bytes()
	x, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	toc := x.TOC()
	if toc.Signature == nil || toc.Signature.Offset != 20 || toc.Signature.Size != 256 {
		t.Fatalf("RSA element: %+v", toc.Signature)
	}
	if toc.XSignature == nil || toc.XSignature.Offset != 276 || toc.XSignature.Size != 1024 {
		t.Fatalf("CMS element: %+v", toc.XSignature)
	}
	if pi := x.Lookup("PackageInfo"); pi.Data.Offset != 1300 {
		t.Errorf("data offset = %d, want 1300", pi.Data.Offset)
	}
	want, _ := x.ComputeTOCDigest()
	if !bytes.Equal(signer.digest, want) {
		t.Error("signer was given a digest that does not match the written TOC")
	}
	heap := data[x.HeapOffset():]
	if heap[20] != 0xAA || heap[20+99] != 0xAA || heap[20+100] != 0 {
		t.Error("RSA signature not placed/padded at offset 20")
	}
	if heap[276] != 0xBB || heap[276+299] != 0xBB || heap[276+300] != 0 || heap[1299] != 0 {
		t.Error("CMS signature not placed/padded at offset 276")
	}
	if heap[1300] != 'x' {
		t.Error("entry data not after the signatures")
	}
	if !strings.Contains(string(x.RawTOC()), "<X509Certificate>AAAA</X509Certificate>") {
		t.Error("certificate chain not in TOC")
	}
}

// TestReadsAppleFixtures opens every committed fixture pkgbuild produced
// and checks the reader's view of it against xar's own.
func TestReadsAppleFixtures(t *testing.T) {
	fixtures, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "cli", "*.pkg"))
	if len(fixtures) == 0 {
		t.Skip("no fixtures in testdata/cli")
	}
	for _, path := range fixtures {
		t.Run(filepath.Base(path), func(t *testing.T) {
			x, err := OpenFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer x.Close()
			if !x.TOCDigestValid() {
				t.Error("TOC digest invalid")
			}
			for _, f := range x.Files() {
				if err := x.Verify(f); err != nil {
					t.Errorf("%s: %v", f.Path(), err)
				}
				for _, ea := range f.EAs {
					rc, err := x.OpenEA(ea)
					if err != nil {
						t.Errorf("%s ea %s: %v", f.Path(), ea.Name, err)
						continue
					}
					io.Copy(io.Discard, rc)
					rc.Close()
				}
			}
			if x.Lookup("PackageInfo") == nil && x.Lookup("Distribution") == nil {
				t.Error("neither PackageInfo nor Distribution found")
			}
		})
	}
	// The dumped TOC of component-basic must parse to the same entries.
	dump, err := os.ReadFile(filepath.Join("..", "..", "testdata", "xar", "component-basic.toc.xml"))
	if err != nil {
		return
	}
	toc, err := parseTOC(dump)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range toc.Files {
		names[f.Name()] = true
	}
	for _, want := range []string{"Bom", "Payload", "Scripts", "PackageInfo"} {
		if !names[want] {
			t.Errorf("xar --dump-toc output lacks %s after parsing", want)
		}
	}
}

func bytesToHex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = digits[c>>4]
		out[i*2+1] = digits[c&0xf]
	}
	return string(out)
}
