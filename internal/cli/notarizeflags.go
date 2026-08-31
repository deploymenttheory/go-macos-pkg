// --notarize on build and product: sign, submit, wait, staple in one go.
package cli

import (
	"fmt"

	"github.com/deploymenttheory/go-macos-pkg/pkg/notary"
	"github.com/spf13/cobra"
)

var buildNotarize bool

// addNotarizeFlags registers --notarize and the credential flags. The
// credentials take the notary- prefix here, beside the sign- prefix the
// signing flags already use.
func addNotarizeFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&buildNotarize, "notarize", false, "after building and signing, submit to Apple's notary service, wait for acceptance and staple the ticket")
	addNotaryFlags(cmd, "notary-")
}

// notarizeAfterBuild runs notarization for a freshly built package when
// --notarize was given. It insists on a signature: an unsigned package
// would be rejected after a long wait.
func notarizeAfterBuild(cmd *cobra.Command, m *manifestFile, path string, signed bool) error {
	if !buildNotarize {
		return nil
	}
	if !signed {
		return usageErrorf("--notarize needs a signed package: add --sign-p12 (or --sign-cert/--sign-key)")
	}
	svc, err := notaryService(cmd, m)
	if err != nil {
		return err
	}
	_, err = notarizeFile(svc, path, "", true, true, false, notary.SubmitOptions{})
	if err != nil {
		return fmt.Errorf("%s was built and signed, but notarization failed: %w", path, err)
	}
	return nil
}
