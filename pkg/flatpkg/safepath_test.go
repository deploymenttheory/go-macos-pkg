package flatpkg

import "testing"

func TestSafeRelPathRejectsTraversal(t *testing.T) {
	cases := []struct {
		name       string
		wantReason bool
	}{
		{"../evil", true},
		{"../../tmp/evil", true},
		{"./../x", true},
		{"a/../../b", true},
		{"/etc/passwd", true},
		{"//etc/passwd", true},
		{"./" + "..", true},
		{"with\x00nul", true},
		// Legitimate names must pass.
		{"foo.pkg", false},
		{"a/b/c", false},
		{"./a/b", false},
		{"..foo", false}, // leading dots that are not a traversal
	}
	for _, tc := range cases {
		rel, _, reason := SafeRelPath(tc.name)
		if (reason != "") != tc.wantReason {
			t.Errorf("SafeRelPath(%q) reason=%q, wantReason=%v", tc.name, reason, tc.wantReason)
			continue
		}
		if reason == "" {
			// A cleared name must never climb out.
			if rel == ".." || len(rel) >= 3 && rel[:3] == "../" {
				t.Errorf("SafeRelPath(%q) returned escaping rel %q", tc.name, rel)
			}
		}
	}
}
