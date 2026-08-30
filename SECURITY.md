# Security

Please report suspected vulnerabilities privately through GitHub's
"Report a vulnerability" form on this repository rather than in a public
issue. We will acknowledge the report and work with you on a fix and a
coordinated disclosure.

Fixes land on the latest release. There is no long-term support branch.

## What this tool is trusted with

Two things, and they carry different risks.

**Untrusted input.** A package may come from anywhere, and reading one means
parsing a xar container, a bill of materials, a cpio payload and several
compression formats, then writing files to disk. Everything under `pkg/` that
reads a package treats its input as hostile.

**Secrets.** Signing needs a private key and notarization needs an App Store
Connect API key. Neither leaves the process. Signing happens in memory; the
notary service receives the package's SHA-256 and, if it accepts the
submission, the package bytes. No key material is uploaded, logged or written
to a temporary file.

## What extraction guarantees

`extract` and `expand` write only inside the destination directory. Two
independent defences hold that line:

- Every entry name is checked before use. Absolute paths, names that climb
  above the root with `..`, and names containing NUL are refused
  (`SafeRelPath`). This is lexical, so it stops names that are dangerous on
  their face.
- Every write goes through an `os.Root` anchored at the destination. This
  stops what the lexical check cannot see: a package may write a symbolic
  link and then a path that traverses it, and the link may point anywhere.
  Creating such a link is still allowed, because real packages contain
  absolute symlinks; following one out of the destination is not.

Sizes are bounded throughout: the table of contents at 256 MiB, entry names at
4 KiB, AppleDouble sidecars at 64 MiB, and decompressed chunks against the
size their own header declares.

What extraction does **not** do: it does not apply ownership (that is the
Installer's job and needs root), and on Windows it renames names the file
system cannot store rather than failing. Both are reported.

## What `verify` checks

- The table-of-contents digest matches the archive, compared in constant time.
- The RSA signature over that digest, and the CMS SignedData, including the
  `messageDigest` and `contentType` signed attributes that RFC 5652 requires.
- The certificate chain, against Apple's roots embedded in the binary (pinned
  by SHA-256 fingerprint) or anchors you supply. Apple's own critical
  extensions are accepted; other unknown critical extensions are not.
- Any RFC 3161 timestamp: the authority's signature over the token, that the
  token attests to *this* signature and not another, and that the authority
  chains to a trusted root, judged at the time the token claims. Only a
  timestamp that passes all three is allowed to move the instant at which the
  signing certificate's validity is judged.

A timestamp that is present but cryptographically wrong fails verification. One
that merely cannot be checked, because it does not parse or its authority is
unknown, is reported as unverified and its time is ignored, so the certificate
is judged at the current time and an expired one fails.

## What `verify` does not check

- **Revocation.** No CRL or OCSP is consulted. A revoked but unexpired
  certificate verifies. Use `verify --online` to ask Apple's ticket database
  whether the package was notarized, which is a different question.
- **Whether Apple would install it.** `verify` reports on the signature.
  Gatekeeper and the Installer apply policies of their own.

## Reproducibility as a security property

`build` is deterministic: the same input and the same `SOURCE_DATE_EPOCH`
produce the same bytes. That makes a package independently rebuildable by
anyone who has the source, which is a check on the build machine that no
signature can provide.
