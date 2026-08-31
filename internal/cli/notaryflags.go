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
	prefix     string
	keyID      string
	issuerID   string
	privateKey string
	profile    string
}

var notaryByCommand = map[*cobra.Command]*notaryFlags{}

// addNotaryFlags registers the App Store Connect credentials.
//
// prefix is empty on notarize, where the flags are the whole point of the
// command, and "notary-" on build and product, where they sit beside the
// signing flags. Without it --key would be a second key file next to
// --sign-key on the same command, and passing one for the other would fail
// as an authentication error rather than as a signing error.
func addNotaryFlags(cmd *cobra.Command, prefix string) {
	nf := &notaryFlags{prefix: prefix}
	notaryByCommand[cmd] = nf
	f := cmd.Flags()
	f.StringVar(&nf.keyID, prefix+"key-id", "", "App Store Connect API key ID (or "+notary.EnvKeyID+")")
	f.StringVar(&nf.issuerID, prefix+"issuer", "", "App Store Connect API issuer ID, a UUID (or "+notary.EnvIssuerID+")")
	f.StringVar(&nf.privateKey, prefix+"key", "", "App Store Connect API private key file, .p8 (or "+notary.EnvPrivateKeyPEM+" / "+notary.EnvPrivateKeyPath+")")
	if prefix == "" {
		f.StringVarP(&nf.profile, "profile", "p", "", "credentials stored earlier under this name by notarize store-credentials")
	} else {
		f.StringVar(&nf.profile, prefix+"profile", "", "credentials stored earlier under this name by notarize store-credentials")
	}
}

// notaryService resolves credentials from flags, then the manifest, then
// the environment, and opens the service. Exit 4 when they are missing.
func notaryService(cmd *cobra.Command, m *manifestFile) (notary.Service, error) {
	nf := notaryByCommand[cmd]
	c := &notary.Credentials{}
	if nf != nil {
		// A profile fills in what the flags left empty, so a stored
		// profile can still have one part of it overridden.
		if nf.profile != "" {
			p, err := loadNotaryProfile(nf.profile)
			if err != nil {
				return nil, err
			}
			if nf.keyID == "" {
				nf.keyID = p.KeyID
			}
			if nf.issuerID == "" {
				nf.issuerID = p.IssuerID
			}
			if nf.privateKey == "" {
				nf.privateKey = p.PrivateKeyPath
			}
		}
		c.KeyID, c.IssuerID = nf.keyID, nf.issuerID
		if nf.privateKey != "" {
			data, err := os.ReadFile(nf.privateKey) //nolint:gosec // the key flag names the key file on purpose
			if err != nil {
				return nil, withCode(ExitAuth, fmt.Errorf("unable to read --%skey: %w", nf.prefix, err))
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
