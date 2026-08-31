# The bill of materials (Bom)

Every component package carries a `Bom`: the list of paths the payload
installs, with mode, owner, size and checksum. The Installer copies it to
`/var/db/receipts` as the receipt `pkgutil --files` reads.

Apple does not document the format. `pkg/bom` follows bomutils and
apple-bom for the structure and the output of `pkgbuild`'s `mkbom` for
the details those two leave open. All integers are big-endian.

## Layout

```
Header (32 bytes)
  magic          8  "BOMStore"
  version        4  1
  numberOfBlocks 4  populated blocks (block 0 not counted)
  indexOffset    4  file offset of the block table
  indexLength    4  its length, free list included
  varsOffset     4  file offset of the variable table
  varsLength     4

Block table:  count u32, then count × { address u32, length u32 }.
              Block 0 is the null block. Everything is addressed by index.
Free list:    count u32, then count × { address, length } (may be empty).
Variables:    count u32, then count × { blockIndex u32, nameLength u8, name }.
              Apple writes BomInfo, Paths, HLIndex, VIndex, Size64.
```

## Trees

A variable points at a **tree** block: `"tree"`, version u32 = 1, child
u32 (a Paths block), blockSize u32, pathCount u32, one unknown byte. A
**Paths** block is `isLeaf u16, count u16, forward u32, backward u32`,
then `count × { index0 u32, index1 u32 }`. Leaves chain through
`forward`. In a leaf, `index0` is a **PathInfo** block (`id u32,
recordIndex u32`) and `index1` a **File** block (`parentID u32, name` NUL
terminated). In a branch, `index0` is a child Paths block.

Leaves are ordered by `(parentID, name)`: a directory's children sit
together, by name, after every entry with a lower parent id. `lsbom`
stops reading at the first entry out of that order.

## Path records

```
type            1  1 file, 2 directory, 3 symlink, 4 device
unknown         1  1
architecture    2  15 for ordinary entries
mode            2  st_mode, type bits included (0o100644, 0o40755, …)
uid, gid        4, 4
mtime           4  seconds since 1970
size            4  file size; directory size as APFS reports it, 32×(children+2)
unknown         1  1
checksum        4  see below (device number for a device)
linkNameLength  4  target length including NUL, 0 otherwise
linkName           for a symlink
```

`mkbom` sizes records by type: 31 bytes for a directory, 35 for a file
(four trailing zero bytes), and for a symlink the target with its NUL
plus eight trailing zero bytes. The writer reproduces that.

The checksum is the POSIX `cksum(1)` CRC: polynomial 0x04C11DB7,
unreflected, zero initial value, the length appended, final inversion. An
empty file's checksum is `0xFFFFFFFF`. It is **not** the CRC-32 zlib and
gzip use, which is what bomutils documents.

`BomInfo` is `version u32 = 1, numberOfPaths u32 = paths + 1,
numberOfEntries u32, entries × 16 bytes`; `mkbom` writes one entry whose
third word is the total size of all files. `HLIndex` (see below) carries one entry
per path whose value is an empty 64-byte tree of its own; `VIndex` and
`Size64` are empty trees until they are needed. When a file is larger than
the path record's 32-bit size field can hold, `Size64` carries its true
size: a 4-byte key block holding the **block index of the path's record**,
and an 8-byte value block holding the size, big-endian.

The key is the record block, not the path id. `HLIndex` below is keyed the
same way. Reading it as a path id yields nothing for the file that has an
entry, so the truncated 32-bit size in the record stands and a 9 GiB file
reports as 1 GiB, its size modulo 2^32. This was established by building a
9 GiB payload with `pkgbuild` and comparing against `lsbom`.

## HLIndex and hard links

`HLIndex` is keyed by a path's record block and valued with an empty
64-byte tree per entry. It holds one entry per inode: every path, except
that the members of a hard-link set (same device and inode in the source
tree) contribute only their **last member in Paths order**. From the
links probe, `a.txt`, `b.txt` and `d/c.txt` share an inode and only
`d/c.txt` is indexed. `._` sidecars are always indexed, even when they
share their owner's cpio inode. The tree's path count is the number of
entries.

## Sidecar records

A `._` AppleDouble entry gets a 31-byte record: type 1, architecture 1,
the owner's full mode, zero uid/gid/mtime/size/checksum, one trailing
zero word. See `payload.md` for the sidecar's content and placement.
