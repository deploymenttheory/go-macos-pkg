package pkgsign

import (
	"bytes"
	"errors"
	"testing"
)

// TestBERToDERDepthBounded pins F11: a stream of constructed indefinite-length
// headers must be refused at a bounded depth, not recursed once per two bytes.
func TestBERToDERDepthBounded(t *testing.T) {
	// 0x30 0x80 = constructed, indefinite length: each pair nests one deeper.
	deep := bytes.Repeat([]byte{0x30, 0x80}, maxBERDepth+50)
	_, err := berToDER(deep)
	if err == nil {
		t.Fatal("deeply nested BER was accepted")
	}
	if !errors.Is(err, errBER) {
		t.Fatalf("got %v, want a malformed-BER error", err)
	}
}
