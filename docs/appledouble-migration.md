# Shared AppleDouble codec

AppleDouble byte encoding and decoding belong to
`github.com/deploymenttheory/go-apfs-v2/pkg/appledouble`. Filesystem operations,
native attributes and portable carriers belong to the APFS SDK's `pkg/hostdata`
and its focused subpackages. Package archive construction and extraction remain
in this repository.

`pkg/appledouble` retains its public import path, type aliases, constants,
sentinels and forwarding functions. The aliased `File` methods run the shared
implementation. There is no local copy of the codec to drift from APFS.
Package integration tests and native pkgbuild fixture comparisons run here;
core codec safety, coverage and native filesystem qualification run in APFS.

## Published dependency and qualification

The module pins published APFS **v0.14.0**, without a local `replace` or workspace.
APFS PR182 completed the agreed codec, metadata transport, resource-fork and
lifecycle qualification. PR183 separated `hostdata` concerns and passed the same
strict Linux, macOS and Windows gates before release. The existing package
wrapper imports only `pkg/appledouble`, so the `hostdata` rename requires no
wrapper API change.

The upstream qualification includes greater-than-95% per-file coverage gates,
37 fuzz targets, native source/Clang evidence, authorization and sandbox cases,
and independent macOS readback of Linux/Windows images and large resource forks.
Its measured native-version/context boundaries and format/resource constraints
still apply; the release does not establish equivalence for unseen macOS cases.
See the upstream [completion matrix](https://github.com/deploymenttheory/go-apfs-v2/blob/v0.14.0/docs/appledouble-completion-matrix.md)
and [package ownership guide](https://github.com/deploymenttheory/go-apfs-v2/blob/v0.14.0/docs/hostdata-packages.md).

## Downstream release gate

PR72 now adopts the qualified published version. Keep it draft while its final
revision runs the existing Linux/macOS/Windows tests, native package acceptance,
builds and lint. The maintainer merges it after validation. Further package
integration changes belong on that same PR.

Passing upstream tests does not replace downstream validation. The package
wrapper's aliases, sentinel identities and forwarding calls, committed native
AppleDouble fixtures, package build/extract round trips and independent
pkgbuild/pkgutil/lsbom comparisons remain required here. Once those downstream
checks pass, notify the maintainer and resume codesign against the published SDK.
