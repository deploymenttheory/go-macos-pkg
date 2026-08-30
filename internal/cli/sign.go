// macospkg sign PKG OUT.pkg: sign a package with a Developer ID
// Installer certificate.
package cli

import (
	"crypto"
	"errors"
	"fmt"
	"os"

	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
	"github.com/deploymenttheory/go-macos-pkg/pkg/staple"
	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
	"github.com/spf13/cobra"
)

var signDigest string

var signCmd = &cobra.Command{
	Use:   "sign PKG OUT.pkg",
	Short: "Sign a package with a Developer ID Installer certificate",
	Long: `Sign a package the way productsign does: an RSA signature and a CMS
signature over the archive's table-of-contents digest, with the certificate
chain embedded, and by default an RFC 3161 timestamp from Apple's server so
the signature outlives the certificate.

The identity comes from a PKCS#12 file (--p12, password via
--p12-password-stdin, MACOSPKG_P12_PASSWORD or --p12-password) or from PEM
files (--cert and --key, plus --chain for intermediates). No keychain is
involved, so this works the same on Linux and Windows.

An existing signature is replaced. A stapled notarization ticket is
removed, since re-signing invalidates it; notarize and staple again.

Examples:
  macospkg sign Foo.pkg Foo-signed.pkg --p12 developer-id.p12 --p12-password-stdin < password.txt
  macospkg sign Foo.pkg Foo-signed.pkg --cert cert.pem --key key.pem --chain intermediates.pem
  macospkg sign Foo.pkg Foo-signed.pkg --p12 id.p12 --no-timestamp`,
	Args: exactArgs(2, "PKG OUT.pkg"),
	RunE: runSign,
}

func init() {
	addSigningFlags(signCmd, "")
	signCmd.Flags().StringVar(&signDigest, "digest", "", "table-of-contents digest to write: sha256 (default) or sha1")
}

// signReport is the JSON schema for macospkg sign.
type signReport struct {
	Input       string `json:"input"`
	Output      string `json:"output"`
	Signer      string `json:"signer"`
	TeamID      string `json:"teamId,omitempty"`
	Digest      string `json:"digest"`
	Timestamped bool   `json:"timestamped"`
	Unstapled   bool   `json:"unstapled"`
	SHA256      string `json:"sha256"`
}

func runSign(cmd *cobra.Command, args []string) error {
	sf := signingByCommand[cmd]
	if !sf.requested(nil) {
		return usageErrorf("a signing identity is required: --p12 FILE or --cert FILE --key FILE")
	}
	alg := xar.ChecksumSHA256
	hash := crypto.SHA256
	switch signDigest {
	case "", "sha256":
	case "sha1":
		alg, hash = xar.ChecksumSHA1, crypto.SHA1
	default:
		return usageErrorf("invalid --digest %q: want sha256 or sha1", signDigest)
	}

	p, err := openPackage(args[0])
	if err != nil {
		return err
	}
	defer p.Close()

	signer, err := signerFromFlags(cmd, nil, hash)
	if err != nil {
		return err
	}
	if signer == nil {
		return usageErrorf("a signing identity is required")
	}

	unstapled := false
	if f, err := os.Open(p.Path); err == nil {
		if st, err := f.Stat(); err == nil {
			if _, err := staple.Read(f, st.Size()); err == nil {
				unstapled = true
				progressf("removing the stapled notarization ticket: re-signing invalidates it")
			}
		}
		f.Close()
	}

	_, err = writePackage(args[1], func(w *os.File) (*flatpkg.BuildResult, error) {
		return nil, xar.Rewrite(p.XAR, w, xar.RewriteOptions{ChecksumAlg: alg, Signer: signer, CreationTime: opts.SourceDateEpoch})
	})
	if err != nil {
		return signError(err)
	}
	report := signReport{Input: p.Path, Output: args[1], Digest: alg.String(), Timestamped: !sf.noTimestamp, Unstapled: unstapled}
	if s, ok := signer.(interface{ SignerName() (string, string) }); ok {
		report.Signer, report.TeamID = s.SignerName()
	}
	report.SHA256, _ = sha256File(args[1])
	if opts.Output == "json" {
		return jsonOut(report)
	}
	progressf("signed %s -> %s (%s%s)", p.Path, args[1], report.Signer, timestampLabel(report.Timestamped))
	return nil
}

func timestampLabel(ts bool) string {
	if ts {
		return ", timestamped"
	}
	return ""
}

func signError(err error) error {
	var coded *codedError
	if errors.As(err, &coded) {
		return err
	}
	return fmt.Errorf("signing failed: %w", err)
}
