# Payload and Scripts

Both are cpio archives, gzip-compressed, of the tree they carry: the
payload rooted at the install location, the scripts directory rooted at
itself.

## cpio, odc

`pkgbuild` writes the portable ASCII ("odc", SUSv2) format. A header is
76 bytes of fixed-width octal ASCII, then the NUL-terminated name, then
the data, with no alignment:

```
magic "070707"  6
dev             6
ino             6
mode            6   type bits and permissions
uid             6
gid             6
nlink           6
rdev            6
mtime          11
namesize        6   including the NUL
filesize       11
```

Entries are `.`, then `./…` paths with parents before their contents;
the archive ends with an entry named `TRAILER!!!`. A symlink's data is
its target. The reader also accepts the SVR4 `newc` (070701) and
`newc-crc` (070702) formats.

`macospkg build` writes odc with uid 0, gid 0, sequential inodes and
`nlink` 1 for files; scripts are forced to mode 0755 so a script committed
from Windows still runs.

## Containers

The first bytes of a Payload say how it is wrapped:

| Bytes | Container | Written by |
|---|---|---|
| `1f 8b 08` | gzip | `pkgbuild` (default), `macospkg build` |
| `pbzx` | pbzx: BE u64 chunk size, then chunks of `{inflated u64, deflated u64, xz stream or raw}` | `pkgbuild --compression latest` |
| `070707` / `07070` | bare cpio | unusual |
| `AA01`, `YAA1`, `AEA1` | Apple Archive | detected, not decoded (exit 5) |

`pkgbuild --large-payload` writes the gzip cpio under the entry name
`LargeSegmentedPayload` and sets `large-segmented="true"` on the
PackageInfo's payload element; it is read like any other payload.

## numberOfFiles and installKBytes

PackageInfo's payload element records `numberOfFiles`, the count of cpio
entries including `.`, and `installKBytes`, which `pkgbuild` computes as:
every entry except the root rounded up to 512-byte blocks, summed, in
whole kilobytes rounded up; directories count with the size APFS reports
(32 × (children + 2)), symlinks with their target length. `macospkg`
reproduces this exactly, which the parity test checks against `pkgbuild`.
