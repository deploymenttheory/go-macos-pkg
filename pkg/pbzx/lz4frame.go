// Apple's LZ4 framing, as libcompression and the kernel (xnu
// osfmk/vm/lz4.h) write it. It is not the LZ4 frame format (no 0x184D2204
// magic): a sequence of blocks, each with a four-byte tag.
//
//	"bv41" u32 decodedSize u32 encodedSize   then encodedSize bytes of LZ4 block
//	"bv4-" u32 size                          then size raw bytes
//	"bv4$"                                   end of stream
//
// Integers little-endian. Blocks are at most 64 KiB decoded.
package pbzx

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/pierrec/lz4/v4"
)

const lz4BlockSize = 1 << 16

var (
	lz4Compressed = []byte("bv41")
	lz4Raw        = []byte("bv4-")
	lz4End        = []byte("bv4$")
)

// decodeLZ4Frames decodes a sequence of Apple LZ4 frames; expected is the
// decoded size, used to size the output.
func decodeLZ4Frames(src []byte, expected int) ([]byte, error) {
	out := make([]byte, 0, expected)
	pos := 0
	for {
		if pos+4 > len(src) {
			return nil, errors.New("lz4: missing end marker")
		}
		tag := src[pos : pos+4]
		pos += 4
		switch {
		case string(tag) == string(lz4End):
			return out, nil
		case string(tag) == string(lz4Compressed):
			if pos+8 > len(src) {
				return nil, errors.New("lz4: truncated block header")
			}
			decoded := int(binary.LittleEndian.Uint32(src[pos:]))
			encoded := int(binary.LittleEndian.Uint32(src[pos+4:]))
			pos += 8
			if decoded > lz4BlockSize*16 || pos+encoded > len(src) {
				return nil, fmt.Errorf("lz4: implausible block (%d decoded, %d encoded)", decoded, encoded)
			}
			dst := make([]byte, decoded)
			n, err := lz4.UncompressBlock(src[pos:pos+encoded], dst)
			if err != nil {
				return nil, err
			}
			if n != decoded {
				return nil, fmt.Errorf("lz4: block decoded to %d bytes, header says %d", n, decoded)
			}
			out = append(out, dst...)
			pos += encoded
		case string(tag) == string(lz4Raw):
			if pos+4 > len(src) {
				return nil, errors.New("lz4: truncated raw block header")
			}
			size := int(binary.LittleEndian.Uint32(src[pos:]))
			pos += 4
			if pos+size > len(src) {
				return nil, errors.New("lz4: truncated raw block")
			}
			out = append(out, src[pos:pos+size]...)
			pos += size
		default:
			return nil, fmt.Errorf("lz4: unknown block tag %q", tag)
		}
	}
}

// encodeLZ4Frames encodes src as Apple LZ4 frames in 64 KiB blocks.
func encodeLZ4Frames(src []byte) []byte {
	out := make([]byte, 0, len(src)/2+64)
	var hdr [8]byte
	for pos := 0; pos < len(src); pos += lz4BlockSize {
		end := min(pos+lz4BlockSize, len(src))
		block := src[pos:end]
		dst := make([]byte, lz4.CompressBlockBound(len(block)))
		n, err := lz4.CompressBlock(block, dst, nil)
		if err != nil || n == 0 || n >= len(block) {
			out = append(out, lz4Raw...)
			binary.LittleEndian.PutUint32(hdr[:4], uint32(len(block)))
			out = append(out, hdr[:4]...)
			out = append(out, block...)
			continue
		}
		out = append(out, lz4Compressed...)
		binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(block)))
		binary.LittleEndian.PutUint32(hdr[4:8], uint32(n))
		out = append(out, hdr[:]...)
		out = append(out, dst[:n]...)
	}
	return append(out, lz4End...)
}
