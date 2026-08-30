// RFC 3161 timestamping, as productsign --timestamp does against Apple's
// server: the CMS signature value is hashed, sent to the server, and the
// signed token that comes back is attached to the signature so it stays
// valid after the certificate expires.
package pkgsign

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"
)

// AppleTimestampURL is Apple's timestamp authority, which productsign
// uses by default.
const AppleTimestampURL = "http://timestamp.apple.com/ts01"

// Timestamper obtains RFC 3161 timestamp tokens.
type Timestamper interface {
	// Timestamp returns the DER ContentInfo token for a signature value.
	Timestamp(signature []byte, hash crypto.Hash) ([]byte, error)
}

// HTTPTimestamper is a Timestamper over HTTP.
type HTTPTimestamper struct {
	URL    string
	Client *http.Client
}

// NewHTTPTimestamper returns a timestamper for url (Apple's when empty).
func NewHTTPTimestamper(url string) *HTTPTimestamper {
	if url == "" {
		url = AppleTimestampURL
	}
	return &HTTPTimestamper{URL: url, Client: &http.Client{Timeout: 30 * time.Second}}
}

type messageImprint struct {
	HashAlgorithm pkix.AlgorithmIdentifier
	HashedMessage []byte
}

type timeStampReq struct {
	Version        int
	MessageImprint messageImprint
	ReqPolicy      asn1.ObjectIdentifier `asn1:"optional"`
	Nonce          *big.Int              `asn1:"optional"`
	CertReq        bool                  `asn1:"optional,default:false"`
}

type pkiStatusInfo struct {
	Status       int
	StatusString asn1.RawValue  `asn1:"optional"`
	FailInfo     asn1.BitString `asn1:"optional"`
}

type timeStampResp struct {
	Status         pkiStatusInfo
	TimeStampToken asn1.RawValue `asn1:"optional"`
}

// Timestamp requests a token for signature.
func (t *HTTPTimestamper) Timestamp(signature []byte, hash crypto.Hash) ([]byte, error) {
	oid, err := digestAlgorithm(hash)
	if err != nil {
		return nil, err
	}
	h := hash.New()
	h.Write(signature)
	nonce, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		return nil, err
	}
	req, err := asn1.Marshal(timeStampReq{
		Version:        1,
		MessageImprint: messageImprint{HashAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oid, Parameters: asn1NullBytes}, HashedMessage: h.Sum(nil)},
		Nonce:          nonce,
		CertReq:        true,
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(req))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/timestamp-query")
	httpReq.Header.Set("Accept", "application/timestamp-reply")
	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("timestamp server %s: %w", t.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("timestamp server %s returned %s", t.URL, resp.Status)
	}
	var tsr timeStampResp
	if _, err := asn1.Unmarshal(body, &tsr); err != nil {
		return nil, fmt.Errorf("timestamp server %s: malformed response: %w", t.URL, err)
	}
	// 0 granted, 1 granted with modifications; anything else is a refusal.
	if tsr.Status.Status > 1 {
		return nil, fmt.Errorf("timestamp server %s refused the request (status %d)", t.URL, tsr.Status.Status)
	}
	if len(tsr.TimeStampToken.FullBytes) == 0 {
		return nil, fmt.Errorf("timestamp server %s returned no token", t.URL)
	}
	return tsr.TimeStampToken.FullBytes, nil
}

// tstInfo is the part of a timestamp token we read back: when it was made.
type tstInfo struct {
	Version        int
	Policy         asn1.ObjectIdentifier
	MessageImprint messageImprint
	SerialNumber   *big.Int
	GenTime        time.Time `asn1:"generalized"`
}

// Timestamp verification.
//
// A token is an RFC 3161 TimeStampToken: a CMS SignedData whose content is
// a TSTInfo naming the time and the message it attests to. Reading GenTime
// out of it proves nothing on its own, because the token travels as an
// *unsigned* CMS attribute and so is not covered by the signature it
// accompanies: anyone can replace it without disturbing the package's own
// signature. Three things have to hold before its time may be believed.
var (
	// ErrTimestampInvalid reports a token that is cryptographically wrong:
	// the timestamp authority's own signature does not verify, or the
	// token attests to a different signature. Either means tampering.
	ErrTimestampInvalid = errors.New("pkgsign: timestamp is invalid")
	// ErrTimestampUnverified reports a token that could not be checked at
	// all: it does not parse, or its authority chains to no trusted root.
	// The token may be perfectly good; we simply cannot say so, so its
	// time must not be used.
	ErrTimestampUnverified = errors.New("pkgsign: timestamp could not be verified")
)

// oidTSTInfo is the eContentType of a TimeStampToken's content.
var oidTSTInfo = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 4}

// VerifyTimestamp checks that a token was issued by a trusted timestamp
// authority over signature, and returns the time it attests to.
//
// The authority's certificate is judged at the time the token claims,
// not at now. That is not circular: the authority's signature covers
// GenTime, so a forged time cannot survive the previous step. It is also
// necessary, because Apple rotates its timestamp signer every few weeks
// (the one seen while writing this was valid for six), so judging at now
// would reject every token more than a month old.
func VerifyTimestamp(token, signature []byte, roots *x509.CertPool) (time.Time, error) {
	info, sd, err := ParseCMS(token)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", ErrTimestampUnverified, err)
	}
	if info.Signer == nil {
		return time.Time{}, fmt.Errorf("%w: the authority's certificate is not in the token", ErrTimestampUnverified)
	}
	if !sd.ContentInfo.ContentType.Equal(oidTSTInfo) {
		return time.Time{}, fmt.Errorf("%w: content type %v is not TSTInfo", ErrTimestampUnverified, sd.ContentInfo.ContentType)
	}
	var inner []byte
	if _, err := asn1.Unmarshal(sd.ContentInfo.Content.Bytes, &inner); err != nil {
		return time.Time{}, fmt.Errorf("%w: TSTInfo content: %v", ErrTimestampUnverified, err)
	}
	var tst tstInfo
	if _, err := asn1.Unmarshal(inner, &tst); err != nil {
		return time.Time{}, fmt.Errorf("%w: TSTInfo: %v", ErrTimestampUnverified, err)
	}

	// The authority signed the TSTInfo, which carries GenTime.
	if err := verifySignedAttrs(sd.SignerInfos[0], info.Signer, info.Hash, inner, oidTSTInfo); err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", ErrTimestampInvalid, err)
	}

	// The TSTInfo is about this signature and no other, so a token cannot
	// be lifted from one package and dropped onto another.
	imprintHash, err := hashFromOID(tst.MessageImprint.HashAlgorithm.Algorithm)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: message imprint: %v", ErrTimestampUnverified, err)
	}
	h := imprintHash.New()
	h.Write(signature)
	if subtle.ConstantTimeCompare(h.Sum(nil), tst.MessageImprint.HashedMessage) != 1 {
		return time.Time{}, fmt.Errorf("%w: it attests to a different signature", ErrTimestampInvalid)
	}

	// The authority is one we trust, as of the time it claims.
	intermediates := x509.NewCertPool()
	for _, c := range info.Certificates {
		if !c.Equal(info.Signer) {
			intermediates.AddCert(c)
		}
	}
	if _, err := info.Signer.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   tst.GenTime,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageTimeStamping},
	}); err != nil {
		return time.Time{}, fmt.Errorf("%w: the authority is not trusted: %v", ErrTimestampUnverified, err)
	}
	return tst.GenTime, nil
}

// TimestampTime extracts the generation time from a timestamp token
// without checking anything. Callers deciding whether to believe the time
// want VerifyTimestamp instead.
func TimestampTime(token []byte) (time.Time, error) {
	var ci contentInfo
	if _, err := asn1.Unmarshal(token, &ci); err != nil {
		return time.Time{}, fmt.Errorf("pkgsign: timestamp token: %w", err)
	}
	var sd signedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return time.Time{}, fmt.Errorf("pkgsign: timestamp token: %w", err)
	}
	var inner []byte
	if _, err := asn1.Unmarshal(sd.ContentInfo.Content.Bytes, &inner); err != nil {
		return time.Time{}, fmt.Errorf("pkgsign: timestamp token content: %w", err)
	}
	var info tstInfo
	if _, err := asn1.Unmarshal(inner, &info); err != nil {
		return time.Time{}, fmt.Errorf("pkgsign: TSTInfo: %w", err)
	}
	return info.GenTime, nil
}
