#!/usr/bin/env bash
#
# Exports the Apple root certificates that pkg/pkgsign/roots embeds, from
# macOS's system root store. The certificates are Apple's; this script is
# the record of where they came from, and it refuses to write a root whose
# fingerprint is not the one pinned below, so a rogue store cannot slip a
# different anchor into the tool.
#
# Developer ID certificates chain to the 2006 "Apple Root CA". The G2/G3
# roots and the Apple Platform roots are embedded for chains that may move
# to them. Their subjects are ordered C,O,OU,CN in the store, so they are
# matched by common name, not by subject prefix.
set -euo pipefail

if [[ "$(uname)" != "Darwin" ]]; then
	echo "export-roots.sh must run on macOS: it reads the system root store" >&2
	exit 1
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/pkg/pkgsign/roots"
STORE=/System/Library/Keychains/SystemRootCertificates.keychain
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# common name | file | SHA-256 fingerprint (colon-free, upper case)
ROOTS=(
	"Apple Root CA|apple_root_ca.pem|B0B1730ECBC7FF4505142C49F1295E6EDA6BCAED7E2C68C5BE91B5A11001F024"
	"Apple Root CA - G2|apple_root_ca_g2.pem|C2B9B042DD57830E7D117DAC55AC8AE19407D38E41D88F3215BC3A890444A050"
	"Apple Root CA - G3|apple_root_ca_g3.pem|63343ABFB89A6A03EBB57E9B3F5FA7BE7C4F5C756F3017B3A8C488C3653E9179"
	"Apple Platform Developer RSA Root CA - G1|apple_platform_developer_rsa_root_ca_g1.pem|8174FDD9DB62E04DD18F17D1224406C7A2CC8D5DB5816F3DC7F5E900047E7FB7"
	"Apple Platform Developer ECC Root CA - G1|apple_platform_developer_ecc_root_ca_g1.pem|99DAD8412FF1155B12759717AEF5A31E6E089E357539FACA3D57E138018493B3"
	"Apple Platform Multipurpose RSA Root CA - G1|apple_platform_multipurpose_rsa_root_ca_g1.pem|FA760B953DFD935CA420BA9BAA5F07067FAD4449B5554B9418CBDD12E0A56EDA"
)

security find-certificate -a -p "$STORE" >"$WORK/all.pem"

# Split the store into one file per certificate and index them by CN.
python3 - "$WORK" <<'PY'
import re, subprocess, sys, os
work = sys.argv[1]
data = open(os.path.join(work, "all.pem")).read()
for i, block in enumerate(re.findall(r"-----BEGIN CERTIFICATE-----.*?-----END CERTIFICATE-----\n", data, re.S)):
    subj = subprocess.run(["openssl", "x509", "-noout", "-subject", "-nameopt", "RFC2253"],
                          input=block, capture_output=True, text=True).stdout
    m = re.search(r"CN=([^,]+)", subj)
    if not m:
        continue
    cn = m.group(1).strip()
    with open(os.path.join(work, cn.replace("/", "_") + ".pem"), "w") as f:
        f.write(block)
PY

status=0
for entry in "${ROOTS[@]}"; do
	IFS='|' read -r cn file want <<<"$entry"
	src="$WORK/$cn.pem"
	if [[ ! -f "$src" ]]; then
		echo "missing from the store: $cn" >&2
		status=1
		continue
	fi
	got="$(openssl x509 -in "$src" -noout -fingerprint -sha256 | sed 's/.*=//; s/://g')"
	if [[ "$got" != "$want"* ]]; then
		echo "fingerprint mismatch for $cn: got $got, pinned $want" >&2
		status=1
		continue
	fi
	cp "$src" "$OUT/$file"
	echo "$file  $cn  $(openssl x509 -in "$src" -noout -enddate | sed 's/notAfter=/expires /')"
done
exit $status
