// macospkg verify PKG: verify a package signature.
package cli

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/pkgsign"
	"github.com/deploymenttheory/go-macos-pkg/pkg/staple"
	"github.com/spf13/cobra"
)

var (
	verifyTeamID         string
	verifyAnchors        string
	verifyAllowUntrusted bool
	verifyRequireStapled bool
	verifyRequireDevID   bool
	verifyOnline         bool
	verifyRevocation     bool
)

var verifyCmd = &cobra.Command{
	Use:   "verify PKG",
	Short: "Verify a package signature and, optionally, its stapled ticket",
	Long: `Check a package's signature the way pkgutil --check-signature does, and
say exactly what was found: whether the table-of-contents digest is intact,
whether the RSA and CMS signatures verify, who signed, whether the chain
leads to an Apple root, and whether a notarization ticket is stapled.

Trust is evaluated against Apple's root certificates built into the tool;
--trust-anchors substitutes a PEM file of roots (for a private CA), and
--allow-untrusted reports an unknown chain without failing on it.

Exit code 7 means the package is unsigned or the check failed; the JSON
output lists every failure.

Examples:
  macospkg verify Foo.pkg
  macospkg verify --team-id ABCDE12345 --require-stapled Foo.pkg
  macospkg verify --trust-anchors my-ca.pem Foo.pkg`,
	Args: exactArgs(1, "PKG"),
	RunE: runVerify,
}

func init() {
	f := verifyCmd.Flags()
	f.StringVar(&verifyTeamID, "team-id", "", "require this Apple team identifier")
	f.StringVar(&verifyAnchors, "trust-anchors", "", "PEM file of root certificates to trust instead of Apple's")
	f.BoolVar(&verifyAllowUntrusted, "allow-untrusted", false, "report an untrusted chain without failing")
	f.BoolVar(&verifyRequireStapled, "require-stapled", false, "fail unless a notarization ticket is stapled")
	f.BoolVar(&verifyRequireDevID, "require-developer-id", false, "fail unless the signer is a Developer ID Installer certificate")
	f.BoolVar(&verifyOnline, "online", false, "ask Apple's ticket database whether this exact package was notarized")
	f.BoolVar(&verifyRevocation, "revocation", false, "ask the certificate authority whether the signing certificate has been revoked since it was issued, which chain verification cannot tell you")
}

// verifyReport is the JSON schema for macospkg verify.
type verifyReport struct {
	Path              string        `json:"path"`
	Signed            bool          `json:"signed"`
	Valid             bool          `json:"valid"`
	Digest            string        `json:"digest,omitempty"`
	DigestValid       bool          `json:"digestValid"`
	RSAValid          bool          `json:"rsaValid"`
	CMSValid          bool          `json:"cmsValid"`
	Trusted           bool          `json:"trusted"`
	TrustError        string        `json:"trustError,omitempty"`
	TeamID            string        `json:"teamId,omitempty"`
	DeveloperID       bool          `json:"developerId"`
	SigningTime       string        `json:"signingTime,omitempty"`
	Timestamped       bool          `json:"timestamped"`
	TimestampVerified bool          `json:"timestampVerified"`
	Timestamp         string        `json:"timestamp,omitempty"`
	TimestampError    string        `json:"timestampError,omitempty"`
	Certificates      []certSummary `json:"certificates"`
	Stapled           bool          `json:"stapled"`
	Notarized         *bool         `json:"notarized,omitempty"` // only with --online
	Revoked           *bool         `json:"revoked,omitempty"`   // only with --revocation
	RevokedAt         string        `json:"revokedAt,omitempty"`
	RevocationChecked bool          `json:"revocationChecked"`
	Errors            []string      `json:"errors"`
}

func runVerify(cmd *cobra.Command, args []string) error {
	p, err := openPackage(args[0])
	if err != nil {
		return err
	}
	defer p.Close()

	o := pkgsign.VerifyOptions{TeamID: verifyTeamID, AllowUntrusted: verifyAllowUntrusted, RequireDeveloperID: verifyRequireDevID}
	if verifyAnchors != "" {
		data, err := os.ReadFile(verifyAnchors)
		if err != nil {
			return usageErrorf("unable to read --trust-anchors: %v", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return usageErrorf("--trust-anchors %s holds no certificates", verifyAnchors)
		}
		o.Anchors = pool
	}
	res, err := pkgsign.Verify(p.XAR, o)
	if err != nil && !errors.Is(err, pkgsign.ErrUnsigned) {
		return err
	}
	report := verifyReport{Path: p.Path, Signed: res.Signed, Digest: res.Digest, DigestValid: res.DigestValid,
		RSAValid: res.RSAValid, CMSValid: res.CMSValid, Trusted: res.Trusted, TrustError: res.TrustError,
		TeamID: res.TeamID, DeveloperID: res.DeveloperID, Timestamped: res.Timestamped,
		TimestampVerified: res.TimestampVerified, TimestampError: res.TimestampError,
		Certificates: []certSummary{}, Errors: res.Errors}
	if report.Errors == nil {
		report.Errors = []string{}
	}
	if !res.SigningTime.IsZero() {
		report.SigningTime = res.SigningTime.UTC().Format(time.RFC3339)
	}
	if !res.Timestamp.IsZero() {
		report.Timestamp = res.Timestamp.UTC().Format(time.RFC3339)
	}
	for _, c := range res.Certificates {
		sum := sha256.Sum256(c.Raw)
		report.Certificates = append(report.Certificates, certSummary{
			Subject: c.Subject.String(), Issuer: c.Issuer.String(), TeamID: pkgsign.TeamIDOf(c),
			NotBefore: c.NotBefore.UTC().Format(time.RFC3339), NotAfter: c.NotAfter.UTC().Format(time.RFC3339),
			SHA256: hex.EncodeToString(sum[:]),
		})
	}
	if f, err := os.Open(p.Path); err == nil {
		if st, err := f.Stat(); err == nil {
			if _, err := staple.Read(f, st.Size()); err == nil {
				report.Stapled = true
			}
		}
		f.Close()
	}
	if verifyRequireStapled && !report.Stapled {
		report.Errors = append(report.Errors, "no notarization ticket is stapled")
	}
	if verifyRevocation {
		if err := checkRevocation(res, &report); err != nil {
			return err
		}
	}
	if verifyOnline {
		record, rerr := staple.RecordNameFor(p.XAR)
		notarized := false
		if rerr == nil {
			_, lerr := stapleLookup().Fetch(context.Background(), record)
			switch {
			case lerr == nil:
				notarized = true
			case errors.Is(lerr, staple.ErrNoTicket):
			default:
				return fmt.Errorf("unable to query Apple's ticket database: %w", lerr)
			}
		}
		report.Notarized = &notarized
		if !notarized {
			report.Errors = append(report.Errors, "Apple has no notarization ticket for this package")
		}
	}
	report.Valid = report.Signed && len(report.Errors) == 0

	if structured() {
		if err := jsonOut(report); err != nil {
			return err
		}
	} else {
		printVerify(&report)
	}
	if !report.Valid {
		if !report.Signed {
			return withCode(ExitSignature, fmt.Errorf("%s is not signed", p.Path))
		}
		return withCode(ExitSignature, fmt.Errorf("%s: %s", p.Path, strings.Join(report.Errors, "; ")))
	}
	return nil
}

func printVerify(r *verifyReport) {
	fmt.Printf("Package:   %s\n", r.Path)
	if !r.Signed {
		fmt.Println("Status:    unsigned")
		return
	}
	status := "valid"
	if !r.Valid {
		status = "INVALID"
	}
	fmt.Printf("Status:    %s\n", status)
	fmt.Printf("Digest:    %s (%s)\n", r.Digest, validLabel(r.DigestValid))
	fmt.Printf("RSA:       %s\n", validLabel(r.RSAValid))
	fmt.Printf("CMS:       %s\n", validLabel(r.CMSValid))
	for i, c := range r.Certificates {
		label := "Signer:   "
		if i > 0 {
			label = "Issuer:   "
		}
		fmt.Printf("%s %s (expires %s)\n", label, cn(c.Subject), c.NotAfter[:10])
	}
	if r.TeamID != "" {
		fmt.Printf("Team ID:   %s\n", r.TeamID)
	}
	fmt.Printf("Developer ID Installer: %v\n", r.DeveloperID)
	if r.Trusted {
		fmt.Println("Chain:     trusted")
	} else {
		fmt.Printf("Chain:     UNTRUSTED (%s)\n", r.TrustError)
	}
	if r.Timestamped {
		state := "unverified"
		if r.TimestampVerified {
			state = "verified"
		}
		fmt.Printf("Timestamp: %s (%s)\n", r.Timestamp, state)
	} else {
		fmt.Println("Timestamp: none")
	}
	if r.Stapled {
		fmt.Println("Staple:    notarization ticket present")
	} else {
		fmt.Println("Staple:    none")
	}
	if r.Notarized != nil {
		if *r.Notarized {
			fmt.Println("Notarized: yes (ticket on record with Apple)")
		} else {
			fmt.Println("Notarized: NO (Apple has no ticket for this package)")
		}
	}
	switch {
	case r.Revoked != nil && *r.Revoked:
		at := ""
		if r.RevokedAt != "" {
			at = " on " + r.RevokedAt
		}
		fmt.Printf("Revoked:   YES (the authority withdrew this certificate%s)\n", at)
	case r.Revoked != nil:
		fmt.Println("Revoked:   no (the authority still vouches for this certificate)")
	}
	for _, e := range r.Errors {
		fmt.Printf("Error:     %s\n", e)
	}
}

// checkRevocation asks the authority whether the signer is still good, and
// records the answer in the report.
//
// A certificate can only be checked against its issuer, so this needs the
// chain the package embeds. Where the package carries only the leaf there
// is nothing to ask with, which is said plainly rather than passed off as
// a clean result.
func checkRevocation(res *pkgsign.Result, report *verifyReport) error {
	if !res.Signed || res.Signer == nil {
		// Already reported as unsigned; saying it twice helps nobody.
		return nil
	}
	if len(res.Certificates) < 2 {
		report.Errors = append(report.Errors, "revocation: the package embeds no issuer certificate to check the signer against")
		return nil
	}
	status, err := pkgsign.CheckRevocation(context.Background(), nil, res.Certificates[0], res.Certificates[1])
	if err != nil {
		report.Errors = append(report.Errors, "revocation: "+err.Error())
		return nil
	}
	report.RevocationChecked = status.Checked
	if !status.Checked {
		// Not an error: a certificate naming no responder cannot be
		// asked about. Worth remarking on for a Developer ID one, since
		// Apple's do carry a responder.
		verbosef("the signing certificate names no revocation responder")
		return nil
	}
	revoked := status.Revoked
	report.Revoked = &revoked
	if revoked {
		if !status.RevokedAt.IsZero() {
			report.RevokedAt = status.RevokedAt.UTC().Format(time.RFC3339)
		}
		report.Valid = false
		report.Errors = append(report.Errors, "revocation: the signing certificate has been revoked")
	}
	return nil
}
