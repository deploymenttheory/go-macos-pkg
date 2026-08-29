// macospkg notarize PKG — submit a package to Apple's notary service.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		if opts.Output == "json" {
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
		if opts.Output == "json" && st != nil {
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
		if opts.Output == "json" {
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

func init() {
	f := notarizeCmd.Flags()
	f.BoolVar(&notarizeWait, "wait", false, "wait for Apple's verdict")
	f.DurationVar(&notarizeTimeout, "timeout", 30*time.Minute, "how long --wait waits before exiting 9")
	f.DurationVar(&notarizeInterval, "poll-interval", 30*time.Second, "how often --wait polls")
	f.BoolVar(&notarizeStaple, "staple", false, "staple the ticket once accepted (implies --wait)")
	f.BoolVar(&notarizeLog, "log", false, "print the developer log once finished (always printed on rejection)")
	f.StringVar(&notarizeName, "name", "", "submission name shown in App Store Connect (default: the file name)")
	f.BoolVar(&notarizeForce, "force", false, "submit even if the package is not signed")
	for _, c := range []*cobra.Command{notarizeCmd, notarizeStatusCmd, notarizeLogCmd, notarizeWaitCmd, notarizeListCmd} {
		addNotaryFlags(c)
	}
	notarizeWaitCmd.Flags().DurationVar(&notarizeTimeout, "timeout", 30*time.Minute, "how long to wait before exiting 9")
	notarizeWaitCmd.Flags().DurationVar(&notarizeInterval, "poll-interval", 30*time.Second, "how often to poll")
	notarizeCmd.AddCommand(notarizeStatusCmd, notarizeLogCmd, notarizeWaitCmd, notarizeListCmd)
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
	p, err := openPackage(args[0])
	if err != nil {
		return err
	}
	if toc := p.XAR.TOC(); toc.Signature == nil && toc.XSignature == nil && !notarizeForce {
		p.Close()
		return withCode(ExitSignature, fmt.Errorf("%s is not signed; Apple's notary service requires a Developer ID Installer signature (sign it first, or --force)", p.Path))
	}
	p.Close()

	svc, err := notaryService(cmd, nil)
	if err != nil {
		return err
	}
	report, err := notarizeFile(svc, args[0], notarizeName, notarizeWait || notarizeStaple, notarizeStaple, notarizeLog)
	if opts.Output == "json" && report != nil {
		if jerr := jsonOut(report); jerr != nil {
			return jerr
		}
	}
	return err
}

// notarizeFile runs the submit / wait / staple sequence used by notarize
// and by build --notarize.
func notarizeFile(svc notary.Service, path, name string, wait, doStaple, printLog bool) (*notarizeReport, error) {
	if name == "" {
		name = filepath.Base(path)
	}
	ctx := context.Background()
	progressf("submitting %s to Apple's notary service", path)
	var lastPct int64 = -1
	sub, sum, err := notary.Submit(ctx, svc, notary.NewS3Uploader(), path, name, func(written, total int64) {
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
		if opts.Output != "json" {
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
			if opts.Output != "json" {
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
