# The xar container

A flat package is a xar archive. This is the form `pkg/xar` reads and
writes, established from Apple's xar source, libarchive's writer, 7-Zip's
reader, the Kaitai specification and the output of `pkgbuild` itself (see
`NOTICE` for the references).

## Header (28 bytes, big-endian)

| Offset | Size | Field | Value |
|---:|---:|---|---|
| 0 | 4 | magic | `0x78617221` (`xar!`) |
| 4 | 2 | size | 28 |
| 6 | 2 | version | 1 |
| 8 | 8 | toc_length_compressed | bytes of zlib TOC that follow |
| 16 | 8 | toc_length_uncompressed | bytes of TOC XML |
| 24 | 4 | cksum_alg | 0 none, 1 SHA-1, 2 MD5, 3 SHA-256, 4 SHA-512 |

The reader accepts `size` from 28 to 64 and skips the extra bytes (the
upstream xar fork writes an algorithm name there when `cksum_alg` is 3,
meaning "other"; Apple's fork means SHA-256 by 3). The writer always
writes 28. Both length fields must be exact: libarchive and 7-Zip refuse
an archive whose declared lengths disagree with the bytes.

## Table of contents

Immediately after the header, `toc_length_compressed` bytes of
**zlib** (RFC 1950) data inflate to the TOC XML. The XML is:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<xar>
    <toc>
        <checksum style="sha256"><offset>0</offset><size>32</size></checksum>
        <creation-time>2024-01-02T03:04:05</creation-time>
        <signature style="RSA">...</signature>       <!-- signed archives -->
        <x-signature style="CMS">...</x-signature>   <!-- signed archives -->
        <file id="1">...</file>
    </toc>
</xar>
```

`<xar>` has no namespace and exactly one child. `<creation-time>` has no
`Z`; the per-file `<ctime>`, `<mtime>` and `<atime>` have one. All are
UTC without fractions.

A `<file>` carries `<name>`, `<type>` (`file`, `directory`, `symlink`,
`hardlink`, `fifo`, `socket`, `character special`, `block special`),
`<mode>` (octal string with a leading 0), `<uid>`, `<user>`, `<gid>`,
`<group>`, the three times, and for files a `<data>` element:

```xml
<data>
    <length>350</length>                 <!-- bytes in the heap -->
    <offset>1220</offset>                <!-- heap-relative -->
    <size>676</size>                     <!-- bytes once decoded -->
    <encoding style="application/x-gzip"/>
    <archived-checksum style="sha256">…</archived-checksum>
    <extracted-checksum style="sha256">…</extracted-checksum>
</data>
```

Despite the name, `application/x-gzip` data is zlib-framed, and every
reader inflates it as such. `application/octet-stream` is stored. The
reader also understands `application/zlib`, `x-bzip2`, `x-lzma` and
`x-xz`. Directories nest their children as `<file>` elements. Apple's
tools sometimes write duplicate `<name>` elements; the last one wins.

## Heap

The heap begins at `28 + toc_length_compressed`. Every offset in the TOC
is relative to it. Its first bytes are the TOC digest: the hash, with
the header's algorithm, of the **compressed** TOC bytes. A signed archive
keeps its signatures right after the digest, then the entry data.

Anything after the last byte the TOC accounts for is not part of the
archive; a stapled notarization ticket lives there (see `staple.md`).

## What pkgbuild writes

Observed from `pkgbuild` on macOS 26: a SHA-1 TOC digest, SHA-1 entry
checksums, entries `Bom`, `Payload`, `Scripts`, `PackageInfo` in that
order, `Bom` and `PackageInfo` gzip-encoded, `Payload` and `Scripts`
stored (they are already gzip cpio), real inode/uid/user metadata, a
`FinderCreateTime` element, and `<ea>` entries for extended attributes.
`macospkg build` writes a SHA-256 digest, the same entry order and
encodings, `root`/`wheel` ownership and no `<ea>` elements. Those describe
the archive entries themselves; a payload file's own extended attributes
travel inside the payload, as `._` AppleDouble entries. See
[`payload.md`](payload.md).
