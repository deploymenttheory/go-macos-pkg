package pbzx

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/ulikunitz/xz"
)

// buildPBZX writes a pbzx stream with the given chunks; compressed chunks
// are xz'd, others stored raw.
func buildPBZX(t *testing.T, chunks [][]byte, compress []bool) []byte {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("pbzx")
	binary.Write(&out, binary.BigEndian, uint64(1<<24))
	for i, c := range chunks {
		stored := c
		if compress[i] {
			var z bytes.Buffer
			zw, err := xz.NewWriter(&z)
			if err != nil {
				t.Fatal(err)
			}
			zw.Write(c)
			zw.Close()
			stored = z.Bytes()
		}
		binary.Write(&out, binary.BigEndian, uint64(len(c)))
		binary.Write(&out, binary.BigEndian, uint64(len(stored)))
		out.Write(stored)
	}
	return out.Bytes()
}

func TestReader(t *testing.T) {
	a := bytes.Repeat([]byte("abcdefgh"), 4096)
	b := []byte("raw chunk that is stored")
	c := bytes.Repeat([]byte("z"), 100000)
	data := buildPBZX(t, [][]byte{a, b, c}, []bool{true, false, true})
	if !IsPBZX(data) {
		t.Fatal("magic not recognised")
	}
	r, err := NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if r.Flags() != 1<<24 {
		t.Errorf("flags = %d", r.Flags())
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(append([]byte{}, a...), b...), c...)
	if !bytes.Equal(got, want) {
		t.Errorf("decoded %d bytes, want %d", len(got), len(want))
	}

	if _, err := NewReader(bytes.NewReader([]byte("nope"))); err == nil {
		t.Error("bad magic accepted")
	}
	// A chunk whose declared inflated size disagrees with its content.
	bad := buildPBZX(t, [][]byte{a}, []bool{true})
	binary.BigEndian.PutUint64(bad[12:20], uint64(len(a)+1))
	r, _ = NewReader(bytes.NewReader(bad))
	if _, err := io.ReadAll(r); err == nil {
		t.Error("short chunk accepted")
	}
}
