// Command-level behaviour that every subcommand shares: version reporting,
// flag validation and the exit-code contract for usage errors.
package acceptance

import (
	"strings"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/exitcode"
)

func TestVersion(t *testing.T) {
	stdout := mustRun(t, "--version")
	if !strings.HasPrefix(stdout, "macospkg version ") {
		t.Fatalf("unexpected --version output: %q", stdout)
	}
}

func TestHelpListsEveryCommandGroup(t *testing.T) {
	stdout := mustRun(t, "--help")
	for _, want := range []string{"Read commands:", "Write commands:", "Signing and notarization:", "Exit codes:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--help is missing %q", want)
		}
	}
}

func TestUnknownFlagIsUsageError(t *testing.T) {
	_, stderr, code := run(t, "--no-such-flag")
	if code != exitcode.Usage {
		t.Fatalf("exit %d (%s), want %d (Usage)\nstderr: %s", code, exitcode.Name(code), exitcode.Usage, stderr)
	}
	if !strings.Contains(stderr, "no-such-flag") {
		t.Errorf("stderr should name the offending flag: %s", stderr)
	}
}
