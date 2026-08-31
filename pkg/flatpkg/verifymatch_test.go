package flatpkg

import (
	"bytes"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
	"github.com/deploymenttheory/go-macos-pkg/pkg/cpio"
)

// payloadOf writes a cpio stream holding the named files.
func payloadOf(t *testing.T, files map[string]string) *cpio.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := cpio.NewWriter(&buf)
	dir := &cpio.Header{Name: ".", Mode: cpio.ModeDir | 0o755, NLink: 2, ModTime: time.Unix(0, 0)}
	if err := w.WriteHeader(dir); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		h := &cpio.Header{
			Name: name, Mode: cpio.ModeRegular | 0o644, NLink: 1,
			ModTime: time.Unix(0, 0), Size: int64(len(content)),
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return cpio.NewReader(bytes.NewReader(buf.Bytes()))
}

// TestVerifyReportsPayloadThatDoesNotMatchTheBom covers a payload that
// carries different files from the ones the bill of materials describes.
//
// The checksum comparison can only run on a file named in both, so a lookup
// that missed was previously passed over in silence: a payload could drop
// the file it promised, deliver another under a new name, and still be
// reported as extracted with exit 0.
func TestVerifyReportsPayloadThatDoesNotMatchTheBom(t *testing.T) {
	checksums := map[string]uint32{
		"./hello.txt": bom.CksumBytes([]byte("hello world\n")),
		"./other.txt": bom.CksumBytes([]byte("second file\n")),
	}

	t.Run("payload swaps a file for one the bom does not name", func(t *testing.T) {
		cr := payloadOf(t, map[string]string{
			"./pwned.txt": "OWNED\n",
			"./other.txt": "second file\n",
		})
		res, err := ExtractCPIO(cr, filepath.Join(t.TempDir(), "out"), ExtractOptions{Checksums: checksums})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Unlisted) != 1 || res.Unlisted[0] != "./pwned.txt" {
			t.Errorf("Unlisted = %v, want [./pwned.txt]", res.Unlisted)
		}
		if len(res.Absent) != 1 || res.Absent[0] != "./hello.txt" {
			t.Errorf("Absent = %v, want [./hello.txt]", res.Absent)
		}
		if !res.Partial() {
			t.Error("a payload that does not match the bom was reported as complete")
		}
	})

	t.Run("a payload that matches is not flagged", func(t *testing.T) {
		cr := payloadOf(t, map[string]string{
			"./hello.txt": "hello world\n",
			"./other.txt": "second file\n",
		})
		res, err := ExtractCPIO(cr, filepath.Join(t.TempDir(), "out"), ExtractOptions{Checksums: checksums})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Unlisted) != 0 || len(res.Absent) != 0 || len(res.Mismatched) != 0 {
			t.Errorf("clean payload flagged: unlisted=%v absent=%v mismatched=%v",
				res.Unlisted, res.Absent, res.Mismatched)
		}
		if res.Partial() {
			t.Error("a matching payload was reported as incomplete")
		}
	})

	t.Run("content corruption is still caught", func(t *testing.T) {
		cr := payloadOf(t, map[string]string{
			"./hello.txt": "HELLO world\n",
			"./other.txt": "second file\n",
		})
		res, err := ExtractCPIO(cr, filepath.Join(t.TempDir(), "out"), ExtractOptions{Checksums: checksums})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Mismatched) != 1 || res.Mismatched[0] != "./hello.txt" {
			t.Errorf("Mismatched = %v, want [./hello.txt]", res.Mismatched)
		}
	})

	// A pattern selects part of a payload, so the rest is absent by
	// design and must not be reported.
	t.Run("a pattern does not make the rest absent", func(t *testing.T) {
		cr := payloadOf(t, map[string]string{
			"./hello.txt": "hello world\n",
			"./other.txt": "second file\n",
		})
		o := ExtractOptions{Checksums: checksums, Pattern: mustRE(t, `hello`)}
		res, err := ExtractCPIO(cr, filepath.Join(t.TempDir(), "out"), o)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Absent) != 0 {
			t.Errorf("Absent = %v with a pattern set, want none", res.Absent)
		}
	})

}

func mustRE(t *testing.T, expr string) *regexp.Regexp {
	t.Helper()
	re, err := regexp.Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	return re
}
