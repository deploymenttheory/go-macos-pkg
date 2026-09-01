package staple

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os/exec"
	"regexp"
	"runtime"
	"testing"
)

// buildSuperBlob assembles an embedded-signature SuperBlob holding one
// CodeDirectory of the given hashType, so the parser can be tested without a
// real Mach-O.
func buildSuperBlob(hashType byte) (blob, codeDir []byte) {
	cd := make([]byte, 40)
	binary.BigEndian.PutUint32(cd[0:4], csCodeDirectory)
	binary.BigEndian.PutUint32(cd[4:8], uint32(len(cd)))
	cd[cdHashTypeOffset] = hashType

	const indexOff = 12
	blobOff := indexOff + 8
	super := make([]byte, blobOff+len(cd))
	binary.BigEndian.PutUint32(super[0:4], csEmbeddedSignature)
	binary.BigEndian.PutUint32(super[4:8], uint32(len(super)))
	binary.BigEndian.PutUint32(super[8:12], 1) // one blob
	binary.BigEndian.PutUint32(super[indexOff:indexOff+4], 0)
	binary.BigEndian.PutUint32(super[indexOff+4:indexOff+8], uint32(blobOff))
	copy(super[blobOff:], cd)
	return super, cd
}

func TestSHA256CDHash(t *testing.T) {
	super, cd := buildSuperBlob(hashSHA256)
	want := sha256.Sum256(cd)
	got, err := sha256CDHash(super)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hex.EncodeToString(got) != hex.EncodeToString(want[:cdHashLen]) {
		t.Fatalf("CDHash = %x, want %x", got, want[:cdHashLen])
	}
}

func TestSHA256CDHashRejects(t *testing.T) {
	// A signature carrying only a SHA-1 CodeDirectory has no ticket key.
	if super, _ := buildSuperBlob(1); func() bool { _, err := sha256CDHash(super); return err == nil }() {
		t.Fatal("a SHA-1-only signature should have no SHA-256 CDHash")
	}
	// Not an embedded signature.
	if _, err := sha256CDHash([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Fatal("non-signature bytes were accepted")
	}
}

// TestCDHashesMatchesCodesign checks the parser against codesign, the
// oracle, on a binary macOS always has. It runs only where both are present.
func TestCDHashesMatchesCodesign(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("codesign oracle is macOS only")
	}
	if _, err := exec.LookPath("codesign"); err != nil {
		t.Skip("codesign not available")
	}
	const bin = "/bin/ls"
	out, err := exec.Command("codesign", "-dvvv", bin).CombinedOutput()
	if err != nil {
		t.Skipf("codesign -dvvv %s: %v", bin, err)
	}
	m := regexp.MustCompile(`(?m)^CDHash=([0-9a-f]+)`).FindSubmatch(out)
	if m == nil {
		t.Skipf("codesign reported no CDHash for %s", bin)
	}
	want := string(m[1])

	hashes, err := CDHashes(bin)
	if err != nil {
		t.Fatalf("CDHashes(%s): %v", bin, err)
	}
	for _, h := range hashes {
		if hex.EncodeToString(h) == want {
			return
		}
	}
	t.Fatalf("codesign CDHash %s not among %x", want, hashes)
}
