// Command tests for sign and verify: our signer against our verifier
// everywhere, and against pkgutil --check-signature, xar and openssl
// where they exist.
package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/exitcode"
)

type verifyJSON struct {
	Signed       bool     `json:"signed"`
	Valid        bool     `json:"valid"`
	Digest       string   `json:"digest"`
	DigestValid  bool     `json:"digestValid"`
	RSAValid     bool     `json:"rsaValid"`
	CMSValid     bool     `json:"cmsValid"`
	Trusted      bool     `json:"trusted"`
	TeamID       string   `json:"teamId"`
	DeveloperID  bool     `json:"developerId"`
	Timestamped  bool     `json:"timestamped"`
	Stapled      bool     `json:"stapled"`
	Errors       []string `json:"errors"`
	Certificates []struct {
		Subject string `json:"subject"`
	} `json:"certificates"`
}

func fixtureKeys(t *testing.T) (p12, cert, key, ca string) {
	t.Helper()
	dir := filepath.Join(repoRoot, "testdata", "cli", "keys")
	p12 = filepath.Join(dir, "fixture-installer.p12")
	if _, err := os.Stat(p12); err != nil {
		t.Skip("fixture keys not committed; run scripts/gen-fixtures.sh")
	}
	return p12, filepath.Join(dir, "fixture-installer.pem"), filepath.Join(dir, "fixture-installer.key"), filepath.Join(dir, "fixture-ca.pem")
}

// signedFixture signs the basic fixture with the fixture identity, without
// a timestamp (no network in the suite), and returns the path.
func signedFixture(t *testing.T) (string, string) {
	t.Helper()
	p12, _, _, ca := fixtureKeys(t)
	src, _ := fixture(t, "component-basic.pkg")
	out := filepath.Join(t.TempDir(), "signed.pkg")
	_, stderr, code := runEnv(t, []string{"MACOSPKG_P12_PASSWORD=fixture"}, "sign", src, out, "--p12", p12, "--no-timestamp")
	if code != 0 {
		t.Fatalf("sign: exit %d\n%s", code, stderr)
	}
	return out, ca
}

func TestSignThenVerify(t *testing.T) {
	signed, ca := signedFixture(t)
	var v verifyJSON
	stdout, stderr, code := run(t, "-o", "json", "verify", "--trust-anchors", ca, "--team-id", "FIXTURE01", "--require-developer-id", signed)
	if code != 0 {
		t.Fatalf("verify: exit %d\n%s\n%s", code, stdout, stderr)
	}
	decodeJSON(t, stdout, &v)
	if !v.Valid || !v.Signed || !v.DigestValid || !v.RSAValid || !v.CMSValid || !v.Trusted || !v.DeveloperID || v.TeamID != "FIXTURE01" {
		t.Errorf("verify: %+v", v)
	}
	if v.Digest != "sha256" {
		t.Errorf("digest = %s", v.Digest)
	}
	if v.Timestamped {
		t.Error("--no-timestamp signature reports a timestamp")
	}
	// Leaf first, then the CA it was issued by, as productsign embeds them.
	if len(v.Certificates) != 2 || !strings.Contains(v.Certificates[0].Subject, "Developer ID Installer: Fixture") || !strings.Contains(v.Certificates[1].Subject, "Certification Authority") {
		t.Errorf("certificates = %+v", v.Certificates)
	}

	// The signed package is still a working package.
	var info infoJSON
	mustRunJSON(t, &info, "info", signed)
	if !info.Signature.Signed || info.Packages[0].Payload.NumberOfFiles == 0 {
		t.Errorf("info after signing: %+v", info)
	}
	dir := filepath.Join(t.TempDir(), "x")
	mustRun(t, "extract", "--verify", signed, dir)
	checkTree(t, dir, manifest.Packages["component-basic.pkg"].Files)
	text := mustRun(t, "verify", "--trust-anchors", ca, signed)
	if !strings.Contains(text, "Status:    valid") {
		t.Errorf("verify text:\n%s", text)
	}

	// Against Apple's roots the fixture CA is not trusted: exit 7, unless
	// untrusted chains are allowed.
	stdout, _, code = run(t, "-o", "json", "verify", signed)
	if code != exitcode.Signature {
		t.Errorf("default anchors: exit %d, want %d", code, exitcode.Signature)
	}
	decodeJSON(t, stdout, &v)
	if v.Trusted || v.Valid || !v.RSAValid || !v.CMSValid {
		t.Errorf("default anchors: %+v", v)
	}
	if _, _, code = run(t, "verify", "--allow-untrusted", signed); code != 0 {
		t.Errorf("--allow-untrusted: exit %d", code)
	}
	if _, _, code = run(t, "verify", "--trust-anchors", ca, "--team-id", "NOPE", signed); code != exitcode.Signature {
		t.Errorf("wrong team: exit %d", code)
	}
	if _, _, code = run(t, "verify", "--trust-anchors", ca, "--require-stapled", signed); code != exitcode.Signature {
		t.Errorf("--require-stapled on an unstapled package: exit %d", code)
	}

	// Unsigned: exit 7, signed=false.
	unsigned, _ := fixture(t, "component-basic.pkg")
	stdout, _, code = run(t, "-o", "json", "verify", unsigned)
	decodeJSON(t, stdout, &v)
	if code != exitcode.Signature || v.Signed {
		t.Errorf("unsigned: exit %d signed %v", code, v.Signed)
	}
	attest(t, "signed with the fixture identity; verify accepts it against the fixture CA and rejects it against Apple's roots")
}

func TestSignTamperAndResign(t *testing.T) {
	signed, ca := signedFixture(t)
	data, _ := os.ReadFile(signed)
	// Flip a byte in the compressed table of contents: the digest no
	// longer matches and both signatures fail.
	tampered := filepath.Join(t.TempDir(), "tampered.pkg")
	bad := append([]byte(nil), data...)
	bad[40] ^= 0x01
	os.WriteFile(tampered, bad, 0o644)
	stdout, _, code := run(t, "-o", "json", "verify", "--trust-anchors", ca, tampered)
	if code == 0 {
		t.Error("tampered TOC verified")
	}
	// It may not even open as a xar any more (zlib checksum); when it
	// does, the digest must be reported invalid.
	if strings.HasPrefix(stdout, "{") {
		var v verifyJSON
		decodeJSON(t, stdout, &v)
		if v.DigestValid || v.Valid {
			t.Errorf("tampered: %+v", v)
		}
	}

	// Re-sign with the PEM form of the same identity and a SHA-1 digest,
	// the way productsign leaves pkgbuild's packages.
	_, cert, key, _ := fixtureKeys(t)
	resigned := filepath.Join(t.TempDir(), "resigned.pkg")
	mustRun(t, "sign", signed, resigned, "--cert", cert, "--key", key, "--chain", ca, "--no-timestamp", "--digest", "sha1")
	var v verifyJSON
	mustRunJSON(t, &v, "verify", "--trust-anchors", ca, resigned)
	if !v.Valid || v.Digest != "sha1" {
		t.Errorf("re-signed: %+v", v)
	}
	if got := mustRun(t, "cat", resigned, "--payload", "./usr/local/fixture/hello.txt"); got != "hello, world\n" {
		t.Errorf("payload after re-sign = %q", got)
	}
}

func TestSignCredentialErrors(t *testing.T) {
	p12, cert, _, _ := fixtureKeys(t)
	src, _ := fixture(t, "component-basic.pkg")
	out := filepath.Join(t.TempDir(), "out.pkg")
	_, stderr, code := runEnv(t, []string{"MACOSPKG_P12_PASSWORD=wrong"}, "sign", src, out, "--p12", p12, "--no-timestamp")
	if code != exitcode.Auth {
		t.Errorf("wrong password: exit %d, want %d\n%s", code, exitcode.Auth, stderr)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a failed sign left an output file")
	}
	_, _, code = runWithStdin(t, "fixture\n", "sign", src, out, "--p12", p12, "--p12-password-stdin", "--no-timestamp")
	if code != 0 {
		t.Errorf("--p12-password-stdin: exit %d", code)
	}
	_, _, code = run(t, "sign", src, out, "--no-timestamp")
	if code != exitcode.Usage {
		t.Errorf("no identity: exit %d", code)
	}
	_, _, code = run(t, "sign", src, out, "--cert", cert, "--no-timestamp")
	if code != exitcode.Usage {
		t.Errorf("cert without key: exit %d", code)
	}
	_, _, code = run(t, "sign", src, out, "--cert", cert, "--key", filepath.Join(repoRoot, "testdata", "cli", "keys", "fixture-ca.key"), "--no-timestamp")
	if code != exitcode.Auth {
		t.Errorf("mismatched key: exit %d, want %d", code, exitcode.Auth)
	}
	_, _, code = run(t, "sign", src, out, "--p12", filepath.Join(t.TempDir(), "missing.p12"), "--no-timestamp")
	if code != exitcode.Auth {
		t.Errorf("missing p12: exit %d", code)
	}
}

func TestBuildSignsInline(t *testing.T) {
	p12, _, _, ca := fixtureKeys(t)
	root, scripts := sourceTree(t)
	out := filepath.Join(t.TempDir(), "signed.pkg")
	args := append(buildArgs(root, scripts, out), "--sign-p12", p12, "--sign-no-timestamp")
	_, stderr, code := runEnv(t, []string{"MACOSPKG_P12_PASSWORD=fixture"}, args...)
	if code != 0 {
		t.Fatalf("build --sign-p12: exit %d\n%s", code, stderr)
	}
	var v verifyJSON
	mustRunJSON(t, &v, "verify", "--trust-anchors", ca, out)
	if !v.Valid {
		t.Errorf("inline-signed build: %+v", v)
	}
	// Signed builds are reproducible too: the signing time is the epoch.
	out2 := filepath.Join(t.TempDir(), "signed2.pkg")
	args = append(buildArgs(root, scripts, out2), "--sign-p12", p12, "--sign-no-timestamp")
	runEnv(t, []string{"MACOSPKG_P12_PASSWORD=fixture"}, args...)
	h1, _ := fileSHA256(out)
	h2, _ := fileSHA256(out2)
	if h1 != h2 {
		t.Error("two signed builds differ")
	}
	// product --sign-* too.
	prod := filepath.Join(t.TempDir(), "product.pkg")
	_, stderr, code = runEnv(t, []string{"MACOSPKG_P12_PASSWORD=fixture"}, "product", prod, "--package", out, "--sign-p12", p12, "--sign-no-timestamp")
	if code != 0 {
		t.Fatalf("product --sign-p12: exit %d\n%s", code, stderr)
	}
	mustRunJSON(t, &v, "verify", "--trust-anchors", ca, prod)
	if !v.Valid {
		t.Errorf("signed product: %+v", v)
	}
}

// --- oracle tests -------------------------------------------------------

// TestPkgutilReadsOurSignature: Apple's pkgutil parses the signature we
// wrote and names the certificate. The fixture CA is not trusted by the
// system, so the status is "untrusted", which is the point: pkgutil got
// as far as evaluating trust, so the structure is right.
func TestPkgutilReadsOurSignature(t *testing.T) {
	requireTools(t, "pkgutil", "xar")
	signed, _ := signedFixture(t)
	out, err := exec.Command("pkgutil", "--check-signature", signed).CombinedOutput()
	text := string(out)
	// pkgutil exits non-zero for an untrusted certificate; the text is
	// what matters.
	if !strings.Contains(text, "Developer ID Installer: Fixture (FIXTURE01)") {
		t.Errorf("pkgutil --check-signature did not name our certificate (%v):\n%s", err, text)
	}
	if strings.Contains(text, "no signature") || strings.Contains(strings.ToLower(text), "invalid signature") {
		t.Errorf("pkgutil rejected the signature structure:\n%s", text)
	}
	attest(t, "pkgutil --check-signature: %s", strings.TrimSpace(firstLineContaining(text, "Status:")))

	// xar sees the signature elements and still extracts.
	toc := hostTool(t, "xar", "--dump-toc=-", "-f", signed)
	if !strings.Contains(toc, `<signature style="RSA">`) || !strings.Contains(toc, `<x-signature style="CMS">`) || !strings.Contains(toc, "<X509Certificate>") {
		t.Errorf("xar --dump-toc lacks the signature elements:\n%s", toc[:min(len(toc), 2000)])
	}
	dir := t.TempDir()
	cmd := exec.Command("xar", "-xf", signed)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("xar -x on the signed package: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "PackageInfo")); err != nil {
		t.Error("xar -x did not extract PackageInfo")
	}
	// pkgutil --expand still works on the signed archive.
	hostTool(t, "pkgutil", "--expand", signed, filepath.Join(t.TempDir(), "expanded"))
}

// TestOpenSSLVerifiesOurCMS: openssl checks the CMS blob independently.
func TestOpenSSLVerifiesOurCMS(t *testing.T) {
	requireTools(t, "openssl")
	signed, ca := signedFixture(t)
	dir := t.TempDir()
	cms := filepath.Join(dir, "sig.der")
	digest := filepath.Join(dir, "digest.bin")
	os.WriteFile(cms, []byte(mustRun(t, "inspect", signed, "cms")), 0o644)
	os.WriteFile(digest, []byte(mustRun(t, "inspect", signed, "digest")), 0o644)
	out, err := exec.Command("openssl", "cms", "-verify", "-inform", "DER", "-in", cms, "-content", digest, "-binary", "-CAfile", ca, "-purpose", "any", "-out", filepath.Join(dir, "content.out")).CombinedOutput()
	if err != nil {
		t.Fatalf("openssl cms -verify failed: %v\n%s", err, out)
	}
	attest(t, "openssl cms -verify accepted our CMS signature against the fixture CA")
}

func firstLineContaining(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}
