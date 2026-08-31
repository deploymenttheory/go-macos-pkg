package flatpkg

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/cpio"
)

// TestEntriesUnderASymlinkAreSkippedNotFatal covers a payload that names a
// symbolic link pointing out of the destination and then names entries
// beneath it.
//
// Writing those entries would follow the link and leave the directory the
// caller chose. os.Root refuses that, but it refuses it as a hard error, so
// one hostile entry abandoned the whole payload: everything after it went
// unextracted and the caller saw a raw "path escapes from parent" with exit
// 1 rather than the skip-and-report the other unsafe paths get.
func TestEntriesUnderASymlinkAreSkippedNotFatal(t *testing.T) {
	outside := t.TempDir()

	var buf bytes.Buffer
	w := cpio.NewWriter(&buf)
	write := func(name string, mode uint32, data string) {
		t.Helper()
		h := &cpio.Header{Name: name, Mode: mode, NLink: 1, ModTime: time.Unix(0, 0), Size: int64(len(data))}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	write("./before.txt", cpio.ModeRegular|0o644, "BEFORE\n")
	write("./link", cpio.ModeSymlink|0o777, outside)
	write("./link/through.txt", cpio.ModeRegular|0o644, "PWNED\n")
	write("./after.txt", cpio.ModeRegular|0o644, "AFTER\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "out")
	res, err := ExtractCPIO(cpio.NewReader(bytes.NewReader(buf.Bytes())), dir, ExtractOptions{})
	if err != nil {
		t.Fatalf("one unsafe entry ended the extraction: %v", err)
	}

	// Nothing may be written through the link.
	if _, err := os.Stat(filepath.Join(outside, "through.txt")); err == nil {
		t.Error("an entry was written outside the destination")
	}

	// The entry is reported, in the same way as any other unsafe path.
	var found bool
	for _, s := range res.Skipped {
		if strings.Contains(s.Path, "through.txt") {
			found = true
			if !strings.Contains(s.Reason, "symbolic link") {
				t.Errorf("reason = %q, want it to name the link", s.Reason)
			}
		}
	}
	if !found {
		t.Errorf("the entry under the link was not reported: %+v", res.Skipped)
	}
	if !res.Partial() {
		t.Error("an extraction that skipped an entry was reported as complete")
	}

	// And the rest of the payload still arrives. This is the part the
	// hard error cost: after.txt comes after the hostile entry.
	for _, name := range []string{"before.txt", "after.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was not extracted: %v", name, err)
		}
	}
}
