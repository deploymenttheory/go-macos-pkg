// A minimal CMS (RFC 5652) SignedData encoder and decoder: enough to
// produce and check the x-signature productsign writes, and no more.
//
// The signed content is the xar TOC digest, carried detached (the TOC
// records where the digest lives). One SignerInfo, identified by issuer
// and serial number, with the three signed attributes every CMS signature
// has (contentType, signingTime, messageDigest) and, optionally, an RFC
// 3161 timestamp token as an unsigned attribute. Certificates are
// included, leaf first.
package pkgsign

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"
)

// Object identifiers used in a SignedData.
var (
	oidSignedData      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidData            = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidAttrContentType = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidAttrMessageDgst = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidAttrSigningTime = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
	oidAttrTimestamp   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 2, 14}
	oidRSAEncryption   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidSHA256WithRSA   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}
	oidSHA1WithRSA     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 5}
	oidDigestSHA1      = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}
	oidDigestSHA256    = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidDigestSHA512    = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}
	asn1NullBytes      = asn1.RawValue{Tag: asn1.TagNull}
)

// ASN.1 shapes, as encoding/asn1 wants them.

// contentInfo wraps the SignedData in the [0] EXPLICIT tag. The wrapper
// is built by hand: encoding/asn1 writes a RawValue's FullBytes verbatim
// and would silently drop the explicit tag.
type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"optional"`
}

type signedData struct {
	Version          int
	DigestAlgorithms []pkix.AlgorithmIdentifier `asn1:"set"`
	ContentInfo      encapContentInfo
	Certificates     asn1.RawValue `asn1:"optional,tag:0"`
	CRLs             asn1.RawValue `asn1:"optional,tag:1"`
	SignerInfos      []signerInfo  `asn1:"set"`
}

type encapContentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"optional"`
}

type issuerAndSerial struct {
	Issuer       asn1.RawValue
	SerialNumber *big.Int
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue `asn1:"set"`
}

type signerInfo struct {
	Version            int
	IssuerAndSerial    issuerAndSerial
	DigestAlgorithm    pkix.AlgorithmIdentifier
	SignedAttrs        asn1.RawValue `asn1:"optional,tag:0"`
	SignatureAlgorithm pkix.AlgorithmIdentifier
	Signature          []byte
	UnsignedAttrs      asn1.RawValue `asn1:"optional,tag:1"`
}

// digestAlgorithm maps a crypto.Hash to its OID.
func digestAlgorithm(h crypto.Hash) (asn1.ObjectIdentifier, error) {
	switch h {
	case crypto.SHA1:
		return oidDigestSHA1, nil
	case crypto.SHA256:
		return oidDigestSHA256, nil
	case crypto.SHA512:
		return oidDigestSHA512, nil
	}
	return nil, fmt.Errorf("pkgsign: unsupported digest %v", h)
}

func hashFromOID(oid asn1.ObjectIdentifier) (crypto.Hash, error) {
	switch {
	case oid.Equal(oidDigestSHA1):
		return crypto.SHA1, nil
	case oid.Equal(oidDigestSHA256):
		return crypto.SHA256, nil
	case oid.Equal(oidDigestSHA512):
		return crypto.SHA512, nil
	}
	return 0, fmt.Errorf("pkgsign: unsupported digest algorithm %v", oid)
}

// CMSOptions configures SignCMS.
type CMSOptions struct {
	// Hash digests the content and the signed attributes.
	Hash crypto.Hash
	// SigningTime is the signingTime attribute; zero means now.
	SigningTime time.Time
	// TimestampToken, when set, is added as the unsigned timeStampToken
	// attribute (the DER ContentInfo returned by an RFC 3161 server).
	TimestampToken []byte
}

// encodeAttrs marshals attributes as a DER SET OF (elements in ascending
// encoded order, as DER requires), which is what the signature covers. As
// stored in the SignerInfo the same bytes carry the context tag [0]; only
// the first byte differs.
func encodeAttrs(attrs []attribute) ([]byte, error) {
	items := make([][]byte, 0, len(attrs))
	for _, a := range attrs {
		b, err := asn1.Marshal(a)
		if err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	sort.Slice(items, func(i, j int) bool { return bytes.Compare(items[i], items[j]) < 0 })
	var body []byte
	for _, it := range items {
		body = append(body, it...)
	}
	return asn1.Marshal(asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSet, IsCompound: true, Bytes: body})
}

// decodeAttrs parses a SET OF Attribute.
func decodeAttrs(set []byte) ([]attribute, error) {
	var attrs []attribute
	if _, err := asn1.UnmarshalWithParams(set, &attrs, "set"); err != nil {
		return nil, err
	}
	return attrs, nil
}

func attrOf(oid asn1.ObjectIdentifier, value any) (attribute, error) {
	v, err := asn1.Marshal(value)
	if err != nil {
		return attribute{}, err
	}
	return attribute{Type: oid, Values: asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSet, IsCompound: true, Bytes: v}}, nil
}

// SignCMS produces a detached SignedData over content.
func SignCMS(content []byte, id *Identity, o CMSOptions) ([]byte, error) {
	if o.Hash == 0 {
		o.Hash = crypto.SHA256
	}
	digestOID, err := digestAlgorithm(o.Hash)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := id.Key.(*rsa.PrivateKey)
	if !ok {
		return nil, ErrNotRSA
	}
	if o.SigningTime.IsZero() {
		o.SigningTime = time.Now()
	}

	h := o.Hash.New()
	h.Write(content)
	contentDigest := h.Sum(nil)

	var attrs []attribute
	a, err := attrOf(oidAttrContentType, oidData)
	if err != nil {
		return nil, err
	}
	attrs = append(attrs, a)
	a, err = attrOf(oidAttrSigningTime, o.SigningTime.UTC().Truncate(time.Second))
	if err != nil {
		return nil, err
	}
	attrs = append(attrs, a)
	a, err = attrOf(oidAttrMessageDgst, contentDigest)
	if err != nil {
		return nil, err
	}
	attrs = append(attrs, a)

	// The signature is over the DER SET OF attributes.
	setBytes, err := encodeAttrs(attrs)
	if err != nil {
		return nil, err
	}
	h = o.Hash.New()
	h.Write(setBytes)
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, o.Hash, h.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("pkgsign: CMS signature: %w", err)
	}

	// Stored with the implicit [0] tag: same content, different tag byte.
	signedAttrs := append([]byte(nil), setBytes...)
	signedAttrs[0] = 0xA0

	si := signerInfo{
		Version: 1,
		IssuerAndSerial: issuerAndSerial{
			Issuer:       asn1.RawValue{FullBytes: id.Cert.RawIssuer},
			SerialNumber: id.Cert.SerialNumber,
		},
		DigestAlgorithm:    pkix.AlgorithmIdentifier{Algorithm: digestOID, Parameters: asn1NullBytes},
		SignedAttrs:        asn1.RawValue{FullBytes: signedAttrs},
		SignatureAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oidRSAEncryption, Parameters: asn1NullBytes},
		Signature:          sig,
	}
	if o.TimestampToken != nil {
		ts := attribute{Type: oidAttrTimestamp, Values: asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSet, IsCompound: true, Bytes: o.TimestampToken}}
		unsigned, err := encodeAttrs([]attribute{ts})
		if err != nil {
			return nil, err
		}
		unsigned[0] = 0xA1
		si.UnsignedAttrs = asn1.RawValue{FullBytes: unsigned}
	}

	var certs bytes.Buffer
	certs.Write(id.Cert.Raw)
	for _, c := range id.Chain {
		certs.Write(c.Raw)
	}
	sd := signedData{
		Version:          1,
		DigestAlgorithms: []pkix.AlgorithmIdentifier{{Algorithm: digestOID, Parameters: asn1NullBytes}},
		ContentInfo:      encapContentInfo{ContentType: oidData},
		Certificates:     asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: certs.Bytes()},
		SignerInfos:      []signerInfo{si},
	}
	sdBytes, err := asn1.Marshal(sd)
	if err != nil {
		return nil, fmt.Errorf("pkgsign: encoding SignedData: %w", err)
	}
	return asn1.Marshal(contentInfo{
		ContentType: oidSignedData,
		Content:     asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: sdBytes},
	})
}

// CMSInfo is what VerifyCMS learns from a SignedData.
type CMSInfo struct {
	Certificates []*x509.Certificate // as embedded, leaf first if the signer put it first
	Signer       *x509.Certificate
	Hash         crypto.Hash
	SigningTime  time.Time
	// TimestampToken is the raw RFC 3161 token, if one is attached.
	TimestampToken []byte
	// SignatureValue is the SignerInfo's signature. An RFC 3161 token
	// attests to these bytes, so verifying one needs them.
	SignatureValue []byte
}

// ErrCMS reports a malformed or failed CMS signature.
var ErrCMS = errors.New("pkgsign: CMS signature")

// ParseCMS decodes a SignedData without verifying it. The input may be
// BER, as Apple's productsign writes it, or DER.
func ParseCMS(raw []byte) (*CMSInfo, *signedData, error) {
	// productsign pads the CMS blob with zeros to its reserved size, and
	// encodes it as BER, whose end-of-contents markers are zero bytes too;
	// so the padding cannot be trimmed first. Convert the first element to
	// DER and let whatever follows fall away.
	der, err := berToDER(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrCMS, err)
	}
	var ci contentInfo
	if _, err := asn1.Unmarshal(der, &ci); err != nil {
		return nil, nil, fmt.Errorf("%w: not a ContentInfo: %v", ErrCMS, err)
	}
	if !ci.ContentType.Equal(oidSignedData) {
		return nil, nil, fmt.Errorf("%w: content type %v is not SignedData", ErrCMS, ci.ContentType)
	}
	if ci.Content.Class != asn1.ClassContextSpecific || ci.Content.Tag != 0 {
		return nil, nil, fmt.Errorf("%w: SignedData is not wrapped in [0]", ErrCMS)
	}
	var sd signedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return nil, nil, fmt.Errorf("%w: malformed SignedData: %v", ErrCMS, err)
	}
	info := &CMSInfo{}
	if len(sd.Certificates.Bytes) > 0 {
		certs, err := x509.ParseCertificates(sd.Certificates.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: embedded certificates: %v", ErrCMS, err)
		}
		info.Certificates = certs
	}
	if len(sd.SignerInfos) != 1 {
		return nil, nil, fmt.Errorf("%w: %d signers, want 1", ErrCMS, len(sd.SignerInfos))
	}
	si := sd.SignerInfos[0]
	info.Hash, err = hashFromOID(si.DigestAlgorithm.Algorithm)
	if err != nil {
		return nil, nil, err
	}
	info.SignatureValue = si.Signature
	for _, c := range info.Certificates {
		if c.SerialNumber.Cmp(si.IssuerAndSerial.SerialNumber) == 0 && bytes.Equal(c.RawIssuer, si.IssuerAndSerial.Issuer.FullBytes) {
			info.Signer = c
		}
	}
	attrs, err := parseAttrs(si.SignedAttrs)
	if err != nil {
		return nil, nil, err
	}
	for _, a := range attrs {
		if a.Type.Equal(oidAttrSigningTime) {
			var t time.Time
			if _, err := asn1.Unmarshal(a.Values.Bytes, &t); err == nil {
				info.SigningTime = t
			}
		}
	}
	if len(si.UnsignedAttrs.Bytes) > 0 {
		uattrs, err := parseAttrs(si.UnsignedAttrs)
		if err == nil {
			for _, a := range uattrs {
				if a.Type.Equal(oidAttrTimestamp) {
					info.TimestampToken = a.Values.Bytes
				}
			}
		}
	}
	return info, &sd, nil
}

func parseAttrs(raw asn1.RawValue) ([]attribute, error) {
	if len(raw.FullBytes) == 0 {
		return nil, nil
	}
	set := append([]byte(nil), raw.FullBytes...)
	set[0] = 0x31
	attrs, err := decodeAttrs(set)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed attributes: %v", ErrCMS, err)
	}
	return attrs, nil
}

// VerifyCMS checks a detached SignedData over content: the messageDigest
// attribute matches the content, and the signer's signature over the
// signed attributes verifies with the embedded certificate. It does not
// evaluate trust in the certificate.
func VerifyCMS(der, content []byte) (*CMSInfo, error) {
	info, sd, err := ParseCMS(der)
	if err != nil {
		return nil, err
	}
	if info.Signer == nil {
		return info, fmt.Errorf("%w: signer certificate is not embedded", ErrCMS)
	}
	si := sd.SignerInfos[0]
	if err := verifySignedAttrs(si, info.Signer, info.Hash, content, sd.ContentInfo.ContentType); err != nil {
		return info, err
	}
	return info, nil
}

// verifySignedAttrs is the RFC 5652 check a SignerInfo has to pass: the
// signed attributes carry a messageDigest matching the content and a
// contentType matching the eContentType, and the signature verifies over
// those attributes. Both our own SignedData and the one inside an RFC
// 3161 timestamp token are checked here, so the two cannot drift apart.
func verifySignedAttrs(si signerInfo, signer *x509.Certificate, hash crypto.Hash, content []byte, eContentType asn1.ObjectIdentifier) error {
	if len(si.SignedAttrs.FullBytes) == 0 {
		return fmt.Errorf("%w: no signed attributes", ErrCMS)
	}
	attrs, err := parseAttrs(si.SignedAttrs)
	if err != nil {
		return err
	}
	var messageDigest []byte
	var contentType asn1.ObjectIdentifier
	var sawDigest, sawType bool
	for _, a := range attrs {
		switch {
		case a.Type.Equal(oidAttrMessageDgst):
			if _, err := asn1.Unmarshal(a.Values.Bytes, &messageDigest); err != nil {
				return fmt.Errorf("%w: malformed messageDigest", ErrCMS)
			}
			sawDigest = true
		case a.Type.Equal(oidAttrContentType):
			if _, err := asn1.Unmarshal(a.Values.Bytes, &contentType); err != nil {
				return fmt.Errorf("%w: malformed contentType", ErrCMS)
			}
			sawType = true
		}
	}
	// RFC 5652 4.5.1: when signed attributes are present both of these are
	// mandatory. An absent messageDigest would otherwise compare equal to
	// a nil digest, and an absent contentType would let a signature made
	// over one content type be replayed as another.
	if !sawDigest {
		return fmt.Errorf("%w: no messageDigest attribute", ErrCMS)
	}
	if !sawType {
		return fmt.Errorf("%w: no contentType attribute", ErrCMS)
	}
	if !contentType.Equal(eContentType) {
		return fmt.Errorf("%w: signed contentType %v is not the content's %v", ErrCMS, contentType, eContentType)
	}
	h := hash.New()
	h.Write(content)
	if subtle.ConstantTimeCompare(h.Sum(nil), messageDigest) != 1 {
		return fmt.Errorf("%w: messageDigest does not match the content", ErrCMS)
	}
	// The attributes are signed as a SET OF, not as the [0] IMPLICIT they
	// are stored in, so the leading tag becomes 0x31 before hashing.
	set := append([]byte(nil), si.SignedAttrs.FullBytes...)
	set[0] = 0x31
	h = hash.New()
	h.Write(set)
	pub, ok := signer.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("%w: signer key is not RSA", ErrCMS)
	}
	if err := rsa.VerifyPKCS1v15(pub, hash, h.Sum(nil), si.Signature); err != nil {
		return fmt.Errorf("%w: signature does not verify: %v", ErrCMS, err)
	}
	return nil
}

// unused guards for algorithm OIDs kept for completeness.
var _ = []asn1.ObjectIdentifier{oidSHA256WithRSA, oidSHA1WithRSA}
var _ = sha1.Size
var _ = sha256.Size
