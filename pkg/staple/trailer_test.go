package staple

import (
	"bytes"
	"testing"
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
