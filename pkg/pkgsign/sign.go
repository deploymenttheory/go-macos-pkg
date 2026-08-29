// Signing: the xar.Signer that reserves space for, and then produces, the
// RSA and CMS signatures productsign writes.
package pkgsign

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// SignOptions configures a Signer.
type SignOptions struct {
	// Hash is the digest the signatures are made with. It must match the
	// archive's table-of-contents digest, which the writer chooses; the
	// writer asks for it through xar.Signer, so leave it zero and the
	// archive's algorithm is used.
	Hash crypto.Hash
	// SigningTime is recorded in the CMS signature; zero means now. Set it
	// with the archive's epoch for reproducible output.
	SigningTime time.Time
	// Timestamper, when set, obtains an RFC 3161 timestamp over the CMS
	// signature and embeds it. Nil leaves the signature untimestamped.
	Timestamper Timestamper
}

// cmsSlack is added to the measured CMS size to absorb the variation
// between the dry run and the real signature (a timestamp token's size
// depends on the server's certificate chain).
const cmsSlack = 512

// Signer implements xar.Signer for an Identity.
type Signer struct {
	id      *Identity
	opts    SignOptions
	rsaSize int
	cmsSize int
	keyInfo *xar.KeyInfo
}

// NewSigner prepares a signer, measuring how much heap the signatures
// will need so the archive writer can reserve it before the table of
// contents is final.
func NewSigner(id *Identity, o SignOptions) (*Signer, error) {
	if err := id.check(); err != nil {
		return nil, err
	}
	if o.Hash == 0 {
		o.Hash = crypto.SHA256
	}
	key := id.Key.(*rsa.PrivateKey)
	s := &Signer{id: id, opts: o, rsaSize: key.Size()}

	// A dry run over a digest of the right length measures the CMS blob,
	// timestamp included, since the token's size does not depend on what
	// is being timestamped.
	dummy := make([]byte, o.Hash.Size())
	if _, err := rand.Read(dummy); err != nil {
		return nil, err
	}
	probe, err := s.cms(dummy)
	if err != nil {
		return nil, err
	}
	s.cmsSize = len(probe) + cmsSlack

	s.keyInfo = &xar.KeyInfo{XMLNS: xar.XMLDSigNamespace}
	s.keyInfo.X509Data.Certificates = append(s.keyInfo.X509Data.Certificates, wrapBase64(id.Cert.Raw))
	for _, c := range id.Chain {
		s.keyInfo.X509Data.Certificates = append(s.keyInfo.X509Data.Certificates, wrapBase64(c.Raw))
	}
	return s, nil
}

// wrapBase64 encodes DER as base64 in 72-column lines, the way
// productsign writes X509Certificate elements.
func wrapBase64(der []byte) string {
	s := base64.StdEncoding.EncodeToString(der)
	var b strings.Builder
	for len(s) > 72 {
		b.WriteString(s[:72])
		b.WriteByte('\n')
		s = s[72:]
	}
	b.WriteString(s)
	return b.String()
}

// Elements returns the TOC signature elements with their reserved sizes.
func (s *Signer) Elements() (*xar.Signature, *xar.Signature) {
	return &xar.Signature{Style: xar.SignatureRSA, Size: int64(s.rsaSize), KeyInfo: s.keyInfo},
		&xar.Signature{Style: xar.SignatureCMS, Size: int64(s.cmsSize), KeyInfo: s.keyInfo}
}

// Sign produces both signatures over the table-of-contents digest.
func (s *Signer) Sign(digest []byte) ([]byte, []byte, error) {
	if len(digest) != s.opts.Hash.Size() {
		return nil, nil, fmt.Errorf("pkgsign: digest is %d bytes, signer expects %d (%v)", len(digest), s.opts.Hash.Size(), s.opts.Hash)
	}
	key := s.id.Key.(*rsa.PrivateKey)
	// The classic signature: PKCS#1 v1.5 over the digest itself, as the
	// xar signing callback has always done it.
	rsaSig, err := rsa.SignPKCS1v15(rand.Reader, key, s.opts.Hash, digest)
	if err != nil {
		return nil, nil, fmt.Errorf("pkgsign: RSA signature: %w", err)
	}
	cmsSig, err := s.cms(digest)
	if err != nil {
		return nil, nil, err
	}
	if len(cmsSig) > s.cmsSize {
		return nil, nil, fmt.Errorf("pkgsign: CMS signature is %d bytes, %d reserved", len(cmsSig), s.cmsSize)
	}
	return rsaSig, cmsSig, nil
}

// cms builds the detached SignedData over digest, timestamped if asked.
func (s *Signer) cms(digest []byte) ([]byte, error) {
	o := CMSOptions{Hash: s.opts.Hash, SigningTime: s.opts.SigningTime}
	if s.opts.Timestamper == nil {
		return SignCMS(digest, s.id, o)
	}
	// A timestamp is over the signature value, which only exists once
	// signed; sign once to get it, fetch the token, then sign again with
	// the token attached. RSA PKCS#1 v1.5 is deterministic, so the second
	// signature equals the first and the token stays valid.
	first, err := SignCMS(digest, s.id, o)
	if err != nil {
		return nil, err
	}
	_, sd, err := ParseCMS(first)
	if err != nil {
		return nil, err
	}
	token, err := s.opts.Timestamper.Timestamp(sd.SignerInfos[0].Signature, s.opts.Hash)
	if err != nil {
		return nil, fmt.Errorf("pkgsign: timestamp: %w", err)
	}
	o.TimestampToken = token
	return SignCMS(digest, s.id, o)
}

// SigningHash reports the digest the signer will use.
func (s *Signer) SigningHash() crypto.Hash { return s.opts.Hash }

// SignerName returns the certificate's common name and team identifier.
func (s *Signer) SignerName() (string, string) { return CommonName(s.id.Cert), s.id.TeamID() }
