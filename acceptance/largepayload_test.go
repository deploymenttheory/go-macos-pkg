package acceptance

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
)

// The --large-payload oracle tests need a file over 8 GiB, which is more
// than a normal test run should write even sparsely, so they are opt-in:
//
//	MACOSPKG_ACCEPTANCE_LARGE=1 go test -run LargePayload ./acceptance/
//
// The file itself is sparse, so it costs no disk; what it costs is the
// time to read 9 GiB through the packager, about twenty seconds.
const largeEnv = "MACOSPKG_ACCEPTANCE_LARGE"

const (
	gib       = 1 << 30
	hugeSize  = 9 * gib // over the 8 GiB an odc header can express
	hugeCksum = 3074140381
)

func requireLarge(t *testing.T) {
	t.Helper()
	if os.Getenv(largeEnv) == "" {
		t.Skipf("set %s=1 to run the >8 GiB payload tests", largeEnv)
	}
}

// hugeRoot writes a destination root holding one sparse file over 8 GiB.
func hugeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "usr", "local")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "huge"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(hugeSize); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestLargePayloadReadsPkgbuildSegments is the regression test for reading
// a --large-payload package. pkgbuild splits a file over 8 GiB into 1 GiB
// segments written consecutively under one name; reading only the first
// segment truncated the file silently, and the bill of materials reported
// the size modulo 2^32 because the Size64 tree was keyed wrongly.
func TestLargePayloadReadsPkgbuildSegments(t *testing.T) {
	requireLarge(t)
	if _, err := exec.LookPath("pkgbuild"); err != nil {
		t.Skip("pkgbuild not available")
	}
	root := hugeRoot(t)
	pkg := filepath.Join(t.TempDir(), "theirs.pkg")
	hostTool(t, "pkgbuild", "--quiet", "--root", root, "--identifier", "com.example.huge",
		"--version", "1", "--install-location", "/", "--large-payload",
		"--min-os-version", "12.0", pkg)

	// The bill of materials must report the whole file, not its low 32
	// bits: the Size64 tree is keyed by the path record, not the path id.
	var line string
	for _, l := range nonEmptyLines(mustRun(t, "inspect", pkg, "bom")) {
		if strings.HasPrefix(l, "./usr/local/huge\t") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("./usr/local/huge is missing from the bom")
	}
	if want := strconv.FormatInt(hugeSize, 10); !strings.Contains(line, want) {
		t.Errorf("bom line %q does not report size %s", line, want)
	}

	// And the payload must read back whole, joined across its segments.
	assertPayloadWhole(t, pkg)
}

// assertPayloadWhole streams the payload file out of pkg and checks its
// length and POSIX cksum, which is the checksum the bill of materials
// records.
func assertPayloadWhole(t *testing.T, pkg string) {
	t.Helper()
	cmd := exec.Command(binPath, "cat", pkg, "--payload", "./usr/local/huge")
	cmd.Env = cleanEnv()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ck := bom.NewCksum()
	n, err := io.Copy(ck, out)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if n != hugeSize {
		t.Errorf("payload read %d bytes, want %d (segments not joined)", n, hugeSize)
	}
	if got := ck.Sum32(); got != hugeCksum {
		t.Errorf("payload checksum %d, want %d", got, hugeCksum)
	}
}

// TestLargePayloadWritesSegmentsPkgutilReads is the write-side oracle: a
// package we build with --large-payload must be one Apple's own pkgutil
// can expand back into the single file it came from.
func TestLargePayloadWritesSegmentsPkgutilReads(t *testing.T) {
	requireLarge(t)
	if _, err := exec.LookPath("pkgutil"); err != nil {
		t.Skip("pkgutil not available")
	}
	root := hugeRoot(t)
	pkg := filepath.Join(t.TempDir(), "ours.pkg")
	mustRun(t, "build", root, pkg, "--identifier", "com.example.huge",
		"--version", "1", "--install-location", "/", "--large-payload",
		"--min-os-version", "12.0")

	dir := filepath.Join(t.TempDir(), "expanded")
	hostTool(t, "pkgutil", "--expand-full", pkg, dir)

	got := filepath.Join(dir, "LargeSegmentedPayload", "usr", "local", "huge")
	st, err := os.Stat(got)
	if err != nil {
		t.Fatalf("pkgutil did not produce the file: %v", err)
	}
	if st.Size() != hugeSize {
		t.Errorf("pkgutil reassembled %d bytes, want %d", st.Size(), hugeSize)
	}
}
