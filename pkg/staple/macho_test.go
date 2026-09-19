package staple

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCDHashesBadInput(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := CDHashes(missing); err == nil {
		t.Fatal("missing file accepted")
	}
	if err := os.WriteFile(missing, []byte("not Mach-O"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CDHashes(missing); err == nil {
		t.Fatal("invalid file accepted")
	}
}
