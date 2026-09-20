package pbzx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
)

// Fixture construction is outside the timed sub-benchmarks. Mixed inputs have
// alternating compressible and stored chunks, generated with a fixed xorshift.
func BenchmarkPBZXDecode(b *testing.B) {
	for _, blockSize := range []int{256 << 10, DefaultBlockSize} {
		for _, mixed := range []bool{false, true} {
			b.Run(fmt.Sprintf("block=%d/mixed=%t", blockSize, mixed), func(b *testing.B) {
				const chunks = 8
				var encoded bytes.Buffer
				w, err := NewWriter(&encoded, XZ, uint64(blockSize))
				if err != nil {
					b.Fatal(err)
				}
				block := make([]byte, blockSize)
				state := uint32(2463534242)
				for chunk := range chunks {
					for i := range block {
						block[i] = byte(i % 251)
						if mixed && chunk%2 != 0 {
							state ^= state << 13
							state ^= state >> 17
							state ^= state << 5
							block[i] = byte(state)
						}
					}
					if _, err := w.Write(block); err != nil {
						b.Fatal(err)
					}
				}
				if err := w.Close(); err != nil {
					b.Fatal(err)
				}
				for _, workers := range []int{0, 1, 2, 4, 8} {
					name := "sequential"
					if workers > 0 {
						name = fmt.Sprintf("workers=%d", workers)
					}
					b.Run(name, func(b *testing.B) {
						b.SetBytes(int64(chunks * blockSize))
						b.ReportAllocs()
						for b.Loop() {
							var r *Reader
							var err error
							if workers == 0 {
								r, err = NewReader(bytes.NewReader(encoded.Bytes()))
							} else {
								r, err = NewConcurrentReader(context.Background(), bytes.NewReader(encoded.Bytes()), workers)
							}
							if err != nil {
								b.Fatal(err)
							}
							n, err := io.Copy(io.Discard, r)
							closeErr := r.Close()
							if err != nil || closeErr != nil || n != int64(chunks*blockSize) {
								b.Fatalf("decode: %d bytes, %v, close %v", n, err, closeErr)
							}
						}
					})
				}
			})
		}
	}
}
