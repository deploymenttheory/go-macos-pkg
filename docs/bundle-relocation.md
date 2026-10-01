# Bundle relocation defaults

Bundle relocation lets Installer find a previously installed bundle that a user
has moved. It is separate from the package-level `relocatable` attribute and
from the package's minimum supported macOS version.

macOS 27's `pkgbuild` defaults `BundleIsRelocatable` to false. Its installed
manual documents this under **COMPONENT PROPERTY LIST**. A default analysis omits
the key; a default package contains `<relocate/>`. Applications still receive
strict-identifier matching. Version checking, upgrades and nested-bundle rules
are unchanged.

## Using the policy

`macospkg` and `flatpkg` use the macOS 27 default on every build host. To retain
the earlier application-only default:

```sh
macospkg build ROOT OUT.pkg --identifier org.example.app --version 1 --legacy-bundle-relocation
macospkg build ROOT components.plist --analyze --legacy-bundle-relocation
```

Library callers set `ComponentOptions.LegacyBundleRelocation`, or use
`AnalyzeBundlesWithOptions(root, AnalyzeOptions{LegacyBundleRelocation: true})`.
`AnalyzeBundles(root)` uses the current default. Build manifests accept
`legacy_bundle_relocation`; an explicitly supplied CLI flag overrides it,
including `--legacy-bundle-relocation=false`.

Explicit component-plist rules take precedence over both defaults. True can
request relocation for an application or another bundle type; false and an
omitted key disable it. Analyze/merge retains existing rules and gives newly
discovered bundles the selected default. Children do not acquire independent
rules just because their parent's default changes.

The existing `NoBundleRelocation` / `--no-bundle-relocation` force override remains
stronger than any explicit rule. This is deliberately stronger than Apple's
undocumented `--no-relocate`, which our capture shows leaves explicit true rules
intact. The tests distinguish these inputs instead of treating the flags as
interchangeable. No native Installer relocation execution is claimed by these
producer/serialization tests.

## Evidence and validation

`testdata/relocation/macos-27` contains unsigned native PackageInfo and analyze
outputs, the exact input trees/plists and a hash manifest. The capture records
macOS version/build, the `pkgbuild` and local manual hashes, commands and UTC time.
Regenerate into a new directory with:

```sh
go run scripts/capture-bundle-relocation.go -out /tmp/new-relocation-capture
```

Eight cases cover default, minimum macOS 26/27, explicit true/false/omitted,
implicit suppression, and explicit true with Apple's suppression flag. Portable
unit tests validate hashes, all policy-list memberships and complete analyzed
plist representations. Separate tests cover the legacy option, CLI/manifest
precedence and macospkg's force override, with no platform skips.

Native acceptance retains the existing full document comparisons for root,
component, prior-package and analyze workflows. On a pre-27 Apple producer it
selects the legacy option explicitly; on macOS 27 it compares the new default.
It does not remove relocation fields from comparisons or infer expectations from
macospkg's output. CI retains Linux, Windows and the existing macOS runner, adds
`xcode-27`, captures each Mac's independent output and gates changed production
statements at 95% coverage. Existing real-package acceptance remains in place.
