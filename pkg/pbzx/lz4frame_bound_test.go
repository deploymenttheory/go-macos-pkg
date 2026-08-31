package pbzx

import (
	"encoding/binary"
	"testing"
)

// rawBlock builds one Apple "bv4-" raw LZ4 block of n bytes.
func rawBlock(n int) []byte {
	b := append([]byte{}, lz4Raw...)
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(n))
	b = append(b, sz[:]...)
	return append(b, make([]byte, n)...)
}

// TestDecodeLZ4FramesEnforcesExpected pins F3: the declared decoded size must
// bound the output during the loop, so a stream of blocks cannot balloon past
// it in memory before the caller's post-hoc check.
func TestDecodeLZ4FramesEnforcesExpected(t *testing.T) {
	const block = 4096
	var src []byte
	src = append(src, rawBlock(block)...)
	src = append(src, rawBlock(block)...) // two blocks: 2*block decoded
	src = append(src, lz4End...)

	// expected smaller than the real output: must error, not allocate 2*block.
	if _, err := decodeLZ4Frames(src, block); err == nil {
		t.Fatal("decodeLZ4Frames accepted output larger than expected")
	}

	// expected matching the real output: must succeed.
	out, err := decodeLZ4Frames(src, 2*block)
	if err != nil {
		t.Fatalf("unexpected error for well-sized frames: %v", err)
	}
	if len(out) != 2*block {
		t.Fatalf("decoded %d bytes, want %d", len(out), 2*block)
	}
}
