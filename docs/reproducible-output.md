# Reproducible output

`build` and `product` produce byte-identical packages for identical
input, on every platform, when the build timestamp is pinned.

## How it works

Every timestamp a package carries — cpio and bill-of-materials
modification times, the xar creation time and entry times, the CMS
signing time — is taken from the epoch instead of the clock. Directory
walks are sorted. Inodes are sequential. gzip streams carry no name or
time. The RSA signature (PKCS#1 v1.5) is deterministic, so a signed build
reproduces too, unless it is timestamped: a timestamp token is issued by
Apple's server at signing time and differs every time.

## SOURCE_DATE_EPOCH

The epoch is resolved as `--source-date-epoch` > `SOURCE_DATE_EPOCH` >
`MACOSPKG_SOURCE_DATE_EPOCH` > config file. The bare variable is the
ecosystem standard build systems set, so it outranks the prefixed form.
Without an epoch, source modification times are preserved and the
archive time is now.

## Verifying

```sh
macospkg build ./root a.pkg --identifier com.example.x --version 1 --source-date-epoch 1700000000
macospkg build ./root b.pkg --identifier com.example.x --version 1 --source-date-epoch 1700000000
shasum -a 256 a.pkg b.pkg
```

The acceptance suite does this on Linux, macOS and Windows and compares
the hashes across a run.
