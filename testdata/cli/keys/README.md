# Fixture signing keys

Test-only. A private certification authority and a leaf certificate shaped
like a Developer ID Installer certificate (same subject pattern, same Apple
marker extension OID `1.2.840.113635.100.6.1.14`), so that signing and
verification can be exercised without Apple's CA. The PKCS#12 password is
`fixture`. Nothing here is trusted by any real system.

| File | Purpose |
|---|---|
| `fixture-ca.pem`, `fixture-ca.key` | the CA; `verify --trust-anchors fixture-ca.pem` |
| `fixture-installer.pem`, `fixture-installer.key` | the leaf and its key, PEM |
| `fixture-installer.p12` | the leaf, key and CA in one bundle, password `fixture` |
