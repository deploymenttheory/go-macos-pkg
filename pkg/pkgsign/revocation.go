// Revocation checking: asking the certificate authority whether a signing
// certificate has been withdrawn since it was issued.
//
// Chain verification says a certificate was validly issued and has not
// expired. It says nothing about whether the issuer has since revoked it,
// which is what happens when a Developer ID key is lost or abused. Apple
// revokes such certificates, and pkgutil --check-signature notices, because
// Security.framework consults the responder. Nothing here did.
//
// This asks the Online Certificate Status Protocol responder the
// certificate itself names. It needs the network, so it happens only when
// it is asked for.
package pkgsign

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/crypto/ocsp"
)

// RevocationStatus is what a responder said about one certificate.
type RevocationStatus struct {
	// Checked reports that a responder answered. False with no error
	// means the certificate names no responder, which is not a failure:
	// plenty of certificates carry none.
	Checked bool
	// Revoked is the answer that matters.
	Revoked bool
	// RevokedAt is when, where the responder said.
	RevokedAt time.Time
	// Reason is the responder's code, where it gave one.
	Reason int
	// Responder is the URL that answered.
	Responder string
}

// ErrRevoked reports a certificate the issuer has withdrawn.
var ErrRevoked = errors.New("pkgsign: the signing certificate has been revoked")

// CheckRevocation asks the responder a certificate names whether it is
// still good.
//
// issuer must be the certificate that signed cert: a response is only
// meaningful against the issuer, and OCSP identifies the subject by hashes
// of the issuer's name and key rather than by name.
//
// A certificate naming no responder returns a status that was not checked
// and no error. Deciding what to make of that is the caller's: for a
// Developer ID certificate it is worth remarking on, since Apple's do carry
// one.
func CheckRevocation(ctx context.Context, client *http.Client, cert, issuer *x509.Certificate) (*RevocationStatus, error) {
	if cert == nil || issuer == nil {
		return nil, errors.New("pkgsign: revocation needs both the certificate and its issuer")
	}
	if len(cert.OCSPServer) == 0 {
		return &RevocationStatus{}, nil
	}
	request, err := ocsp.CreateRequest(cert, issuer, nil)
	if err != nil {
		return nil, fmt.Errorf("pkgsign: building the revocation request: %w", err)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	// A certificate may name several responders. The first that answers
	// settles it; the rest are tried only because one being unreachable
	// is not an answer.
	var lastErr error
	for _, responder := range cert.OCSPServer {
		status, err := askResponder(ctx, client, responder, request, cert, issuer)
		if err != nil {
			lastErr = err
			continue
		}
		status.Responder = responder
		return status, nil
	}
	return nil, fmt.Errorf("pkgsign: no revocation responder answered: %w", lastErr)
}

// askResponder posts one OCSP request and reads the answer.
func askResponder(ctx context.Context, client *http.Client, responder string, request []byte, cert, issuer *x509.Certificate) (*RevocationStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responder, bytes.NewReader(request))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/ocsp-request")
	req.Header.Set("Accept", "application/ocsp-response")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", responder, resp.Status)
	}
	// A response is small; a large body is a proxy or an error page.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	// ParseResponseForCert checks the responder's own signature against the
	// issuer, so a forged answer is not believed, and binds the answer to
	// cert's serial number. Passing a nil cert (as ParseResponse does) would
	// accept a Good status issued for any certificate the same CA signed,
	// including a throwaway leaf, so the revoked certificate would read as
	// good.
	parsed, err := ocsp.ParseResponseForCert(body, cert, issuer)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", responder, err)
	}
	// Refuse a stale answer. x/crypto/ocsp does not check the validity
	// window, and the request carries no nonce (its RequestOptions has no
	// field for one and it would not validate the echo), so without this a
	// captured pre-revocation Good response replays indefinitely over the
	// plaintext responder connection. A small skew tolerates clock drift.
	const skew = 5 * time.Minute
	now := time.Now()
	if !parsed.ThisUpdate.IsZero() && parsed.ThisUpdate.After(now.Add(skew)) {
		return nil, fmt.Errorf("%s: response is not yet valid (thisUpdate %s)", responder, parsed.ThisUpdate)
	}
	if !parsed.NextUpdate.IsZero() && parsed.NextUpdate.Before(now.Add(-skew)) {
		return nil, fmt.Errorf("%s: response expired (nextUpdate %s)", responder, parsed.NextUpdate)
	}
	status := &RevocationStatus{Checked: true}
	switch parsed.Status {
	case ocsp.Revoked:
		status.Revoked = true
		status.RevokedAt = parsed.RevokedAt
		status.Reason = parsed.RevocationReason
	case ocsp.Good:
	default:
		// Unknown: the responder does not recognize the certificate,
		// which is not the same as saying it is good.
		return nil, fmt.Errorf("%s does not know this certificate", responder)
	}
	return status, nil
}
