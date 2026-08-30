# Roadmap

Current capabilities are described in [`README.md`](README.md) and their
implementation state in [`TOOLS_STATUS.md`](TOOLS_STATUS.md). This file lists
what is planned or under consideration, roughly in priority order.

## Recently completed

- Read, expand and extract; build and product; sign and verify; notarize
  and staple — all platforms, all checked against Apple's tools on macOS.
- **Hard links and extended attributes** — carried as `pkgbuild` carries
  them, as `._` AppleDouble sidecars, and reapplied on a repack.
- **`pbz*` payloads** — the family is read, and pbzx is written
  (`build --compression pbzx`).
- **Apple's roots embedded** — `verify` chains to G2, G3 and Platform
  without a system trust store.

## Near term

- **`--component-plist`** — per-bundle relocation and version rules.

## Under consideration

- **Apple Archive payloads** — the `--large-payload` / `--compression latest`
  format; detected and reported today.
- **Multipart S3 upload** for packages over 5 GiB.
- **Receipt inspection** of `/var/db/receipts`.

Contributions and priority feedback are welcome via GitHub issues.
