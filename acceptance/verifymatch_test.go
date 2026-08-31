package acceptance

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/exitcode"
)

// TestExtractVerifyRejectsSwappedPayload builds a package, then rewrites its
// payload so it delivers a file the bill of materials does not name and drops
// one it does. --verify has to notice.
//
// It used to report success: the checksum lookup is keyed by the Bom's paths
// and missed, and a miss was passed over rather than reported, so the whole
// payload could be swapped out and the extraction still exited 0.
func TestExtractVerifyRejectsSwappedPayload(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "usr", "local")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	pkg := filepath.Join(work, "base.pkg")
	mustRun(t, "build", root, pkg, "--identifier", "com.example.swap", "--version", "1", "--install-location", "/")

	// Take the package apart, swap the payload, and put it back. flatten
	// takes entries as they stand, so the Bom is left describing the file
	// that is no longer there.
	exp := filepath.Join(work, "exp")
	mustRun(t, "expand", pkg, exp)

	raw, err := exec.Command(binPath, "cat", pkg, "Payload", "--raw").Output()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plain, []byte("hello.txt")) {
		t.Fatal("payload does not carry hello.txt")
	}
	// Same length, so nothing in the cpio headers has to move.
	plain = bytes.ReplaceAll(plain, []byte("hello.txt"), []byte("pwned.txt"))
	plain = bytes.ReplaceAll(plain, []byte("hello world"), []byte("OWNED!!!!!!"))

	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exp, "Payload"), out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	swapped := filepath.Join(work, "swapped.pkg")
	mustRun(t, "flatten", exp, swapped)

	stdout, stderr, code := run(t, "extract", "--verify", swapped, filepath.Join(work, "out"))
	if code != exitcode.Partial {
		t.Errorf("exit %d, want %d: a payload that does not match the bom was accepted\n%s%s",
			code, exitcode.Partial, stdout, stderr)
	}
	if !strings.Contains(stderr, "pwned.txt") {
		t.Errorf("the file the bom does not name was not reported:\n%s", stderr)
	}
	if !strings.Contains(stderr, "hello.txt") {
		t.Errorf("the file the payload never delivered was not reported:\n%s", stderr)
	}
}
