package acceptance

import (
	"os"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/exitcode"
)

// TestVerifyRejectsTamperedContents covers a package whose signature is
// intact but whose heap has been altered.
//
// The signature covers the table of contents, and the table of contents
// carries a checksum for every entry. Checking only the signature therefore
// says nothing about the bytes in the heap: they can be replaced and every
// signature check still passes. Apple's "pkgutil --check-signature" reports
// such a package as "package is invalid (checksum did not verify)"; this
// reported it as valid and exited 0.
func TestVerifyRejectsTamperedContents(t *testing.T) {
	signed, ca := signedFixture(t)

	// The signature has to be good to begin with, or the rest proves
	// nothing about the contents check.
	if _, stderr, code := run(t, "verify", "--trust-anchors", ca, signed); code != 0 {
		t.Fatalf("the freshly signed package did not verify: exit %d\n%s", code, stderr)
	}

	data, err := os.ReadFile(signed)
	if err != nil {
		t.Fatal(err)
	}
	// A byte deep in the heap, well past the header and table of contents,
	// so the signature still covers exactly what it covered before.
	at := len(data) - len(data)/4
	data[at] ^= 0xFF
	tampered := signed + ".tampered"
	if err := os.WriteFile(tampered, data, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, "verify", "--trust-anchors", ca, tampered)
	if code != exitcode.Signature {
		t.Errorf("exit %d, want %d: a package with altered contents verified\n%s%s",
			code, exitcode.Signature, stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "contents") {
		t.Errorf("the failure does not mention the contents:\n%s%s", stdout, stderr)
	}
}
