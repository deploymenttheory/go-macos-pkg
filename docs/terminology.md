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

## Identifiers

| Concept | Go |
|---|---|
| the xar container | `xar.Reader`, `xar.Writer`, `xar.TOC`, `xar.File` |
| a flat package of either kind | `flatpkg.Package`, `flatpkg.Kind{Component,Product}` |
| one component inside a product archive | `flatpkg.Component` (`Name` is its directory, `""` at the root) |
| PackageInfo, Distribution | `flatpkg.PackageInfo`, `flatpkg.Distribution` (fields named after the XML) |
| bill of materials | `bom.BOM`, `bom.Entry`, `bom.Builder` |
| cpio payload | `cpio.Reader`, `cpio.Writer`, `cpio.Header` |
| signing | `pkgsign.Identity`, `pkgsign.Signer`, `pkgsign.Verify` |
| notary service | `notary.Service`, `notary.Submission`, `notary.Status` |
| ticket, trailer | `staple.Ticket`, `staple.Trailer` |

Exit codes are named in `pkg/exitcode`; commands mirror Apple's verbs
(`build` ≈ `pkgbuild`, `product` ≈ `productbuild`, `sign` ≈ `productsign`,
`expand`/`verify` ≈ `pkgutil --expand`/`--check-signature`, `notarize` ≈
`notarytool`, `staple` ≈ `stapler`).
