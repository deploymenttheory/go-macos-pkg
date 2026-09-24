package pkgsign

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/asn1"
	"testing"
)

func TestCMSWithoutSignedAttributes(t *testing.T) {
	id, _ := testIdentity(t)
	content := []byte("detached content")
	for _, hash := range []crypto.Hash{crypto.SHA1, crypto.SHA256} {
		t.Run(hash.String(), func(t *testing.T) {
			der, err := SignCMS(content, id, CMSOptions{Hash: hash})
			if err != nil {
				t.Fatal(err)
			}
			_, sd, err := ParseCMS(der)
			if err != nil {
				t.Fatal(err)
			}
			si := &sd.SignerInfos[0]
			si.SignedAttrs = asn1.RawValue{}
			h := hash.New()
			h.Write(content)
			si.Signature, err = rsa.SignPKCS1v15(rand.Reader, id.Key.(*rsa.PrivateKey), hash, h.Sum(nil))
			if err != nil {
				t.Fatal(err)
			}
			der = marshalSignedData(t, sd)
			info, err := VerifyCMS(der, content)
			if err != nil {
				t.Fatal(err)
			}
			if !info.Signer.Equal(id.Cert) || !info.SigningTime.IsZero() {
				t.Error("signer or absent signing time was not preserved")
			}
			if _, err := VerifyCMS(der, []byte("other content")); err == nil {
				t.Error("wrong content verified")
			}
			si.Signature[0] ^= 1
			if _, err := VerifyCMS(marshalSignedData(t, sd), content); err == nil {
				t.Error("tampered signature verified")
			}
			si.Signature[0] ^= 1

			// An explicitly empty set is not an absent signedAttrs field.
			si.SignedAttrs = asn1.RawValue{FullBytes: []byte{0xa0, 0}}
			if _, err := VerifyCMS(marshalSignedData(t, sd), content); err == nil {
				t.Error("empty signed attributes verified")
			}
			si.SignedAttrs = asn1.RawValue{}
			sd.ContentInfo.ContentType = oidTSTInfo
			if _, err := VerifyCMS(marshalSignedData(t, sd), content); err == nil {
				t.Error("non-id-data content verified without signed attributes")
			}
		})
	}
}

func marshalSignedData(t *testing.T, sd *signedData) []byte {
	t.Helper()
	body, err := asn1.Marshal(*sd)
	if err != nil {
		t.Fatal(err)
	}
	der, err := asn1.Marshal(contentInfo{
		ContentType: oidSignedData,
		Content:     asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: body},
	})
	if err != nil {
		t.Fatal(err)
	}
	return der
}
