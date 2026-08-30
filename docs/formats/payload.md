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
| `pbzx` | pbz* with xz chunks | `pkgbuild --compression latest`, `macospkg build --compression pbzx` |
| `pbze`, `pbz4`, `pbzz` | pbz* with LZFSE, Apple-framed LZ4, zlib chunks | `aa archive`, libParallelCompression; read |
| `pbzb` | pbz* with LZBITMAP | detected, not decodable (exit 5): no public specification |
| `070707` / `07070` | bare cpio | unusual |
| `AA01`, `YAA1`, `AEA1` | Apple Archive | recognised; the Installer does not read it either |

`pkgbuild --large-payload` writes the gzip cpio under the entry name
`LargeSegmentedPayload` and sets `large-segmented="true"` on the
PackageInfo's payload element; it is read like any other payload.

### The pbz* container

```
magic      4  "pbz" + algorithm letter (x xz, e LZFSE, 4 LZ4, z zlib, b LZBITMAP)
blockSize  8  BE u64: the writer's block size
chunks, to end of input:
  inflated 8  BE u64, decoded size
  deflated 8  BE u64, stored size
  data        compressed, or the plain bytes when deflated == inflated
```

No trailer. What `pkgbuild --compression latest` writes, from its own
output on macOS 26 for every `--min-os-version` from 12.0 to 26.0: `pbzx`,
16 MiB blocks, each chunk one complete xz stream with **no integrity
check** (stream flags `00 00`), one LZMA2 block with an 8 MiB dictionary
(property `0x16`, the equivalent of `xz -6`). PackageInfo carries nothing
about the container; `pkgbuild` only insists on `--min-os-version` ≥ 12.0,
which `macospkg build --compression pbzx` mirrors. Scripts stay gzip.

Apple's LZ4 framing (`pbz4`, and `aa -a lz4`) is not the LZ4 frame format:
blocks are `"bv41"` + LE u32 decoded size + LE u32 encoded size + an LZ4
block, `"bv4-"` + LE u32 size + raw bytes, and `"bv4$"` ends the stream;
blocks decode to at most 64 KiB.

## numberOfFiles and installKBytes

PackageInfo's payload element records `numberOfFiles`, the count of cpio
entries including `.`, and `installKBytes`, which `pkgbuild` computes as:
every entry except the root rounded up to 512-byte blocks, summed, in
whole kilobytes rounded up; directories count with the size APFS reports
(32 × (children + 2)), symlinks with their target length. `macospkg`
reproduces this exactly, which the parity test checks against `pkgbuild`.
