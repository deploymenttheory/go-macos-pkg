// Reading the CDHash from a Mach-O code signature, the way Apple keys an
// application's notarization ticket.
//
// A signed Mach-O carries an LC_CODE_SIGNATURE load command pointing at an
// embedded signature SuperBlob (magic 0xfade0cc0). The SuperBlob indexes
// blobs; the CodeDirectory (magic 0xfade0c02) is the one that matters. Its
// CDHash is the hash of the whole CodeDirectory blob, using the algorithm
// the CodeDirectory's own hashType field names, and the ticket record uses
// the first twenty bytes of the SHA-256 form. codesign prints the same
// value as CDHash=.
//
// A universal ("fat") binary carries one code signature per architecture,
// so it has one CDHash per architecture. Apple's ticket covers all of them
// and its public database answers to any, so CDHashes returns every one and
// the caller tries each until the lookup resolves.
//
// The parser reads the container with debug/macho and the signature bytes
// itself: no codesign, nothing outside the Go standard library. The
// signature structures are big-endian regardless of the Mach-O's byte order.
package staple

import (
	"crypto/sha256"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

const (
	// lcCodeSignature is LC_CODE_SIGNATURE: a linkedit_data_command whose
	// dataoff/datasize locate the embedded signature within the image.
	lcCodeSignature = 0x1d
	// csEmbeddedSignature is the SuperBlob magic, csCodeDirectory the
	// CodeDirectory blob magic.
	csEmbeddedSignature = 0xfade0cc0
	csCodeDirectory     = 0xfade0c02
	// hashSHA256 is the CodeDirectory hashType for SHA-256, the algorithm a
	// notarized Developer ID signature uses and the one the ticket is keyed
	// on. cdHashLen is how much of it the record name carries.
	hashSHA256 = 2
	cdHashLen  = 20
	// cdHashTypeOffset is the byte offset of hashType within a CodeDirectory.
	cdHashTypeOffset = 37
)

// CDHashes returns the SHA-256 CDHash, truncated to twenty bytes, for every
// architecture of the Mach-O at path. It errors when the file is not a
// Mach-O, or carries no SHA-256 code signature (an unsigned or ad-hoc
// binary has no ticket).
func CDHashes(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out [][]byte
	if fat, ferr := macho.NewFatFile(f); ferr == nil {
		for _, a := range fat.Arches {
			h, err := cdHashFromImage(f, int64(a.Offset), a.File)
			if err != nil {
				return nil, err
			}
			out = append(out, h)
		}
	} else {
		mf, err := macho.NewFile(f)
		if err != nil {
			return nil, fmt.Errorf("staple: %s is not a Mach-O: %w", path, err)
		}
		h, err := cdHashFromImage(f, 0, mf)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// cdHashFromImage reads one architecture's SHA-256 CDHash. base is the
// image's offset within the file, zero for a thin binary and the fat-arch
// offset for one slice of a universal binary; the load command's dataoff is
// relative to it.
func cdHashFromImage(r io.ReaderAt, base int64, f *macho.File) ([]byte, error) {
	off, size, ok := codeSignatureRange(f)
	if !ok {
		return nil, fmt.Errorf("staple: the executable is not code-signed")
	}
	buf := make([]byte, size)
	if _, err := r.ReadAt(buf, base+int64(off)); err != nil {
		return nil, fmt.Errorf("staple: reading the code signature: %w", err)
	}
	return sha256CDHash(buf)
}

// codeSignatureRange finds the LC_CODE_SIGNATURE load command and returns
// the file range of the signature. debug/macho does not type this command,
// so it arrives as raw bytes read in the image's own byte order.
func codeSignatureRange(f *macho.File) (off, size uint32, ok bool) {
	for _, l := range f.Loads {
		lb, isBytes := l.(macho.LoadBytes)
		if !isBytes {
			continue
		}
		raw := lb.Raw()
		if len(raw) < 16 {
			continue
		}
		if f.ByteOrder.Uint32(raw[0:4]) == lcCodeSignature {
			return f.ByteOrder.Uint32(raw[8:12]), f.ByteOrder.Uint32(raw[12:16]), true
		}
	}
	return 0, 0, false
}

// sha256CDHash parses an embedded signature SuperBlob and returns the
// twenty-byte SHA-256 CDHash of its CodeDirectory. A binary may carry more
// than one CodeDirectory (a SHA-1 and a SHA-256 one, in alternate slots);
// only the SHA-256 form keys a notarization ticket.
func sha256CDHash(buf []byte) ([]byte, error) {
	be := binary.BigEndian
	if len(buf) < 12 || be.Uint32(buf[0:4]) != csEmbeddedSignature {
		return nil, fmt.Errorf("staple: the code signature is not an embedded signature")
	}
	count := be.Uint32(buf[8:12])
	for i := uint32(0); i < count; i++ {
		ix := 12 + i*8
		if int(ix)+8 > len(buf) {
			return nil, fmt.Errorf("staple: the code signature index runs past its data")
		}
		blobOff := be.Uint32(buf[ix+4 : ix+8])
		if int(blobOff)+8 > len(buf) {
			return nil, fmt.Errorf("staple: a code signature blob runs past its data")
		}
		blob := buf[blobOff:]
		if be.Uint32(blob[0:4]) != csCodeDirectory {
			continue
		}
		blen := be.Uint32(blob[4:8])
		if int(blen) > len(blob) || int(blen) <= cdHashTypeOffset {
			return nil, fmt.Errorf("staple: the CodeDirectory length is out of range")
		}
		cd := blob[:blen]
		if cd[cdHashTypeOffset] != hashSHA256 {
			continue
		}
		sum := sha256.Sum256(cd)
		return sum[:cdHashLen], nil
	}
	return nil, fmt.Errorf("staple: the executable has no SHA-256 code directory")
}
