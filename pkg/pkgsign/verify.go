// Verifying signed packages: the digest, both signatures, and the
// certificate chain, reported in detail rather than as a single yes.
package pkgsign

import (
	"crypto"
	"crypto/rsa"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// VerifyOptions configures Verify.
type VerifyOptions struct {
	// Anchors are the trusted roots; nil means Apple's roots.
	Anchors *x509.CertPool
	// AllowUntrusted reports an unknown chain as such without failing.
	AllowUntrusted bool
	// TeamID, when set, must match the signer's organisational unit.
	TeamID string
	// Now is the time to evaluate certificate validity at; zero means
	// the signature's timestamp when present, else the current time.
	Now time.Time
	// RequireDeveloperID insists the leaf carries Apple's Developer ID
	// Installer marker, as the Installer does for distribution.
	RequireDeveloperID bool
}

// Result is what Verify found.
type Result struct {
	Signed      bool
	Digest      string // algorithm name
	DigestValid bool   // stored TOC digest matches the TOC

	RSAPresent bool
	RSAValid   bool
	CMSPresent bool
	CMSValid   bool

	Certificates []*x509.Certificate // leaf first, as embedded
	Signer       *x509.Certificate
	TeamID       string
	DeveloperID  bool // leaf has the Developer ID Installer marker

	SigningTime time.Time
	Timestamped bool
	Timestamp   time.Time

	Trusted    bool
	TrustError string
	Chain      []*x509.Certificate // verified chain to an anchor, when trusted

	// Errors lists everything that failed, in the order found.
	Errors []string
}

// Valid reports whether the signature is sound and trusted (or trust was
// waived).
func (r *Result) Valid() bool { return r.Signed && len(r.Errors) == 0 }

func (r *Result) fail(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

// ErrUnsigned reports an archive with no signature elements.
var ErrUnsigned = errors.New("pkgsign: the package is not signed")

// Verify checks the signatures of an opened archive.
func Verify(x *xar.Reader, o VerifyOptions) (*Result, error) {
	r := &Result{Digest: x.Header().ChecksumAlg.String()}
	toc := x.TOC()
	if toc.Signature == nil && toc.XSignature == nil {
		r.fail("the package is not signed")
		return r, ErrUnsigned
	}
	r.Signed = true

	digest, err := x.ComputeTOCDigest()
	if err != nil {
		r.fail("%v", err)
		return r, nil
	}
	if stored := x.StoredTOCDigest(); stored != nil && subtle.ConstantTimeCompare(stored, digest) == 1 {
		r.DigestValid = true
	} else {
		r.fail("the stored table-of-contents digest does not match the table of contents")
	}
	hash, err := hashFor(x.Header().ChecksumAlg)
	if err != nil {
		r.fail("%v", err)
		return r, nil
	}

	// Certificates from whichever element carries them.
	var chainB64 []string
	for _, el := range []*xar.Signature{toc.Signature, toc.XSignature} {
		if el != nil && el.KeyInfo != nil && len(el.KeyInfo.X509Data.Certificates) > 0 {
			chainB64 = el.KeyInfo.X509Data.Certificates
			break
		}
	}
	for _, b64 := range chainB64 {
		der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(b64), ""))
		if err != nil {
			r.fail("malformed certificate in the table of contents: %v", err)
			continue
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			r.fail("unparseable certificate in the table of contents: %v", err)
			continue
		}
		r.Certificates = append(r.Certificates, c)
	}
	if len(r.Certificates) == 0 {
		r.fail("no certificates in the table of contents")
		return r, nil
	}
	r.Signer = r.Certificates[0]
	r.TeamID = TeamIDOf(r.Signer)
	r.DeveloperID = IsDeveloperIDInstaller(r.Signer)

	// RSA: PKCS#1 v1.5 over the digest with the leaf's key.
	if el := toc.Signature; el != nil {
		r.RSAPresent = true
		sig, err := readHeap(x, el.Offset, el.Size)
		if err != nil {
			r.fail("RSA signature: %v", err)
		} else if pub, ok := r.Signer.PublicKey.(*rsa.PublicKey); !ok {
			r.fail("RSA signature: the signer's key is not RSA")
		} else if err := rsa.VerifyPKCS1v15(pub, hash, digest, sig[:min(len(sig), pub.Size())]); err != nil {
			r.fail("RSA signature does not verify")
		} else {
			r.RSAValid = true
		}
	}

	// CMS: detached SignedData over the digest.
	if el := toc.XSignature; el != nil {
		r.CMSPresent = true
		blob, err := readHeap(x, el.Offset, el.Size)
		if err != nil {
			r.fail("CMS signature: %v", err)
		} else {
			info, err := VerifyCMS(blob, digest)
			if info != nil {
				r.SigningTime = info.SigningTime
				if info.TimestampToken != nil {
					r.Timestamped = true
					if t, err := TimestampTime(info.TimestampToken); err == nil {
						r.Timestamp = t
					}
				}
				if info.Signer != nil && !info.Signer.Equal(r.Signer) {
					r.fail("the CMS signer differs from the certificate in the table of contents")
				}
			}
			if err != nil {
				r.fail("%v", err)
			} else {
				r.CMSValid = true
			}
		}
	}

	// Trust.
	if o.TeamID != "" && !strings.EqualFold(o.TeamID, r.TeamID) {
		r.fail("signed by team %q, not %q", r.TeamID, o.TeamID)
	}
	if o.RequireDeveloperID && !r.DeveloperID {
		r.fail("the certificate is not a Developer ID Installer certificate")
	}
	anchors := o.Anchors
	if anchors == nil {
		anchors = AppleRoots()
	}
	// Apple marks its own extensions critical, and Go refuses to build a
	// chain through a critical extension it does not understand. The
	// markers are exactly that: markers. Accept Apple's, and only
	// Apple's, before asking for the chain.
	for _, c := range r.Certificates {
		allowAppleCriticalExtensions(c)
	}
	intermediates := x509.NewCertPool()
	for _, c := range r.Certificates[1:] {
		intermediates.AddCert(c)
	}
	at := o.Now
	if at.IsZero() {
		at = time.Now()
		// A timestamped signature stays valid after its certificate
		// expires: evaluate at the time the timestamp attests to.
		if r.Timestamped && !r.Timestamp.IsZero() {
			at = r.Timestamp
		}
	}
	chains, err := r.Signer.Verify(x509.VerifyOptions{
		Roots:         anchors,
		Intermediates: intermediates,
		CurrentTime:   at,
		// Apple's leaf carries code-signing style usages the standard
		// library would otherwise refuse for "any" purpose.
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil {
		r.TrustError = err.Error()
		if !o.AllowUntrusted {
			r.fail("certificate chain is not trusted: %v", err)
		}
	} else {
		r.Trusted = true
		r.Chain = chains[0]
	}
	return r, nil
}

// appleArc is the prefix of Apple's private extension OIDs
// (1.2.840.113635.100).
var appleArc = []int{1, 2, 840, 113635, 100}

// allowAppleCriticalExtensions removes Apple's marker OIDs from the
// certificate's list of unhandled critical extensions.
func allowAppleCriticalExtensions(c *x509.Certificate) {
	kept := c.UnhandledCriticalExtensions[:0]
	for _, oid := range c.UnhandledCriticalExtensions {
		if !hasPrefix(oid, appleArc) {
			kept = append(kept, oid)
		}
	}
	c.UnhandledCriticalExtensions = kept
}

func hasPrefix(oid, prefix []int) bool {
	if len(oid) < len(prefix) {
		return false
	}
	for i := range prefix {
		if oid[i] != prefix[i] {
			return false
		}
	}
	return true
}

func hashFor(alg xar.ChecksumAlg) (crypto.Hash, error) {
	switch alg {
	case xar.ChecksumSHA1:
		return crypto.SHA1, nil
	case xar.ChecksumSHA256:
		return crypto.SHA256, nil
	case xar.ChecksumSHA512:
		return crypto.SHA512, nil
	}
	return 0, fmt.Errorf("pkgsign: cannot verify a %s table-of-contents digest", alg)
}

// readHeap reads a heap range as stored. Padding is not trimmed: a BER
// CMS blob ends in zero bytes that are part of it, and the RSA signature
// is exactly its reserved size.
func readHeap(x *xar.Reader, offset, size int64) ([]byte, error) {
	if size <= 0 || size > 1<<20 {
		return nil, fmt.Errorf("implausible size %d", size)
	}
	sec, err := x.HeapSection(offset, size)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	n, err := sec.ReadAt(buf, 0)
	if err != nil && n != len(buf) {
		return nil, err
	}
	return buf, nil
}
