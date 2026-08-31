// macospkg notarize PKG: submit a package to Apple's notary service.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/notary"
	"github.com/deploymenttheory/go-macos-pkg/pkg/staple"
	"github.com/spf13/cobra"
)

var (
	notarizeWait     bool
	notarizeTimeout  time.Duration
	notarizeInterval time.Duration
	notarizeStaple   bool
	notarizeLog      bool
	notarizeName     string
	notarizeForce    bool
	notarizeWebhook  string
)

var notarizeCmd = &cobra.Command{
	Use:   "notarize PKG",
	Short: "Submit a package to Apple's notary service; wait and staple",
	Long: `Submit a signed package to Apple's notary service, the way notarytool
submit does, from any platform. With --wait the command polls until Apple
has a verdict and exits 0 for Accepted, 8 for Invalid or Rejected (printing
the developer log's issues), or 9 if --timeout passes first. With --staple
the ticket is fetched and stapled to the package once accepted.

Credentials are an App Store Connect API key: --key-id, --issuer-id and
--private-key (the .p8 file), or the environment variables APPLE_KEY_ID,
APPLE_ISSUER_ID and APPLE_PRIVATE_KEY_PEM (its content) or
APPLE_PRIVATE_KEY_PATH.

The package must already be signed with a Developer ID Installer
certificate; Apple rejects unsigned submissions, so this refuses them
unless --force is given.

Subcommands: notarize status ID, notarize log ID, notarize wait ID,
notarize list.

Examples:
  macospkg notarize Foo.pkg --wait --staple
  macospkg notarize Foo.pkg                    # prints the submission id
  macospkg notarize status 2efe2717-52ef-43a5-96dc-0797e4ca1041
  macospkg notarize log 2efe2717-52ef-43a5-96dc-0797e4ca1041`,
	Args: exactArgs(1, "PKG"),
	RunE: runNotarize,
}

var notarizeStatusCmd = &cobra.Command{
	Use:   "status ID",
	Short: "Show a submission's status",
	Args:  exactArgs(1, "ID"),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := notaryService(cmd, nil)
		if err != nil {
			return err
		}
		st, err := svc.Status(context.Background(), args[0])
		if err != nil {
			return notaryError(err)
		}
		if structured() {
			return jsonOut(st)
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", st.ID, st.Status, st.CreatedDate, st.Name)
		return nil
	},
}

var notarizeLogCmd = &cobra.Command{
	Use:   "log ID",
	Short: "Fetch a submission's developer log (JSON)",
	Args:  exactArgs(1, "ID"),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := notaryService(cmd, nil)
		if err != nil {
			return err
		}
		log, err := notary.FetchLog(context.Background(), svc, nil, args[0])
		if err != nil {
			return notaryError(err)
		}
		if _, err := os.Stdout.Write(log); err != nil {
			return err
		}
		fmt.Println()
		return nil
	},
}

var notarizeWaitCmd = &cobra.Command{
	Use:   "wait ID",
	Short: "Wait for a submission to finish",
	Args:  exactArgs(1, "ID"),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := notaryService(cmd, nil)
		if err != nil {
			return err
		}
		st, err := waitFor(svc, args[0])
		if structured() && st != nil {
			if jerr := jsonOut(st); jerr != nil {
				return jerr
			}
		}
		return err
	},
}

var notarizeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent submissions",
	Args:  exactArgs(0, "no arguments"),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := notaryService(cmd, nil)
		if err != nil {
			return err
		}
		list, err := svc.List(context.Background())
		if err != nil {
			return notaryError(err)
		}
		if structured() {
			for _, st := range list {
				if err := jsonOut(st); err != nil {
					return err
				}
			}
			return nil
		}
		for _, st := range list {
			fmt.Printf("%s\t%s\t%s\t%s\n", st.ID, st.Status, st.CreatedDate, st.Name)
		}
		return nil
	},
}

var notarizeStoreCmd = &cobra.Command{
	Use:   "store-credentials NAME",
	Short: "Remember a set of notarization credentials under a name",
	Long: `Write the key ID, the issuer ID and the path to the .p8 under a name, so
later commands can say --profile NAME instead of repeating all three.

The private key is not copied. The profile holds the path to it, so the
key stays wherever you keep it and there is one copy of the secret rather
than two. notarytool stores its profiles in the Keychain, which does not
exist off macOS; this is a file, readable only by you, under the
directory the operating system gives for application configuration.

Examples:
  macospkg notarize store-credentials release \
      --key-id ABC123 --issuer-id 1234-5678 --private-key ~/keys/AuthKey.p8
  macospkg notarize Foo.pkg --profile release --wait --staple`,
	Args: exactArgs(1, "NAME"),
	RunE: runNotarizeStore,
}

// runNotarizeStore writes a credential profile.
func runNotarizeStore(cmd *cobra.Command, args []string) error {
	nf := notaryByCommand[cmd]
	if nf == nil || nf.keyID == "" || nf.issuerID == "" || nf.privateKey == "" {
		return usageErrorf("store-credentials needs --key-id, --issuer-id and --private-key")
	}
	// Resolve the key path now, so a profile written from one directory
	// still works from another, and fail here rather than at the first
	// use if the file is not there.
	keyPath, err := filepath.Abs(nf.privateKey)
	if err != nil {
		return err
	}
	if _, err := os.Stat(keyPath); err != nil {
		return withCode(ExitAuth, fmt.Errorf("unable to read --private-key: %w", err))
	}
	path, err := saveNotaryProfile(args[0], notaryProfile{
		KeyID: nf.keyID, IssuerID: nf.issuerID, PrivateKeyPath: keyPath,
	})
	if err != nil {
		return err
	}
	if structured() {
		return jsonOut(struct {
			Profile        string `json:"profile"`
			Path           string `json:"path"`
			KeyID          string `json:"keyId"`
			IssuerID       string `json:"issuerId"`
			PrivateKeyPath string `json:"privateKeyPath"`
		}{args[0], path, nf.keyID, nf.issuerID, keyPath})
	}
	progressf("wrote %s; the private key stays at %s", path, keyPath)
	return nil
}

func init() {
	f := notarizeCmd.Flags()
	f.BoolVar(&notarizeWait, "wait", false, "wait for Apple's verdict")
	f.DurationVar(&notarizeTimeout, "timeout", 30*time.Minute, "how long --wait waits before exiting 9")
	f.DurationVar(&notarizeInterval, "poll-interval", 30*time.Second, "how often --wait polls")
	f.BoolVar(&notarizeStaple, "staple", false, "staple the ticket once accepted (implies --wait)")
	f.BoolVar(&notarizeLog, "log", false, "print the developer log once finished (always printed on rejection)")
	f.StringVar(&notarizeName, "name", "", "submission name shown in App Store Connect (default: the file name)")
	f.BoolVar(&notarizeForce, "force", false, "submit even if the file is not signed")
	f.StringVar(&notarizeWebhook, "webhook", "", "public URL for Apple to post the verdict to when notarization finishes, so a job need not sit and poll; best effort, so keep --wait or a later status check as the answer you rely on")
	for _, c := range []*cobra.Command{notarizeCmd, notarizeStatusCmd, notarizeLogCmd, notarizeWaitCmd, notarizeListCmd, notarizeStoreCmd} {
		addNotaryFlags(c)
	}
	notarizeWaitCmd.Flags().DurationVar(&notarizeTimeout, "timeout", 30*time.Minute, "how long to wait before exiting 9")
	notarizeWaitCmd.Flags().DurationVar(&notarizeInterval, "poll-interval", 30*time.Second, "how often to poll")
	notarizeCmd.AddCommand(notarizeStatusCmd, notarizeLogCmd, notarizeWaitCmd, notarizeListCmd, notarizeStoreCmd)
}

// notarizeReport is the JSON schema for macospkg notarize.
type notarizeReport struct {
	Package      string          `json:"package"`
	SubmissionID string          `json:"submissionId"`
	Name         string          `json:"name"`
	SHA256       string          `json:"sha256"`
	Status       string          `json:"status"`
	Stapled      bool            `json:"stapled"`
	Log          json.RawMessage `json:"log,omitempty"`
}

func runNotarize(cmd *cobra.Command, args []string) error {
	if err := checkNotarizable(args[0]); err != nil {
		return err
	}

	svc, err := notaryService(cmd, nil)
	if err != nil {
		return err
	}
	report, err := notarizeFile(svc, args[0], notarizeName, notarizeWait || notarizeStaple, notarizeStaple, notarizeLog,
		notary.SubmitOptions{Webhook: notarizeWebhook})
	if structured() && report != nil {
		if jerr := jsonOut(report); jerr != nil {
			return jerr
		}
	}
	return err
}

// notarizeFile runs the submit / wait / staple sequence used by notarize
// and by build --notarize.
func notarizeFile(svc notary.Service, path, name string, wait, doStaple, printLog bool, o notary.SubmitOptions) (*notarizeReport, error) {
	if name == "" {
		name = filepath.Base(path)
	}
	ctx := context.Background()
	progressf("submitting %s to Apple's notary service", path)
	var lastPct int64 = -1
	sub, sum, err := notary.Submit(ctx, svc, notary.NewS3Uploader(), path, name, o, func(written, total int64) {
		if total > 0 {
			if pct := written * 100 / total; pct/10 != lastPct/10 {
				lastPct = pct
				verbosef("uploaded %d%%", pct)
			}
		}
	})
	report := &notarizeReport{Package: path, Name: name, SHA256: sum}
	if sub != nil {
		report.SubmissionID = sub.ID
	}
	if err != nil {
		return report, notaryError(err)
	}
	progressf("submission id %s", sub.ID)
	if !wait {
		report.Status = notary.StatusInProgress
		if !structured() {
			fmt.Println(sub.ID)
		}
		return report, nil
	}
	st, err := waitFor(svc, sub.ID)
	if st != nil {
		report.Status = st.Status
	}
	rejected := errors.Is(err, notary.ErrRejected)
	if (printLog || rejected) && st != nil && st.Done() {
		if log, lerr := notary.FetchLog(ctx, svc, nil, sub.ID); lerr == nil {
			report.Log = log
			if !structured() {
				printLogIssues(log)
			}
		} else {
			verbosef("unable to fetch the log: %v", lerr)
		}
	}
	if err != nil {
		return report, err
	}
	if doStaple {
		if err := stapleFile(path, path); err != nil {
			return report, err
		}
		report.Stapled = true
		progressf("stapled the notarization ticket to %s", path)
	}
	return report, nil
}

func waitFor(svc notary.Service, id string) (*notary.Status, error) {
	progressf("waiting for Apple (poll every %s, up to %s)", notarizeInterval, notarizeTimeout)
	st, err := notary.Wait(context.Background(), svc, id, notary.WaitOptions{
		Interval: notarizeInterval,
		Timeout:  notarizeTimeout,
		Progress: func(s *notary.Status) { verbosef("%s: %s", s.ID, s.Status) },
	})
	if err != nil {
		return st, notaryError(err)
	}
	progressf("%s: %s", id, st.Status)
	return st, nil
}

func printLogIssues(log json.RawMessage) {
	for _, i := range notary.ParseLogIssues(log) {
		fmt.Fprintf(os.Stderr, "%s: %s", i.Severity, i.Message)
		if i.Path != "" {
			fmt.Fprintf(os.Stderr, " (%s)", i.Path)
		}
		fmt.Fprintln(os.Stderr)
	}
}

// stapleFile looks up the ticket for the package and appends it.
func stapleFile(src, dst string) error {
	x, err := openXAR(src)
	if err != nil {
		return err
	}
	record, err := staple.RecordNameFor(x)
	x.Close()
	if err != nil {
		return withCode(ExitSignature, err)
	}
	ticket, err := stapleLookup().Fetch(context.Background(), record)
	if err != nil {
		if errors.Is(err, staple.ErrNoTicket) {
			return withCode(ExitSignature, fmt.Errorf("no notarization ticket on record for %s (record %s): it has not been notarized, or the ticket is not published yet; retry shortly", src, record))
		}
		return err
	}
	if err := staple.Staple(src, dst, ticket); err != nil {
		return err
	}
	return nil
}

// stapleLookup is replaceable for tests.
var stapleLookup = staple.NewLookup

// checkNotarizable refuses a file the notary service would reject anyway.
//
// Apple takes three kinds: a flat package, a disk image and a zip archive.
// Only a package can be read here, and only a package's signature can be
// checked before the upload, so that is the only one this looks inside. A
// disk image or a zip goes up as it is: what matters is the signature on
// what it contains, which Apple checks and reports in the log, and neither
// container is something this tool can open. --force skips the check
// entirely.
func checkNotarizable(path string) error {
	if notarizeForce {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return withCode(ExitBadPackage, err)
	}
	if st.IsDir() {
		// An .app bundle is a directory, and Apple's service takes an
		// archive rather than a directory. Say so plainly: it is a
		// common mistake and the upload would fail late.
		return usageErrorf("%s is a directory; the notary service takes a flat package, a disk image or a zip archive, so archive it first", path)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".dmg", ".zip":
		// Nothing to check here: the signature that matters is on what
		// the container holds, which Apple checks on its side.
		verbosef("submitting %s as-is; its contents are checked by Apple, not here", filepath.Base(path))
		return nil
	}
	p, err := openPackage(path)
	if err != nil {
		return err
	}
	defer p.Close()
	if toc := p.XAR.TOC(); toc.Signature == nil && toc.XSignature == nil {
		return withCode(ExitSignature, fmt.Errorf("%s is not signed; Apple's notary service requires a Developer ID Installer signature (sign it first, or --force)", p.Path))
	}
	return nil
}
