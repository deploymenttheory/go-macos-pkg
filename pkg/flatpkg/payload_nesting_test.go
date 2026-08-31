package flatpkg

import (
	"bytes"
	"compress/gzip"
	"testing"
)

// TestOpenCPIORejectsDeepGzipNesting pins F5: a nested gzip chain must be
// refused at a bounded depth rather than recursing per layer.
func TestOpenCPIORejectsDeepGzipNesting(t *testing.T) {
	data := []byte("not really a cpio")
	for i := 0; i < maxPayloadNesting+4; i++ {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		data = buf.Bytes()
	}
	if _, _, err := OpenCPIO(bytes.NewReader(data)); err == nil {
		t.Fatal("deeply nested gzip payload was accepted; recursion is unbounded")
	}
}
