# Roadmap

Current capabilities are described in [`README.md`](README.md) and their
implementation state in [`TOOLS_STATUS.md`](TOOLS_STATUS.md). This file lists
what is planned or under consideration, roughly in priority order.

## Recently completed

- Read, expand and extract; build and product; sign and verify; notarize
  and staple, on all platforms, all checked against Apple's tools on macOS.
- **Hard links and extended attributes**: carried as `pkgbuild` carries
  them, as `._` AppleDouble sidecars, and reapplied on a repack.
- **`pbz*` payloads**: the whole family is read, LZBITMAP included, and
  pbzx, pbze and pbzb are written (`build --compression pbzx|lzfse|lzbitmap`).
  pbz4 and pbzz are written by `pkg/pbzx` but refused by `build`, because
  macOS cannot read either as a package Payload.
- **Apple's roots embedded**: `verify` chains to G2, G3 and Platform
  without a system trust store.

## Near term

- **`productbuild` parity**: `--product` (the pre-install requirements
  property list), `--synthesize`, `--package-path`, `--scripts`,
  `--plugins`, `--ui`, and the one-step `--component`, `--root` and
  `--content` modes.
- **`pkgutil` parity**: `flatten`, the inverse of `expand`, and listing
  filters.

## Under consideration

- **Apple Archive payloads**: the `--large-payload` / `--compression latest`
  format; detected and reported today.
- **Multipart S3 upload** for packages over 5 GiB.
- **Receipt inspection** of `/var/db/receipts`.

Contributions and priority feedback are welcome via GitHub issues.
