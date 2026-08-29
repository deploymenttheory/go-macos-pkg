#!/usr/bin/env bash
#
# Generates the committed fixtures in testdata/ with Apple's own tools:
# pkgbuild, productbuild, productsign, cpio and xar.
#
# The fixtures are committed rather than built during the test run, which is
# what lets the suite run on Linux and Windows against packages Apple's tools
# produced. This script is the record of how they were made, not a build
# step -- so regenerating is a deliberate, macOS-only maintenance operation.
#
# Nothing here is used by the macospkg binary itself, which never calls an
# Apple tool.
set -euo pipefail

if [[ "$(uname)" != "Darwin" ]]; then
	echo "gen-fixtures.sh must run on macOS: it uses pkgbuild, productbuild and productsign" >&2
	exit 1
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/testdata/cli"
KEYS="$OUT/keys"
WORK="$(mktemp -d)"
KEYCHAIN="$WORK/fixture.keychain-db"
IDENTITY="Developer ID Installer: Fixture (FIXTURE01)"
# A fixed timestamp for every source file, so a regenerated fixture differs
# only where the tools themselves are non-deterministic.
STAMP="202401020304.05"

cleanup() {
	security delete-keychain "$KEYCHAIN" 2>/dev/null || true
	rm -rf "$WORK"
}
trap cleanup EXIT

mkdir -p "$OUT" "$KEYS" "$ROOT/testdata/cpio" "$ROOT/testdata/xar"

log() { printf '==> %s\n' "$*" >&2; }

# deterministic_bytes N writes N bytes that are the same on every run: the
# AES-CTR keystream of a fixed passphrase over zeros. /dev/urandom would make
# the fixtures unreproducible.
deterministic_bytes() {
	head -c "$1" /dev/zero | openssl enc -aes-256-ctr -pass pass:go-macos-pkg-fixture -nosalt 2>/dev/null
}

# stamp DIR gives every entry under DIR the fixed mtime and strips extended
# attributes. macOS 14 and later attach com.apple.provenance to files that
# tracked processes create and will not let it be removed; pkgbuild then
# carries it into the payload as ._ AppleDouble entries. When that happens
# the manifest records appleDouble=true so the tests know to expect them.
# The entries are legitimate pkgbuild output, so the reader has to cope
# either way; regenerate from an untracked shell for cleaner fixtures.
stamp() {
	xattr -cr "$1" 2>/dev/null || true
	find "$1" -exec touch -h -t "$STAMP" {} +
}

# ---------------------------------------------------------------- source trees

populate() {
	local root="$1"
	mkdir -p "$root/usr/local/fixture/bin" "$root/usr/local/fixture/sub/nested"
	printf 'hello, world\n' >"$root/usr/local/fixture/hello.txt"
	: >"$root/usr/local/fixture/empty.txt"
	printf '#!/bin/sh\necho tool\n' >"$root/usr/local/fixture/bin/tool"
	chmod 755 "$root/usr/local/fixture/bin/tool"
	printf 'deep\n' >"$root/usr/local/fixture/sub/nested/deep.txt"
	ln -s hello.txt "$root/usr/local/fixture/link"
	printf 'unicode\n' >"$root/usr/local/fixture/unicode-é.txt"
	deterministic_bytes 307200 >"$root/usr/local/fixture/big.bin"
	chmod -R go-w "$root"
	stamp "$root"
}

log "source trees"
populate "$WORK/root"

# A second tree with a 20 MiB file of a repeating 4 KiB pattern: large
# enough that pkgbuild's pbzx container has more than one 16 MiB chunk,
# small enough (once compressed) to commit.
cp -R "$WORK/root" "$WORK/root-pbzx"
python3 - "$WORK/root-pbzx/usr/local/fixture/huge.bin" <<'PY'
import sys
block = bytes(range(256)) * 16          # 4 KiB
with open(sys.argv[1], "wb") as f:
    for _ in range(5120):               # 20 MiB
        f.write(block)
PY
stamp "$WORK/root-pbzx"

# An application bundle, so pkgbuild records bundle-version and relocate.
APP="$WORK/root-app/Applications/Fixture.app/Contents"
mkdir -p "$APP/MacOS"
cat >"$APP/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key><string>Fixture</string>
	<key>CFBundleIdentifier</key><string>com.deploymenttheory.fixture.app</string>
	<key>CFBundleName</key><string>Fixture</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>1.0</string>
	<key>CFBundleVersion</key><string>100</string>
</dict>
</plist>
PLIST
printf '#!/bin/sh\necho fixture\n' >"$APP/MacOS/Fixture"
chmod 755 "$APP/MacOS/Fixture"
stamp "$WORK/root-app"

mkdir -p "$WORK/scripts"
printf '#!/bin/sh\nexit 0\n' >"$WORK/scripts/preinstall"
printf '#!/bin/sh\nexit 0\n' >"$WORK/scripts/postinstall"
chmod 755 "$WORK/scripts"/*
stamp "$WORK/scripts"

# ------------------------------------------------------------------- packages

cd "$WORK"
log "component packages"
pkgbuild --quiet --root root --identifier com.deploymenttheory.fixture.basic --version 1.0.0 \
	--install-location / --scripts scripts --ownership recommended component-basic.pkg
pkgbuild --quiet --root root --identifier com.deploymenttheory.fixture.noscripts --version 2.1 \
	--install-location /opt/fixture --ownership recommended component-noscripts.pkg
pkgbuild --quiet --root root-pbzx --identifier com.deploymenttheory.fixture.pbzx --version 1.0 \
	--install-location / --ownership recommended --compression latest --min-os-version 12.0 component-pbzx.pkg
pkgbuild --quiet --root root --identifier com.deploymenttheory.fixture.largepayload --version 1.0 \
	--install-location / --ownership recommended --large-payload --min-os-version 12.0 component-large-payload.pkg
pkgbuild --quiet --root root-app --identifier com.deploymenttheory.fixture.bundle --version 1.0 \
	--install-location / --ownership recommended component-bundle.pkg

log "product archives"
mkdir -p res
printf '<html><body><h1>Welcome to Fixture</h1></body></html>\n' >res/welcome.html
printf 'Fixture licence: do as you like.\n' >res/license.txt
productbuild --quiet --synthesize --package component-basic.pkg --package component-noscripts.pkg dist-basic.xml
python3 - dist-basic.xml <<'PY'
import sys, re
p = sys.argv[1]
s = open(p).read()
s = re.sub(r'(<installer-gui-script[^>]*>)',
           r'\1\n    <title>Fixture</title>\n    <welcome file="welcome.html" mime-type="text/html"/>\n    <license file="license.txt" mime-type="text/plain"/>',
           s, count=1)
open(p, "w").write(s)
PY
productbuild --quiet --distribution dist-basic.xml --resources res --package-path . product-basic.pkg

cat >dist-custom.xml <<'XML'
<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
    <title>Fixture Custom</title>
    <options customize="allow" require-scripts="false" hostArchitectures="arm64,x86_64" rootVolumeOnly="true"/>
    <domains enable_anywhere="false" enable_currentUserHome="false" enable_localSystem="true"/>
    <volume-check>
        <allowed-os-versions>
            <os-version min="12.0"/>
        </allowed-os-versions>
    </volume-check>
    <installation-check script="checkRAM()"/>
    <script>
    function checkRAM() {
        return system.sysctl('hw.memsize') > 1024;
    }
    </script>
    <choices-outline>
        <line choice="basic"/>
        <line choice="extra"/>
    </choices-outline>
    <choice id="basic" title="Basic" description="The basic component" visible="true" start_selected="true">
        <pkg-ref id="com.deploymenttheory.fixture.basic"/>
    </choice>
    <choice id="extra" title="Extra" description="An optional extra" visible="true" start_selected="false">
        <pkg-ref id="com.deploymenttheory.fixture.noscripts"/>
    </choice>
    <pkg-ref id="com.deploymenttheory.fixture.basic" version="1.0.0" onConclusion="none">component-basic.pkg</pkg-ref>
    <pkg-ref id="com.deploymenttheory.fixture.noscripts" version="2.1" onConclusion="none">component-noscripts.pkg</pkg-ref>
</installer-gui-script>
XML
productbuild --quiet --distribution dist-custom.xml --package-path . product-custom-dist.pkg


log "compression variants"
# pkgbuild picks the payload container from --min-os-version. Build one
# package per version and let the manifest record what came out; the
# tests read the manifest rather than assuming.
for v in 12.0 13.0 14.0 15.0 26.0; do
	pkgbuild --quiet --root root --identifier "com.deploymenttheory.fixture.latest$v" --version 1.0 \
		--install-location / --ownership recommended --scripts scripts --compression latest --min-os-version "$v" "component-latest-$v.pkg"
done

log "hard links and extended attributes"
mkdir -p root-links/d root-links/attrs
printf 'shared content\n' >root-links/a.txt
ln root-links/a.txt root-links/b.txt
ln root-links/a.txt root-links/d/c.txt
printf 'three links\n' >root-links/p
ln root-links/p root-links/q
ln root-links/p root-links/r
printf 'has attributes\n' >root-links/attrs/x
printf 'finder\n' >root-links/attrs/finder
printf 'rsrc\n' >root-links/attrs/rsrc
: >root-links/attrs/empty
ln -s x root-links/attrs/link
stamp root-links
# Attributes go on after stamp, which clears them; the mtimes are then
# pinned again without touching the attributes.
xattr -w com.example.one hello root-links/attrs/x
xattr -wx com.example.big "$(deterministic_bytes 300 | xxd -p | tr -d '\n')" root-links/attrs/x
xattr -wx com.apple.FinderInfo "$(printf '41%.0s' $(seq 32))" root-links/attrs/finder
xattr -w com.apple.ResourceFork 'resource fork bytes' root-links/attrs/rsrc
xattr -w com.example.empty v root-links/attrs/empty
xattr -s -w com.example.onlink yes root-links/attrs/link
xattr -w com.example.ondir dirval root-links/attrs
find root-links -exec touch -h -t "$STAMP" {} +
pkgbuild --quiet --root root-links --identifier com.deploymenttheory.fixture.links --version 1.0 \
	--install-location / --ownership recommended component-links.pkg

log "apple archive samples"
mkdir -p "$ROOT/testdata/aa"
for a in raw lzfse lzma zlib lz4; do
	aa archive -d root-links -o "$ROOT/testdata/aa/aa-$a.aar" -a "$a"
done
aa list -v -i "$ROOT/testdata/aa/aa-raw.aar" >"$ROOT/testdata/aa/aa-raw.list.txt"

# ---------------------------------------------------------------- signing keys

# A private CA and a leaf shaped like a Developer ID Installer certificate
# (same subject pattern, same marker extension), so the signature code can
# be exercised, and verify's chain checks proven, without Apple's CA. They
# are generated once and committed; the CA key is committed too so the
# signed fixtures can be regenerated against the same chain. Test-only.
if [[ ! -f "$KEYS/fixture-installer.p12" ]]; then
	log "signing keys"
	cat >"$WORK/ca.cnf" <<'CNF'
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = Fixture Developer ID Certification Authority
OU = Fixture Certification Authority
O = Deployment Theory
C = GB
[ext]
basicConstraints = critical, CA:TRUE, pathlen:0
keyUsage = critical, keyCertSign, cRLSign
subjectKeyIdentifier = hash
# Apple's "Developer ID Certification Authority" marker
1.2.840.113635.100.6.2.6 = DER:05:00
CNF
	cat >"$WORK/leaf.cnf" <<'CNF'
[req]
distinguished_name = dn
prompt = no
[dn]
CN = Developer ID Installer: Fixture (FIXTURE01)
OU = FIXTURE01
O = Deployment Theory
C = GB
[ext]
basicConstraints = critical, CA:FALSE
keyUsage = critical, digitalSignature
extendedKeyUsage = codeSigning
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid
# Apple's "Developer ID Installer" marker
1.2.840.113635.100.6.1.14 = DER:05:00
CNF
	openssl req -x509 -newkey rsa:2048 -nodes -days 7300 -sha256 \
		-config "$WORK/ca.cnf" -keyout "$KEYS/fixture-ca.key" -out "$KEYS/fixture-ca.pem" 2>/dev/null
	openssl req -newkey rsa:2048 -nodes -sha256 -config "$WORK/leaf.cnf" \
		-keyout "$KEYS/fixture-installer.key" -out "$WORK/leaf.csr" 2>/dev/null
	openssl x509 -req -in "$WORK/leaf.csr" -CA "$KEYS/fixture-ca.pem" -CAkey "$KEYS/fixture-ca.key" \
		-CAcreateserial -days 7300 -sha256 -extfile "$WORK/leaf.cnf" -extensions ext \
		-out "$KEYS/fixture-installer.pem" 2>/dev/null
	# The legacy PKCS#12 algorithms: macOS's security(1) cannot import the
	# AES/PBKDF2 bundles OpenSSL 3 writes by default.
	openssl pkcs12 -export -inkey "$KEYS/fixture-installer.key" -in "$KEYS/fixture-installer.pem" \
		-certfile "$KEYS/fixture-ca.pem" -passout pass:fixture -out "$KEYS/fixture-installer.p12" \
		-macalg sha1 -keypbe PBE-SHA1-3DES -certpbe PBE-SHA1-3DES
	rm -f "$KEYS/fixture-ca.srl"
	cat >"$KEYS/README.md" <<'MD'
# Fixture signing keys

Test-only. A private certification authority and a leaf certificate shaped
like a Developer ID Installer certificate (same subject pattern, same Apple
marker extension OID `1.2.840.113635.100.6.1.14`), so that signing and
verification can be exercised without Apple's CA. The PKCS#12 password is
`fixture`. Nothing here is trusted by any real system.

| File | Purpose |
|---|---|
| `fixture-ca.pem`, `fixture-ca.key` | the CA; `verify --trust-anchors fixture-ca.pem` |
| `fixture-installer.pem`, `fixture-installer.key` | the leaf and its key, PEM |
| `fixture-installer.p12` | the leaf, key and CA in one bundle, password `fixture` |
MD
fi

# ------------------------------------------------------------------- signing

# productsign only accepts an identity whose chain the system trusts, so
# this step succeeds only on a machine where fixture-ca.pem has been added
# as a trusted root (security add-trusted-cert, which needs an admin). Where
# it cannot, the signed fixtures are simply not regenerated: the signing
# tests use packages signed by macospkg itself, and the CI job that holds a
# real Developer ID certificate covers productsign parity.
log "signed packages"
SIGNED=0
if security create-keychain -p fixture "$KEYCHAIN" &&
	security set-keychain-settings "$KEYCHAIN" &&
	security unlock-keychain -p fixture "$KEYCHAIN" &&
	security import "$KEYS/fixture-installer.p12" -k "$KEYCHAIN" -P fixture \
		-T /usr/bin/productsign -T /usr/bin/security -T /usr/bin/codesign >/dev/null &&
	{ security import "$KEYS/fixture-ca.pem" -k "$KEYCHAIN" -T /usr/bin/productsign >/dev/null 2>&1 || true; } &&
	security set-key-partition-list -S apple-tool:,apple: -s -k fixture "$KEYCHAIN" >/dev/null 2>&1 &&
	productsign --keychain "$KEYCHAIN" --sign "$IDENTITY" --timestamp=none component-basic.pkg signed-component.pkg >/dev/null 2>&1 &&
	productsign --keychain "$KEYCHAIN" --sign "$IDENTITY" --timestamp=none product-basic.pkg signed-product.pkg >/dev/null 2>&1; then
	SIGNED=1
else
	echo "warning: productsign with the fixture identity failed; signed fixtures not regenerated" >&2
fi

# ------------------------------------------------------------ other test data

log "cpio and xar samples"
(cd root && find . | LC_ALL=C sort | cpio -o --format odc --owner 0:80 2>/dev/null) >"$ROOT/testdata/cpio/odc.cpio"
(cd root && find . | LC_ALL=C sort | cpio -o --format newc --owner 0:80 2>/dev/null) >"$ROOT/testdata/cpio/newc.cpio"
(cd root && xar -cf "$ROOT/testdata/xar/plain.xar" --compression=gzip usr)
xar --dump-toc="$ROOT/testdata/xar/component-basic.toc.xml" -f component-basic.pkg

# ----------------------------------------------------------------- manifest

log "manifest"
for pkg in component-basic component-noscripts component-pbzx component-large-payload component-bundle product-basic product-custom-dist \
	component-latest-26.0 component-links; do
	cp "$pkg.pkg" "$OUT/"
done
# What --compression latest produced per --min-os-version, for the record.
: >"$WORK/latest-probe.txt"
for v in 12.0 13.0 14.0 15.0 26.0; do
	rm -rf "$WORK/lp"; pkgutil --expand "component-latest-$v.pkg" "$WORK/lp"
	printf '%s %s\n' "$v" "$(head -c 4 "$WORK/lp/Payload" | xxd -p)" >>"$WORK/latest-probe.txt"
done
python3 "$ROOT/scripts/decode-payload.py" "$OUT/component-links.pkg" >"$OUT/component-links.probe.json"
if [[ $SIGNED == 1 ]]; then
	cp signed-component.pkg signed-product.pkg "$OUT/"
fi

python3 - "$OUT" "$WORK" "$SIGNED" "$IDENTITY" <<'PY'
import hashlib, json, os, plistlib, subprocess, sys, xml.etree.ElementTree as ET

out, work, signed, identity = sys.argv[1], sys.argv[2], sys.argv[3] == "1", sys.argv[4]

def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()

def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout

def sniff(path):
    with open(path, "rb") as f:
        head = f.read(8)
    if head.startswith(b"\x1f\x8b\x08"): return "gzip-cpio"
    if head.startswith(b"pbz"): return "pbz%s-cpio" % head[3:4].decode()
    if head.startswith(b"070707") or head.startswith(b"07070"): return "cpio"
    if head[:4] in (b"AA01", b"YAA1", b"AEA1"): return "apple-archive"
    return "unknown"

def pbz_info(path):
    """Block size and chunk count of a pbz* container."""
    import struct
    with open(path, "rb") as f:
        data = f.read()
    if not data.startswith(b"pbz"):
        return None
    block = struct.unpack(">Q", data[4:12])[0]
    pos, chunks = 12, 0
    while pos + 16 <= len(data):
        inflated, deflated = struct.unpack(">QQ", data[pos:pos + 16])
        pos += 16 + deflated
        chunks += 1
    return {"blockSize": block, "chunks": chunks}

roots = {
    "component-basic": "root", "component-noscripts": "root", "component-pbzx": "root-pbzx",
    "component-large-payload": "root", "component-bundle": "root-app", "component-links": "root-links",
}
for _v in ("12.0", "13.0", "14.0", "15.0", "26.0"):
    roots["component-latest-" + _v] = "root"

def files_from_bom(bom, root):
    files = {}
    for line in run("lsbom", "-p", "fmugscl", bom).splitlines():
        cols = line.split("\t")
        path, mode, uid, gid = cols[0], int(cols[1], 8), int(cols[2]), int(cols[3])
        size = int(cols[4]) if len(cols) > 4 and cols[4] else 0
        crc = int(cols[5]) if len(cols) > 5 and cols[5] else 0
        link = cols[6] if len(cols) > 6 else ""
        kind = {0o040000: "dir", 0o120000: "link", 0o100000: "file"}.get(mode & 0o170000, "other")
        entry = {"type": kind, "mode": "%o" % (mode & 0o7777), "uid": uid, "gid": gid}
        if kind == "file":
            entry["size"] = size
            entry["crc32"] = crc
            src = os.path.join(root, path)
            if os.path.isfile(src):
                entry["sha256"] = sha256(src)
        elif kind == "link":
            entry["target"] = link
        files[path] = entry
    return files

import importlib.util
_spec = importlib.util.spec_from_file_location("decode_payload", os.path.join(os.path.dirname(os.path.abspath(sys.argv[0])) if False else os.path.join(os.path.dirname(out), "..", "scripts"), "decode-payload.py"))
_dp = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_dp)

def xar_entry(path, name):
    try:
        return _dp.read_xar_entry(path, name)
    except KeyError:
        return None

def sniff_bytes(head):
    if head.startswith(b"\x1f\x8b\x08"): return "gzip-cpio"
    if head.startswith(b"pbz"): return "pbz%s-cpio" % head[3:4].decode()
    if head.startswith(b"070707") or head.startswith(b"07070"): return "cpio"
    return "unknown"

def component(exp, root, pkg_path="", comp_prefix=""):
    info = ET.parse(os.path.join(exp, "PackageInfo")).getroot()
    payload = info.find("payload")
    scripts = info.find("scripts")
    payload_entry = "Payload" if os.path.exists(os.path.join(exp, "Payload")) else (
        "LargeSegmentedPayload" if os.path.exists(os.path.join(exp, "LargeSegmentedPayload")) else "")
    c = {
        "payloadEntry": payload_entry,
        "identifier": info.get("identifier"),
        "version": info.get("version"),
        "installLocation": info.get("install-location"),
        "generatorVersion": info.get("generator-version"),
        "numberOfFiles": int(payload.get("numberOfFiles")),
        "installKBytes": int(payload.get("installKBytes")),
        "scripts": [s.tag for s in scripts] if scripts is not None else [],
        "payloadEncoding": sniff(os.path.join(exp, payload_entry)) if payload_entry else "",
    }
    if payload_entry:
        info_pbz = pbz_info(os.path.join(exp, payload_entry))
        if info_pbz:
            c["payloadBlockSize"] = info_pbz["blockSize"]
            c["payloadChunks"] = info_pbz["chunks"]
    scripts_raw = xar_entry(pkg_path, (comp_prefix + "Scripts") if comp_prefix else "Scripts")
    if scripts_raw is not None:
        c["scriptsEncoding"] = sniff_bytes(scripts_raw)
    bundles = info.find("bundle-version")
    if bundles is not None and len(bundles):
        c["bundles"] = [{"id": b.get("id"), "path": b.get("path"), "version": b.get("CFBundleShortVersionString")} for b in bundles]
    if root is not None:
        c["files"] = files_from_bom(os.path.join(exp, "Bom"), os.path.join(work, root))
        if any(os.path.basename(p).startswith("._") for p in c["files"]):
            manifest["generator"]["appleDouble"] = True
    return c

manifest = {
    "generator": {
        "macos": run("sw_vers", "-productVersion").strip(),
        "pkgbuild": "",
        "script": "scripts/gen-fixtures.sh",
        "appleDouble": False,
    },
    "packages": {},
}

names = ["component-basic", "component-noscripts", "component-pbzx", "component-large-payload",
         "component-bundle", "product-basic", "product-custom-dist", "component-links", "component-latest-26.0"]
probe = {}
for line in open(os.path.join(work, "latest-probe.txt")):
    v, magic = line.split()
    probe[v] = {"70627a78": "pbzx-cpio", "70627a65": "pbze-cpio", "70627a34": "pbz4-cpio", "70627a7a": "pbzz-cpio", "70627a62": "pbzb-cpio", "1f8b0800": "gzip-cpio"}.get(magic, magic)
manifest["generator"]["compressionLatest"] = probe
if signed:
    names += ["signed-component", "signed-product"]

for name in names:
    path = os.path.join(out, name + ".pkg")
    exp = os.path.join(work, "expand-" + name)
    run("pkgutil", "--expand", path, exp)
    entries = [e for e in run("xar", "-tf", path).splitlines() if e]
    m = {"sha256": sha256(path), "entries": sorted(entries)}
    base = name.replace("signed-", "")
    if os.path.exists(os.path.join(exp, "Distribution")):
        m["kind"] = "product"
        dist = ET.parse(os.path.join(exp, "Distribution")).getroot()
        m["title"] = (dist.findtext("title") or "")
        m["choices"] = [c.get("id") for c in dist.findall("choice")]
        m["pkgRefs"] = [{"id": r.get("id"), "version": r.get("version"), "path": (r.text or "").strip(), "installKBytes": r.get("installKBytes")}
                        for r in dist.findall("pkg-ref") if (r.text or "").strip()]
        m["resources"] = sorted(e for e in entries if e.startswith("Resources/") and not e.endswith("/"))
        m["components"] = {}
        for d in sorted(os.listdir(exp)):
            if os.path.exists(os.path.join(exp, d, "PackageInfo")):
                m["components"][d] = component(os.path.join(exp, d), None, path, d + "/")
    else:
        m["kind"] = "component"
        m.update(component(exp, roots.get(base), path, ""))
        manifest["generator"]["pkgbuild"] = m.get("generatorVersion", "")
    if name.startswith("signed-"):
        m["signedBy"] = identity
        m["digest"] = "sha1"
    manifest["packages"][name + ".pkg"] = m

with open(os.path.join(out, "manifest.json"), "w", encoding="utf-8") as f:
    json.dump(manifest, f, indent=1, sort_keys=True, ensure_ascii=False)
    f.write("\n")
PY

log "done: $(ls "$OUT"/*.pkg | wc -l | tr -d ' ') packages in $OUT"
