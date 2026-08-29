package cli

import (
	"errors"
	"fmt"
	"testing"
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
