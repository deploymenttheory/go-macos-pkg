package pkgsign

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// testIdentity makes a CA and a Developer-ID-shaped leaf in memory.
func testIdentity(t *testing.T) (*Identity, *x509.Certificate) {
	t.Helper()
	caKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Test Developer ID Certification Authority", OrganizationalUnit: []string{"Test CA"}},
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		ExtraExtensions: []pkix.Extension{{Id: OIDDeveloperIDCA, Value: []byte{5, 0}}},
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)

	leafKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "Developer ID Installer: Test (TEAM123456)", OrganizationalUnit: []string{"TEAM123456"}},
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		ExtraExtensions: []pkix.Extension{{Id: OIDDeveloperIDInstaller, Value: []byte{5, 0}}},
	}
	leafDER, _ := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &leafKey.PublicKey, caKey)
	leaf, _ := x509.ParseCertificate(leafDER)
	id := &Identity{Cert: leaf, Key: leafKey, Chain: []*x509.Certificate{ca}}
	id.orderChain()
	if err := id.check(); err != nil {
		t.Fatal(err)
	}
	return id, ca
}

func TestCMSRoundTrip(t *testing.T) {
	id, _ := testIdentity(t)
	content := sha256.New().Sum([]byte("digest"))
	when := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	der, err := SignCMS(content, id, CMSOptions{Hash: crypto.SHA256, SigningTime: when})
	if err != nil {
		t.Fatal(err)
	}
	info, err := VerifyCMS(der, content)
	if err != nil {
		t.Fatal(err)
	}
	if info.Signer == nil || info.Signer.Subject.CommonName != id.Cert.Subject.CommonName {
		t.Errorf("signer = %v", info.Signer)
	}
	if !info.SigningTime.Equal(when) {
		t.Errorf("signingTime = %v", info.SigningTime)
	}
	if len(info.Certificates) != 2 {
		t.Errorf("certificates embedded = %d, want leaf and CA", len(info.Certificates))
	}
	// Deterministic for a fixed signing time (RSA PKCS#1 v1.5).
	der2, _ := SignCMS(content, id, CMSOptions{Hash: crypto.SHA256, SigningTime: when})
	if !bytes.Equal(der, der2) {
		t.Error("CMS output is not deterministic")
	}
	// Wrong content, tampered bytes.
	if _, err := VerifyCMS(der, []byte("other")); err == nil {
		t.Error("wrong content verified")
	}
	bad := append([]byte(nil), der...)
	bad[len(bad)-10] ^= 0xff
	if _, err := VerifyCMS(bad, content); err == nil {
		t.Error("tampered signature verified")
	}
	// Trailing zero padding, as stored in a xar heap, is tolerated.
	padded := append(append([]byte(nil), der...), make([]byte, 100)...)
	if _, err := VerifyCMS(padded, content); err != nil {
		t.Errorf("padded CMS: %v", err)
	}
	// A timestamp token is carried through.
	token, _ := asn1.Marshal(contentInfo{ContentType: oidSignedData, Content: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: []byte{0x30, 0}}})
	withTS, err := SignCMS(content, id, CMSOptions{Hash: crypto.SHA256, SigningTime: when, TimestampToken: token})
	if err != nil {
		t.Fatal(err)
	}
	info, err = VerifyCMS(withTS, content)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(info.TimestampToken, token) {
		t.Error("timestamp token not recovered")
	}
}

// fakeTimestamper hands back a fixed token and records what it was asked
// to stamp.
type fakeTimestamper struct{ got []byte }

func (f *fakeTimestamper) Timestamp(sig []byte, _ crypto.Hash) ([]byte, error) {
	f.got = sig
	return []byte{0x30, 0x03, 0x02, 0x01, 0x01}, nil
}

func TestSignAndVerifyArchive(t *testing.T) {
	id, ca := testIdentity(t)
	ts := &fakeTimestamper{}
	signer, err := NewSigner(id, SignOptions{Hash: crypto.SHA256, SigningTime: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC), Timestamper: ts})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w, err := xar.NewWriter(&out, xar.WriterOptions{ChecksumAlg: xar.ChecksumSHA256, TempDir: t.TempDir(), Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	w.AddFile("PackageInfo", xar.FileHeader{Mode: 0o644}, xar.EncodingGzip, strings.NewReader("<pkg-info/>"))
	w.AddFile("Payload", xar.FileHeader{Mode: 0o644}, xar.EncodingNone, bytes.NewReader(bytes.Repeat([]byte("p"), 5000)))
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

	res, err := Verify(x, VerifyOptions{Anchors: anchors, TeamID: "TEAM123456", RequireDeveloperID: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid() {
		t.Fatalf("not valid: %v", res.Errors)
	}
	if !res.DigestValid || !res.RSAValid || !res.CMSValid || !res.Trusted || !res.DeveloperID || res.TeamID != "TEAM123456" {
		t.Errorf("result: %+v", res)
	}
	if !res.Timestamped {
		t.Error("timestamp not reported")
	}
	if len(res.Certificates) != 2 {
		t.Errorf("TOC carries %d certificates, want leaf + CA", len(res.Certificates))
	}
	if len(ts.got) == 0 {
		t.Error("timestamper was not asked to stamp the signature")
	}
	// Entries still verify after the signature shifted them.
	for _, f := range x.Files() {
		if err := x.Verify(f); err != nil {
			t.Errorf("%s: %v", f.Path(), err)
		}
	}

	// Wrong team, untrusted anchors, tampering.
	res, _ = Verify(x, VerifyOptions{Anchors: anchors, TeamID: "OTHER"})
	if res.Valid() {
		t.Error("wrong team accepted")
	}
	res, _ = Verify(x, VerifyOptions{})
	if res.Valid() || res.Trusted {
		t.Error("chain to a private CA was trusted against Apple's roots")
	}
	res, _ = Verify(x, VerifyOptions{AllowUntrusted: true})
	if !res.Valid() || res.Trusted {
		t.Errorf("--allow-untrusted: valid %v trusted %v errors %v", res.Valid(), res.Trusted, res.Errors)
	}
	tampered := append([]byte(nil), data...)
	tampered[x.HeapOffset()+5] ^= 0xff // inside the stored digest
	tx, _ := xar.Open(bytes.NewReader(tampered), int64(len(tampered)))
	res, _ = Verify(tx, VerifyOptions{Anchors: anchors})
	if res.Valid() || res.DigestValid {
		t.Error("tampered digest accepted")
	}
	tampered = append([]byte(nil), data...)
	tampered[x.HeapOffset()+40] ^= 0xff // inside the RSA signature
	tx, _ = xar.Open(bytes.NewReader(tampered), int64(len(tampered)))
	res, _ = Verify(tx, VerifyOptions{Anchors: anchors})
	if res.Valid() || res.RSAValid || !res.CMSValid {
		t.Errorf("tampered RSA: valid %v rsa %v cms %v", res.Valid(), res.RSAValid, res.CMSValid)
	}

	// Re-sign through Rewrite: same content, new identity, and the
	// unsigned rewrite strips signatures.
	id2, ca2 := testIdentity(t)
	signer2, _ := NewSigner(id2, SignOptions{Hash: crypto.SHA1})
	var resigned bytes.Buffer
	if err := xar.Rewrite(x, &resigned, xar.RewriteOptions{ChecksumAlg: xar.ChecksumSHA1, Signer: signer2}); err != nil {
		t.Fatal(err)
	}
	rx, err := xar.Open(bytes.NewReader(resigned.Bytes()), int64(resigned.Len()))
	if err != nil {
		t.Fatal(err)
	}
	anchors2 := x509.NewCertPool()
	anchors2.AddCert(ca2)
	res, _ = Verify(rx, VerifyOptions{Anchors: anchors2})
	if !res.Valid() || res.Digest != "sha1" {
		t.Errorf("re-signed: valid %v digest %s errors %v", res.Valid(), res.Digest, res.Errors)
	}
	if rx.Lookup("Payload") == nil {
		t.Fatalf("re-signed archive lost Payload; entries: %v", rx.Files())
	}
	rc, err := rx.Open(rx.Lookup("Payload"))
	if err != nil {
		t.Fatalf("open Payload after re-sign: %v", err)
	}
	payload, _ := readAll(rc)
	if len(payload) != 5000 || payload[0] != 'p' {
		t.Errorf("payload after re-sign: %d bytes", len(payload))
	}
	var stripped bytes.Buffer
	if err := xar.Rewrite(rx, &stripped, xar.RewriteOptions{}); err != nil {
		t.Fatal(err)
	}
	sx, _ := xar.Open(bytes.NewReader(stripped.Bytes()), int64(stripped.Len()))
	if _, err := Verify(sx, VerifyOptions{}); err != ErrUnsigned {
		t.Errorf("stripped archive: %v", err)
	}
	if !sx.TOCDigestValid() {
		t.Error("stripped archive digest invalid")
	}
}

func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return out, nil
			}
			return out, err
		}
	}
}

func TestIdentityLoading(t *testing.T) {
	keys := filepath.Join("..", "..", "testdata", "cli", "keys")
	p12, err := os.ReadFile(filepath.Join(keys, "fixture-installer.p12"))
	if err != nil {
		t.Skip("fixture keys not committed")
	}
	id, err := LoadP12(p12, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if id.TeamID() != "FIXTURE01" || !IsDeveloperIDInstaller(id.Cert) {
		t.Errorf("p12 identity: team %q devid %v", id.TeamID(), IsDeveloperIDInstaller(id.Cert))
	}
	if len(id.Chain) != 1 || id.Chain[0].Subject.CommonName != "Fixture Developer ID Certification Authority" {
		t.Errorf("chain = %d certificates", len(id.Chain))
	}
	if _, err := LoadP12(p12, "wrong"); err == nil || !strings.Contains(err.Error(), "password") {
		t.Errorf("wrong password: %v", err)
	}
	pemID, err := LoadPEMFiles(filepath.Join(keys, "fixture-installer.pem"), filepath.Join(keys, "fixture-installer.key"), filepath.Join(keys, "fixture-ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if !pemID.Cert.Equal(id.Cert) {
		t.Error("PEM and P12 identities differ")
	}
	// Mismatched key.
	if _, err := LoadPEMFiles(filepath.Join(keys, "fixture-installer.pem"), filepath.Join(keys, "fixture-ca.key"), ""); err != ErrKeyMismatch {
		t.Errorf("mismatched key: %v", err)
	}
}

func TestAppleRoots(t *testing.T) {
	roots := AppleRootCertificates()
	if len(roots) == 0 {
		t.Fatal("no Apple roots embedded")
	}
	found := false
	for _, r := range roots {
		if r.Subject.CommonName == "Apple Root CA" {
			found = true
		}
	}
	if !found {
		t.Error("the 2006 Apple Root CA, which Developer ID chains to, is missing")
	}
}
