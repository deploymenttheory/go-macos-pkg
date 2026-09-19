package acceptance

import (
	"encoding/hex"
	"os/exec"
	"regexp"
	"runtime"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/staple"
)

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

	hashes, err := staple.CDHashes(bin)
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
