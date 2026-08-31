# The command line

Every flag `macospkg` accepts, what it does, and when reaching for it is
the right call. [`README.md`](../README.md) is the tour; this is the
reference.

The shape is always the same: the package is the first argument, data
goes to stdout, diagnostics and progress go to stderr, and the exit code
says what happened. That makes every command safe to pipe and safe to
test.

## Contents

- [Global flags](#global-flags)
- [Configuration and environment](#configuration-and-environment)
- [Exit codes](#exit-codes)
- [Reading a package](#reading-a-package): `info`, `list`, `cat`, `inspect`
- [Unpacking](#unpacking): `expand`, `flatten`, `extract`
- [Building](#building): `build`, `product`
- [Signing](#signing): `sign`, `verify`
- [Notarizing](#notarizing): `notarize`, `staple`, `unstaple`
- [Receipts](#receipts): `receipts list|info|files`
- [Which command do I want?](#which-command-do-i-want)

---

## Global flags

Accepted by every command.

| Flag | Default | What it does |
|---|---|---|
| `-o, --output text\|json\|plist` | `text` | Output format. `json` gives one document, or one line per entry for listings; `plist` gives the property list macOS reads natively. |
| `-q, --quiet` | off | Suppress progress and non-essential messages. Errors still go to stderr. |
| `--verbose` | off | Diagnostics on stderr. Safe to combine with `-o json`, which writes only to stdout. |
| `--source-date-epoch N` | unset | Pin every timestamp the package carries, in decimal seconds since 1970 UTC. |
| `--temp-dir DIR` | beside the output | Where scratch files go while building. |

**When to use `-o json`.** Any time something other than a person reads
the output. The text format is for humans and its layout may change; the
JSON is a contract. In CI, prefer `macospkg verify -o json | jq -e
'.status == "valid"'` over grepping prose.

**When to use `-o plist`.** When the reader is a macOS tool: `plutil`,
`PlistBuddy`, or anything that speaks property lists already. It carries
the same fields as the JSON, so a script can move between them.

The two differ in one way, because the formats do. JSON is a line per
value, so a listing streams and stays greppable. A property list is one
document, so a listing comes out as a single array once the command
finishes:

```console
$ macospkg -o plist info Foo.pkg | plutil -extract packages.0.version raw -
1.0.0
```

**When to use `--source-date-epoch`.** Whenever you want two builds of
the same input to produce the same bytes, which is most of the time in
CI. Without it the package carries the current time and no two builds
match. See [`reproducible-output.md`](reproducible-output.md).

**When to use `--temp-dir`.** When the output lands somewhere small or
slow, such as a network mount, or when a sandbox restricts writes. The
scratch file is about the size of the finished package.

## Configuration and environment

Precedence is flag, then `MACOSPKG_<FLAG>` environment variable, then
`~/.config/macospkg/config.yaml`. A flag name becomes a variable name by
upper-casing it and replacing dashes with underscores, so `--temp-dir`
reads `MACOSPKG_TEMP_DIR`.

`SOURCE_DATE_EPOCH` is the one exception: the bare variable is the
ecosystem standard, so it outranks `MACOSPKG_SOURCE_DATE_EPOCH`. The full
order is `--source-date-epoch`, then `SOURCE_DATE_EPOCH`, then
`MACOSPKG_SOURCE_DATE_EPOCH`, then the config file.

Credentials are read from the environment so they never appear in a
process listing:

| Variable | Used by |
|---|---|
| `MACOSPKG_P12_PASSWORD` | `sign`, `build --sign-*`, `product --sign-*` |
| `APPLE_KEY_ID`, `APPLE_ISSUER_ID` | `notarize` |
| `APPLE_PRIVATE_KEY_PEM` or `APPLE_PRIVATE_KEY_PATH` | `notarize` |
| `APPLE_API_KEY_ID`, `APPLE_API_ISSUER`, `APPLE_API_KEY` | `notarize`, for interoperability |

The second set is electron-builder's, accepted so a project that already
notarizes does not have to set the same three things twice under different
names. `APPLE_API_KEY` is the `.p8` base64-encoded, which is how a key
survives being a CI secret; a key pasted in unencoded is taken as well. The
names in the first set win where both are set.

## Exit codes

Distinct codes exist so a script can branch without parsing text.

| Code | Meaning | What to do about it |
|---:|---|---|
| 0 | success | |
| 1 | error | A real failure. Read stderr. |
| 2 | usage error | A flag is wrong or missing. Not retryable. |
| 3 | not a flat package | Missing, not a xar, or a xar with no `PackageInfo` or `Distribution`. |
| 4 | credentials missing or rejected | A password, key or API key is absent or wrong. Not retryable without new secrets. |
| 5 | unsupported | An Apple Archive payload, ownership on Windows, a non-RSA key. |
| 6 | partial result | Some entries were skipped. The output is usable but incomplete. |
| 7 | signature or ticket check failed | Treat as a hard failure in a release job. |
| 8 | notarization rejected | Apple refused the package. The log prints the issues. Not retryable unchanged. |
| 9 | wait timed out | Apple is still processing. Retry with `notarize status ID`. |

The contract lives in [`pkg/exitcode`](../pkg/exitcode/exitcode.go). Codes
8 and 9 are deliberately different: 9 says nothing about the verdict, so a
CI job should retry it rather than fail the build.

---

## Reading a package

Nothing in this section writes anything or needs a Mac.

### `info PKG`

The summary you want first: whether it is a component package or a
product archive, each component's identifier, version, install location,
payload size and scripts, the Distribution's title, choices and
architectures, the signing certificate chain and team, and whether a
notarization ticket is stapled.

No flags of its own. Reach for `info -o json` in CI to assert on identity
and version before shipping.

### `list PKG [flags]`

The files the package installs, read from the bill of materials. This is
what `lsbom` prints for an expanded package, without expanding anything,
so it is fast on a multi-gigabyte package.

| Flag | What it does |
|---|---|
| `--archive` | List the xar archive entries instead of payload files. |
| `--component NAME` | Only this component of a product archive, for example `foo.pkg`. |
| `-l, --long` | Mode, uid/gid, size and modification time as well as the path. |
| `--scripts` | List the Scripts archive entries instead of the payload. |
| `--only-files` | Leave out directories. |
| `--only-dirs` | Leave out files. |
| `--regexp RE` | Only paths matching this regular expression. |

Use `list` to answer "what does this install"; use `--archive` to answer
"what is in the container", which is a different question and a common
source of confusion.

`--only-files` and `--only-dirs` are pkgutil's flags, where they narrow a
receipt's listing; they mean the same here for a package that is not
installed yet. `--regexp` is pkgutil's name too, though pkgutil matches
package identifiers with it and this matches paths.

**Getting a `Bom` out for `lsbom`.** `pkgutil --bom` writes the bill of
materials to a temporary file so you can run `lsbom` on it. There is no
separate command for that here, because `list` already prints what `lsbom`
prints. To get the file itself, `cat` it:
`macospkg cat Foo.pkg Bom > Bom && lsbom Bom`. Not `--raw`: that writes the
entry's stored bytes, which for a `Bom` is the gzip it is stored as.

### `cat PKG ENTRY [--raw]` and `cat PKG --payload PATH`

Write one thing to stdout. An archive entry (`PackageInfo`,
`Distribution`, `foo.pkg/Bom`, `Resources/en.lproj/welcome.html`) is
decoded; `--raw` writes the stored bytes without decoding the entry's
encoding, which is what you want when comparing against another tool's
output.

| Flag | What it does |
|---|---|
| `--payload PATH` | Write one file out of the payload, named as the bill of materials names it. |
| `--component NAME` | Which component of a product archive `--payload` reads from. |
| `--raw` | Do not decode the entry's stored encoding. |

`--payload` and a positional `ENTRY` are mutually exclusive.

### `inspect PKG VERB [NAME]`

Raw structures, for when something is wrong and you need to see the
bytes. No flags; the verb selects what to print.

`header`, `toc`, `packageinfo [NAME]`, `distribution`, `bom [NAME]`,
`signature`, `cms`, `rsa`, `digest`, `ticket`.

`inspect PKG digest` prints exactly the bytes a signature covers, which is
the fastest way to work out why a signature does not verify.

## Unpacking

### `expand PKG DIR`

`pkgutil --expand`: every archive entry decoded into a new directory,
Scripts unpacked, Payload left as its gzip cpio. Output is byte-identical
to `pkgutil`'s.

| Flag | Default | What it does |
|---|---|---|
| `--full` | off | Also unpack each Payload into a directory (`pkgutil --expand-full`). |
| `--verify` | off | Verify every archive entry against its stored checksums. |
| `--symlinks auto\|real\|file` | `auto` | How to recreate symbolic links. |
| `--xattrs auto\|apply\|file\|skip` | `auto` | What to do with `._` AppleDouble sidecars. |
| `--hard-links` | `true` | Recreate hard links; `--hard-links=false` writes copies. |

Use `expand` when you want to inspect or edit the package's own parts.
Use `extract` when you want the files it installs.

### `flatten DIR OUT.pkg`

`pkgutil --flatten`: the inverse of `expand`, and the way to change one
thing in a package without rebuilding it. Expand it, edit the
`PackageInfo` or a script, flatten it again.

No flags of its own beyond `--sign-*`.

The contents are taken as they stand. Every file becomes an archive entry
with the bytes it has on disk, so a Payload `expand` left packed goes back
exactly as it came out. A directory named `Scripts` is packed into the
archive a package expects, which is the one name `expand` unpacks.

**Nothing is recomputed.** The bill of materials, the payload counts and
the install size are whatever the directory already says, so editing a
payload here leaves the package describing the old one. Use `build` for
that. A signature and a stapled ticket do not survive either, since both
cover bytes that have just changed.

### `extract PKG DIR`

The payload files as they would land under the install location, with
modes and times.

| Flag | Default | What it does |
|---|---|---|
| `--component NAME` | all | Only this component of a product archive. |
| `--scripts` | off | Extract the Scripts archives instead of the payloads. |
| `--pattern RE` | all | Only payload paths matching this regular expression. |
| `--symlinks auto\|real\|file` | `auto` | `real` fails where the host refuses links; `file` writes the target as a file. |
| `--xattrs auto\|apply\|file\|skip` | `auto` | `apply` sets attributes on the owner, `file` keeps `._` files, `skip` drops them. |
| `--hard-links` | `true` | Recreate hard links, or write copies. |
| `--verify` | off | Check every file against the bill of materials' CRC-32. |

Exit 6 if anything was skipped: device nodes, paths that would escape
`DIR`, links the host refused. Check for it rather than assuming success.

**On `--xattrs auto`.** `._` sidecars become extended attributes where the
host stores them and stay as `._` files where it does not. Nothing is
lost either way, and building the extracted tree again reproduces the
package. Choose explicitly only when you need one behaviour on every
platform.

## Building

### `build SRC [OUT.pkg]`

A component package, what `pkgbuild` makes, from a directory. `SRC` may
also be a munkipkg-style project directory holding `build-info.yaml`,
`payload/` and `scripts/`, in which case flags override the manifest.

**Identity.** `--identifier ID` and `--version V` are required unless a
manifest supplies them.

| Flag | What it does |
|---|---|
| `--identifier ID` | Package identifier, for example `com.example.foo`. The Installer treats two packages as the same product when their identifiers match, so keep it stable across releases. |
| `--version V` | Package version. Compared against an installed copy to decide upgrade or downgrade. |
| `--install-location PATH` | Where the payload is installed. Left out of the document when unset, which the Installer reads as `/`. |
| `--scripts DIR` | Directory of install scripts. `preinstall` and `postinstall` run as the package's own scripts; anything else is available for them to call. |
| `--nopayload` | A scripts-only package with no payload. |
| `--large-payload` | Name the payload entry `LargeSegmentedPayload`, for payloads holding files of 8 GiB or more. Needs `--min-os-version 12.0` or later. |
| `--component BUNDLE` | Package the named bundle rather than a directory. Repeatable. With exactly one, the identifier, version and install location are read out of its `Info.plist`. In this mode the first argument is the output path, not a source. |
| `--prior PKG` | Take the identifier and install location from a previous build of the same package, and increment its version. |

**Payload shape.**

| Flag | Default | What it does |
|---|---|---|
| `--ownership recommended\|preserve\|preserve-other` | `recommended` | `recommended` records everything as `root:wheel`, which is what an installer package should carry. `preserve` records the tree's real owners and is refused on Windows. |
| `--filter RE` | the defaults below | Payload paths to leave out, as a regular expression on `./path`. Repeatable. Naming any filter replaces the defaults rather than adding to them, as pkgbuild does. |
| `--exclude RE` | none | An alias for `--filter`. |
| `--executable RE` | none | Paths to mark executable, for hosts with no execute bit. Repeatable, and the reason a Windows build can produce a working package. |
| `--compression gzip\|none\|pbzx\|latest\|lzfse\|lzbitmap` | `gzip` | The payload container. See below. |
| `--pbzx-block-size N` | 16 MiB | Block size for any `pbz*` container. The default is pkgbuild's. |
| `--xattrs fs\|none` | `fs` | Whether to read extended attributes from the tree, as pkgbuild does. |
| `--exclude-xattr RE` | none | Attribute names to drop. Repeatable. Use it for host bookkeeping such as `com.apple.provenance`. |
| `--hard-links auto\|copy` | `auto` | Whether files sharing an inode are packaged as one. Windows exposes no inode, so they become copies there. |

**Installer behaviour.**

| Flag | Default | What it does |
|---|---|---|
| `--min-os-version V` | unset | Minimum macOS version. Set automatically to 12.0 by `--compression pbzx`, since older systems cannot install it. |
| `--auth root\|none` | `root` | Whether the Installer needs authorisation. |
| `--postinstall-action none\|logout\|restart\|shutdown` | `none` | What the Installer does when it finishes. |
| `--relocatable` | off | Mark the package relocatable. |
| `--no-bundle-relocation` | off | Always install bundles at their packaged paths, instead of following one the user moved. |
| `--preserve-xattr` | off | Set `preserve-xattr` on the package. |

**Other.**

| Flag | What it does |
|---|---|
| `--analyze` | Write a component property list describing the bundles in `SRC` instead of building a package. The second argument is the plist path. |
| `--component-plist FILE` | Per-bundle rules. With `--analyze`, the prior list whose settings are carried forward. |
| `--manifest FILE` | Read options from a `build-info.yaml`, `.json` or `.plist`. |
| `--sign-*` | Sign in the same run. Same flags as `sign`, prefixed `sign-`. |
| `--notarize` | Notarize and staple in the same run. Requires a signing identity. |

**Default filters.** With no `--filter`, `build` leaves out any path
component named exactly `.svn`, `CVS` or `.DS_Store`, whether it is a file
or a directory, which is what `pkgbuild` does. Names that merely resemble
those, such as `CVSdir` or `.svnfile`, are kept. A directory the filters
empty is dropped too, and that cascades to its parents; a directory that
was already empty on disk is packaged.

Naming even one filter turns the defaults off, so to keep everything pass
a pattern that cannot match, such as `--filter 'a^'`. That matters when
rebuilding a tree you got from `expand` or `extract`, where you want
fidelity rather than source-tree hygiene.

**Three ways to say what the package is.** `--identifier` and `--version`
are the usual answer. `--component Foo.app` reads all three out of the
bundle: the identifier from `CFBundleIdentifier`, the version from
`CFBundleShortVersionString` normalised to three numbers (`4` and `4.0`
both become `4.0.0`, `4.0.1.2` becomes `4.0.1`), and the install location
from the directory holding the bundle, which is an absolute build path and
almost never what you want to ship, so pass `--install-location` as well.
`--prior old.pkg` reads the identifier and install location from a package
you built before and increments its version to the next integer, so a
prior `1.0.0` gives `2` and a prior `9.9.9` gives `10`.

A component build also reports different payload numbers from a build of
the same files as a directory, because it counts the bundle's own entries
rather than the archive's: no `._` sidecars, and no directory sizes. That
is pkgbuild's behaviour, not a choice made here.

**Per-bundle rules.** Without a component property list every bundle in
the payload gets the same treatment: version-checked and upgraded, and, if
it is an application, relocated and matched on a strict identifier.
Frameworks, plug-ins and the rest are installed where the package puts
them.

To vary that, run `build ROOT components.plist --analyze` to get a
template, edit it, then pass it back with `--component-plist`. The keys
are Apple's:

| Key | Effect |
|---|---|
| `BundleIsVersionChecked` | Adds the bundle to `bundle-version`, so a newer copy on disk is left alone. |
| `BundleOverwriteAction` | `upgrade` replaces the bundle atomically, dropping paths it no longer has; `update` overwrites and leaves the rest, and installs nothing where there is no bundle already. |
| `BundleIsRelocatable` | Installs over a copy the user has moved, rather than at the packaged path. |
| `BundleHasStrictIdentifier` | Requires the identifier at the install path to match. |
| `BundlePreInstallScriptPath`, `BundlePostInstallScriptPath` | Scripts for this bundle alone, named relative to `--scripts`. |
| `BundleInstallScriptTimeout` | Seconds before the system kills the script. Honoured by macOS 15 and later. Absent means 21600, far longer than the 600 a package's own scripts get. |
| `ChildBundles` | Bundles nested inside this one. They are described but never given rules of their own: they are installed as part of the bundle that contains them. |

A component property list is **exhaustive**. A bundle it does not name is
not described at all, so adding a bundle to the payload means adding it to
the list. That is what `--analyze --component-plist old.plist` is for: it
re-reads the root and carries the settings of bundles that still exist
forward onto the fresh list.

**Choosing a compression.** `gzip` is pkgbuild's default and the only
container every macOS can install, so it is the right answer unless you
have a reason. `none` stores the cpio as it stands, which is worth it only
when the payload is already compressed and the second pass would buy little
while costing installation time. `pbzx` (which pkgbuild spells `latest`) is smaller but
needs macOS 12. `lzfse` and `lzbitmap` write containers macOS reads but
pkgbuild never writes. The details are in
[`formats/payload.md`](formats/payload.md).

### `product OUT.pkg --package X.pkg [...]`

A product archive, what `productbuild` makes, from component packages.
This is the form to distribute and the form notarization expects.

| Flag | What it does |
|---|---|
| `--package PKG` | A component package to add. Repeatable. |
| `--root DIR`, `--root-install-path PATH` | Package a directory tree as its own component. productbuild spells the install path as a second argument to `--root`. |
| `--content DIR` | Package a directory's contents as their own component, for in-app content. |
| `--component BUNDLE[:INSTALL_PATH]` | Package a bundle as its own component, reading its identity from `Info.plist`. Repeatable. |
| `--component-compression MODE` | Payload container for what `--component` builds. A package given with `--package` keeps whatever container it was built with, which is productbuild's limit too. |
| `--large-payload` | Build the components here with the format that carries files of 8 GiB and over. |
| `--distribution FILE` | Use this Distribution instead of synthesising one, naming its packages by file name. |
| `--package-path DIR` | Where to look for the packages a `--distribution` names. Repeatable; the working directory is searched too. |
| `--synthesize` | Write the synthesised Distribution to the output path instead of building an archive. |
| `--scripts DIR` | Embed a directory as the `Scripts` entry, for the `system.run()` commands a Distribution invokes. Not a component's install scripts: nothing here runs on its own. |
| `--plugins DIR` | Embed a directory as the `PlugIns` entry: an `InstallerSections.plist` and the Installer plug-in bundles. |
| `--ui NAME` | The interface the synthesised `choices-outline` is for. `mas` marks one meant for the Mac App Store. |
| `--resources DIR` | Embed a directory as `Resources/`, for the welcome, licence and background files the Distribution names. |
| `--title T` | Title for the synthesised Distribution. |
| `--product-id ID`, `--product-version V` | Identity for the synthesised Distribution. |
| `--product FILE` | Pre-install requirements property list: what the machine must have before the Installer will proceed. See below. |
| `--min-os-version V` | Adds a `volume-check` with an `allowed-os-versions` floor. A shorthand for a `--product` list carrying only `os`. |
| `--host-architectures A,B` | Comma-separated `hostArchitectures`. Defaults to `x86_64,arm64`, which is what productbuild writes. |
| `--sign-*`, `--notarize` | As for `build`. |

Without `--distribution` the synthesised document installs every package
with no customisation, byte for byte as productbuild writes it. Supply
your own when you need choices, localisation or scripts.

**Pre-install requirements.** `--product` takes the property list
`productbuild --product` takes, and each key drives one part of the
Distribution:

| Key | Becomes |
|---|---|
| `os` (array of strings) | `<allowed-os-versions>`. The highest is open-ended; each other one is capped just below the next release of its line, so `10.5.4` is capped `before 10.6` and `13.4` `before 13.5`. |
| `arch` (array) | `hostArchitectures` on `<options>`. |
| `ram` (real, gigabytes) | `<installation-check><ram min-gb="…"/>`. |
| `bundle` (array), `all-bundles` (bool) | `<volume-check><required-bundles>`: bundles that must already be installed. |
| `gl-renderer`, `cl-device`, `metal-device`, `single-graphics-device` | `<installation-check><required-graphics>`. |
| `sysctl-requirements` (predicate) | `<installation-check><hardware-properties>`. |
| `home` (bool) | `<domains>`, opening installation into a user's home directory. |

A graphics requirement implies a system version that can run the check:
10.6.8 for OpenGL, 10.7 for OpenCL, 10.14.4 for Metal. A version you asked
for that is below the floor is dropped rather than raised, since no such
system could run the check anyway.

Two notes on fidelity. The manual says `sysctl-requirements` raises the
floor to 10.10; current productbuild adds no version check for it, and this
follows the tool. And productbuild passes the three graphics predicates
through `NSPredicate`, which rewrites them (`version >= 2.0` comes back as
`version >= 2`); there is no `NSPredicate` here, so they are written as you
gave them.

**Two shapes of Distribution.** The document `--synthesize` writes is not
the one a package carries, and productbuild rewrites one into the other as
it embeds it. `product` does the same, so a distribution you hand it can
name its packages as plain file names:

| | written by `--synthesize` | carried in a package |
|---|---|---|
| package reference | `Foo.pkg` | `#Foo.pkg` |
| sizes | none | `installKBytes`, `updateKBytes` |
| per-package stub | `<pkg-ref id="X"/>` | `<pkg-ref id="X"><bundle-version/></pkg-ref>` |

A document handed to `--distribution` is re-serialised rather than edited,
which additionally declares `standalone="yes"` on it. One synthesised
straight into a package does not, so the two routes to a package differ by
that one attribute. Both match productbuild.

A document that already names archive entries is left exactly as it is, so
expanding a package and building it again gives back the same bytes.

**Building the payload here.** `--root`, `--content` and `--component`
save a separate `build` step. Each names the component after its source
rather than after the archive, and gives it version `0` unless it can read
one: `--component` takes the identifier, the version and the title from the
bundle's `Info.plist`, and records the bundle's details in the Distribution
so the Installer can version check without opening the payload.

**Large payloads.** Only macOS 12 and later can read one, so a product
carrying such a component requires it: the synthesised Distribution gets an
`allowed-os-versions` floor of `12.0.0`. That happens whether the format
was asked for with `--large-payload` or the component simply arrived with
one already. A version you asked for that is higher stands alone.

**On `--ui`.** It renames as well as labels. The top choice takes the
interface's own name instead of `default`, and every package's choice and
reference is prefixed with it, so a document can carry an outline per
interface without their ids colliding. It shapes a synthesised document
only, so passing it with `--distribution` is refused rather than ignored.

The usual route is `product --synthesize dist.xml --package A.pkg`, edit
`dist.xml` to add choices or a licence, then
`product Out.pkg --distribution dist.xml --package-path .`.

## Signing

### `sign PKG OUT.pkg`

What `productsign` does: an RSA and a CMS signature over the table of
contents, the certificate chain embedded, timestamped by Apple unless
told otherwise. No keychain is involved, which is what lets it run
anywhere.

| Flag | What it does |
|---|---|
| `--p12 FILE` | PKCS#12 holding the Developer ID Installer certificate and key. |
| `--p12-password-stdin` | Read the PKCS#12 password from stdin. Prefer this. |
| `--p12-password PW` | The password as an argument. Visible in a process listing; prefer the two above or `MACOSPKG_P12_PASSWORD`. |
| `--cert PEM`, `--key PEM` | A PEM certificate and its key, instead of a PKCS#12. |
| `--chain PEM` | Intermediates to embed. |
| `--timestamp URL` | A different RFC 3161 server. Apple's is the default. |
| `--no-timestamp` | Do not timestamp. Only for an air-gapped build; a Developer ID signature should be timestamped so it outlives the certificate. |
| `--digest sha256\|sha1` | The table-of-contents digest. `sha256` unless you must match something old. |

A stapled ticket is removed on signing, because re-signing invalidates
it. Staple again afterwards.

### `verify PKG`

What `pkgutil --check-signature` does, with every finding reported
separately: digest, RSA, CMS, chain to Apple's built-in roots, team,
timestamp and staple.

| Flag | What it does |
|---|---|
| `--team-id ID` | Require this Apple team identifier. Use it to catch a package signed with the wrong certificate. |
| `--require-developer-id` | Fail unless the signer is a Developer ID Installer certificate. |
| `--require-stapled` | Fail unless a notarization ticket is stapled. |
| `--online` | Ask Apple whether this exact package was notarized. Needs network. |
| `--revocation` | Ask the certificate authority whether the signing certificate has been withdrawn since it was issued. Needs network. |
| `--trust-anchors PEM` | Trust these roots instead of Apple's. For testing with your own CA. |
| `--allow-untrusted` | Report an untrusted chain without failing. |

Exit 7 on any failure. The release-gate form is
`macospkg verify --require-developer-id --require-stapled --online
--revocation PKG`, which checks everything that matters before you ship.

**On `--revocation`.** Verifying the chain says a certificate was validly
issued and has not expired. It says nothing about whether the authority has
since withdrawn it, which is what happens when a Developer ID key is lost
or abused. `pkgutil --check-signature` notices, because Security.framework
consults the responder; this did not until you ask it to.

A certificate that names no responder is reported as unchecked rather than
as good, and is not a failure: plenty of certificates carry none.

## Notarizing

### `notarize PKG`

What `notarytool submit` does: register the package, upload it, and
optionally wait for the verdict and staple the ticket.

| Flag | Default | What it does |
|---|---|---|
| `--wait` | off | Poll for the verdict. Exit 0 accepted, 8 rejected, 9 timed out. |
| `--staple` | off | Staple the ticket once accepted. Implies `--wait`. |
| `--timeout D` | `30m` | How long `--wait` waits before exiting 9. |
| `--poll-interval D` | `30s` | How often it polls. |
| `--log` | off | Print the developer log when finished. Always printed on rejection. |
| `--name N` | the file name | The submission name shown in App Store Connect. |
| `--force` | off | Submit a file without checking it first. |
| `-p, --profile NAME` | none | Credentials stored earlier under this name. |
| `--webhook URL` | none | A public URL for Apple to post the verdict to when notarization finishes, so a job need not sit and poll. |

**What can be submitted.** Apple's service takes a flat package, a disk
image or a zip archive. A package is opened and its signature checked
before anything is uploaded, because that can be done locally and an
unsigned package would only be rejected later. A `.dmg` or `.zip` goes up
as it is: the signature that matters is on what it contains, which Apple
checks and reports in the log. An `.app` bundle is a directory, so archive
it first, which `notarize` says rather than letting the upload fail.

`--force` skips the check entirely.

**On acceleration.** Uploads go through S3 transfer acceleration, over
Amazon's edge network rather than straight to the region. There is no
setting for it: Apple's own documented example asks for it, building its S3
client with `use_accelerate_endpoint: True`, and `notarytool` turns it on
too.

**Large files.** There is no size limit. A file over 5 GiB, which is what
S3 will take in one request, is uploaded in parts, and a failure aborts the
upload rather than leaving an incomplete one in Apple's bucket.

Credentials come from `--key-id`, `--issuer-id` and `--private-key`
(an App Store Connect `AuthKey.p8`), or from `APPLE_KEY_ID`,
`APPLE_ISSUER_ID` and `APPLE_PRIVATE_KEY_PEM` or
`APPLE_PRIVATE_KEY_PATH`.

Subcommands, all taking the same credential flags:

| Command | What it does |
|---|---|
| `notarize status ID` | The verdict for one submission. |
| `notarize wait ID` | Poll an existing submission. Takes `--timeout` and `--poll-interval`. |
| `notarize log ID` | The developer log for one submission. |
| `notarize list` | Recent submissions for the team. |
| `notarize store-credentials NAME` | Remember a set of credentials under a name. |

**On profiles.** `notarytool` keeps credentials in the Keychain, which does
not exist off macOS. `store-credentials` writes a small file instead, under
the directory the operating system gives for configuration, readable only
by you.

It stores a **pointer, not a copy**: the key ID, the issuer ID and the path
to the `.p8`. The private key stays where you keep it, so there is one copy
of the secret rather than two. That does mean the `.p8` has to stay put,
unlike a notarytool profile which is self-contained.

Rejections are code 8 and timeouts are code 9 for a reason: a timeout
says nothing about the verdict, so retry it with `notarize status`
rather than failing the build.

### `staple PKG [OUT.pkg]` and `unstaple PKG [OUT.pkg]`

What `stapler staple` does: fetch the ticket from Apple's public database
and append it, so Gatekeeper can check it with no network.

| Flag | What it does |
|---|---|
| `--check` | Report whether a ticket is present, changing nothing. Exit 7 if there is none. |
| `--ticket FILE` | Staple a ticket fetched elsewhere, instead of looking it up. |

Staple after signing, never before: signing invalidates a ticket.

## Receipts

### `receipts list|info|files`

What a volume records about the packages installed on it. A receipt is two
files the Installer wrote under `var/db/receipts`: a property list of what
was installed and when, and a bill of materials listing every path.

| Command | What it does |
|---|---|
| `receipts list [--regexp RE]` | The package identifiers on the volume. |
| `receipts info PKGID` | Version, install location, when it was installed and by what. |
| `receipts files PKGID [--only-files\|--only-dirs]` | Every path the package installed. |

`--volume PATH` picks the volume; the running system's root by default.

**Why a directory and not the system.** This reads `var/db/receipts`
directly, so it works against a volume mounted anywhere, from any operating
system: point `--volume` at a disk and see what a Mac has installed on it.

**What it cannot see.** On a running macOS, `pkgutil --pkgs` lists more
than this. The packages that make up the system itself are held in a sealed
database that pkgutil reaches through a private interface, and no directory
on disk lists them. Everything here is a package pkgutil knows, but not the
other way round: on this machine, 32 of the 123 pkgutil reports. What you
see is what was installed onto the volume, which is the part worth
auditing.

**Nothing here writes.** `pkgutil` can forget a receipt (`--forget`) or
relearn one (`--learn`). Both change what the system believes it installed
and need root, and neither is something this tool should do behind the
Installer's back.

---

## Which command do I want?

| I want to | Command |
|---|---|
| know what this package is | `info` |
| know what it installs | `list` |
| know what is inside the container | `list --archive` |
| read one file it installs | `cat PKG --payload ./path` |
| take the package apart | `expand --full` |
| put it back together after an edit | `flatten` |
| get the files out | `extract` |
| work out why a signature fails | `inspect PKG digest`, then `verify` |
| build one package from a directory | `build` |
| combine packages for distribution | `product` |
| sign something already built | `sign` |
| check something before shipping | `verify --require-developer-id --require-stapled --online` |
| get it notarized | `notarize --wait --staple` |
| know what a disk has installed on it | `receipts list --volume /Volumes/X` |
| do the whole release in one run | `build ... --sign-p12 F --notarize` |

## See also

- [`signing.md`](signing.md) and [`notarization.md`](notarization.md) for
  the workflows end to end.
- [`reproducible-output.md`](reproducible-output.md) for what makes two
  builds identical.
- [`formats/`](formats/) for the byte-level details of each structure.
- [`terminology.md`](terminology.md) for why the flags are named as they
  are.
