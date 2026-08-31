package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
)

func TestExitCodeFor(t *testing.T) {
	base := errors.New("boom")
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"plain", base, ExitError},
		{"coded", withCode(ExitBadPackage, base), ExitBadPackage},
		{"wrapped coded", fmt.Errorf("outer: %w", withCode(ExitAuth, base)), ExitAuth},
		{"usage", usageErrorf("bad %s", "flag"), ExitUsage},
		{"nil through withCode", withCode(ExitPartial, nil), ExitOK},
	}
	for _, tc := range cases {
		if got := exitCodeFor(tc.err); got != tc.want {
			t.Errorf("%s: exitCodeFor = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestSentinelsSurviveWrapping pins the mapping from the library's
// sentinel errors onto exit codes. These used to be matched by searching
// the message text, which quietly stops working the moment a message is
// reworded; errors.Is does not care about wording, but it does care that
// every layer wraps with %w, which is what this checks.
func TestSentinelsSurviveWrapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		map_ func(error) error
		want int
	}{
		{
			"unsupported payload",
			fmt.Errorf("%s Payload: %w", "component.pkg", fmt.Errorf("%w: Apple Archive", flatpkg.ErrUnsupportedPayload)),
			payloadOpenError, ExitUnsupported,
		},
		{
			"unsupported payload, unwrapped",
			fmt.Errorf("%w: unrecognized payload container", flatpkg.ErrUnsupportedPayload),
			payloadOpenError, ExitUnsupported,
		},
		{
			"other payload error",
			errors.New("unable to read Payload"),
			payloadOpenError, ExitError,
		},
		{
			"unsupported on platform",
			fmt.Errorf("unable to write out.pkg: %w", fmt.Errorf("%w: preserving ownership", flatpkg.ErrUnsupportedOnPlatform)),
			buildError, ExitUnsupported,
		},
		{
			"compression needs a minimum",
			fmt.Errorf("outer: %w", flatpkg.ErrCompressionNeedsMinOS),
			buildError, ExitUsage,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(tc.map_(tc.err)); got != tc.want {
				t.Errorf("exit %d, want %d", got, tc.want)
			}
		})
	}
}
