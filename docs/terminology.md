# Terminology

Apple's own names, as used by `man pkgbuild`, `man productbuild`, `man
pkgutil` and `man xar`, are the reference for prose, flags and Go
identifiers. This file exists so the code does not drift into a private
vocabulary.

## Prose

| Write | Not | Because |
|---|---|---|
| flat package | flat pkg, xar package | `pkgutil(1)`: "flat packages" |
| component package | flat package (for the component), leaf package | `pkgbuild(1)` builds "component packages" |
| product archive | distribution package, metapackage | `productbuild(1)` builds "product archives"; `Distribution` is the file inside one |
| payload | files, contents | `pkgbuild --root`: "the payload" |
| bill of materials, Bom | BOM file, manifest | `mkbom(8)`; the entry is named `Bom` |
| table of contents, TOC | index, header XML | `xar(1)` |
| heap | data area, body | the xar term |
| install location | destination, prefix | `--install-location` |
| receipt | install record | `pkgutil --pkgs` lists "receipts" |
| Developer ID Installer certificate | signing cert, installer cert | Apple's certificate name |
| notarize, notarization ticket, staple | notarise, notary ticket, attach | Apple's spelling and verbs, even in British prose |
| PackageInfo, Distribution, Payload, Scripts | lower-case forms | they are entry names |
| extended attributes, xattr | metadata, attributes | `xattr(1)`; the flags are `--xattrs`, `--exclude-xattr` |
| AppleDouble sidecar, `._` entry | resource file, dot-underscore file | the format's own name; the payload entry is `._name` |
| hard-link set | hard-linked files, alias | the members share one inode; `pkgbuild` packages them as one |
| destination root | source directory, input tree | `pkgbuild --root`: "the entire contents of the directory tree at root-path" |
| identity | certificate and key, cert pair | `productsign(1)`: "a certificate and corresponding private key -- together called an identity" |
| the macOS Installer, then the Installer | the installer, Installer.app | Apple's own first-then-short usage |
| pre-install requirements property list | product definition plist | `productbuild(1)` renamed it; the old name is Apple's own former one |
| 32-bit CRC checksum | CRC-32 | `lsbom(1)`'s words. It is the POSIX `cksum(1)` algorithm and **not** zlib's CRC-32, which is why the bare "CRC-32" is wrong; see [`formats/bom.md`](formats/bom.md) |

## Spelling

American, throughout prose, comments and identifiers, because Apple's is:
`--synthesize`, "localized resources", `auth="root"`, `<license>`. So
synthesize, authorization, localization, normalize, serialize, recognize,
finalize, canonicalize, license. This is the one place the project does not
follow British usage, and it is not a style preference: `productbuild
--synthesize` is a flag name, not a word we get to choose.

## Flags

Where Apple has a flag for the same thing, it is spelled Apple's way. Where
this table shows a difference, the difference is the point.

| macospkg | Apple | Why it differs |
|---|---|---|
| `build SRC` | `pkgbuild --root root-path` | the destination root is positional here |
| `build --filter` | `pkgbuild --filter` | same |
| `build --identifier`, `--version`, `--install-location`, `--scripts`, `--ownership`, `--min-os-version`, `--nopayload`, `--large-payload`, `--component`, `--component-plist`, `--prior`, `--analyze`, `--compression` | the same, on `pkgbuild` | same |
| `product --identifier`, `--version` | `productbuild --identifier`, `--version` | same |
| `product --root DIR:INSTALL_PATH` | `productbuild --root root-path install-path` | one flag cannot take two values, so a colon separates them, as for `--component` |
| `product --component PATH:INSTALL_PATH` | `productbuild --component component-path install-path` | as above |
| `sign --p12`, `--cert`, `--key`, `--chain` | `productsign --sign identity` | no keychain exists off macOS, so the identity is a file. `--chain` is the counterpart of Apple's `--cert` (intermediates to embed); our `--cert` is the signing certificate itself |
| `sign --timestamp URL\|none` | `productsign --timestamp`, `--timestamp=none` | timestamping is on by default here, so a bare `--timestamp` would say nothing |
| `sign --digest` | none | Apple gives no control over it |
| `verify` | `pkgutil --check-signature` | every finding is reported separately, and `--revocation` goes further than Apple's |
| `expand`, `expand --full` | `pkgutil --expand`, `--expand-full` | same behaviour, spelled as a subcommand |
| `flatten` | `pkgutil --flatten` | same |
| `list`, `list -l` | `lsbom`, `pkgutil --payload-files` | reads the Bom in place, without expanding first. `-l` is long format, as in `ls(1)`, not `lsbom`'s "list symlinks" |
| `list --only-files`, `--only-dirs`, `--regexp` | the same, on `pkgutil` | same |
| `extract --regexp` | none | `pkgutil` has no extract; `--regexp` matches `list` and `pkgutil`'s spelling |
| `receipts list\|info\|files` | `pkgutil --pkgs\|--pkg-info\|--files` | read-only; `--forget` and `--learn` are a stated non-goal |
| `notarize` | `notarytool submit` | same |
| `notarize info`, `history`, `log`, `wait`, `store-credentials` | the same, on `notarytool` | same |
| `notarize --key`, `--key-id`, `--issuer`, `--profile` | `notarytool --key`, `--key-id`, `--issuer`, `--keychain-profile` | there is no Keychain to store a profile in, so `--profile` names a file |
| `build`/`product` `--notary-*`, `--sign-*` | not applicable | one command here does the work of three of Apple's, so the two credential sets are prefixed apart. Without it `--key` and `--sign-key` would both be key files |
| `staple`, `staple --check`, `unstaple` | `stapler staple`, `stapler validate` | flat packages only; `unstaple` has no counterpart |

## Identifiers

| Concept | Go |
|---|---|
| the xar container | `xar.Reader`, `xar.Writer`, `xar.TOC`, `xar.File` |
| a flat package of either kind | `flatpkg.Package`, `flatpkg.Kind{Component,Product}` |
| one component inside a product archive | `flatpkg.Component` (`Name` is its directory, `""` at the root) |
| PackageInfo, Distribution | `flatpkg.PackageInfo`, `flatpkg.Distribution` (fields named after the XML) |
| bill of materials | `bom.BOM`, `bom.Entry`, `bom.Builder` |
| cpio payload | `cpio.Reader`, `cpio.Writer`, `cpio.Header` |
| a `._` sidecar's content | `appledouble.File`, `appledouble.Attr` |
| where a build takes attributes from | `flatpkg.XattrSource`, `flatpkg.XattrOverride` |
| what an extraction does with sidecars | `flatpkg.XattrMode` |
| signing | `pkgsign.Identity`, `pkgsign.Signer`, `pkgsign.Verify` |
| notary service | `notary.Service`, `notary.Submission`, `notary.Status` |
| ticket, trailer | `staple.Ticket`, `staple.Trailer` |

Exit codes are named in `pkg/exitcode`.

Apple's tool names belong in these docs and in Go comments, not in `--help`
output. The command line says what it does; this file says what Apple calls
it.
