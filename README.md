# macospkg

[![CI](https://github.com/deploymenttheory/go-macos-pkg/actions/workflows/ci.yml/badge.svg)](https://github.com/deploymenttheory/go-macos-pkg/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/deploymenttheory/go-macos-pkg)](https://github.com/deploymenttheory/go-macos-pkg/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/deploymenttheory/go-macos-pkg.svg)](https://pkg.go.dev/github.com/deploymenttheory/go-macos-pkg)
[![Go Version](https://img.shields.io/github/go-mod/go-version/deploymenttheory/go-macos-pkg)](go.mod)
[![License](https://img.shields.io/github/license/deploymenttheory/go-macos-pkg)](LICENSE)

A cross-platform, self-contained toolkit for macOS installer packages
(`.pkg`). It reads and writes the xar container, the bill of materials, the
cpio payload, `PackageInfo` and `Distribution` directly, without
`pkgbuild`, `productbuild`, `productsign`, `pkgutil`, `notarytool`,
`stapler` or a Mac, so a package can be inspected, built, signed,
notarized and stapled on a Linux or Windows CI runner exactly as on macOS.

```console
$ macospkg build ./root Foo.pkg --identifier com.example.foo --version 1.2.0 --scripts ./scripts
built Foo.pkg: com.example.foo 1.2.0, 14 files, 306 KB installed

$ macospkg sign Foo.pkg Foo-signed.pkg --p12 developer-id.p12 --p12-password-stdin < pw.txt
signed Foo.pkg -> Foo-signed.pkg (Developer ID Installer: Example Ltd (ABCDE12345), timestamped)

$ macospkg notarize Foo-signed.pkg --wait --staple
submission id 2efe2717-52ef-43a5-96dc-0797e4ca1041
2efe2717-52ef-43a5-96dc-0797e4ca1041: Accepted
stapled the notarization ticket to Foo-signed.pkg

$ macospkg verify --online Foo-signed.pkg
Status:    valid
Chain:     trusted
Timestamp: 2026-08-29T14:48:07Z
Staple:    notarization ticket present
Notarized: yes (ticket on record with Apple)
```

## Features

| | macOS | Linux | Windows |
|---|:---:|:---:|:---:|
| Inspect: `info`, `list`, `cat`, `inspect` | ✅ | ✅ | ✅ |
| Unpack: `expand` (pkgutil parity), `extract` | ✅ | ✅ | ✅ |
| Build component packages and product archives, reproducibly | ✅ | ✅ | ✅ |
| Sign with a Developer ID Installer certificate, with Apple timestamps | ✅ | ✅ | ✅ |
| Verify signatures against Apple's roots; team, timestamp, staple | ✅ | ✅ | ✅ |
| Notarize (App Store Connect API key) and staple | ✅ | ✅ | ✅ |
| Payloads: read and write gzip cpio and pbzx | ✅ | ✅ | ✅ |
| Payloads: read `pbze`/`pbz4`/`pbzz` and `pkgbuild --large-payload` | ✅ | ✅ | ✅ |
| Hard links and extended attributes (`._` sidecars), as pkgbuild carries them | ✅ | ✅ | xattrs as `._` files; links as copies |

See [`TOOLS_STATUS.md`](TOOLS_STATUS.md) for the exact state of each area.

### Payload containers

A Payload is a cpio archive; what differs between packages is the
container it is wrapped in, which the first bytes identify. None of this
depends on the host: the readers and writers are ordinary Go, so macOS,
Linux and Windows behave identically.

| Container | What it is | Support |
|---|---|---|
| gzip cpio | `pkgbuild`'s default, and the only container every macOS can install | read + write (the default) |
| `pbzx` | xz-compressed 16 MiB blocks: smaller, but only macOS 12 and later installs it | read + write (`--compression pbzx`, which `pkgbuild` spells `latest`) |
| `pbze` | the same container with LZFSE chunks. `pkgbuild` never writes it, but macOS reads it | read + write (`--compression lzfse`) |
| `pbzb` | the same container with LZBITMAP, Apple's undocumented codec. macOS reads it too | read + write (`--compression lzbitmap`) |
| `pbz4`, `pbzz` | the same container with Apple-framed LZ4 or zlib chunks, written by `aa` and libParallelCompression | read; `pkg/pbzx` writes them, but `build` refuses (see below) |
| Apple Archive | a different format entirely; the Installer does not read it either | detected, reported, exit 5 |

`build` will not write `pbz4` or `pbzz`, even though `pkg/pbzx` can and
Apple's own `aa` reads what it produces. macOS cannot read either as a
package Payload: `pkgutil --expand-full` fails with `cpio read error: bad
file format`, so the package would not install. `pbze` and `pbzb` are
offered because the opposite is true, and the acceptance suite pins it:
`pkgutil` unpacks either Payload byte for byte, single-chunk and
multi-chunk, and `installer` installs it.

LZBITMAP has no published specification. `pkg/lzbitmap` is a Go
translation of Corellium's MIT-licensed `libzbitmap`, which
reverse-engineered it; `NOTICE` carries the copyright. Both directions are
judged against Apple's own `aa`, which reads what we write and writes what
we read.

`--large-payload` is `pkgbuild`'s flag, not one of ours. It writes an
ordinary gzip cpio but names the archive entry `LargeSegmentedPayload`,
so such packages are read like any other; `macospkg build` always writes
the entry as `Payload`. Choosing `--compression pbzx` sets the package's
minimum system version to 12.0 unless you ask for a higher one, because
older systems cannot install it. The byte-level details are in
[`docs/formats/payload.md`](docs/formats/payload.md).

## Install

Download a release archive for your platform from the
[releases page](https://github.com/deploymenttheory/go-macos-pkg/releases),
or build from source:

```console
go build -o macospkg ./cmd/macospkg
```

Release archives are pure Go binaries with no runtime dependencies. Each
release ships a `checksums.txt` signed keylessly with cosign:

```console
cosign verify-blob \
  --bundle macospkg_<version>_checksums.txt.sigstore.json \
  --certificate-identity-regexp 'https://github.com/deploymenttheory/go-macos-pkg/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  macospkg_<version>_checksums.txt
shasum -a 256 -c macospkg_<version>_checksums.txt --ignore-missing
```

## Command reference

Every command takes the package first; `-o json` turns any output into
JSON (one document, or one line per entry for listings).

### `info PKG`

Kind (component package or product archive), each component's identity,
payload size and scripts, the Distribution's title, choices, architectures
and resources, the signature's certificate chain and team, and whether a
notarization ticket is stapled.

### `list PKG [--archive] [--component NAME] [-l|--long] [--scripts]`

The files the package installs, from the bill of materials: what `lsbom`
prints for an expanded package, without expanding. `-l` adds mode, owner,
size and time; `--archive` lists the xar entries; `--scripts` the Scripts
archives.

### `cat PKG ENTRY [--raw]` / `cat PKG --payload PATH`

One archive entry (`PackageInfo`, `Distribution`, `foo.pkg/Bom`,
`Resources/en.lproj/welcome.html`) decoded to stdout, or one file out of
the payload (`--payload ./usr/local/bin/tool`). `--raw` writes the entry's
stored bytes without decoding its encoding.

### `inspect PKG header|toc|packageinfo|distribution|bom|signature|cms|rsa|digest|ticket`

Raw structures: the xar header decoded, the table of contents XML as
stored, the bill of materials in `lsbom` columns, the signature elements
and PEM chain, the raw CMS or RSA signature bytes, the digest they cover,
the stapled ticket.

### `expand PKG DIR [--full] [--verify] [--xattrs MODE] [--hard-links=false]`

`pkgutil --expand`: every entry decoded into a new directory, Scripts
unpacked, Payload left as its gzip cpio. `--full` unpacks the payloads
too (`--expand-full`). Byte-identical to `pkgutil`'s output. `--xattrs`
and `--hard-links` are passed to the payload extractions, and mean what
they mean for `extract`.

### `extract PKG DIR [--component NAME] [--scripts] [--pattern RE] [--symlinks auto|real|file] [--xattrs auto|apply|file|skip] [--hard-links=false] [--verify]`

The payload files, as they would land under the install location, with
modes and times. `--verify` checks every file against the bill of
materials' checksum. Exit 6 if anything was skipped (device nodes, paths
that would escape `DIR`, links the host refused).

`._` AppleDouble sidecars become extended attributes on their owners
where the host stores them and stay `._` files where it does not, so
nothing is lost on Linux or Windows and building the extracted tree again
restores the package (`--xattrs auto`, the default; `apply`, `file` and
`skip` choose explicitly). Hard links are recreated as hard links;
`--hard-links=false` writes copies.

### `build SRC [OUT.pkg] --identifier ID --version V [options]`

A component package, what `pkgbuild` makes, from a directory. Options:
`--install-location`, `--scripts DIR`, `--ownership recommended|preserve|preserve-other`,
`--min-os-version`, `--postinstall-action`, `--nopayload`,
`--no-bundle-relocation`, `--relocatable`, `--preserve-xattr`,
`--auth root|none` (whether the Installer needs authorisation),
`--exclude RE`, `--executable RE` (for hosts without execute bits),
`--manifest build-info.yaml`, and `--sign-*` / `--notarize` to finish the
job in one run.

`--compression gzip|pbzx|latest` selects the payload container (`--pbzx-block-size`
tunes pbzx; the default matches `pkgbuild`'s 16 MiB). Extended attributes
and hard links are carried as `pkgbuild` carries them: `--xattrs fs|none`
says whether to read attributes from the tree, `--exclude-xattr RE`
(repeatable) drops names such as `com.apple.provenance`, and
`--hard-links auto|copy` says whether files sharing an inode are packaged
as one. A manifest's `file_xattrs` overrides attributes per file, or per
folder with a trailing `/`. See [`docs/formats/payload.md`](docs/formats/payload.md).

`SRC` may be a munkipkg-style project directory holding
`build-info.yaml|json|plist`, `payload/` and `scripts/`; flags override
the manifest.

Output is reproducible: set `--source-date-epoch` or `SOURCE_DATE_EPOCH`
and identical input gives identical bytes on every platform. Bundles in
the payload are recorded in `bundle-version` and `relocate` as `pkgbuild`
does; `numberOfFiles` and `installKBytes` are computed by `pkgbuild`'s
rules.

### `product OUT.pkg --package X.pkg… [--distribution D.xml] [--resources DIR] [--title T] [--min-os-version] [--host-architectures]`

A product archive, what `productbuild` makes, from component packages,
with a synthesised Distribution or one you supply. `--product-id` and
`--product-version` set the synthesised Distribution's identity; `--sign-*`
and `--notarize` finish the job in one run, as for `build`.

### `sign PKG OUT.pkg (--p12 F | --cert PEM --key PEM [--chain PEM]) [--no-timestamp] [--timestamp URL] [--digest sha256|sha1]`

`productsign`: an RSA and a CMS signature over the table of contents,
certificate chain embedded, timestamped by Apple's server unless told
not to. The PKCS#12 password comes from `--p12-password-stdin`,
`MACOSPKG_P12_PASSWORD` or `--p12-password`. A stapled ticket is removed,
since re-signing invalidates it.

### `verify PKG [--team-id ID] [--trust-anchors PEM] [--allow-untrusted] [--require-developer-id] [--require-stapled] [--online]`

`pkgutil --check-signature`, with every finding reported separately:
digest, RSA, CMS, chain to Apple's roots (built in), team, timestamp,
staple, and with `--online` whether Apple's ticket database has a ticket
for this exact package. Exit 7 on any failure.

### `notarize PKG [--wait] [--staple] [--timeout 30m] [--poll-interval 30s] [--log]`

`notarytool submit`: registers the package, uploads it, and with `--wait`
polls for the verdict (exit 0 Accepted, 8 Invalid/Rejected with the log's
issues printed, 9 timed out); `--staple` then attaches the ticket.
Credentials: `--key-id`, `--issuer-id`, `--private-key AuthKey.p8`, or
`APPLE_KEY_ID`, `APPLE_ISSUER_ID`, `APPLE_PRIVATE_KEY_PEM` /
`APPLE_PRIVATE_KEY_PATH`. `--name` sets the submission name shown in App
Store Connect (default: the file name), and `--force` submits a package
that is not signed. Subcommands `status`, `log`, `wait`, `list`.

### `staple PKG [OUT.pkg] [--check] [--ticket FILE]` / `unstaple PKG [OUT.pkg]`

`stapler staple`: fetches the ticket from Apple's public database and
appends it. `--check` reports whether one is present; `--ticket` staples
a ticket fetched elsewhere.

## Global flags

| Flag | Description |
|---|---|
| `-o, --output text\|json` | output format |
| `-q, --quiet` | suppress progress messages |
| `--verbose` | diagnostics on stderr |
| `--source-date-epoch N` | pin every timestamp for reproducible output |
| `--temp-dir DIR` | where scratch files go while building (default: beside the output) |

Configuration precedence: flag > `MACOSPKG_<FLAG>` environment variable >
`~/.config/macospkg/config.yaml`. `SOURCE_DATE_EPOCH` is the exception:
the bare variable outranks `MACOSPKG_SOURCE_DATE_EPOCH`.

## Exit codes

| Code | Meaning |
|---:|---|
| 0 | success |
| 1 | error |
| 2 | usage error |
| 3 | not a flat package (missing, not a xar, or a xar without PackageInfo/Distribution) |
| 4 | credentials missing or rejected (PKCS#12 password, key mismatch, notary API key) |
| 5 | unsupported (Apple Archive payload, ownership on Windows, non-RSA key, >5 GiB upload) |
| 6 | partial result (some entries skipped) |
| 7 | signature or ticket check failed, or no ticket available |
| 8 | notarization rejected |
| 9 | wait timed out |

The contract lives in [`pkg/exitcode`](pkg/exitcode/exitcode.go).

## In CI

```yaml
- uses: actions/checkout@v7
- name: Build, sign, notarize and staple
  env:
    APPLE_KEY_ID: ${{ secrets.APPLE_KEY_ID }}
    APPLE_ISSUER_ID: ${{ secrets.APPLE_ISSUER_ID }}
    APPLE_PRIVATE_KEY_PEM: ${{ secrets.APPLE_PRIVATE_KEY_PEM }}
    MACOSPKG_P12_PASSWORD: ${{ secrets.DEVID_P12_PASSWORD }}
    SOURCE_DATE_EPOCH: ${{ github.event.head_commit.timestamp }}
  run: |
    echo "${{ secrets.DEVID_P12_BASE64 }}" | base64 -d > devid.p12
    macospkg build ./root Foo-${VERSION}.pkg --identifier com.example.foo --version "$VERSION" \
      --scripts ./scripts --sign-p12 devid.p12 --notarize
    macospkg verify --require-stapled --online Foo-${VERSION}.pkg
```

The same job runs on `ubuntu-latest`, `windows-latest` and `macos-latest`.
See [`docs/notarization.md`](docs/notarization.md) and
[`docs/signing.md`](docs/signing.md).

## Using it as a library

```go
import "github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"

p, err := flatpkg.Open("Foo.pkg")
for _, c := range p.Components {
    fmt.Println(c.Info.Identifier, c.Info.Version)
}
```

Key packages: `pkg/xar` (container), `pkg/bom` (bill of materials),
`pkg/cpio` and `pkg/pbzx` (payloads), `pkg/appledouble` (the `._` sidecars
that carry extended attributes), `pkg/flatpkg` (packages, build, expand,
extract), `pkg/pkgsign` (sign, verify), `pkg/notary`, `pkg/staple`.
The format details are written down in [`docs/formats/`](docs/formats/).

## How it is tested

The binary never calls an Apple tool. Apple's `pkgbuild`, `pkgutil`,
`lsbom`, `xar`, `installer`, `stapler` and `spctl` are used only by the
acceptance suite on macOS, as independent oracles: the fixtures in
`testdata/cli` were produced by Apple's tools once and are committed, so
Linux and Windows test the reader against Apple's bytes; the macOS leg
additionally builds the same tree with `pkgbuild` and with `macospkg` and
compares what `lsbom`, `pkgutil` and `xar` say about each, installs our
package with `installer`, checks our signature with `pkgutil
--check-signature` and `openssl cms`, and our staple with `stapler
validate` and `spctl`. A gated job signs and notarizes with a real
Developer ID against Apple's services.

Two real-world packages are the oracles on every platform: Google's Go
installer (`go1.27.0.darwin-arm64.pkg`, stapled, no bundles) and
PowerShell (`powershell-7.6.1-osx-arm64.pkg`, an app bundle with a symlink
and a postinstall script). Each has its signature verified against Apple's
roots and its ticket against Apple's database, then is expanded with
`macospkg` and rebuilt with `macospkg`, and the rebuilt package is compared
with the original entry for entry: PackageInfo numbers, every
bill-of-materials entry (17,356 of them for Go), every payload file's
bytes, the Distribution and resources. On macOS, `pkgutil`, `lsbom` and
`xar` compare the two as well, and `installer` installs the rebuilt
package.

## Development

```console
go build ./...
go test ./pkg/... ./internal/...
go test -v ./acceptance/
```

Fixtures are regenerated with `scripts/gen-fixtures.sh` on macOS. See
[`CONTRIBUTING.md`](CONTRIBUTING.md).

## Licensing

MIT. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE) for the reference
implementations that informed the format code; none is vendored.

## Acknowledgements

The flat package format is Apple's and undocumented; this tool stands on
the people who worked it out before: Rob Braun's xar, Fabian Renn's
bomutils, Gregory Szorc's apple-platform-rs, SAS's relic, libarchive, and
Greg Neagle's munki-pkg. See [`NOTICE`](NOTICE).
