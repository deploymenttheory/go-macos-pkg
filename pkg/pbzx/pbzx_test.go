package pbzx

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ulikunitz/xz"
)

// buildPBZX writes a pbzx stream by hand; compressed chunks are xz'd,
// others stored raw.
func buildPBZX(t *testing.T, chunks [][]byte, compress []bool) []byte {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("pbzx")
	binary.Write(&out, binary.BigEndian, uint64(1<<24))
	for i, c := range chunks {
		stored := c
		if compress[i] {
			var z bytes.Buffer
			zw, err := xz.NewWriter(&z)
			if err != nil {
				t.Fatal(err)
			}
			zw.Write(c)
			zw.Close()
			stored = z.Bytes()
		}
		binary.Write(&out, binary.BigEndian, uint64(len(c)))
		binary.Write(&out, binary.BigEndian, uint64(len(stored)))
		out.Write(stored)
	}
	return out.Bytes()
}

func TestReaderHandMade(t *testing.T) {
	a := bytes.Repeat([]byte("abcdefgh"), 4096)
	b := []byte("raw chunk that is stored")
	c := bytes.Repeat([]byte("z"), 100000)
	data := buildPBZX(t, [][]byte{a, b, c}, []bool{true, false, true})
	if !IsPBZX(data) {
		t.Fatal("magic not recognized")
	}
	r, err := NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if r.BlockSize() != 1<<24 || r.Algorithm() != XZ {
		t.Errorf("block %d algo %s", r.BlockSize(), r.Algorithm())
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(append([]byte{}, a...), b...), c...)
	if !bytes.Equal(got, want) {
		t.Errorf("decoded %d bytes, want %d", len(got), len(want))
	}
	if _, err := NewReader(bytes.NewReader([]byte("nope"))); err != ErrNotPBZ {
		t.Errorf("bad magic: %v", err)
	}
	// pbzb is read like any other container now; an unknown letter is not.
	if r, err := NewReader(bytes.NewReader(append([]byte("pbzb"), make([]byte, 8)...))); err != nil || r.Algorithm() != LZBitmap {
		t.Errorf("pbzb: %v", err)
	}
	if _, err := NewReader(bytes.NewReader(append([]byte("pbzq"), make([]byte, 8)...))); err == nil {
		t.Error("unknown algorithm accepted")
	}
	bad := buildPBZX(t, [][]byte{a}, []bool{true})
	binary.BigEndian.PutUint64(bad[12:20], uint64(len(a)+1))
	r, _ = NewReader(bytes.NewReader(bad))
	if _, err := io.ReadAll(r); err == nil {
		t.Error("short chunk accepted")
	}
}

func TestWriterRoundTrips(t *testing.T) {
	pattern := bytes.Repeat([]byte("0123456789abcdef"), 400) // 6400 bytes, compressible
	random := make([]byte, 5000)
	x := uint32(2463534242) // xorshift32: incompressible, deterministic
	for i := range random {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		random[i] = byte(x)
	}
	inputs := map[string][]byte{
		"empty":          {},
		"compressible":   pattern,
		"incompressible": random,
		"exact block":    bytes.Repeat([]byte("x"), 1024),
		"three chunks":   bytes.Repeat([]byte("abc"), 900), // 2700 bytes over 1024-byte blocks
		"mixed":          append(append([]byte{}, pattern...), random...),
	}
	for _, algo := range []Algorithm{XZ, LZFSE, LZ4, Zlib, LZBitmap} {
		for name, in := range inputs {
			var out bytes.Buffer
			w, err := NewWriter(&out, algo, 1024)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(in); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			data := out.Bytes()
			if !bytes.Equal(data[:4], algo.Magic()) || binary.BigEndian.Uint64(data[4:12]) != 1024 {
				t.Errorf("%s/%s: header %x", algo, name, data[:12])
			}
			r, err := NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("%s/%s: %v", algo, name, err)
			}
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("%s/%s: %v", algo, name, err)
			}
			if !bytes.Equal(got, in) {
				t.Errorf("%s/%s: round trip differs (%d vs %d bytes)", algo, name, len(got), len(in))
			}
			if name == "three chunks" {
				if n := countChunks(t, data); n != 3 {
					t.Errorf("%s: %d chunks, want 3", algo, n)
				}
			}
			if name == "incompressible" {
				// Stored: the chunk sizes agree.
				inflated := binary.BigEndian.Uint64(data[12:20])
				deflated := binary.BigEndian.Uint64(data[20:28])
				if inflated != deflated {
					t.Errorf("%s: incompressible chunk was not stored (%d/%d)", algo, inflated, deflated)
				}
			}
		}
	}
	if _, err := NewWriter(io.Discard, Algorithm('q'), 0); err == nil {
		t.Error("writer created for an unknown algorithm")
	}
}

func countChunks(t *testing.T, data []byte) int {
	t.Helper()
	pos, n := 12, 0
	for pos+16 <= len(data) {
		deflated := binary.BigEndian.Uint64(data[pos+8 : pos+16])
		pos += 16 + int(deflated)
		n++
	}
	return n
}

// TestXZParity writes the fixture's 20 MiB pattern the way pkgbuild did
// and checks the shape pkgbuild's own output has: two chunks of the
// same sizes, one xz stream each, no integrity check, LZMA2 with an
// 8 MiB dictionary.
func TestXZParity(t *testing.T) {
	block := make([]byte, 4096)
	for i := range block {
		block[i] = byte(i)
	}
	var out bytes.Buffer
	w, _ := NewWriter(&out, XZ, DefaultBlockSize)
	// 20 MiB of the 4 KiB pattern plus the fixture's other 307 KB or so
	// is what made two chunks; the pattern alone gives the same split.
	for i := 0; i < 5120; i++ {
		w.Write(block)
	}
	w.Write(bytes.Repeat([]byte{1, 2, 3}, 100000))
	w.Close()
	data := out.Bytes()
	if n := countChunks(t, data); n != 2 {
		t.Fatalf("%d chunks, want 2", n)
	}
	if got := binary.BigEndian.Uint64(data[12:20]); got != 16777216 {
		t.Errorf("first chunk inflated %d, want 16777216", got)
	}
	stream := data[28:]
	if !bytes.Equal(stream[:6], xzMagic) {
		t.Fatalf("chunk is not xz: %x", stream[:6])
	}
	if stream[6] != 0 || stream[7] != 0 {
		t.Errorf("stream flags %x, want 0000 (no check)", stream[6:8])
	}
	// Block header: size byte, flags, then filter records; find LZMA2 (0x21)
	// with a one-byte property that must be 0x16 (8 MiB dictionary).
	hdrLen := (int(stream[12]) + 1) * 4
	blockHdr := stream[12 : 12+hdrLen]
	if !bytes.Contains(blockHdr, []byte{0x21, 0x01, 0x16}) {
		t.Errorf("block header lacks LZMA2 with an 8 MiB dictionary: %x", blockHdr)
	}
}

// TestDecodesAppleArchiveSamples uses aa's own output as the reference: every
// compressed variant must decode to exactly the bytes of the raw archive.
func TestDecodesAppleArchiveSamples(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "aa", "aa-raw.aar"))
	if err != nil {
		t.Skip("aa samples not committed")
	}
	for name, algo := range map[string]Algorithm{"lzma": XZ, "lzfse": LZFSE, "lz4": LZ4, "zlib": Zlib} {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "aa", "aa-"+name+".aar"))
		if err != nil {
			t.Fatal(err)
		}
		r, err := NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.Algorithm() != algo || r.BlockSize() != 4<<20 {
			t.Errorf("%s: algo %s block %d", name, r.Algorithm(), r.BlockSize())
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(got, raw) {
			t.Errorf("%s: decoded %d bytes differ from the raw archive (%d bytes)", name, len(got), len(raw))
		}
	}
}
