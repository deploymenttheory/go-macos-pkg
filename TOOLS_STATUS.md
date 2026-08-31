# Implementation status

A snapshot of what is implemented. See [`README.md`](README.md) for usage and
[`TOOLS_ROADMAP.md`](TOOLS_ROADMAP.md) for what is planned.

Legend: ✅ implemented · 🟡 partial · ⬜ not yet

Every ✅ below is backed by the acceptance suite on all three platforms; the
macOS leg additionally checks the result against Apple's own tools.

| Area | macOS | Linux | Windows |
|---|:---:|:---:|:---:|
| CLI skeleton, exit codes, release pipeline | ✅ | ✅ | ✅ |
| Read xar (header, TOC, entries, checksums, signature elements) | ✅ | ✅ | ✅ |
| Read bill of materials (Bom) | ✅¹ | ✅¹ | ✅¹ |
| Read cpio payloads: gzip, pbz* (xz, LZFSE, LZ4, zlib, LZBITMAP), odc, newc | ✅ | ✅ | ✅ |
| Write pbzx, pbze and pbzb payloads (`build --compression pbzx\|lzfse\|lzbitmap`) | ✅⁶ | ✅⁶ | ✅⁶ |
| Read `--large-payload` packages (LargeSegmentedPayload) | ✅ | ✅ | ✅ |
| Apple Archive payloads | ⬜² | ⬜² | ⬜² |
| PackageInfo and Distribution models | ✅ | ✅ | ✅ |
| `info`, `list`, `cat`, `inspect` | ✅ | ✅ | ✅ |
| `expand` (pkgutil --expand / --expand-full parity) | ✅³ | ✅³ | ✅³ |
| `extract` (payload, scripts, pattern, verify) | ✅ | ✅ | 🟡⁴ |
| `build` (component package) | ✅⁶ | ✅⁶ | ✅⁶ |
| `product` (product archive) | ✅ | ✅ | ✅ |
| Reproducible output (`SOURCE_DATE_EPOCH`) | ✅ | ✅ | ✅ |
| Bill of materials writer | ✅⁷ | ✅⁷ | ✅⁷ |
| Hard links and extended attributes (`._` AppleDouble sidecars) | ✅⁶ | ✅⁶ | 🟡⁴ |
| `sign` (RSA + CMS, Apple timestamp) | ✅⁸ | ✅⁸ | ✅⁸ |
| `verify` (digest, signatures, chain to Apple's roots, timestamp, staple) | ✅⁹ | ✅⁹ | ✅⁹ |
| `notarize` (submit, upload, wait, log) | ✅¹⁰ | ✅¹⁰ | ✅¹⁰ |
| `staple`, `unstaple`, `verify --online` | ✅ | ✅ | ✅ |

¹ The `Size64` tree, which records sizes over 4 GiB, is read on a
best-effort basis: its layout is not documented anywhere and no fixture
exercises it. Sizes under 4 GiB come from the path record and are exact.

² Apple Archive is detected and reported (exit 5); nothing decodes it yet.
Note that `pkgbuild --large-payload` does not produce Apple Archive on
current macOS: it produces a gzip cpio named `LargeSegmentedPayload`,
which is fully supported.

³ Byte-identical to `pkgutil` for every entry. `._` AppleDouble sidecar
entries are turned back into extended attributes where the host takes
them and kept as `._` files where it does not (Linux stores `user.*` and
refuses Apple's names; Windows stores none), so nothing is lost and
building the unpacked tree again reproduces the package. `--xattrs`
selects `apply`, `file` or `skip` explicitly. Hard links are recreated as
hard links.

⁴ On Windows, permission bits and ownership cannot be applied. Symbolic
links need the symlink privilege, and `--symlinks auto` writes the target
as a file where they cannot be created (reported, exit 6 if you asked for
`--symlinks real`). Names Windows cannot store are sanitised and reported.
Windows exposes no inode, so hard links are packaged and extracted as
copies; extended attributes travel as `._` files rather than as host
attributes, which loses nothing, since a build reads them back.

⁵ (superseded by ⁸–¹⁰ below)

⁶ Parity with `pkgbuild` is checked by the macOS acceptance leg. The
`PackageInfo` and `Distribution` documents are compared byte for byte
against the ones `pkgbuild` and `productbuild` write for the same input
(`acceptance/parity_test.go`), with a single normalisation:
`generator-version`, which names the tool and which macospkg must not
copy. That covers attribute order, script timeouts, the absence of a
trailing newline, and the fact that `install-location` is written only
when it is asked for. It also covers which bundle list each kind of bundle
is referenced from: every bundle is version-checked and upgraded, only an
application is relocated and strictly identified, and a bundle nested
inside another is described but never referenced. One deliberate
difference: pkgbuild emits the `bundle` elements, and the bundle-specific
scripts, in its own hash order, which is deterministic for a given set of
names but is neither the walk order nor a sort, and the same package can
order the two differently. macospkg sorts by path instead and the
comparison sorts both sides; the package's own scripts still follow the
bundle-specific ones, which is pkgbuild's rule and is compared exactly. Matching Apple's order would mean
reimplementing its hashing and would make the output depend on which macOS
built the package. Beyond the documents, the same source tree built
both ways gives identical `lsbom` output, identical
`installKBytes`, identical `xar -tf` and `pkgutil --payload-files`, and
`installer` installs the result, sidecars included: extended attributes
are carried exactly as pkgbuild carries them (`._` AppleDouble entries with
pkgbuild's headers and bill-of-materials records; `--exclude-xattr` prunes
host bookkeeping such as `com.apple.provenance`), and hard links are
packaged as one inode. One deliberate difference: `--ownership preserve`
is refused on Windows, and Windows exposes no inode, so hard links become
copies there. `--large-payload`
output is read but not written (its segmentation past 8 GiB is untested).
pbzx output matches pkgbuild's parameters (16 MiB blocks, one xz stream
per chunk, no check, 8 MiB dictionary); pkgbuild has written pbzx for
`--compression latest` on every macOS from 12 to 26, which the fixture
manifest records.

⁷ Round-trip checked against Google's Go installer: expanded and rebuilt
with macospkg, the bill of materials is identical in all 17,356 entries
and `installKBytes` matches exactly. Byte layout differs from `mkbom`'s
(block placement is ours), block contents follow it: the POSIX `cksum` checksum, APFS directory sizes,
`(parent, name)`-ordered leaves, one `HLIndex` entry per inode (a
hard-link set is indexed once, by its last member in path order, as
`mkbom` does).

⁸ Checked against Apple's tools on macOS: `pkgutil --check-signature`
parses our signature and its timestamp, `openssl cms -verify` accepts the
CMS blob, `xar` and `pkgutil --expand` read the signed archive. Only RSA
identities are supported (Developer ID Installer certificates are RSA).
No keychain is used; the identity comes from a PKCS#12 or PEM files.

⁹ Validated against Google's Go installer (Apple-signed, notarized,
stapled): the BER-encoded CMS Apple writes is normalised, Apple's critical
marker extensions are accepted, and the chain reaches the built-in Apple
Root CA. No revocation checking.

¹⁰ The four notary API calls go through `deploymenttheory/go-sdk-appleservices`;
the S3 upload (single PUT, 5 GiB limit), polling and log download are
here. The end-to-end job needs Developer ID secrets and runs on the main
repository's CI only.
