// Signing flags shared by build, product and sign, registered from one
// place so the three commands cannot drift apart, and the identity
// loading behind them.
package cli

import (
	"bufio"
	"crypto"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/deploymenttheory/go-macos-pkg/pkg/pkgsign"
	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
	"github.com/spf13/cobra"
)

// signingFlags holds the values of one command's signing flags.
type signingFlags struct {
	prefix      string
	p12         string
	p12Password string
	p12Stdin    bool
	cert        string
	key         string
	chain       string
	timestamp   string
}

// signingByCommand keeps each command's flag values apart: build and
// product both use the "sign-" prefix.
var signingByCommand = map[*cobra.Command]*signingFlags{}

// addSigningFlags registers the signing flags on cmd with the given prefix
// ("" for the sign command, "sign-" for build and product).
func addSigningFlags(cmd *cobra.Command, prefix string) {
	sf := &signingFlags{prefix: prefix}
	signingByCommand[cmd] = sf
	f := cmd.Flags()
	f.StringVar(&sf.p12, prefix+"p12", "", "PKCS#12 file holding the Developer ID Installer certificate and key")
	f.StringVar(&sf.p12Password, prefix+"p12-password", "", "password of the PKCS#12 file (prefer --"+prefix+"p12-password-stdin, MACOSPKG_P12_PASSWORD, or the manifest's p12_password_env)")
	f.BoolVar(&sf.p12Stdin, prefix+"p12-password-stdin", false, "read the PKCS#12 password from standard input")
	f.StringVar(&sf.cert, prefix+"cert", "", "PEM certificate to sign with (with --"+prefix+"key)")
	f.StringVar(&sf.key, prefix+"key", "", "PEM private key matching --"+prefix+"cert")
	f.StringVar(&sf.chain, prefix+"chain", "", "PEM file of intermediate certificates to embed")
	f.StringVar(&sf.timestamp, prefix+"timestamp", "", "RFC 3161 timestamp server URL, or \"none\" to sign without a timestamp (default Apple's server)")
}

// timestamping resolves whether to timestamp, and against which server.
//
// The flag decides when it is given: "none" turns timestamping off, any
// other value is the server URL. Otherwise the manifest's
// signing_info.timestamp decides, and failing that a signature is
// timestamped, since a timestamp is what keeps it valid past the
// certificate's expiry. An empty URL means the default server.
func (sf *signingFlags) timestamping(m *manifestFile) (on bool, url string) {
	if strings.EqualFold(sf.timestamp, "none") {
		return false, ""
	}
	if sf.timestamp != "" {
		return true, sf.timestamp
	}
	if m != nil && m.SigningInfo != nil && m.SigningInfo.Timestamp != nil {
		return *m.SigningInfo.Timestamp, ""
	}
	return true, ""
}

// signingRequested reports whether any signing input was given.
func (sf *signingFlags) requested(m *manifestFile) bool {
	if sf == nil {
		return false
	}
	if sf.p12 != "" || sf.cert != "" || sf.key != "" {
		return true
	}
	return m != nil && m.SigningInfo != nil && (m.SigningInfo.P12Path != "" || m.SigningInfo.CertificatePath != "")
}

// loadIdentity resolves the identity from flags, then the manifest.
func (sf *signingFlags) loadIdentity(m *manifestFile) (*pkgsign.Identity, error) {
	p12, cert, key, chain := sf.p12, sf.cert, sf.key, sf.chain
	password := sf.p12Password
	passwordVar := "MACOSPKG_P12_PASSWORD" //nolint:gosec // the variable's name, not a secret

	if m != nil && m.SigningInfo != nil {
		si := m.SigningInfo
		if p12 == "" && cert == "" {
			p12, cert, key, chain = si.P12Path, si.CertificatePath, si.KeyPath, si.ChainPath
		}
		if si.P12PasswordEnv != "" {
			passwordVar = si.P12PasswordEnv
		}
	}
	switch {
	case p12 != "":
		if sf.p12Stdin {
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil && line == "" {
				return nil, withCode(ExitAuth, fmt.Errorf("unable to read the PKCS#12 password from stdin: %w", err))
			}
			password = strings.TrimRight(line, "\r\n")
		} else if password == "" {
			password = os.Getenv(passwordVar)
		}
		data, err := os.ReadFile(p12)
		if err != nil {
			return nil, withCode(ExitAuth, fmt.Errorf("unable to read %s: %w", p12, err))
		}
		id, err := pkgsign.LoadP12(data, password)
		if err != nil {
			return nil, identityError(err)
		}
		return id, nil
	case cert != "":
		if key == "" {
			return nil, usageErrorf("--%skey is required with --%scert", prefixOf(sf), prefixOf(sf))
		}
		id, err := pkgsign.LoadPEMFiles(cert, key, chain)
		if err != nil {
			return nil, identityError(err)
		}
		return id, nil
	case key != "":
		return nil, usageErrorf("--%scert is required with --%skey", prefixOf(sf), prefixOf(sf))
	}
	return nil, usageErrorf("no signing identity: give --%sp12 or --%scert/--%skey", prefixOf(sf), prefixOf(sf), prefixOf(sf))
}

func prefixOf(sf *signingFlags) string { return sf.prefix }

// identityError maps identity failures onto exit codes: a bad password or
// a mismatched key is an authentication problem, an ECDSA key a limitation.
func identityError(err error) error {
	switch {
	case errors.Is(err, pkgsign.ErrNotRSA):
		return withCode(ExitUnsupported, err)
	case errors.Is(err, pkgsign.ErrBadPassword), errors.Is(err, pkgsign.ErrKeyMismatch):
		return withCode(ExitAuth, err)
	case os.IsNotExist(err):
		return withCode(ExitAuth, err)
	}
	return withCode(ExitAuth, err)
}

// signerFromFlags builds a signer from cmd's signing flags, falling back
// to the manifest's signing_info. It returns nil when no signing was
// requested. hash is the digest the archive will use.
func signerFromFlags(cmd *cobra.Command, m *manifestFile, hash crypto.Hash) (xar.Signer, error) {
	sf := signingByCommand[cmd]
	if !sf.requested(m) {
		return nil, nil
	}
	id, err := sf.loadIdentity(m)
	if err != nil {
		return nil, err
	}
	o := pkgsign.SignOptions{Hash: hash, SigningTime: opts.SourceDateEpoch}
	if on, url := sf.timestamping(m); on {
		o.Timestamper = pkgsign.NewHTTPTimestamper(url)
	}
	s, err := pkgsign.NewSigner(id, o)
	if err != nil {
		if o.Timestamper != nil {
			return nil, fmt.Errorf("%w (use --%stimestamp none to sign without a timestamp)", err, sf.prefix)
		}
		return nil, err
	}
	verbosef("signing as %s (team %s)", pkgsign.CommonName(id.Cert), id.TeamID())
	return s, nil
}
