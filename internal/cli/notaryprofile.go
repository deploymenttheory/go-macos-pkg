// Credential profiles: a name for a set of notarization credentials, so a
// local workflow does not repeat three flags on every command.
//
// notarytool keeps its profiles in the Keychain, which does not exist off
// macOS. This keeps a small file instead, and deliberately keeps a pointer
// rather than a copy: the profile holds the key ID, the issuer ID and the
// path to the .p8, never the key itself. A file of secrets that outlives
// the one you already have is a liability, and copying a private key to
// make a command shorter is not a trade worth offering.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// notaryProfile is what a named profile remembers.
type notaryProfile struct {
	KeyID    string `json:"keyId"`
	IssuerID string `json:"issuerId"`
	// PrivateKeyPath points at the .p8; the key is not copied here.
	PrivateKeyPath string `json:"privateKeyPath"`
}

// notaryProfileDir is where profiles live.
func notaryProfileDir() (string, error) {
	home, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "macospkg", "notary"), nil
}

// notaryProfilePath resolves one profile's file, refusing a name that
// would reach outside the directory.
func notaryProfilePath(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", usageErrorf("%q is not a profile name", name)
	}
	dir, err := notaryProfileDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}

// saveNotaryProfile writes a profile, readable only by its owner.
func saveNotaryProfile(name string, p notaryProfile) (string, error) {
	path, err := notaryProfilePath(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// loadNotaryProfile reads a profile.
func loadNotaryProfile(name string) (*notaryProfile, error) {
	path, err := notaryProfilePath(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // the profile name chooses the file on purpose
	if err != nil {
		return nil, withCode(ExitAuth, fmt.Errorf("no notary profile named %q (store one with: macospkg notarize store-credentials %s ...)", name, name))
	}
	var p notaryProfile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, withCode(ExitAuth, fmt.Errorf("profile %q is not readable: %w", name, err))
	}
	return &p, nil
}
