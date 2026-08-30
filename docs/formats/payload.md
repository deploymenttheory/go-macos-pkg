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
| `pbze` | pbz* with LZFSE chunks | `aa archive -a lzfse`, `macospkg build --compression lzfse` |
| `pbz4`, `pbzz` | pbz* with Apple-framed LZ4 or zlib chunks | `aa archive`, libParallelCompression; `pkg/pbzx` writes them, `macospkg build` refuses |
| `pbzb` | pbz* with LZBITMAP | detected, not decodable (exit 5): no public specification |
| `070707` / `07070` | bare cpio | unusual |
| `AA01`, `YAA1`, `AEA1` | Apple Archive | recognised; the Installer does not read it either |

`pkgbuild --large-payload` writes the gzip cpio under the entry name
`LargeSegmentedPayload` and sets `large-segmented="true"` on the
PackageInfo's payload element; it is read like any other payload.

### Which containers a package may use

`pkgbuild` writes only gzip and pbzx, so what macOS accepts for the other
members of the family is not something Apple documents. Measured on macOS
26, by building the same tree in each container and handing it to Apple's
own readers:

| Container | `aa` reads ours | `pkgutil --expand-full` | `macospkg build` |
|---|---|---|---|
| `pbzx` | yes | yes | `--compression pbzx` |
| `pbze` | yes | yes | `--compression lzfse` |
| `pbz4` | yes | **no**: `cpio read error: bad file format` | refused |
| `pbzz` | yes | **no**: `cpio read error: bad file format` | refused |

The `aa` column matters because it separates two explanations for the
`pkgutil` failure. Apple's own `aa list` reads the pbz4 and pbzz streams
`pkg/pbzx` writes, so those streams are well formed; it is macOS's package
reader that will not take them. That is why `pkg/pbzx` still writes all
four while `flatpkg.ParseCompression` accepts only the two macOS can
install.

`pbze` is the surprise: `pkgbuild` never emits it, but macOS unpacks it
happily, single-chunk and multi-chunk alike, and `installer` installs the
result. The acceptance suite pins all of this.

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

## Hard links

pkgbuild writes every member of a hard-link set as a full entry (each
carries the data) with the same cpio inode number and a link count of
the set's size; the bill of materials records each member normally and
indexes the inode once (see `bom.md`). `installKBytes` counts the set
once. `macospkg build` does the same when the host reports inodes
(`--hard-links auto`); `--hard-links copy` packages the members as
separate files, which is all Windows can do. `extract` and `expand --full`
link later members to the first (`--hard-links=false` writes copies).

## Extended attributes: AppleDouble sidecars

pkgbuild carries a file's extended attributes as a second entry named
`._<name>` beside it, holding an AppleDouble file. Pinned from
`testdata/cli/component-links.probe.json` (pkgbuild on macOS 26):

- A file's sidecar follows the file; a directory's follows its whole
  subtree; the payload root gets none. Symlinks get sidecars too.
- Sidecar cpio header: mode `100644` whatever the owner is; uid, gid,
  mtime and link count copied from the owner. A sidecar of a hard-linked
  file shares the owner's inode number; other sidecars get their own.
- Sidecar Bom record: 31 bytes: type 1 (file), architecture 1, the
  owner's full mode (so `lsbom` shows a directory's sidecar with a
  directory mode), and zero uid, gid, mtime, size and checksum. Sidecars
  count in `numberOfFiles` but not in `installKBytes` or in a directory's
  size.
- The Scripts archive gets the same treatment.

The AppleDouble layout (`pkg/appledouble`):

```
0    00 05 16 07                magic
4    00 02 00 00                version
8    "Mac OS X        "         16-byte filler
24   u16 2                      entries
26   {9, 50, attrEnd-50}        Finder info entry: covers everything up to the resource fork
38   {2, attrEnd, len(rsrc)}    resource fork entry
50   32 bytes                   Finder info (com.apple.FinderInfo, else zeros)
82   2 bytes                    pad
84   "ATTR" u32 debug_tag=0 u32 total_size=attrEnd u32 data_start
     u32 data_length u32 reserved[3] u16 flags=0 u16 num_attrs
120  entries                    {u32 offset, u32 length, u16 flags=0, u8 namelen incl. NUL, name NUL}
                                each padded to a multiple of 4; sorted by name
data_start                      values, in entry order
attrEnd                         resource fork (com.apple.ResourceFork)
```

`com.apple.FinderInfo` and `com.apple.ResourceFork` go in their slots,
never in the attribute list. `macospkg build` reads attributes from the
tree (all of them on macOS, `user.*` on Linux, none on Windows), from a
manifest's `file_xattrs`, and from `._` files already in the tree (a tree
exported from macOS, or one an extraction left behind), and encodes the
same bytes on every host, so a Linux build of a manifest reproduces a
macOS build. `--exclude-xattr` drops names such as `com.apple.provenance`,
which macOS stamps on files a process creates and which pkgbuild copies
into every package built on such a host.

### Unpacking and packing again

Nothing is dropped on a host that cannot store Apple's attributes.
`extract` and `expand --full` set each attribute individually; Linux
takes `user.*` and refuses the rest, so under `--xattrs auto` the refused
ones are written back out as a `._` sidecar file beside their owner,
the same name and encoding a build reads. Building that tree again
restores the whole set, so an unpack/repack round trip on Linux or
Windows reproduces the package it started from. The count is reported as
`xattrFiles`; an extraction that keeps attributes this way is complete,
not partial. An explicit `--xattrs apply` reports the refused names as
skipped instead, because the caller asked for them to be applied;
`--xattrs file` writes every sidecar as a file and applies none.

### Overriding attributes on a repack

What a tree carries is packaged again by default. A manifest's
`file_xattrs` overrides that, in the order the rules are written:

```yaml
file_xattrs:
  # One file: com.example.tag is added, or given a new value.
  - path: usr/local/bin/tool
    xattrs:
      com.example.tag: aGVsbG8=          # base64
  # A folder and everything beneath it; replace makes the listed
  # attributes the whole set for those paths.
  - path: usr/local/share/
    replace: true
    xattrs:
      com.example.owner: dGVhbQ==
  # Replace with nothing strips a path.
  - path: usr/local/cache/
    replace: true
```

A rule's own values are not subject to `--exclude-xattr`: naming a path
and a value is more specific than filtering a name across the tree. A
rule that matches no payload entry is an error, so a mistyped path does
not pass silently.
