package exitcode

import "testing"

func TestName(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{OK, "OK"},
		{Error, "Error"},
		{Usage, "Usage"},
		{BadPackage, "BadPackage"},
		{Auth, "Auth"},
		{Unsupported, "Unsupported"},
		{Partial, "Partial"},
		{Signature, "Signature"},
		{NotaryRejected, "NotaryRejected"},
		{Timeout, "Timeout"},
		{42, "Unknown"},
	}
	for _, tc := range cases {
		if got := Name(tc.code); got != tc.want {
			t.Errorf("Name(%d) = %q, want %q", tc.code, got, tc.want)
		}
	}
}

// The numeric values are the contract; a renumbering must fail loudly.
func TestValuesAreStable(t *testing.T) {
	want := map[string]int{
		"OK": 0, "Error": 1, "Usage": 2, "BadPackage": 3, "Auth": 4,
		"Unsupported": 5, "Partial": 6, "Signature": 7, "NotaryRejected": 8, "Timeout": 9,
	}
	for name, code := range want {
		if Name(code) != name {
			t.Errorf("code %d is %q, want %q", code, Name(code), name)
		}
	}
}
