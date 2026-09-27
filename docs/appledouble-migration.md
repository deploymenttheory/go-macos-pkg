# Shared AppleDouble codec

AppleDouble byte encoding and decoding now belong to
`github.com/deploymenttheory/go-apfs-v2/pkg/appledouble`. Filesystem operations,
native attributes and portable carriers belong to the APFS SDK's `pkg/hostmeta`.
Package archive construction/extraction remains in this repository.

`pkg/appledouble` retains its public import path, type aliases, constants,
sentinels and forwarding functions. The aliased `File` methods run the shared
implementation. There is no local copy of the codec to drift from APFS.
Package integration tests and native pkgbuild fixture comparisons still run here;
the moved decoder safety tests and core codec coverage gate run in APFS.

The relocation uses APFS v0.13.0, which contains the shared codec. It uses no local
`replace` or workspace. This release establishes ownership of the implementation;
codec parity and metadata transport qualification remain separate work.

Keep this migration in draft PR #72 until the outstanding APFS work is complete.
Add further downstream changes to that PR, then update to the qualified APFS
release and notify the maintainer once package validation passes. Do not merge
the draft merely because the relocation release exists.

The next APFS phase must investigate actual macOS packing/unpacking behavior,
including attribute/header limits, names, FinderInfo normalization, empty values,
resource forks, malformed records and memory bounds. Core unit coverage must
exceed 95% on Linux, macOS and Windows. High coverage alone does not establish
native parity or close the host extraction/packing gap.

Codesign remains paused until the shared compatibility work is complete,
validated and released. See the APFS
SDK's `docs/appledouble-migration.md` for the full staged release gate.
