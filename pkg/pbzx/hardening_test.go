package pbzx

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	xzdecode "github.com/mikelolasagasti/xz"
	"github.com/ulikunitz/xz"
)

func pbzChunk(algo Algorithm, blockSize, inflated uint64, stored []byte) []byte {
	b := binary.BigEndian.AppendUint64(algo.Magic(), blockSize)
	b = binary.BigEndian.AppendUint64(b, inflated)
	b = binary.BigEndian.AppendUint64(b, uint64(len(stored)))
	return append(b, stored...)
}

func encodeForTest(t *testing.T, algo Algorithm, plain []byte, blockSize uint64) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := NewWriter(&b, algo, blockSize)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestXZDictionaryOptions(t *testing.T) {
	// Both a full block and a short final block use the writer's dictionary.
	const dictionary = 16 << 20
	stream := binary.BigEndian.AppendUint64(XZ.Magic(), dictionary)
	for _, size := range []int{dictionary, 1024} {
		var compressed bytes.Buffer
		w, err := (xz.WriterConfig{DictCap: dictionary}).NewWriter(&compressed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(bytes.Repeat([]byte{'x'}, size)); err != nil {
			t.Fatal(err)
		}
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
		stream = append(stream, pbzChunk(XZ, dictionary, uint64(size), compressed.Bytes())[12:]...)
	}
	for _, limit := range []uint64{0, 8 << 20, dictionary - 1, dictionary, 32 << 20} {
		for _, workers := range []int{0, 1, 4} {
			t.Run(fmtCase(limit, workers), func(t *testing.T) {
				opts := ReaderOptions{MaxXZDictionarySize: limit}
				var r *Reader
				var err error
				if workers == 0 {
					r, err = NewReaderWithOptions(bytes.NewReader(stream), opts)
				} else {
					r, err = NewConcurrentReaderWithOptions(t.Context(), bytes.NewReader(stream), workers, opts)
				}
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				n, err := io.Copy(io.Discard, r)
				if limit > 0 && limit < dictionary {
					if !errors.Is(err, xzdecode.ErrMemlimit) || n != 0 {
						t.Fatalf("limit not enforced: %d bytes, %v", n, err)
					}
				} else if err != nil || n != dictionary+1024 {
					t.Fatalf("valid dictionary rejected: %d bytes, %v", n, err)
				}
			})
		}
	}
	for _, limit := range []uint64{1, maxBufferedChunk, maxBufferedChunk + 1, ^uint64(0)} {
		for _, concurrent := range []bool{false, true} {
			opts := ReaderOptions{MaxXZDictionarySize: limit}
			var r *Reader
			var err error
			if concurrent {
				r, err = NewConcurrentReaderWithOptions(t.Context(), bytes.NewReader(fakeStream(0)), 1, opts)
			} else {
				r, err = NewReaderWithOptions(bytes.NewReader(fakeStream(0)), opts)
			}
			if (err != nil) != (limit > maxBufferedChunk) {
				t.Fatalf("limit %d: %v", limit, err)
			}
			if r != nil {
				r.Close()
			}
		}
	}
}

func fmtCase(limit uint64, workers int) string {
	return fmt.Sprintf("limit=%d/workers=%d", limit, workers)
}

func TestDictionaryAutomaticBounds(t *testing.T) {
	for _, tc := range []struct{ block, inflated, want uint64 }{
		{0, 1024, 8 << 20}, {1024, 1024, 8 << 20},
		{16 << 20, 1024, 16 << 20}, {1024, 32 << 20, 32 << 20},
		{^uint64(0), 1024, maxBufferedChunk}, {0, 1 << 40, maxBufferedChunk},
	} {
		if got := (decoderConfig{blockSize: tc.block}).dictionaryLimit(tc.inflated); uint64(got) != tc.want {
			t.Fatalf("block %d, inflated %d: %d, want %d", tc.block, tc.inflated, got, tc.want)
		}
	}
}

func TestLZFSEInvalidStatesReturnErrors(t *testing.T) {
	for _, offset := range []int{32, 34, 36, 38, 44, 46, 48} {
		frame := make([]byte, 776)
		copy(frame, "bvx1")
		copy(frame[772:], "bvx$")
		binary.LittleEndian.PutUint32(frame[4:], 1024)
		binary.LittleEndian.PutUint32(frame[12:], 4)
		bound := uint16(1024)
		switch offset {
		case 44, 46:
			bound = 64
		case 48:
			bound = 256
		}
		binary.LittleEndian.PutUint16(frame[offset:], bound-1)
		if err := checkBufferedSize(LZFSE, frame, 1024); err != nil {
			t.Fatalf("valid state boundary: %v", err)
		}
		for _, state := range []uint16{bound, 65535} {
			binary.LittleEndian.PutUint16(frame[offset:], state)
			input := pbzChunk(LZFSE, 16<<20, 1024, frame)
			for name, open := range readers() {
				t.Run(fmt.Sprintf("%d/%d/%s", offset, state, name), func(t *testing.T) {
					r, err := open(bytes.NewReader(input))
					if err != nil {
						t.Fatal(err)
					}
					defer r.Close()
					if _, err = io.Copy(io.Discard, r); err == nil || !strings.Contains(err.Error(), "state") {
						t.Fatalf("invalid state was not rejected: %v", err)
					}
				})
			}
		}
	}
}

func TestBufferedCodecPanicIsOrderedError(t *testing.T) {
	input := encodeForTest(t, LZFSE, bytes.Repeat([]byte("x"), 2048), 1024)
	input = append(pbzChunk(LZFSE, 1024, 3, []byte("abc")), input[12:]...)
	for _, concurrent := range []bool{false, true} {
		codec := func(_ Algorithm, _ []byte, _ int) ([]byte, error) { panic("broken codec") }
		var r *Reader
		var err error
		if concurrent {
			r, err = newConcurrentReaderWithDecoder(t.Context(), bytes.NewReader(input), 2, ReaderOptions{}, codec)
		} else {
			r, err = NewReader(bytes.NewReader(input))
			if err == nil {
				r.decoder.buffered = codec
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		for read := range 2 {
			got, readErr := io.ReadAll(r)
			if readErr == nil || !strings.Contains(readErr.Error(), "lzfse decoder panic: broken codec") {
				t.Fatalf("codec panic did not become a sticky error: %v", readErr)
			}
			if read == 0 && string(got) != "abc" || read == 1 && len(got) != 0 {
				t.Fatalf("out-of-order data on read %d: %q", read, got)
			}
		}
		if err = r.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloseWaitsForRealBufferedCodec(t *testing.T) {
	plain := bytes.Repeat([]byte("real codec cancellation\n"), 12000)[:256<<10]
	for _, algo := range []Algorithm{LZFSE, LZ4, LZBitmap} {
		input := encodeForTest(t, algo, plain, 256<<10)
		if binary.BigEndian.Uint64(input[12:]) == binary.BigEndian.Uint64(input[20:]) {
			t.Fatalf("%s fixture must enter the compressed codec", algo)
		}
		if err := checkBufferedSize(algo, input[28:], uint64(len(plain))); err != nil {
			t.Fatalf("%s fixture: %v", algo, err)
		}
		for _, closeOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/close=%t", algo, closeOnly), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				entered, run, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				allowRun := sync.OnceFunc(func() { close(run) })
				allowReturn := sync.OnceFunc(func() { close(release) })
				produced := make(chan bool, 1)
				codec := func(a Algorithm, src []byte, size int) ([]byte, error) {
					close(entered)
					<-run
					// Execute the real, non-interruptible codec while cancelled.
					out, err := decodeBuffered(a, src, size)
					produced <- err == nil && bytes.Equal(out, plain)
					<-release
					return out, err
				}
				r, err := newConcurrentReaderWithDecoder(ctx, bytes.NewReader(input), 1, ReaderOptions{}, codec)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { cancel(); allowRun(); allowReturn(); _ = r.Close() })
				await(t, entered)
				readDone, closeDone := make(chan error, 1), make(chan error, 1)
				go func() { _, err := r.Read(make([]byte, 1)); readDone <- err }()
				if !closeOnly {
					cancel()
				}
				go func() { closeDone <- r.Close() }()
				await(t, r.parallel.ctx.Done())
				if err := await(t, readDone); !errors.Is(err, context.Canceled) {
					t.Fatalf("blocked read: %v", err)
				}
				allowRun()
				if !await(t, produced) {
					t.Fatal("real codec did not produce the expected output")
				}
				select {
				case <-closeDone:
					t.Fatal("Close returned while the codec call was active")
				default:
				}
				allowReturn()
				if err := await(t, closeDone); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
