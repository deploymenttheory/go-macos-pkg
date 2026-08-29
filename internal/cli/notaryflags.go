// Notarization credentials shared by notarize, build and product.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/deploymenttheory/go-macos-pkg/internal/tools"
	"github.com/deploymenttheory/go-macos-pkg/pkg/notary"
	"github.com/spf13/cobra"
)

type notaryFlags struct {
	keyID      string
	issuerID   string
	privateKey string
}

var notaryByCommand = map[*cobra.Command]*notaryFlags{}

// addNotaryFlags registers --key-id, --issuer-id and --private-key.
func addNotaryFlags(cmd *cobra.Command) {
	nf := &notaryFlags{}
	notaryByCommand[cmd] = nf
	f := cmd.Flags()
	f.StringVar(&nf.keyID, "key-id", "", "App Store Connect API key ID (or "+notary.EnvKeyID+")")
	f.StringVar(&nf.issuerID, "issuer-id", "", "App Store Connect API issuer ID (or "+notary.EnvIssuerID+")")
	f.StringVar(&nf.privateKey, "private-key", "", "App Store Connect API private key file, .p8 (or "+notary.EnvPrivateKeyPEM+" / "+notary.EnvPrivateKeyPath+")")
}

// notaryService resolves credentials from flags, then the manifest, then
// the environment, and opens the service. Exit 4 when they are missing.
func notaryService(cmd *cobra.Command, m *manifestFile) (notary.Service, error) {
	nf := notaryByCommand[cmd]
	c := &notary.Credentials{}
	if nf != nil {
		c.KeyID, c.IssuerID = nf.keyID, nf.issuerID
		if nf.privateKey != "" {
			data, err := os.ReadFile(nf.privateKey) //nolint:gosec // --private-key names the key file on purpose
			if err != nil {
				return nil, withCode(ExitAuth, fmt.Errorf("unable to read --private-key: %w", err))
			}
			c.PrivateKey = data
		}
	}
	if m != nil && m.NotarizationInfo != nil {
		ni := m.NotarizationInfo
		if c.KeyID == "" {
			c.KeyID = ni.KeyID
		}
		if c.IssuerID == "" {
			c.IssuerID = ni.IssuerID
		}
		if len(c.PrivateKey) == 0 && ni.PrivateKeyPath != "" {
			data, err := os.ReadFile(ni.PrivateKeyPath) //nolint:gosec // the manifest names the key file on purpose
			if err != nil {
				return nil, withCode(ExitAuth, fmt.Errorf("unable to read the manifest's private_key_path: %w", err))
			}
			c.PrivateKey = data
		}
	}
	if c.KeyID == "" {
		c.KeyID = os.Getenv(notary.EnvKeyID)
	}
	if c.IssuerID == "" {
		c.IssuerID = os.Getenv(notary.EnvIssuerID)
	}
	if len(c.PrivateKey) == 0 {
		env, err := notary.CredentialsFromEnv()
		if err == nil {
			c.PrivateKey = env.PrivateKey
		} else if pem := os.Getenv(notary.EnvPrivateKeyPEM); pem != "" {
			c.PrivateKey = []byte(pem)
		} else if path := os.Getenv(notary.EnvPrivateKeyPath); path != "" {
			data, err := os.ReadFile(path) //nolint:gosec // the variable names the key file on purpose
			if err != nil {
				return nil, withCode(ExitAuth, fmt.Errorf("unable to read %s: %w", notary.EnvPrivateKeyPath, err))
			}
			c.PrivateKey = data
		}
	}
	svc, err := notary.NewService(c, "macospkg/"+tools.Version())
	if err != nil {
		if errors.Is(err, notary.ErrCredentials) {
			return nil, withCode(ExitAuth, err)
		}
		return nil, err
	}
	return svc, nil
}

// notaryError maps notary failures onto the exit-code contract.
func notaryError(err error) error {
	switch {
	case errors.Is(err, notary.ErrCredentials):
		return withCode(ExitAuth, err)
	case errors.Is(err, notary.ErrRejected):
		return withCode(ExitNotaryRejected, err)
	case errors.Is(err, notary.ErrTimeout):
		return withCode(ExitTimeout, err)
	case errors.Is(err, notary.ErrUnsupported):
		return withCode(ExitUnsupported, err)
	}
	return err
}
