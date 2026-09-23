package pkgsign

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// rehashedSigner treats the TOC digest as the message passed to RSA signing.
type rehashedSigner struct{ *Signer }

func (s rehashedSigner) Sign(digest []byte) ([]byte, []byte, error) {
	_, cms, err := s.Signer.Sign(digest)
	if err != nil {
		return nil, nil, err
	}
	h := s.opts.Hash.New()
	h.Write(digest)
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.id.Key.(*rsa.PrivateKey), s.opts.Hash, h.Sum(nil))
	return sig, cms, err
}

func TestVerifyRSADigestForms(t *testing.T) {
	id, ca := testIdentity(t)
	anchors := x509.NewCertPool()
	anchors.AddCert(ca)
	opts := VerifyOptions{Anchors: anchors, TeamID: id.TeamID(), RequireDeveloperID: true}
	for _, alg := range []xar.ChecksumAlg{xar.ChecksumSHA1, xar.ChecksumSHA256} {
		for _, rehash := range []bool{false, true} {
			name := alg.String() + "/digest"
			if rehash {
				name += "-rehashed"
			}
			t.Run(name, func(t *testing.T) {
				hash := crypto.SHA1
				if alg == xar.ChecksumSHA256 {
					hash = crypto.SHA256
				}
				signer, err := NewSigner(id, SignOptions{Hash: hash})
				if err != nil {
					t.Fatal(err)
				}
				var archiveSigner xar.Signer = signer
				if rehash {
					archiveSigner = rehashedSigner{signer}
				}
				var out bytes.Buffer
				w, err := xar.NewWriter(&out, xar.WriterOptions{ChecksumAlg: alg, Signer: archiveSigner, TempDir: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				if err := w.AddFile("PackageInfo", xar.FileHeader{Mode: 0o644}, xar.EncodingGzip, strings.NewReader("<pkg-info/>")); err != nil {
					t.Fatal(err)
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				x, err := xar.Open(bytes.NewReader(out.Bytes()), int64(out.Len()))
				if err != nil {
					t.Fatal(err)
				}
				res, err := Verify(x, opts)
				if err != nil {
					t.Fatal(err)
				}
				if !res.Valid() || !res.DigestValid || !res.RSAValid || !res.CMSValid || !res.Trusted {
					t.Fatalf("verification failed: %v", res.Errors)
				}
				bad := bytes.Clone(out.Bytes())
				bad[x.HeapOffset()+x.TOC().Signature.Offset] ^= 1
				tampered, err := xar.Open(bytes.NewReader(bad), int64(len(bad)))
				if err != nil {
					t.Fatal(err)
				}
				res, err = Verify(tampered, opts)
				if err != nil {
					t.Fatal(err)
				}
				if res.Valid() || res.RSAValid || !res.CMSValid {
					t.Errorf("tampered RSA: valid %v rsa %v cms %v", res.Valid(), res.RSAValid, res.CMSValid)
				}
			})
		}
	}
}
