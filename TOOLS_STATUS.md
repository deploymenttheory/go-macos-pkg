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
| Write pbzx, pbze and pbzb payloads (`build --compression pbzx\|lzfse\|lzbitmap`) | ✅⁴ | ✅⁴ | ✅⁴ |
| Read `--large-payload` packages (LargeSegmentedPayload) | ✅ | ✅ | ✅ |
| PackageInfo and Distribution models | ✅ | ✅ | ✅ |
| `info`, `list`, `cat`, `inspect` | ✅ | ✅ | ✅ |
| `expand` (pkgutil --expand / --expand-full parity) | ✅² | ✅² | ✅² |
| `flatten` (pkgutil --flatten parity) | ✅⁹ | ✅⁹ | ✅⁹ |
| `extract` (payload, scripts, regexp, verify) | ✅ | ✅ | 🟡³ |
| `build` (component package) | ✅⁴ | ✅⁴ | ✅⁴ |
| `product` (product archive) | ✅ | ✅ | ✅ |
| Reproducible output (`SOURCE_DATE_EPOCH`) | ✅ | ✅ | ✅ |
| Bill of materials writer | ✅⁵ | ✅⁵ | ✅⁵ |
| Hard links and extended attributes (`._` AppleDouble sidecars) | ✅⁴ | ✅⁴ | 🟡³ |
| `sign` (RSA + CMS, Apple timestamp) | ✅⁶ | ✅⁶ | ✅⁶ |
| `verify` (digest, signatures, chain to Apple's roots, timestamp, staple) | ✅⁷ | ✅⁷ | ✅⁷ |
| `notarize` (submit, upload, wait, log) | ✅⁸ | ✅⁸ | ✅⁸ |
| `staple`, `unstaple`, `verify --online` | ✅¹¹ | ✅¹¹ | ✅¹¹ |
| `receipts` (pkgutil's receipt database, read only) | ✅¹⁰ | ✅¹⁰ | ✅¹⁰ |

¹ Sizes over 4 GiB come from the `Size64` tree, which is keyed by a path's
record block rather than by its path id. That was determined by building a
9 GiB payload with `pkgbuild` and comparing against `lsbom`, and it is
checked by `TestLargePayloadReadsPkgbuildSegments`; keyed the other way a
9 GiB file reads back as 1 GiB, its size modulo 2^32. Sizes under 4 GiB
come from the path record and are exact.

² Byte-identical to `pkgutil` for every entry. `._` AppleDouble sidecar
entries are turned back into extended attributes where the host takes
them and kept as `._` files where it does not (Linux stores `user.*` and
refuses Apple's names; Windows stores none), so nothing is lost and
building the unpacked tree again reproduces the package. `--xattrs`
selects `apply`, `file` or `skip` explicitly. Hard links are recreated as
hard links.

³ On Windows, permission bits and ownership cannot be applied. Symbolic
links need the symlink privilege, and `--symlinks auto` writes the target
as a file where they cannot be created (reported, exit 6 if you asked for
`--symlinks real`). Names Windows cannot store are sanitized and reported.
Windows exposes no inode, so hard links are packaged and extracted as
copies; extended attributes travel as `._` files rather than as host
attributes, which loses nothing, since a build reads them back.

⁴ Parity with `pkgbuild` is checked by the macOS acceptance leg. The
`PackageInfo` and `Distribution` documents are compared byte for byte
against the ones `pkgbuild` and `productbuild` write for the same input
(`acceptance/parity_test.go`), with a single normalization:
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
copies there. `--large-payload` is read and written. It is not a different
container: the same gzip cpio (or pbzx, which is orthogonal) under a
different entry name and marked in the PackageInfo, byte-identical to
pkgbuild's. Past 8 GiB it segments, splitting a file into 1 GiB entries
under one path, and both directions are checked against Apple's tools by
the opt-in tests described in `acceptance/doc.go`: a file too large for an
odc header is not something a committed fixture can carry, so those tests
build one and are skipped unless asked for.
pbzx output matches pkgbuild's parameters (16 MiB blocks, one xz stream
per chunk, no check, 8 MiB dictionary); pkgbuild has written pbzx for
`--compression latest` on every macOS from 12 to 26, which the fixture
manifest records.

⁵ Round-trip checked against Google's Go installer: expanded and rebuilt
with macospkg, the bill of materials is identical in all 17,356 entries
and `installKBytes` matches exactly. Byte layout differs from `mkbom`'s
(block placement is ours), block contents follow it: the POSIX `cksum` checksum, APFS directory sizes,
`(parent, name)`-ordered leaves, one `HLIndex` entry per inode (a
hard-link set is indexed once, by its last member in path order, as
`mkbom` does).

⁶ Checked against Apple's tools on macOS: `pkgutil --check-signature`
parses our signature and its timestamp, `openssl cms -verify` accepts the
CMS blob, `xar` and `pkgutil --expand` read the signed archive. Only RSA
identities are supported (Developer ID Installer certificates are RSA).
No keychain is used; the identity comes from a PKCS#12 or PEM files.

⁷ Validated against Google's Go installer (Apple-signed, notarized,
stapled): the BER-encoded CMS Apple writes is normalized, Apple's critical
marker extensions are accepted, and the chain reaches the built-in Apple
Root CA. `--revocation` asks the responder the signing certificate names
whether the authority has withdrawn it since, which chain verification
cannot tell you and which `pkgutil --check-signature` consults too. It
needs the network, so it happens only when asked for; a certificate naming
no responder is reported as unchecked rather than as good.

⁸ The four notary API calls go through `deploymenttheory/go-sdk-appleservices`;
the S3 upload, polling and log download are here. A file over 5 GiB, which
is S3's limit for one request, goes up in parts, and a failed upload is
aborted rather than left incomplete in Apple's bucket. `--webhook` asks
Apple to post the verdict rather than being polled for it. Uploads go through S3
transfer acceleration, as `notarytool` and Apple's own documented example
both do, and there is no setting to turn it off. `notarize store-credentials`
remembers a set of credentials under a name, as `notarytool
store-credentials` does, but writes a 0600 file rather than a Keychain item
and stores the path to the `.p8` rather than a copy of it: one copy of a
secret is better than two. One deliberate difference from `notarytool`:
authentication is by App Store Connect API key only. `notarytool` also
accepts an Apple ID with an app-specific password, which is not part of the
documented Notary REST API, and guessing at an undocumented endpoint that
handles credentials is not a trade worth making. The end-to-end job needs
Developer ID secrets and runs on the main repository's CI only.

⁹ Expanding and flattening a package returns every entry unchanged, byte
for byte, except the `Scripts` archives, which are built afresh from the
unpacked directory and hold the same paths with the same modes.
`pkgutil --flatten` does the same. The one difference is ownership: an
archive a package arrives with records the uid of whoever built it, and
`pkgutil --flatten` records the uid of whoever expanded it, while macospkg
records root:wheel so the same directory gives the same package on any
machine. The Installer runs scripts as root whatever the archive says.

¹⁰ Reads `<volume>/var/db/receipts`, so it works against a volume mounted
anywhere rather than only the running system. Checked against `pkgutil`:
`receipts info` agrees with `--pkg-info` field for field and `receipts
files` with `--files` path for path, 8,704 of them for one package on the
development machine. It sees less than `pkgutil --pkgs` does, and
deliberately: macOS keeps its own packages in a sealed database reached
through a private interface, which no directory lists. Every identifier
here is one `pkgutil` reports; 32 of its 123 on that machine. The writes,
`--forget` and `--learn`, are a non-goal: both change what the system
believes it installed, and neither belongs to a tool that is not the
Installer.

¹¹ Flat packages and `.app` bundles. A bundle's ticket is keyed on the
CDHash of its main executable's Mach-O code signature (read without
codesign, one per architecture for a universal binary) and written to
`Contents/CodeResources`; verified end to end against a stapled application.
UDIF disk images, which `stapler` also handles, are not done here: a disk
image is `go-apfs-v2`'s domain and needs a notarized image to work out where
the ticket goes. See issue #30.
