package staple

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

func TestRecordName(t *testing.T) {
	digest := make([]byte, 32)
	for i := range digest {
		digest[i] = byte(i)
	}
	got, err := RecordName(xar.ChecksumSHA256, digest)
	if err != nil || got != "2/2/000102030405060708090a0b0c0d0e0f10111213" {
		t.Errorf("sha256: %q %v", got, err)
	}
	got, _ = RecordName(xar.ChecksumSHA1, digest[:20])
	if got != "2/1/000102030405060708090a0b0c0d0e0f10111213" {
		t.Errorf("sha1: %q", got)
	}
	if _, err := RecordName(xar.ChecksumMD5, digest); err == nil {
		t.Error("md5 accepted")
	}
}

func TestLookup(t *testing.T) {
	ticket := append([]byte("s8ch"), []byte("ticket bytes")...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		switch r.URL.Path {
		case "/found":
			w.Write([]byte(`{"records":[{"recordName":"2/2/abc","recordType":"DeveloperIDTicket","fields":{"signedTicket":{"type":"BYTES","value":"` + base64.StdEncoding.EncodeToString(ticket) + `"}}}]}`))
		case "/missing":
			w.Write([]byte(`{"records":[{"recordName":"2/2/abc","reason":"Record not found","serverErrorCode":"NOT_FOUND"}]}`))
		default:
			http.Error(w, "boom", 500)
		}
	}))
	defer srv.Close()
	l := &Lookup{URL: srv.URL + "/found", Client: srv.Client()}
	got, err := l.Fetch(context.Background(), "2/2/abc")
	if err != nil || string(got) != string(ticket) {
		t.Errorf("found: %q %v", got, err)
	}
	l.URL = srv.URL + "/missing"
	if _, err := l.Fetch(context.Background(), "2/2/abc"); !errors.Is(err, ErrNoTicket) {
		t.Errorf("missing: %v", err)
	}
	l.URL = srv.URL + "/error"
	if _, err := l.Fetch(context.Background(), "2/2/abc"); err == nil || errors.Is(err, ErrNoTicket) {
		t.Errorf("server error: %v", err)
	}
}
