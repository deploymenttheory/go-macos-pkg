package pkgsign

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/ocsp"
)

// makeIssuer builds a self-signed authority to issue and to answer for.
func makeIssuer(t *testing.T) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Issuer"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// makeLeaf issues a certificate naming a responder, or naming none where
// responder is empty.
func makeLeaf(t *testing.T, issuer *x509.Certificate, issuerKey *rsa.PrivateKey, responder string) *x509.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "Test Leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	if responder != "" {
		template.OCSPServer = []string{responder}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, issuer, &key.PublicKey, issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// respondWith serves one OCSP status for whatever it is asked about.
func respondWith(t *testing.T, status int, issuer *x509.Certificate, issuerKey *rsa.PrivateKey) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		req, err := ocsp.ParseRequest(body)
		if err != nil {
			t.Errorf("the request did not parse: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		template := ocsp.Response{
			Status:       status,
			SerialNumber: req.SerialNumber,
			ThisUpdate:   time.Now().Add(-time.Minute),
			NextUpdate:   time.Now().Add(time.Hour),
		}
		if status == ocsp.Revoked {
			template.RevokedAt = time.Now().Add(-2 * time.Hour)
			template.RevocationReason = ocsp.KeyCompromise
		}
		resp, err := ocsp.CreateResponse(issuer, issuer, template, issuerKey)
		if err != nil {
			t.Errorf("building the response: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/ocsp-response")
		_, _ = w.Write(resp)
	}))
}

// TestCheckRevocationReadsTheAnswer covers both answers that matter.
//
// Chain verification cannot tell a live certificate from a withdrawn one,
// which is the whole reason for asking, so both directions are pinned.
func TestCheckRevocationReadsTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ocspStatus  int
		wantRevoked bool
	}{
		{"good", ocsp.Good, false},
		{"revoked", ocsp.Revoked, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The issuer comes first, then the responder that answers
			// for it, and only then the leaf, since the leaf has to
			// name the responder's address.
			issuer, key := makeIssuer(t)
			srv := respondWith(t, tc.ocspStatus, issuer, key)
			defer srv.Close()
			leaf := makeLeaf(t, issuer, key, srv.URL)

			status, err := CheckRevocation(context.Background(), srv.Client(), leaf, issuer)
			if err != nil {
				t.Fatal(err)
			}
			if !status.Checked {
				t.Fatal("the responder was not asked")
			}
			if status.Revoked != tc.wantRevoked {
				t.Errorf("revoked = %v, want %v", status.Revoked, tc.wantRevoked)
			}
			if tc.wantRevoked && status.RevokedAt.IsZero() {
				t.Error("a revoked certificate should say when")
			}
		})
	}
}

// TestCheckRevocationWithoutAResponder pins that a certificate naming no
// responder is not an error. Plenty carry none, and treating silence as a
// failure would refuse packages that are perfectly good.
func TestCheckRevocationWithoutAResponder(t *testing.T) {
	issuer, key := makeIssuer(t)
	leaf := makeLeaf(t, issuer, key, "")
	status, err := CheckRevocation(context.Background(), nil, leaf, issuer)
	if err != nil {
		t.Fatal(err)
	}
	if status.Checked || status.Revoked {
		t.Errorf("a certificate with no responder should be unchecked, got %+v", status)
	}
}

// TestCheckRevocationNeedsBoth pins that a check without the issuer is
// refused rather than guessed at: OCSP identifies a certificate by hashes
// of its issuer, so there is nothing to ask without one.
func TestCheckRevocationNeedsBoth(t *testing.T) {
	issuer, key := makeIssuer(t)
	leaf := makeLeaf(t, issuer, key, "")
	if _, err := CheckRevocation(context.Background(), nil, leaf, nil); err == nil {
		t.Error("a check with no issuer should fail")
	}
	if _, err := CheckRevocation(context.Background(), nil, nil, issuer); err == nil {
		t.Error("a check with no certificate should fail")
	}
}

// respondForSerial serves a Good status for a fixed serial, ignoring the
// serial the request asked about: a substituted response a same-CA responder
// (or a network attacker) could return to make a revoked certificate read as
// good if the answer were not bound to the certificate under test.
func respondForSerial(t *testing.T, serial *big.Int, issuer *x509.Certificate, issuerKey *rsa.PrivateKey) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		template := ocsp.Response{
			Status:       ocsp.Good,
			SerialNumber: serial,
			ThisUpdate:   time.Now().Add(-time.Minute),
			NextUpdate:   time.Now().Add(time.Hour),
		}
		resp, err := ocsp.CreateResponse(issuer, issuer, template, issuerKey)
		if err != nil {
			t.Errorf("building the response: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/ocsp-response")
		_, _ = w.Write(resp)
	}))
}

// TestCheckRevocationRejectsMismatchedSerial pins F8: an OCSP answer for a
// different certificate of the same CA must not settle this certificate.
func TestCheckRevocationRejectsMismatchedSerial(t *testing.T) {
	issuer, key := makeIssuer(t)
	// The leaf has serial 2 (see makeLeaf); answer for serial 999 instead.
	srv := respondForSerial(t, big.NewInt(999), issuer, key)
	defer srv.Close()
	leaf := makeLeaf(t, issuer, key, srv.URL)

	_, err := CheckRevocation(context.Background(), srv.Client(), leaf, issuer)
	if err == nil {
		t.Fatal("a Good response for a different serial was accepted; the answer is not bound to the certificate")
	}
}

// TestCheckRevocationRejectsStaleResponse pins F9: an expired response is a
// replay and must not settle the certificate.
func TestCheckRevocationRejectsStaleResponse(t *testing.T) {
	issuer, key := makeIssuer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req, err := ocsp.ParseRequest(body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		template := ocsp.Response{
			Status:       ocsp.Good,
			SerialNumber: req.SerialNumber,
			ThisUpdate:   time.Now().Add(-48 * time.Hour),
			NextUpdate:   time.Now().Add(-24 * time.Hour), // expired yesterday
		}
		resp, _ := ocsp.CreateResponse(issuer, issuer, template, key)
		w.Header().Set("Content-Type", "application/ocsp-response")
		_, _ = w.Write(resp)
	}))
	defer srv.Close()
	leaf := makeLeaf(t, issuer, key, srv.URL)

	if _, err := CheckRevocation(context.Background(), srv.Client(), leaf, issuer); err == nil {
		t.Fatal("an expired OCSP response was accepted")
	}
}
