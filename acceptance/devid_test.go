// The end-to-end test with a real Developer ID Installer certificate and
// App Store Connect API key: build, sign, notarize and staple with only
// the macospkg binary, then let macOS judge the result. It runs where the
// secrets are (CI on the main repository) and skips everywhere else.
package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func developerID(t *testing.T) (p12, teamID string) {
	t.Helper()
	p12 = os.Getenv("MACOSPKG_DEVID_P12")
	if p12 == "" || os.Getenv("APPLE_KEY_ID") == "" {
		t.Skip("MACOSPKG_DEVID_P12 and APPLE_* are not set")
	}
	return p12, os.Getenv("MACOSPKG_DEVID_TEAM_ID")
}

// TestDeveloperIDSignAndNotarize is the whole pipeline against Apple.
func TestDeveloperIDSignAndNotarize(t *testing.T) {
	p12, teamID := developerID(t)
	root, scripts := sourceTree(t)
	component := filepath.Join(t.TempDir(), "Fixture.pkg")
	mustRun(t, buildArgs(root, scripts, component)...)

	// A product archive, signed and timestamped with the real identity.
	product := filepath.Join(t.TempDir(), "Fixture-1.0.0.pkg")
	_, stderr, code := run(t, "product", product, "--package", component, "--title", "macospkg acceptance", "--sign-p12", p12)
	if code != 0 {
		t.Fatalf("product --sign-p12: exit %d\n%s", code, stderr)
	}
	var v verifyJSON
	mustRunJSON(t, &v, "verify", "--require-developer-id", product)
	if !v.Valid || !v.Trusted || !v.Timestamped {
		t.Fatalf("verify before notarization: %+v", v)
	}
	if teamID != "" && v.TeamID != teamID {
		t.Fatalf("signed by team %s, expected %s", v.TeamID, teamID)
	}
	if out, err := hostToolOutput("pkgutil", "--check-signature", product); err != nil || !strings.Contains(out, "Developer ID Installer") {
		t.Fatalf("pkgutil --check-signature: %v\n%s", err, out)
	}
	attest(t, "pkgutil --check-signature: %s", strings.TrimSpace(firstLineContaining(hostTool(t, "pkgutil", "--check-signature", product), "Status:")))

	// Notarize and staple.
	stdout, stderr, code := run(t, "-o", "json", "notarize", product, "--wait", "--staple", "--timeout", "30m", "--poll-interval", "20s")
	if code != 0 {
		t.Fatalf("notarize: exit %d\n%s\n%s", code, stdout, stderr)
	}
	var rep struct {
		SubmissionID string `json:"submissionId"`
		Status       string `json:"status"`
		Stapled      bool   `json:"stapled"`
	}
	decodeJSON(t, stdout, &rep)
	if rep.Status != "Accepted" || !rep.Stapled {
		t.Fatalf("notarize: %+v", rep)
	}
	attest(t, "notarization %s: %s", rep.SubmissionID, rep.Status)

	mustRunJSON(t, &v, "verify", "--require-stapled", "--online", product)
	if !v.Valid || !v.Stapled {
		t.Fatalf("verify after stapling: %+v", v)
	}

	// macOS's verdict.
	if out, err := hostToolOutput("xcrun", "stapler", "validate", product); err != nil {
		t.Errorf("stapler validate: %v\n%s", err, out)
	}
	if out, err := hostToolOutput("spctl", "--assess", "--type", "install", "-vv", product); err != nil || !strings.Contains(out, "accepted") {
		t.Errorf("spctl --assess: %v\n%s", err, out)
	} else {
		attest(t, "spctl: %s", strings.TrimSpace(out))
	}
	// And the submission is visible through the status commands.
	if got := mustRun(t, "notarize", "status", rep.SubmissionID); !strings.Contains(got, "Accepted") {
		t.Errorf("notarize status: %s", got)
	}
	if got := mustRun(t, "notarize", "log", rep.SubmissionID); !strings.Contains(got, "\"status\"") {
		t.Errorf("notarize log: %s", got)
	}
}
