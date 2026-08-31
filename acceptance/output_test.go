// Machine-readable output: JSON, and the property list macOS reads
// natively.
package acceptance

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlistOutputIsValid checks that -o plist produces a property list
// Apple's own tools accept, for a single report and for a listing.
//
// The two shapes differ. JSON is a line per value, so a listing streams; a
// property list is one document, so a listing becomes an array. That is the
// only shape it can take, and it is worth pinning that the listing did not
// come out as several documents glued together.
func TestPlistOutputIsValid(t *testing.T) {
	requireTools(t, "plutil")
	pkg, _ := fixture(t, "component-basic.pkg")

	for _, tc := range []struct {
		name, root string
		args       []string
	}{
		{"info", "<dict>", []string{"-o", "plist", "info", pkg}},
		{"list", "<array>", []string{"-o", "plist", "list", pkg}},
		{"list --archive", "<array>", []string{"-o", "plist", "list", "--archive", pkg}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := mustRun(t, tc.args...)
			assert.Contains(t, out, `<!DOCTYPE plist`)
			assert.Contains(t, out, tc.root, "a listing is one array, not several documents")
			assert.Equal(t, 1, strings.Count(out, "<plist version"), "one document, not one per record")
			// plutil is the arbiter of whether it is a property list.
			assert.Contains(t, hostToolStdin(t, out, "plutil", "-lint", "-"), "OK")
		})
	}
}

// TestPlistCarriesTheSameFieldsAsJSON pins that the two formats describe
// the same report, so a script can move between them.
func TestPlistCarriesTheSameFieldsAsJSON(t *testing.T) {
	requireTools(t, "plutil")
	pkg, _ := fixture(t, "component-basic.pkg")

	// plutil converts the property list back to JSON, which should then
	// match what -o json printed.
	asPlist := mustRun(t, "-o", "plist", "info", pkg)
	roundTripped := hostToolStdin(t, asPlist, "plutil", "-convert", "json", "-o", "-", "-")

	var fromPlist, fromJSON map[string]any
	require.NoError(t, json.Unmarshal([]byte(roundTripped), &fromPlist))
	require.NoError(t, json.Unmarshal([]byte(mustRun(t, "-o", "json", "info", pkg)), &fromJSON))

	// Compare the keys rather than the values: JSON has null where a
	// property list simply omits the key, which is the one difference
	// between them and is by design.
	for k, v := range fromJSON {
		if v == nil {
			continue
		}
		assert.Containsf(t, fromPlist, k, "the property list is missing %s", k)
	}
	assert.Equal(t, fromJSON["kind"], fromPlist["kind"])
	attest(t, "plist and json describe the same report")
}

// TestOutputFormatIsChecked keeps an unknown format from being taken for
// text and quietly printing prose to something expecting a document.
func TestOutputFormatIsChecked(t *testing.T) {
	pkg, _ := fixture(t, "component-basic.pkg")
	_, stderr, code := run(t, "-o", "yaml", "info", pkg)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "text, json or plist")
}

// hostToolStdin runs a host tool with something on its standard input,
// which is how plutil reads a document it is handed rather than a file.
func hostToolStdin(t *testing.T, stdin, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "%s %v: %s", name, args, out)
	return string(out)
}
