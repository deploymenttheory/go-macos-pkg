// Apple's root certificates, embedded so verify can build a chain without
// a keychain or a system trust store.
//
// The PEM files are exported from macOS's system root store by
// scripts/export-roots.sh, which pins each root's SHA-256 fingerprint so
// the set cannot drift silently. Developer ID certificates chain to the
// 2006 "Apple Root CA" (expires 2035); the G2 and G3 roots (2039) and the
// Apple Platform Developer and Multipurpose roots (2049) are embedded for
// chains that move to them. TestAppleRoots pins the same fingerprints.
package pkgsign

import (
	"crypto/x509"
	"embed"
	"encoding/pem"
	"sync"
)

//go:embed roots/*.pem
var rootFS embed.FS

var (
	rootsOnce sync.Once
	rootPool  *x509.CertPool
	rootCerts []*x509.Certificate
)

// AppleRoots returns a pool holding Apple's root certificates.
func AppleRoots() *x509.CertPool {
	loadRoots()
	return rootPool.Clone()
}

// AppleRootCertificates returns Apple's root certificates.
func AppleRootCertificates() []*x509.Certificate {
	loadRoots()
	return append([]*x509.Certificate(nil), rootCerts...)
}

func loadRoots() {
	rootsOnce.Do(func() {
		rootPool = x509.NewCertPool()
		entries, _ := rootFS.ReadDir("roots")
		for _, e := range entries {
			data, err := rootFS.ReadFile("roots/" + e.Name())
			if err != nil {
				continue
			}
			for {
				var block *pem.Block
				block, data = pem.Decode(data)
				if block == nil {
					break
				}
				c, err := x509.ParseCertificate(block.Bytes)
				if err != nil {
					continue
				}
				rootPool.AddCert(c)
				rootCerts = append(rootCerts, c)
			}
		}
	})
}
