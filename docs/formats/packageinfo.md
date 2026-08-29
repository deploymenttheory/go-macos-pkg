# PackageInfo and Distribution

## PackageInfo

The manifest of a component package, as `pkgbuild` writes it:

```xml
<?xml version="1.0" encoding="utf-8"?>
<pkg-info overwrite-permissions="true" relocatable="false" identifier="com.example.foo"
          postinstall-action="none" version="1.0" format-version="2"
          generator-version="InstallCmds-864.12 (25F80)" install-location="/" auth="root">
    <payload numberOfFiles="27" installKBytes="306"/>
    <bundle-version>
        <bundle id="com.example.foo" path="./Applications/Foo.app"
                CFBundleShortVersionString="1.0" CFBundleVersion="100"/>
    </bundle-version>
    <upgrade-bundle/><update-bundle/><atomic-update-bundle/><strict-identifier/>
    <relocate><bundle id="com.example.foo"/></relocate>
    <scripts>
        <preinstall file="./preinstall"/>
        <postinstall file="./postinstall" timeout="600"/>
    </scripts>
</pkg-info>
```

Attributes: `identifier`, `version`, `format-version` (2),
`install-location`, `auth` (`root`|`none`), `overwrite-permissions`,
`relocatable`, `generator-version`, `postinstall-action`
(`none`|`logout`|`restart`|`shutdown`), `minimumSystemVersion`,
`preserve-xattr`, `useHFSPlusCompression`. Recognised scripts:
`preflight`, `preinstall`, `preupgrade`, `postinstall`, `postupgrade`,
`postflight`. Every bundle found in the payload (directories named
`*.app`, `*.framework`, `*.bundle`, `*.plugin`, `*.kext`, … with an
`Info.plist`) is listed in `bundle-version`, and in `relocate` unless
bundle relocation is suppressed.

## Distribution

The script of a product archive, run by the Installer:

```xml
<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
    <title>Foo</title>
    <options customize="never" require-scripts="false" hostArchitectures="arm64,x86_64"/>
    <volume-check><allowed-os-versions><os-version min="12.0"/></allowed-os-versions></volume-check>
    <choices-outline>
        <line choice="default"><line choice="com.example.foo"/></line>
    </choices-outline>
    <choice id="default"/>
    <choice id="com.example.foo" visible="false"><pkg-ref id="com.example.foo"/></choice>
    <pkg-ref id="com.example.foo" version="1.0" onConclusion="none" installKBytes="306" auth="root">#Foo.pkg</pkg-ref>
</installer-gui-script>
```

The text of a top-level `pkg-ref` is the component's location in the
archive (`#Foo.pkg`). Other elements: `domains`, `product`, `welcome`,
`readme`, `license`, `conclusion`, `background`, `background-darkAqua`
(each with a `file` in `Resources/`), `installation-check`, `locator`,
and `script` holding JavaScript. The model in `pkg/flatpkg` reads the
common parts and keeps the raw document, which `expand` writes unchanged.
`macospkg product` synthesises the shape above when no Distribution is
given, as `productbuild --synthesize` does.
