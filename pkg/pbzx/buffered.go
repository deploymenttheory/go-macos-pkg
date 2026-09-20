package pbzx

import (
	"encoding/binary"
	"fmt"

	"github.com/deploymenttheory/go-macos-pkg/pkg/lzbitmap"
)

// checkBufferedSize checks allocation-driving frame fields before handing a
// whole stream to a codec that cannot accept an output limit. The codecs still
// validate the compressed contents. LZ4 enforces its limit during decoding.
func checkBufferedSize(algo Algorithm, src []byte, remaining uint64) error {
	if algo == LZ4 {
		return nil
	}
	if algo == LZBitmap {
		if len(src) < 4 || string(src[:4]) != lzbitmap.Magic {
			return fmt.Errorf("pbzx: invalid LZBITMAP magic")
		}
		src = src[4:]
	}
	for len(src) != 0 {
		var raw, stored uint64
		if algo == LZBitmap {
			if len(src) < 6 {
				return fmt.Errorf("pbzx: truncated LZBITMAP frame")
			}
			stored = uint64(src[0]) | uint64(src[1])<<8 | uint64(src[2])<<16
			raw = uint64(src[3]) | uint64(src[4])<<8 | uint64(src[5])<<16
			if stored < 6 || raw > lzbitmap.MaxChunk {
				return fmt.Errorf("pbzx: invalid LZBITMAP frame size")
			}
			if raw == 0 {
				return nil
			}
		} else {
			if len(src) >= 4 && string(src[:4]) == "bvx$" {
				return nil
			}
			if len(src) < 8 {
				return fmt.Errorf("pbzx: truncated LZFSE frame")
			}
			raw = uint64(binary.LittleEndian.Uint32(src[4:]))
			switch string(src[:4]) {
			case "bvx-":
				stored = 8 + raw
			case "bvxn":
				if len(src) < 12 {
					return fmt.Errorf("pbzx: truncated LZVN frame")
				}
				stored = 12 + uint64(binary.LittleEndian.Uint32(src[8:]))
			case "bvx1", "bvx2":
				var literals, matches uint64
				if string(src[:4]) == "bvx1" {
					if len(src) < 772 {
						return fmt.Errorf("pbzx: truncated LZFSE v1 frame")
					}
					literals = uint64(binary.LittleEndian.Uint32(src[12:]))
					matches = uint64(binary.LittleEndian.Uint32(src[16:]))
					stored = 772 + uint64(binary.LittleEndian.Uint32(src[20:])) + uint64(binary.LittleEndian.Uint32(src[24:]))
				} else {
					if len(src) < 32 {
						return fmt.Errorf("pbzx: truncated LZFSE v2 frame")
					}
					v0, v1 := binary.LittleEndian.Uint64(src[8:]), binary.LittleEndian.Uint64(src[16:])
					literals, matches = v0&0xfffff, (v0>>40)&0xfffff
					header := uint64(binary.LittleEndian.Uint32(src[24:]))
					if header < 32 {
						return fmt.Errorf("pbzx: invalid LZFSE v2 header size")
					}
					stored = header + (v0>>20)&0xfffff + (v1>>40)&0xfffff
				}
				// These are the codec's per-frame limits. In particular, v1
				// literals drive a scratch allocation without a decoder guard.
				if literals > 40000 || matches > 10000 {
					return fmt.Errorf("pbzx: LZFSE frame exceeds symbol limits")
				}
			default:
				return fmt.Errorf("pbzx: unknown LZFSE frame")
			}
		}
		if raw > remaining {
			return fmt.Errorf("pbzx: %s frames exceed declared chunk size", algo)
		}
		if stored > uint64(len(src)) {
			return fmt.Errorf("pbzx: truncated %s frame body", algo)
		}
		remaining -= raw
		src = src[stored:]
	}
	return nil
}
