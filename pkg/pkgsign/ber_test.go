package pkgsign

import (
	"bytes"
	"encoding/asn1"
	"testing"
)

func TestBERToDER(t *testing.T) {
	// DER passes through.
	der, _ := asn1.Marshal(struct {
		A int
		B []byte
	}{7, []byte("xyz")})
	got, err := berToDER(der)
	if err != nil || !bytes.Equal(got, der) {
		t.Fatalf("DER passthrough: %v\n%x\n%x", err, got, der)
	}
	// Indefinite-length SEQUENCE containing an INTEGER and a constructed
	// OCTET STRING split in two, itself indefinite.
	ber := []byte{
		0x30, 0x80, // SEQUENCE, indefinite
		0x02, 0x01, 0x07, // INTEGER 7
		0x24, 0x80, // OCTET STRING, constructed, indefinite
		0x04, 0x01, 'x',
		0x04, 0x02, 'y', 'z',
		0x00, 0x00, // end of octet string
		0x00, 0x00, // end of sequence
		0x00, 0x00, 0x00, // padding after
	}
	got, err = berToDER(ber)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, der) {
		t.Errorf("BER conversion:\n got %x\nwant %x", got, der)
	}
	if _, err := berToDER([]byte{0x30, 0x80, 0x02, 0x01}); err == nil {
		t.Error("truncated BER accepted")
	}
	// Long-form definite length with an indefinite child.
	inner := append([]byte{0x30, 0x80, 0x02, 0x01, 0x01, 0x00, 0x00}, []byte{}...)
	long := append([]byte{0x30, 0x81, byte(len(inner))}, inner...)
	got, err = berToDER(long)
	want := []byte{0x30, 0x05, 0x30, 0x03, 0x02, 0x01, 0x01}
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("long form: %v %x", err, got)
	}
}
