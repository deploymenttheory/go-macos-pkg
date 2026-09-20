package xar

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestVerifiedOpenFailures(t *testing.T) {
	x, _ := verifiedFixture(t, EncodingNone, false)
	if _, err := x.OpenVerified(&File{}); err == nil {
		t.Fatal("opened entry without data")
	}
	for _, test := range []struct {
		name string
		edit func(*Data)
	}{
		{"malformed archived digest", func(d *Data) { d.ArchivedChecksum.Value = "!" }},
		{"wrong digest size", func(d *Data) { d.ArchivedChecksum.Value = "00" }},
		{"none digest with value", func(d *Data) { d.ArchivedChecksum.Style = "none" }},
		{"whitespace digest", func(d *Data) { d.ArchivedChecksum.Value = " " }},
		{"bad zlib header", func(d *Data) { d.Encoding.Style = EncodingZlib }},
		{"bad xz header", func(d *Data) { d.Encoding.Style = EncodingXZ }},
		{"bad lzma header", func(d *Data) { d.Encoding.Style = EncodingLZMA }},
	} {
		t.Run(test.name, func(t *testing.T) {
			x, _ := verifiedFixture(t, EncodingNone, false)
			f := x.Lookup("Payload")
			test.edit(f.Data)
			if r, err := x.OpenVerified(f); err == nil {
				r.Close()
				t.Fatal("opened invalid entry")
			}
		})
	}
}

func TestVerifiedEmptyAndRepeatedReads(t *testing.T) {
	for _, empty := range []bool{false, true} {
		x, want := verifiedFixture(t, EncodingNone, false)
		f := x.Lookup("Payload")
		if empty {
			f.Data.Size, f.Data.Length = 0, 0
			f.Data.ArchivedChecksum, f.Data.ExtractedChecksum = nil, nil
			want = nil
		}
		r, err := x.OpenVerified(f)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if n, err := r.Read(nil); n != 0 || err != nil {
			t.Fatalf("empty read: %d, %v", n, err)
		}
		got, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("content: %d, %v", len(got), err)
		}
		for range 2 {
			if n, err := r.Read(make([]byte, 1)); n != 0 || err != io.EOF {
				t.Fatalf("repeated EOF: %d, %v", n, err)
			}
		}
	}
}

type failingReaderAt struct {
	io.ReaderAt
	limit int64
	err   error
}

func (r failingReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if offset >= r.limit {
		return 0, r.err
	}
	if int64(len(p)) > r.limit-offset {
		p = p[:r.limit-offset]
	}
	return r.ReaderAt.ReadAt(p, offset)
}

func TestVerifiedStoredReadErrors(t *testing.T) {
	want := errors.New("source failed")
	x, _ := verifiedFixture(t, EncodingNone, false)
	x.r = failingReaderAt{ReaderAt: x.r, limit: x.heapOffset + x.Lookup("Payload").Data.Offset + 3, err: want}
	r, err := x.OpenVerified(x.Lookup("Payload"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got, err := io.ReadAll(r); !errors.Is(err, want) || len(got) != 3 {
		t.Fatalf("stored read: %d, %v", len(got), err)
	}
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, want) {
		t.Fatalf("error was not sticky: %v", err)
	}
}

func TestVerifiedFinishErrors(t *testing.T) {
	plain := []byte("finished")
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		data []byte
		size int64
	}{
		{"truncated trailer", compressed.Bytes()[:compressed.Len()-1], int64(len(plain))},
		{"oversized output", compressed.Bytes(), int64(len(plain) - 1)},
		{"truncated body", compressed.Bytes()[:compressed.Len()/2], int64(len(plain))},
	} {
		t.Run(test.name, func(t *testing.T) {
			x := &Reader{r: bytes.NewReader(test.data), size: int64(len(test.data))}
			r, err := x.OpenVerified(&File{Data: &Data{Length: x.size, Size: test.size, Encoding: Encoding{Style: EncodingZlib}}})
			if err == nil {
				_, err = io.Copy(io.Discard, r)
				r.Close()
			}
			if err == nil {
				t.Fatal("accepted damaged stream")
			}
		})
	}
	// A decoder can finish before stored padding; failure while draining that
	// padding must still fail verification even with no archived checksum.
	stored := append(bytes.Clone(compressed.Bytes()), make([]byte, 8192)...)
	want := errors.New("padding failed")
	x := &Reader{r: failingReaderAt{ReaderAt: bytes.NewReader(stored), limit: 4096, err: want}, size: int64(len(stored))}
	r, err := x.OpenVerified(&File{Data: &Data{Length: x.size, Size: int64(len(plain)), Encoding: Encoding{Style: EncodingZlib}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := io.Copy(io.Discard, r); !errors.Is(err, want) {
		t.Fatalf("padding error lost: %v", err)
	}
	// The supplied archive length can overstate the actual backing storage.
	x = &Reader{r: bytes.NewReader(nil), size: 4}
	r, err = x.OpenVerified(&File{Data: &Data{Length: 4}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := io.ReadAll(r); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("missing stored bytes: %v", err)
	}
}

func TestVerifiedEmptyDigestValueIsAbsent(t *testing.T) {
	x, want := verifiedFixture(t, EncodingNone, false)
	f := x.Lookup("Payload")
	f.Data.ArchivedChecksum.Value = ""
	f.Data.ExtractedChecksum.Value = ""
	f.Data.ArchivedChecksum.Style = "unknown"
	r, err := x.OpenVerified(f)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("absent digests: %v", err)
	}
	if _, err := newDigestCheck(&Digest{Style: "sha256", Value: strings.Repeat("00", 32)}); err != nil {
		t.Fatal(err)
	}
}
