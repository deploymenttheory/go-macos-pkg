// Apple's root certificates, embedded so verify can build a chain without
// a keychain or a system trust store.
//
// The PEM files were exported from macOS's SystemRootCertificates keychain:
//
//	security find-certificate -a -p /System/Library/Keychains/SystemRootCertificates.keychain
//
// Developer ID certificates chain to the 2006 "Apple Root CA"; the G2 and
// G3 roots are included for completeness. They expire in 2035 and 2039.
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
