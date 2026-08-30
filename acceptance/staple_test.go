// Command tests for staple, unstaple and notarize: the file mechanics
// everywhere, and Apple's real ticket database against a real notarized
// package when one is supplied.
package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/exitcode"
)

type stapleJSON struct {
	Path        string `json:"path"`
	Stapled     bool   `json:"stapled"`
	TicketBytes int    `json:"ticketBytes"`
	RecordName  string `json:"recordName"`
	Replaced    bool   `json:"replaced"`
}

func TestStapleFromFileAndUnstaple(t *testing.T) {
	signed, ca := signedFixture(t)
	ticket := filepath.Join(t.TempDir(), "ticket.bin")
	os.WriteFile(ticket, append([]byte("s8ch"), []byte(strings.Repeat("T", 300))...), 0o644)

	// Unstapled to begin with.
	stdout, _, code := run(t, "-o", "json", "staple", "--check", signed)
	if code != exitcode.Signature {
		t.Errorf("--check on an unstapled package: exit %d, want %d", code, exitcode.Signature)
	}
	var rep stapleJSON
	decodeJSON(t, stdout, &rep)
	if rep.Stapled || !strings.HasPrefix(rep.RecordName, "2/2/") || len(rep.RecordName) != 4+40 {
		t.Errorf("check: %+v", rep)
	}

	stapled := filepath.Join(t.TempDir(), "stapled.pkg")
	mustRunJSON(t, &rep, "staple", signed, stapled, "--ticket", ticket)
	if !rep.Stapled || rep.TicketBytes != 304 || rep.Replaced {
		t.Errorf("staple: %+v", rep)
	}
	mustRunJSON(t, &rep, "staple", "--check", stapled)
	if !rep.Stapled || rep.TicketBytes != 304 {
		t.Errorf("check after staple: %+v", rep)
	}
	// Everything else still reads the archive, ignoring the trailer.
	var v verifyJSON
	mustRunJSON(t, &v, "verify", "--trust-anchors", ca, "--require-stapled", stapled)
	if !v.Valid || !v.Stapled {
		t.Errorf("verify --require-stapled: %+v", v)
	}
	var info infoJSON
	mustRunJSON(t, &info, "info", stapled)
	if !info.Staple.Present {
		t.Error("info does not report the staple")
	}
	if got := mustRun(t, "cat", stapled, "--payload", "./usr/local/fixture/hello.txt"); got != "hello, world\n" {
		t.Errorf("payload after staple = %q", got)
	}
	if got := mustRun(t, "inspect", stapled, "ticket"); !strings.HasPrefix(got, "s8ch") || len(got) != 304 {
		t.Errorf("inspect ticket = %d bytes", len(got))
	}

	// Stapling again replaces, in place.
	mustRunJSON(t, &rep, "staple", stapled, "--ticket", ticket)
	if !rep.Replaced {
		t.Error("second staple did not report a replacement")
	}
	mustRunJSON(t, &rep, "staple", "--check", stapled)
	if rep.TicketBytes != 304 {
		t.Errorf("after re-staple: %+v", rep)
	}

	// Unstaple: byte-identical to the original signed package.
	plain := filepath.Join(t.TempDir(), "plain.pkg")
	mustRun(t, "unstaple", stapled, plain)
	a, _ := fileSHA256(signed)
	b, _ := fileSHA256(plain)
	if a != b {
		t.Error("unstaple did not restore the original bytes")
	}
	// Re-signing a stapled package drops the ticket and says so.
	p12, _, _, _ := fixtureKeys(t)
	resigned := filepath.Join(t.TempDir(), "resigned.pkg")
	_, stderr, code := runEnv(t, []string{"MACOSPKG_P12_PASSWORD=fixture"}, "sign", stapled, resigned, "--p12", p12, "--no-timestamp")
	if code != 0 || !strings.Contains(stderr, "removing the stapled") {
		t.Errorf("sign on a stapled package: exit %d\n%s", code, stderr)
	}
	if _, _, code := run(t, "staple", "--check", resigned); code != exitcode.Signature {
		t.Error("ticket survived re-signing")
	}
	// A bogus ticket file is refused.
	bad := filepath.Join(t.TempDir(), "bad.bin")
	os.WriteFile(bad, []byte("not a ticket"), 0o644)
	out2 := filepath.Join(t.TempDir(), "x.pkg")
	mustRun(t, "staple", signed, out2, "--ticket", bad) // staple attaches what it is given...
	if _, _, code := run(t, "staple", "--check", out2); code != exitcode.Signature {
		t.Error("...but a ticket without the s8ch magic must not be recognised as one")
	}
}

func TestNotarizePreconditions(t *testing.T) {
	unsigned, _ := fixture(t, "component-basic.pkg")
	_, stderr, code := run(t, "notarize", unsigned)
	if code != exitcode.Signature || !strings.Contains(stderr, "not signed") {
		t.Errorf("notarize unsigned: exit %d\n%s", code, stderr)
	}
	signed, _ := signedFixture(t)
	_, stderr, code = run(t, "notarize", signed)
	if code != exitcode.Auth || !strings.Contains(stderr, "APPLE_KEY_ID") {
		t.Errorf("notarize without credentials: exit %d, want %d\n%s", code, exitcode.Auth, stderr)
	}
	_, _, code = runEnv(t, []string{"APPLE_KEY_ID=K", "APPLE_ISSUER_ID=I", "APPLE_PRIVATE_KEY_PEM=junk"}, "notarize", signed)
	if code != exitcode.Auth {
		t.Errorf("notarize with an unparseable key: exit %d, want %d", code, exitcode.Auth)
	}
	_, _, code = run(t, "notarize", "status", "abc")
	if code != exitcode.Auth {
		t.Errorf("status without credentials: exit %d", code)
	}
	root, scripts := sourceTree(t)
	out := filepath.Join(t.TempDir(), "x.pkg")
	_, _, code = run(t, append(buildArgs(root, scripts, out), "--notarize")...)
	if code != exitcode.Usage {
		t.Errorf("build --notarize without signing: exit %d, want %d", code, exitcode.Usage)
	}
}

// --- real-package tests --------------------------------------------------

// realPackage returns the Apple-signed, notarized package the environment
// points at, or skips.
func realPackage(t *testing.T) string {
	t.Helper()
	p := os.Getenv("MACOSPKG_ACCEPTANCE_PKG")
	if p == "" {
		t.Skip("MACOSPKG_ACCEPTANCE_PKG is not set")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("MACOSPKG_ACCEPTANCE_PKG: %v", err)
	}
	return p
}

// TestRealPackageVerifies checks a package Apple's tools signed against
// Apple's roots built into ours.
func TestRealPackageVerifies(t *testing.T) {
	pkg := realPackage(t)
	var v verifyJSON
	stdout, stderr, code := run(t, "-o", "json", "verify", "--require-developer-id", pkg)
	if code != 0 {
		t.Fatalf("verify: exit %d\n%s\n%s", code, stdout, stderr)
	}
	decodeJSON(t, stdout, &v)
	if !v.Valid || !v.Trusted || !v.RSAValid || !v.CMSValid || !v.DeveloperID {
		t.Errorf("real package: %+v", v)
	}
	if want := os.Getenv("MACOSPKG_ACCEPTANCE_TEAM_ID"); want != "" && v.TeamID != want {
		t.Errorf("team id = %s, want %s", v.TeamID, want)
	}
	var info infoJSON
	mustRunJSON(t, &info, "info", pkg)
	attest(t, "%s: %s, %d entries, signed by team %s, timestamped=%v, stapled=%v", filepath.Base(pkg), info.Kind, info.XAR.Entries, v.TeamID, v.Timestamped, v.Stapled)
	// Every payload extracts and verifies.
	dir := filepath.Join(t.TempDir(), "x")
	if _, _, code := run(t, "extract", "--verify", pkg, dir); code != 0 && code != exitcode.Partial {
		t.Errorf("extract --verify: exit %d", code)
	}
}

// TestRealPackageStaple exercises Apple's ticket database: the package's
// ticket is on record, and after unstapling we can fetch and re-staple
// it ourselves.
func TestRealPackageStaple(t *testing.T) {
	pkg := realPackage(t)
	if os.Getenv("MACOSPKG_ACCEPTANCE_OFFLINE") != "" {
		t.Skip("offline")
	}
	var rep stapleJSON
	stdout, _, code := run(t, "-o", "json", "staple", "--check", pkg)
	decodeJSON(t, stdout, &rep)
	if code != 0 || !rep.Stapled {
		t.Skipf("%s is not stapled; nothing to compare (%+v)", filepath.Base(pkg), rep)
	}
	var v verifyJSON
	mustRunOnlineJSON(t, &v, "verify", "--online", "--require-stapled", pkg)
	if !v.Valid {
		t.Errorf("verify --online: %+v", v)
	}
	plain := filepath.Join(t.TempDir(), "plain.pkg")
	mustRun(t, "unstaple", pkg, plain)
	if _, _, code := run(t, "staple", "--check", plain); code != exitcode.Signature {
		t.Error("unstaple left a ticket")
	}
	restapled := filepath.Join(t.TempDir(), "restapled.pkg")
	mustRunOnlineJSON(t, &rep, "staple", plain, restapled)
	if !rep.Stapled || rep.TicketBytes == 0 {
		t.Errorf("staple from Apple: %+v", rep)
	}
	a, _ := fileSHA256(pkg)
	b, _ := fileSHA256(restapled)
	if a != b {
		// Apple may have re-issued the ticket; the bytes need not match,
		// but the trailer must be recognised.
		attest(t, "re-stapled ticket differs from the original (%d bytes)", rep.TicketBytes)
	} else {
		attest(t, "re-stapled package is byte-identical to Apple's (%d-byte ticket)", rep.TicketBytes)
	}
	// Apple's stapler agrees, where it exists.
	if _, err := os.Stat("/usr/bin/xcrun"); err == nil {
		if out, err := hostToolOutput("xcrun", "stapler", "validate", restapled); err != nil {
			t.Errorf("stapler validate rejected our staple: %v\n%s", err, out)
		} else {
			attest(t, "xcrun stapler validate accepted our re-stapled package")
		}
		if out, err := hostToolOutput("xcrun", "stapler", "validate", plain); err == nil {
			t.Errorf("stapler validate accepted the unstapled copy:\n%s", out)
		}
		if out, err := hostToolOutput("spctl", "--assess", "--type", "install", "-vv", restapled); err != nil {
			t.Errorf("spctl rejected the re-stapled package: %v\n%s", err, out)
		}
	}
}
