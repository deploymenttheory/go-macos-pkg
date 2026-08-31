package pkgsign

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// appleToken is a real token from Apple's timestamp authority, with the
// bytes it attests to. scripts/gen-fixtures.sh regenerates both.
func appleToken(t *testing.T) (token, signed []byte) {
	t.Helper()
	token, err := os.ReadFile("../../testdata/timestamp/apple-token.der")
	if err != nil {
		t.Skip("fixture missing:", err)
	}
	signed, err = os.ReadFile("../../testdata/timestamp/apple-token.signed-value")
	if err != nil {
		t.Skip("fixture missing:", err)
	}
	return token, signed
}

// TestVerifyTimestampAcceptsApple is the reference: Apple's own authority,
// its own token, checked against the roots we embed.
func TestVerifyTimestampAcceptsApple(t *testing.T) {
	token, signed := appleToken(t)
	at, err := VerifyTimestamp(token, signed, AppleRoots())
	if err != nil {
		t.Fatalf("Apple's own token rejected: %v", err)
	}
	if at.IsZero() || at.After(time.Now().Add(time.Hour)) {
		t.Errorf("generation time = %v", at)
	}
	// The authority's certificate lives for weeks, not years, so the time
	// has to be judged at the token's own instant for this to keep working.
	claimed, err := TimestampTime(token)
	if err != nil || !claimed.Equal(at) {
		t.Errorf("verified time %v does not match the claimed %v (%v)", at, claimed, err)
	}
}

// TestVerifyTimestampRejectsTransplant is the attack the message imprint
// exists to stop: a token that is entirely genuine, moved onto another
// signature.
func TestVerifyTimestampRejectsTransplant(t *testing.T) {
	token, signed := appleToken(t)
	other := append(append([]byte(nil), signed...), '!')
	_, err := VerifyTimestamp(token, other, AppleRoots())
	if err == nil {
		t.Fatal("a token for another signature was accepted")
	}
	if !errors.Is(err, ErrTimestampInvalid) {
		t.Errorf("error = %v, want ErrTimestampInvalid", err)
	}
}

// TestVerifyTimestampRejectsForgedTime is the attack that motivated all
// of this. The token's TSTInfo is rewritten to claim a different time, as
// someone wanting an expired certificate to look valid would do. The
// authority's signature no longer covers it.
func TestVerifyTimestampRejectsForgedTime(t *testing.T) {
	token, signed := appleToken(t)
	genuine, err := TimestampTime(token)
	if err != nil {
		t.Fatal(err)
	}
	// GeneralizedTime is stored as ASCII, so the year can be edited in
	// place without disturbing any length.
	forged := bytes.Replace(token,
		[]byte(genuine.UTC().Format("20060102150405")),
		[]byte(genuine.UTC().AddDate(-3, 0, 0).Format("20060102150405")), 1)
	if bytes.Equal(forged, token) {
		t.Skip("could not locate the generation time in the token")
	}
	if claimed, err := TimestampTime(forged); err != nil || claimed.Equal(genuine) {
		t.Skipf("the edit did not move the claimed time (%v, %v)", claimed, err)
	}
	_, err = VerifyTimestamp(forged, signed, AppleRoots())
	if err == nil {
		t.Fatal("a token with a forged generation time was accepted")
	}
	if !errors.Is(err, ErrTimestampInvalid) {
		t.Errorf("error = %v, want ErrTimestampInvalid", err)
	}
}

// TestVerifyTimestampUnverifiedCases separates "this is wrong" from "I
// cannot tell", because only the first is grounds for failing a package.
func TestVerifyTimestampUnverifiedCases(t *testing.T) {
	token, signed := appleToken(t)
	for _, tc := range []struct {
		name  string
		token []byte
		roots *x509.CertPool
	}{
		{"not a token", []byte{0x30, 0x03, 0x02, 0x01, 0x01}, AppleRoots()},
		{"empty", nil, AppleRoots()},
		{"authority we do not trust", token, x509.NewCertPool()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyTimestamp(tc.token, signed, tc.roots)
			if err == nil {
				t.Fatal("accepted")
			}
			if errors.Is(err, ErrTimestampInvalid) {
				t.Errorf("reported as tampering rather than unverifiable: %v", err)
			}
			if !errors.Is(err, ErrTimestampUnverified) {
				t.Errorf("error = %v, want ErrTimestampUnverified", err)
			}
		})
	}
}

// TestSignedAttrsNeedContentType covers RFC 5652 4.5.1. The attributes
// are signed correctly; the only thing wrong is that contentType is
// missing, or names a content type other than the one signed.
func TestSignedAttrsNeedContentType(t *testing.T) {
	id, _ := testIdentity(t)
	content := []byte("the content")
	h := crypto.SHA256.New()
	h.Write(content)
	digest := h.Sum(nil)

	// Sign the attribute set exactly as SignCMS does, so the only thing
	// under test is which attributes are in it.
	build := func(attrs []attribute) signerInfo {
		t.Helper()
		set, err := encodeAttrs(attrs)
		if err != nil {
			t.Fatal(err)
		}
		hh := crypto.SHA256.New()
		hh.Write(set)
		sig, err := rsa.SignPKCS1v15(rand.Reader, id.Key.(*rsa.PrivateKey), crypto.SHA256, hh.Sum(nil))
		if err != nil {
			t.Fatal(err)
		}
		stored := append([]byte(nil), set...)
		stored[0] = 0xA0
		return signerInfo{SignedAttrs: asn1.RawValue{FullBytes: stored}, Signature: sig}
	}
	digestAttr, err := attrOf(oidAttrMessageDgst, digest)
	if err != nil {
		t.Fatal(err)
	}
	typeAttr, err := attrOf(oidAttrContentType, oidData)
	if err != nil {
		t.Fatal(err)
	}
	wrongType, err := attrOf(oidAttrContentType, oidSignedData)
	if err != nil {
		t.Fatal(err)
	}

	if err := verifySignedAttrs(build([]attribute{digestAttr, typeAttr}), id.Cert, crypto.SHA256, content, oidData); err != nil {
		t.Fatalf("a correct signer info was rejected: %v", err)
	}
	if err := verifySignedAttrs(build([]attribute{digestAttr}), id.Cert, crypto.SHA256, content, oidData); err == nil {
		t.Error("signed attributes with no contentType were accepted")
	}
	if err := verifySignedAttrs(build([]attribute{digestAttr, wrongType}), id.Cert, crypto.SHA256, content, oidData); err == nil {
		t.Error("a contentType naming another content type was accepted")
	}
	if err := verifySignedAttrs(build([]attribute{typeAttr}), id.Cert, crypto.SHA256, content, oidData); err == nil {
		t.Error("signed attributes with no messageDigest were accepted")
	}
}

// TestExpiredCertificateIsNotRescuedByAnUnverifiedTimestamp is the whole
// point of verifying tokens. The certificate has expired. A token claiming
// a time when it was still valid rides along as an *unsigned* CMS
// attribute, so attaching one costs an attacker nothing and does not
// disturb the signature. Before tokens were checked, that was enough to
// make an expired certificate look valid.
func TestExpiredCertificateIsNotRescuedByAnUnverifiedTimestamp(t *testing.T) {
	// A certificate that died a month ago, and a token claiming a time
	// when it was still alive.
	within := time.Now().AddDate(0, 0, -45)
	id, ca := identityValid(t, time.Now().AddDate(0, 0, -90), time.Now().AddDate(0, 0, -30))
	signer, err := NewSigner(id, SignOptions{
		Hash:        crypto.SHA256,
		SigningTime: within,
		Timestamper: fixedTimestamper(forgedTokenClaiming(t, within)),
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w, err := xar.NewWriter(&out, xar.WriterOptions{ChecksumAlg: xar.ChecksumSHA256, TempDir: t.TempDir(), Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	w.AddFile("PackageInfo", xar.FileHeader{Mode: 0o644}, xar.EncodingGzip, strings.NewReader("<pkg-info/>"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := out.Bytes()
	x, err := xar.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	anchors := x509.NewCertPool()
	anchors.AddCert(ca)
	res, err := Verify(x, VerifyOptions{Anchors: anchors})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Timestamped {
		t.Fatal("the token was not seen at all")
	}
	if res.TimestampVerified {
		t.Error("a token signed by nobody was treated as verified")
	}
	if res.Valid() {
		t.Errorf("an expired certificate passed on the word of an unverified timestamp: %+v", res.Errors)
	}
}

// forgedTokenClaiming rewrites the real token's generation time. The
// authority's signature no longer covers it, which is the point: this is
// what an attacker can produce, and nothing more, because the token rides
// as an unsigned attribute that costs nothing to replace.
func forgedTokenClaiming(t *testing.T, at time.Time) []byte {
	t.Helper()
	token, _ := appleToken(t)
	genuine, err := TimestampTime(token)
	if err != nil {
		t.Fatal(err)
	}
	const layout = "20060102150405"
	forged := bytes.Replace(token, []byte(genuine.UTC().Format(layout)), []byte(at.UTC().Format(layout)), 1)
	claimed, err := TimestampTime(forged)
	if err != nil || !claimed.UTC().Truncate(time.Second).Equal(at.UTC().Truncate(time.Second)) {
		t.Skipf("could not move the claimed time (%v, %v)", claimed, err)
	}
	return forged
}

// fixedTimestamper hands back a token someone else made.
type fixedTimestamper []byte

func (f fixedTimestamper) Timestamp([]byte, crypto.Hash) ([]byte, error) { return f, nil }
