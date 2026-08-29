package staple

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

func TestEncodeRead(t *testing.T) {
	archive := []byte("xar!pretend this is an archive")
	ticket := append([]byte("s8ch"), bytes.Repeat([]byte{7}, 100)...)
	file := append(append([]byte{}, archive...), Encode(ticket)...)

	got, err := Read(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data, ticket) {
		t.Error("ticket bytes differ")
	}
	if got.Offset != int64(len(archive)) {
		t.Errorf("offset = %d, want %d", got.Offset, len(archive))
	}
	if _, err := Read(bytes.NewReader(archive), int64(len(archive))); err != ErrNoTicket {
		t.Errorf("unstapled: %v, want ErrNoTicket", err)
	}
	// A ticket without its magic is refused rather than returned.
	bad := append(append([]byte{}, archive...), Encode([]byte("nope"))...)
	if _, err := Read(bytes.NewReader(bad), int64(len(bad))); err == nil || err == ErrNoTicket {
		t.Errorf("bad ticket magic: %v", err)
	}
}

// TestStapleInPlace covers src == dst, which Windows only allows once the
// source is closed.
func TestStapleInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.pkg")
	// A minimal xar: header + empty TOC is enough for Open.
	var out bytes.Buffer
	w, err := xar.NewWriter(&out, xar.WriterOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddFile("PackageInfo", xar.FileHeader{Mode: 0o644}, xar.EncodingNone, strings.NewReader("<pkg-info/>")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	ticket := append([]byte("s8ch"), bytes.Repeat([]byte{9}, 50)...)
	if err := Staple(path, path, ticket); err != nil {
		t.Fatalf("staple in place: %v", err)
	}
	got, err := Has(path)
	if err != nil || !bytes.Equal(got.Data, ticket) {
		t.Fatalf("after in-place staple: %v", err)
	}
	if err := Staple(path, path, ticket); err != nil {
		t.Fatalf("re-staple in place: %v", err)
	}
	if err := Unstaple(path, path); err != nil {
		t.Fatalf("unstaple in place: %v", err)
	}
	if _, err := Has(path); err != ErrNoTicket {
		t.Errorf("after unstaple: %v", err)
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, out.Bytes()) {
		t.Error("unstaple did not restore the original bytes")
	}
}
