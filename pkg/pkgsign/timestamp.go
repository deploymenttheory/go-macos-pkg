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
	"crypto/x509/pkix"
	"encoding/asn1"
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

// TimestampTime extracts the generation time from a timestamp token.
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
