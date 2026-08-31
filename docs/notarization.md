# Notarization

`macospkg notarize` submits a signed package to Apple's notary service,
waits for the verdict and staples the ticket, the work of `notarytool submit --wait`
and `stapler staple`, from Linux, Windows or macOS. `build --notarize`
and `product --notarize` do it as the last step of a build.

## Credentials

An App Store Connect API key with the Developer role: the key ID, the
issuer ID and the `.p8` private key. Give them as `--key-id`,
`--issuer` and `--key path.p8`, or as environment variables:

```
APPLE_KEY_ID=ABC123DEFG
APPLE_ISSUER_ID=12345678-1234-1234-1234-123456789012
APPLE_PRIVATE_KEY_PEM="-----BEGIN PRIVATE KEY-----…"   # or APPLE_PRIVATE_KEY_PATH=AuthKey.p8
```

Missing or rejected credentials exit 4.

## What happens

1. The package's SHA-256 is registered with Apple (`POST /notary/v2/submissions`),
   which answers with a submission id and temporary S3 credentials.
2. The file is uploaded with one signed `PUT` to the bucket in `us-west-2`.
3. With `--wait`, the status is polled (`--poll-interval`, default 30 s)
   until it is `Accepted`, `Invalid` or `Rejected`, or `--timeout`
   (default 30 min) passes: exit 0, 8 (with the developer log's issues
   printed) or 9.
4. With `--staple`, the ticket is fetched from Apple's public database and
   appended to the package.

`notarize info ID`, `notarize log ID`, `notarize wait ID` and
`notarize history` cover the rest of `notarytool`.

The package must already be signed with a Developer ID Installer
certificate and a timestamp; the service rejects anything else, and the
command refuses to submit an unsigned package unless `--force`.

## In CI

```yaml
- name: Build, sign, notarize
  env:
    APPLE_KEY_ID: ${{ secrets.APPLE_KEY_ID }}
    APPLE_ISSUER_ID: ${{ secrets.APPLE_ISSUER_ID }}
    APPLE_PRIVATE_KEY_PEM: ${{ secrets.APPLE_PRIVATE_KEY_PEM }}
    MACOSPKG_P12_PASSWORD: ${{ secrets.DEVID_P12_PASSWORD }}
  run: |
    echo "${{ secrets.DEVID_P12_BASE64 }}" | base64 -d > devid.p12
    macospkg build ./root Foo.pkg --identifier com.example.foo --version "$VERSION" \
      --sign-p12 devid.p12 --notarize
```

This runs identically on `ubuntu-latest`, `windows-latest` and
`macos-latest`. This repository's own CI does exactly this on macOS, then
asks `stapler validate` and `spctl --assess` for their verdict.
