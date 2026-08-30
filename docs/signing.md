# Signing

`macospkg sign`, and `--sign-*` on `build` and `product`, produce the
signature `productsign` produces, and `verify` checks it the way `pkgutil
--check-signature` does, with no keychain, on any platform.

## What a signature is

Two signatures over the xar table-of-contents digest, stored at the start
of the heap right after the digest, and described in the TOC:

```xml
<signature style="RSA">
    <offset>32</offset><size>256</size>
    <KeyInfo xmlns="http://www.w3.org/2000/09/xmldsig#"><X509Data>
        <X509Certificate>…leaf…</X509Certificate>
        <X509Certificate>…Developer ID Certification Authority…</X509Certificate>
        <X509Certificate>…Apple Root CA…</X509Certificate>
    </X509Data></KeyInfo>
</signature>
<x-signature style="CMS"><offset>288</offset><size>…</size>…</x-signature>
```

The RSA one is PKCS#1 v1.5 over the digest itself. The CMS one is a
detached SignedData whose content is the digest, with `contentType`,
`signingTime` and `messageDigest` signed attributes and, when
timestamped, an RFC 3161 token as an unsigned attribute. Apple writes the
CMS blob as BER with indefinite lengths; `verify` normalises it to DER
before parsing. Certificates are base64 DER in 72-column lines, leaf
first.

Because the signatures sit before the entry data, signing shifts every
entry offset by the signatures' size. `sign` rewrites the table of
contents and copies the heap through unchanged, which is what makes
signing a multi-gigabyte package cheap.

## Identity

A Developer ID Installer certificate and its RSA key, from
`--p12 file.p12` (password via `--p12-password-stdin`,
`MACOSPKG_P12_PASSWORD`, `--p12-password`, or a manifest's
`signing_info.p12_password_env`) or `--cert leaf.pem --key key.pem
[--chain intermediates.pem]`. The chain is ordered automatically; a
self-signed root is embedded if supplied, as `productsign` does.

Timestamps come from `http://timestamp.apple.com/ts01` by default
(`--timestamp URL` to change, `--no-timestamp` to skip). A timestamped
signature stays valid after the certificate expires; Apple's notary
service requires one.

## Verification

`verify` reports every fact separately in JSON (digest, RSA, CMS, chain,
team, timestamp, staple) and exits 7 if any check fails. Trust is
evaluated against Apple's root certificates built in (`pkg/pkgsign/roots`,
exported from macOS's system roots), or `--trust-anchors file.pem` for a
private CA. Apple marks its certificate extensions critical; those are
accepted. Validity is judged at the timestamp's time when there is one.
There is no revocation checking.

`--online` also asks Apple's ticket database whether this exact package
was notarized; `--require-stapled` insists on a stapled ticket;
`--require-developer-id` and `--team-id` pin the signer.
