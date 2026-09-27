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

The relocation temporarily uses an immutable APFS commit pseudo-version because
no release contains the moved package yet. It uses no local `replace`, workspace,
or invented future release tag. Merge the APFS relocation first. Replace this pin
with a released version once the codec and metadata transport work are qualified.

The next APFS phase must investigate actual macOS packing/unpacking behavior,
including attribute/header limits, names, FinderInfo normalization, empty values,
resource forks, malformed records and memory bounds. Core unit coverage must
exceed 95% on Linux, macOS and Windows. High coverage alone does not establish
native parity or close the host extraction/packing gap.

Do not publish an APFS version simply to unblock the relocation. Codesign remains
paused until the shared work is complete, validated and released. See the APFS
SDK's `docs/appledouble-migration.md` for the full staged release gate.
