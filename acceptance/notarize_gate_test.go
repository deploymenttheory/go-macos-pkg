// What notarize will and will not send, decided before anything is uploaded.
//
// These need no credentials: the point is which files get as far as needing
// them. A package is opened and its signature checked here, because that can
// be done locally and a package Apple would reject is not worth uploading. A
// disk image or a zip goes up as it is.
package acceptance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotarizeAcceptsDiskImagesAndArchives(t *testing.T) {
	work := t.TempDir()
	// Not real archives: nothing here opens them, which is the point.
	// Reaching the credential check is how we know they were accepted.
	for _, name := range []string{"app.dmg", "app.zip", "APP.DMG"} {
		path := filepath.Join(work, name)
		require.NoError(t, os.WriteFile(path, []byte("not really an archive\n"), 0o644))
		_, stderr, code := run(t, "notarize", path)
		assert.Equalf(t, 4, code, "%s should have reached the credential check", name)
		assert.Containsf(t, stderr, "credentials", "%s should have reached the credential check", name)
	}
}

func TestNotarizeRefusesWhatAppleWouldReject(t *testing.T) {
	work := t.TempDir()

	// A bundle is a directory, and the service takes an archive. Saying so
	// beats failing after the upload.
	bundle := filepath.Join(work, "Thing.app")
	require.NoError(t, os.MkdirAll(bundle, 0o755))
	_, stderr, code := run(t, "notarize", bundle)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "archive it first")

	// An unsigned package is refused before the upload; that is covered by
	// TestNotarizePreconditions. What is worth checking here is the other
	// half: --force says submit it anyway, and reaches the credentials.
	pkg, _ := fixture(t, "component-basic.pkg")
	_, stderr, code = run(t, "notarize", pkg, "--force")
	assert.Equal(t, 4, code)
	assert.Contains(t, stderr, "credentials")

	// A file that is not there at all is a missing-package error.
	_, _, code = run(t, "notarize", filepath.Join(work, "absent.pkg"))
	assert.Equal(t, 3, code)
}
