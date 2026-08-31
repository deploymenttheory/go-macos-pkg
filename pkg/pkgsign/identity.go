// Package pkgsign signs and verifies flat packages the way productsign
// and pkgutil --check-signature do: an RSA signature and a CMS signature
// over the xar table-of-contents digest, with the certificate chain in the
// table of contents.
//
// Nothing here touches a keychain. The identity comes from a PKCS#12 file
// or PEM files, so the same command works on Linux and Windows.
package pkgsign

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// Apple's certificate marker extensions.
var (
	// OIDDeveloperIDInstaller marks a Developer ID Installer leaf.
	OIDDeveloperIDInstaller = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 1, 14}
	// OIDDeveloperIDApplication marks a Developer ID Application leaf.
	OIDDeveloperIDApplication = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 1, 13}
	// OIDDeveloperIDCA marks the Developer ID Certification Authority.
	OIDDeveloperIDCA = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 2, 6}
)

// Identity is a signing certificate with its private key and the
// intermediate certificates to embed.
type Identity struct {
	Cert  *x509.Certificate
	Key   crypto.Signer
	Chain []*x509.Certificate // intermediates, leaf's issuer first
}

// Errors.
var (
	ErrNotRSA      = errors.New("pkgsign: the signing key is not RSA; Developer ID Installer certificates are RSA and the xar signature format needs one")
	ErrKeyMismatch = errors.New("pkgsign: the private key does not match the certificate")
	ErrBadPassword = errors.New("pkgsign: unable to decode the PKCS#12 file: wrong password or unsupported format")
)

// LoadP12 decodes a PKCS#12 bundle: the leaf, its key and any CA
// certificates it carries.
func LoadP12(data []byte, password string) (*Identity, error) {
	key, cert, cas, err := pkcs12.DecodeChain(data, password)
	if err != nil {
		if errors.Is(err, pkcs12.ErrIncorrectPassword) {
			return nil, ErrBadPassword
		}
		return nil, fmt.Errorf("%w: %v", ErrBadPassword, err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("pkgsign: PKCS#12 key of type %T cannot sign", key)
	}
	id := &Identity{Cert: cert, Key: signer}
	for _, c := range cas {
		if !c.Equal(cert) {
			id.Chain = append(id.Chain, c)
		}
	}
	id.orderChain()
	return id, id.check()
}

// LoadPEM builds an identity from a PEM certificate (possibly followed by
// its chain), a PEM private key, and an optional PEM chain file.
func LoadPEM(certPEM, keyPEM, chainPEM []byte) (*Identity, error) {
	certs, err := parsePEMCerts(certPEM)
	if err != nil {
		return nil, err
	}
	if len(certs) == 0 {
		return nil, errors.New("pkgsign: no certificate in the PEM file")
	}
	key, err := parsePEMKey(keyPEM)
	if err != nil {
		return nil, err
	}
	id := &Identity{Cert: certs[0], Key: key, Chain: certs[1:]}
	if len(chainPEM) > 0 {
		chain, err := parsePEMCerts(chainPEM)
		if err != nil {
			return nil, err
		}
		for _, c := range chain {
			if !c.Equal(id.Cert) {
				id.Chain = append(id.Chain, c)
			}
		}
	}
	id.orderChain()
	return id, id.check()
}

// LoadPEMFiles is LoadPEM over files; chainPath may be empty.
func LoadPEMFiles(certPath, keyPath, chainPath string) (*Identity, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	var chainPEM []byte
	if chainPath != "" {
		chainPEM, err = os.ReadFile(chainPath)
		if err != nil {
			return nil, err
		}
	}
	return LoadPEM(certPEM, keyPEM, chainPEM)
}

// check confirms the key matches the certificate and is RSA.
func (id *Identity) check() error {
	pub, ok := id.Cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		if _, ecdsaKey := id.Cert.PublicKey.(*ecdsa.PublicKey); ecdsaKey {
			return ErrNotRSA
		}
		return fmt.Errorf("pkgsign: unsupported certificate key type %T", id.Cert.PublicKey)
	}
	priv, ok := id.Key.(*rsa.PrivateKey)
	if !ok {
		return ErrNotRSA
	}
	if priv.N.Cmp(pub.N) != 0 || priv.E != pub.E {
		return ErrKeyMismatch
	}
	return nil
}

// orderChain sorts the chain so each certificate issues the one before
// it, starting from the leaf's issuer, up to and including a self-signed
// root if one was supplied (productsign embeds the whole chain, root
// included); strays are dropped.
func (id *Identity) orderChain() {
	var ordered []*x509.Certificate
	remaining := append([]*x509.Certificate(nil), id.Chain...)
	current := id.Cert
	for len(remaining) > 0 {
		found := -1
		for i, c := range remaining {
			if c.Subject.String() == current.Issuer.String() && current.CheckSignatureFrom(c) == nil {
				found = i
				break
			}
		}
		if found < 0 {
			break
		}
		next := remaining[found]
		remaining = append(remaining[:found], remaining[found+1:]...)
		ordered = append(ordered, next)
		if next.Subject.String() == next.Issuer.String() && next.CheckSignatureFrom(next) == nil {
			break // the root; nothing issues it
		}
		current = next
	}
	id.Chain = ordered
}

// TeamID returns the Apple team identifier, which Developer ID
// certificates carry in the organizational unit.
func (id *Identity) TeamID() string { return TeamIDOf(id.Cert) }

// TeamIDOf returns the organizational unit of a certificate.
func TeamIDOf(c *x509.Certificate) string {
	if len(c.Subject.OrganizationalUnit) > 0 {
		return c.Subject.OrganizationalUnit[0]
	}
	return ""
}

// IsDeveloperIDInstaller reports whether a certificate carries Apple's
// Developer ID Installer marker extension.
func IsDeveloperIDInstaller(c *x509.Certificate) bool {
	return hasExtension(c, OIDDeveloperIDInstaller)
}

func hasExtension(c *x509.Certificate, oid asn1.ObjectIdentifier) bool {
	for _, e := range c.Extensions {
		if e.Id.Equal(oid) {
			return true
		}
	}
	return false
}

// CommonName is the certificate's common name, for messages.
func CommonName(c *x509.Certificate) string { return c.Subject.CommonName }

func parsePEMCerts(data []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("pkgsign: certificate: %w", err)
		}
		out = append(out, c)
	}
	return out, nil
}

func parsePEMKey(data []byte) (crypto.Signer, error) {
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if !strings.Contains(block.Type, "PRIVATE KEY") {
			continue
		}
		if strings.Contains(block.Headers["Proc-Type"], "ENCRYPTED") {
			return nil, errors.New("pkgsign: encrypted PEM keys are not supported; use a PKCS#12 file")
		}
		if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			if s, ok := k.(crypto.Signer); ok {
				return s, nil
			}
		}
		if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			return k, nil
		}
		if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
			return k, nil
		}
		return nil, errors.New("pkgsign: unable to parse the private key")
	}
	return nil, errors.New("pkgsign: no private key in the PEM file")
}
