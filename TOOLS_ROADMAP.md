# Roadmap

Current capabilities are described in [`README.md`](README.md) and their
implementation state in [`TOOLS_STATUS.md`](TOOLS_STATUS.md). This file lists
what is planned or under consideration, roughly in priority order.

## Recently completed

- Read, expand and extract; build and product; sign and verify; notarize
  and staple — all platforms, all checked against Apple's tools on macOS.

## Near term

- **Apple root G2/G3** — only the 2006 Apple Root CA (which Developer ID
  chains to) is embedded today.
- **`--component-plist`** — per-bundle relocation and version rules.

## Under consideration

- **Apple Archive payloads** — the `--large-payload` / `--compression latest`
  format; detected and reported today.
- **Multipart S3 upload** for packages over 5 GiB.
- **Receipt inspection** of `/var/db/receipts`.

Contributions and priority feedback are welcome via GitHub issues.
