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
- **`pkgutil` parity**: `flatten`, the listing filters, and a reader for a
  volume's receipt database.
- **`notarytool` parity**: `.dmg` and `.zip` submissions, webhooks,
  credential profiles, multipart upload for anything over 5 GiB, and
  property-list output.
- **Revocation checking** in `verify`, which `pkgutil --check-signature`
  does and we did not.
- **`pkgbuild` and `productbuild` parity**: every option either implemented
  or recorded as a non-goal, with the `PackageInfo` and `Distribution`
  documents compared byte for byte against Apple's own output.

## Near term

- **`stapler` parity**: `.app` bundles and `.dmg` images. The bundle design
  is worked out and proven; what it needs is a reader for Mach-O code
  signatures, to compute the CDHash the ticket is keyed on. Disk images
  come from `deploymenttheory/go-apfs-v2` rather than a second
  implementation here. See issue #30.
- **Move the notarization workflow into the SDK**. `pkg/notary` has no
  dependency on anything else here, and an SDK that stops before the upload
  leaves every consumer to write it. See issue #29.

Contributions and priority feedback are welcome via GitHub issues.
