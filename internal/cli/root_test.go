package cli

import (
	"testing"
	"time"
)

func TestParseSourceDateEpoch(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{"0", time.Unix(0, 0).UTC(), false},
		{"1700000000", time.Unix(1700000000, 0).UTC(), false},
		{" 1700000000 ", time.Unix(1700000000, 0).UTC(), false},
		{"-1", time.Time{}, true},
		{"1.5", time.Time{}, true},
		{"soon", time.Time{}, true},
		{"", time.Time{}, true},
	}
	for _, tc := range cases {
		got, err := parseSourceDateEpoch(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseSourceDateEpoch(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if err != nil {
			if exitCodeFor(err) != ExitUsage {
				t.Errorf("parseSourceDateEpoch(%q) error is exit %d, want usage", tc.in, exitCodeFor(err))
			}
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("parseSourceDateEpoch(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
