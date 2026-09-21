package pbzx

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestBufferedFrameValidation(t *testing.T) {
	v2 := make([]byte, 32)
	copy(v2, "bvx2")
	for _, tc := range []struct {
		name string
		algo Algorithm
		src  []byte
	}{
		{"bitmap magic", LZBitmap, []byte("bad")},
		{"bitmap header", LZBitmap, []byte("ZBM\x09\x06")},
		{"bitmap short size", LZBitmap, []byte("ZBM\x09\x05\x00\x00\x01\x00\x00")},
		{"bitmap raw limit", LZBitmap, []byte("ZBM\x09\x06\x00\x00\x01\x80\x00")},
		{"lzfse header", LZFSE, []byte("bvx")},
		{"lzvn header", LZFSE, []byte("bvxn\x01\x00\x00\x00")},
		{"v1 header", LZFSE, []byte("bvx1\x01\x00\x00\x00")},
		{"v2 header", LZFSE, []byte("bvx2\x01\x00\x00\x00")},
		{"v2 header size", LZFSE, v2},
		{"unknown magic", LZFSE, []byte("bvx?\x01\x00\x00\x00")},
		{"truncated body", LZFSE, []byte("bvx-\x01\x00\x00\x00")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkBufferedSize(tc.algo, tc.src, 1<<20); err == nil {
				t.Fatal("accepted invalid frame")
			}
		})
	}
	if err := checkBufferedSize(LZFSE, []byte("bvx-\x01\x00\x00\x00x"), 1); err != nil {
		t.Fatalf("valid raw frame: %v", err)
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestReadersRejectBrokenChunks(t *testing.T) {
	truncated := pbzChunk(LZFSE, 1024, 1024, []byte("bvx$"))
	binary.BigEndian.PutUint64(truncated[20:], 20)
	for name, input := range map[string][]byte{
		"xz magic":        pbzChunk(XZ, 1024, 1024, []byte("not xz")),
		"zlib header":     pbzChunk(Zlib, 1024, 1024, []byte("not zlib")),
		"buffered short":  truncated,
		"buffered size":   pbzChunk(LZFSE, 1024, 1024, []byte("bvx$")),
		"lz4 missing end": pbzChunk(LZ4, 1024, 1024, []byte("oops")),
	} {
		for mode, open := range readers() {
			t.Run(name+"/"+mode, func(t *testing.T) {
				r, err := open(bytes.NewReader(input))
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				if _, err := io.Copy(io.Discard, r); err == nil {
					t.Fatal("accepted damaged chunk")
				}
			})
		}
	}
	input := pbzChunk(LZFSE, maxBufferedChunk, maxBufferedChunk+1, []byte("bvx$"))
	r, err := NewReader(bytes.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := io.ReadAll(r); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized buffered chunk: %v", err)
	}
	want := errors.New("chunk read failed")
	input = pbzChunk(LZFSE, 1024, 1024, []byte("bvx$"))
	r, err = NewReader(io.MultiReader(bytes.NewReader(input[:28]), readerFunc(func([]byte) (int, error) { return 0, want })))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := io.ReadAll(r); !errors.Is(err, want) {
		t.Fatalf("source error lost: %v", err)
	}
}

func TestReadersEmptyChunksAndNilReads(t *testing.T) {
	input := pbzChunk(XZ, 1024, 0, nil)
	input = append(input, pbzChunk(XZ, 1024, 4, []byte("tail"))[12:]...)
	for name, open := range readers() {
		t.Run(name, func(t *testing.T) {
			r, err := open(bytes.NewReader(input))
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if n, err := r.Read(nil); n != 0 || err != nil {
				t.Fatalf("nil read: %d, %v", n, err)
			}
			if r.Flags() != r.BlockSize() {
				t.Fatal("legacy metadata changed")
			}
			got, err := io.ReadAll(r)
			if err != nil || string(got) != "tail" {
				t.Fatalf("empty chunk lost tail: %q, %v", got, err)
			}
			if _, err := r.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("repeated EOF: %v", err)
			}
		})
	}
}

func TestConcurrentConstructorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := NewConcurrentReader(ctx, bytes.NewReader(fakeStream(0)), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled constructor: %v", err)
	}
	chunk := &concurrentChunk{}
	if err := chunk.decode(ctx, XZ, decoderConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled chunk: %v", err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	input := bytes.NewReader(fakeStream(0))
	source := readerFunc(func(p []byte) (int, error) {
		n, err := input.Read(p)
		cancel()
		return n, err
	})
	if _, err := NewConcurrentReader(ctx, source, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel during header: %v", err)
	}
}
