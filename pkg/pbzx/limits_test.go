package pbzx

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"runtime"
	"testing"

	xzdecode "github.com/mikelolasagasti/xz"
)

func TestReadersRejectOversizedDictionaryBeforeAllocation(t *testing.T) {
	stream := buildPBZX(t, [][]byte{bytes.Repeat([]byte("x"), 1024)}, []bool{true})
	// Change the dictionary property in an otherwise valid XZ block to 64 MiB.
	block := stream[28+12:]
	headerSize := (int(block[0]) + 1) * 4
	if block[2] != 0x21 || block[3] != 1 {
		t.Fatal("expected LZMA2 filter")
	}
	block[4] = 28
	binary.LittleEndian.PutUint32(block[headerSize-4:], crc32.ChecksumIEEE(block[:headerSize-4]))
	chunk := bytes.Clone(stream[12:])
	stream = stream[:12]
	binary.BigEndian.PutUint64(stream[4:], 1024)
	for range 4 {
		stream = append(stream, chunk...)
	}
	for name, open := range readers() {
		t.Run(name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			r, err := open(bytes.NewReader(stream))
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.Copy(io.Discard, r)
			r.Close()
			runtime.ReadMemStats(&after)
			if !errors.Is(err, xzdecode.ErrMemlimit) {
				t.Fatalf("dictionary limit: %v", err)
			}
			if n := after.TotalAlloc - before.TotalAlloc; n > 4<<20 {
				t.Fatalf("allocated %d bytes before rejecting dictionary", n)
			}
		})
	}
}

func TestBufferedCodecSizeGuards(t *testing.T) {
	u32 := func(tag string, values ...uint32) []byte {
		b := []byte(tag)
		for _, v := range values {
			b = binary.LittleEndian.AppendUint32(b, v)
		}
		return b
	}
	v1 := make([]byte, 772)
	copy(v1, "bvx1")
	binary.LittleEndian.PutUint32(v1[12:], ^uint32(0))
	v2 := make([]byte, 32)
	copy(v2, "bvx2")
	binary.LittleEndian.PutUint32(v2[4:], 1<<30)
	binary.LittleEndian.PutUint32(v2[24:], 32)
	for _, test := range []struct {
		name string
		algo Algorithm
		data []byte
	}{
		{"LZVN expansion", LZFSE, u32("bvxn", 1<<30, 0)},
		{"LZFSE v1 literals", LZFSE, v1},
		{"LZFSE v2 expansion", LZFSE, v2},
		{"LZFSE aggregate", LZFSE, append(u32("bvx-", 4), append([]byte("abcd"), append(u32("bvx-", 4), []byte("efgh")...)...)...)},
		{"LZBITMAP expansion", LZBitmap, []byte{'Z', 'B', 'M', 9, 15, 0, 0, 0, 128, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := checkBufferedSize(test.algo, test.data, 7); err == nil {
				t.Fatal("accepted oversized allocation")
			}
		})
	}
}

func TestConcurrentRealDecoderCancellation(t *testing.T) {
	for _, algo := range []Algorithm{XZ, Zlib, LZFSE, LZ4, LZBitmap} {
		t.Run(algo.String(), func(t *testing.T) {
			var encoded bytes.Buffer
			w, err := NewWriter(&encoded, algo, 256<<10)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(bytes.Repeat([]byte("real codec cancellation\n"), 100000)); err != nil {
				t.Fatal(err)
			}
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			// Cancel at the first checkpoint after the real decoder produces
			// output, without depending on scheduling or replacing the codec.
			data := encoded.Bytes()
			header := chunkHeader{binary.BigEndian.Uint64(data[12:]), binary.BigEndian.Uint64(data[20:])}
			chunk := &concurrentChunk{header: header, src: data[28 : 28+header.deflated]}
			ctx, cancel := context.WithCancel(t.Context())
			progress := &cancelOnOutput{Context: ctx, cancel: cancel, chunk: chunk}
			if err := chunk.decode(progress, algo); !errors.Is(err, context.Canceled) {
				cancel()
				t.Fatalf("cancellation after decoded output: %v", err)
			}
			cancel()
			if len(chunk.dst) < 64<<10 || chunk.dst[0] != 'r' {
				t.Fatal("decoder did not produce the expected output before cancellation")
			}
			for _, closeOnly := range []bool{false, true} {
				ctx, cancel := context.WithCancel(t.Context())
				r, err := NewConcurrentReader(ctx, bytes.NewReader(encoded.Bytes()), 4)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				if _, err = io.ReadFull(r, make([]byte, 1)); err != nil {
					r.Close()
					cancel()
					t.Fatal(err)
				}
				if !closeOnly {
					cancel()
				}
				done := make(chan error, 1)
				go func() { done <- r.Close() }()
				if err = await(t, done); err != nil {
					t.Fatal(err)
				}
				cancel()
				if _, err = r.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
					t.Fatalf("read after cancellation: %v", err)
				}
			}
		})
	}
}

type cancelOnOutput struct {
	context.Context
	cancel context.CancelFunc
	chunk  *concurrentChunk
}

func (c *cancelOnOutput) Err() error {
	if len(c.chunk.dst) != 0 && c.chunk.dst[0] != 0 {
		c.cancel()
	}
	return c.Context.Err()
}
