// macospkg staple PKG — attach a notarization ticket; unstaple removes it.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/deploymenttheory/go-macos-pkg/pkg/staple"
	"github.com/spf13/cobra"
)

var (
	stapleCheck  bool
	stapleTicket string
)

var stapleCmd = &cobra.Command{
	Use:   "staple PKG [OUT.pkg]",
	Short: "Attach a notarization ticket; unstaple removes one",
	Long: `Fetch the notarization ticket Apple issued for the package and append it,
the way stapler staple does, so the Installer can verify notarization
offline. The ticket is looked up in Apple's public ticket database by the
package's signature digest; no credentials are needed.

--check reports whether a ticket is already stapled (exit 7 if not).
--ticket FILE staples a ticket from a file instead of looking it up, for
air-gapped builds where the ticket was fetched elsewhere.
OUT.pkg writes the stapled package to a new path; without it the package
is updated in place.

Examples:
  macospkg staple Foo.pkg
  macospkg staple --check Foo.pkg
  macospkg staple Foo.pkg Foo-stapled.pkg`,
	Args: rangeArgs(1, 2, "PKG [OUT.pkg]"),
	RunE: runStaple,
}

var unstapleCmd = &cobra.Command{
	Use:   "unstaple PKG [OUT.pkg]",
	Short: "Remove a stapled notarization ticket",
	Args:  rangeArgs(1, 2, "PKG [OUT.pkg]"),
	RunE: func(cmd *cobra.Command, args []string) error {
		x, err := openXAR(args[0])
		if err != nil {
			return err
		}
		x.Close()
		dst := args[0]
		if len(args) > 1 {
			dst = args[1]
		}
		if err := staple.Unstaple(args[0], dst); err != nil {
			return err
		}
		progressf("removed any stapled ticket: %s", dst)
		return nil
	},
}

func init() {
	stapleCmd.Flags().BoolVar(&stapleCheck, "check", false, "report whether a ticket is stapled, without changing anything")
	stapleCmd.Flags().StringVar(&stapleTicket, "ticket", "", "staple this ticket file instead of looking the ticket up")
}

// stapleReport is the JSON schema for macospkg staple.
type stapleReport struct {
	Path        string `json:"path"`
	Stapled     bool   `json:"stapled"`
	TicketBytes int    `json:"ticketBytes,omitempty"`
	RecordName  string `json:"recordName,omitempty"`
	Replaced    bool   `json:"replaced,omitempty"`
}

func runStaple(cmd *cobra.Command, args []string) error {
	x, err := openXAR(args[0])
	if err != nil {
		return err
	}
	record, recErr := staple.RecordNameFor(x)
	x.Close()

	existing, _ := staple.Has(args[0])
	if stapleCheck {
		report := stapleReport{Path: args[0], Stapled: existing != nil, RecordName: record}
		if existing != nil {
			report.TicketBytes = len(existing.Data)
		}
		switch {
		case opts.Output == "json":
			if err := jsonOut(report); err != nil {
				return err
			}
		case existing != nil:
			fmt.Printf("%s: notarization ticket stapled (%d bytes)\n", args[0], len(existing.Data))
		default:
			fmt.Printf("%s: no notarization ticket\n", args[0])
		}
		if existing == nil {
			return withCode(ExitSignature, fmt.Errorf("%s has no stapled ticket", args[0]))
		}
		return nil
	}

	dst := args[0]
	if len(args) > 1 {
		dst = args[1]
	}
	var ticket []byte
	switch {
	case stapleTicket != "":
		ticket, err = os.ReadFile(stapleTicket)
		if err != nil {
			return usageErrorf("unable to read --ticket: %v", err)
		}
	default:
		if recErr != nil {
			return withCode(ExitSignature, recErr)
		}
		ticket, err = stapleLookup().Fetch(context.Background(), record)
		if err != nil {
			if errors.Is(err, staple.ErrNoTicket) {
				return withCode(ExitSignature, fmt.Errorf("no notarization ticket on record for %s (record %s): it has not been notarized, or the ticket is not published yet; retry shortly", args[0], record))
			}
			return err
		}
	}
	if err := staple.Staple(args[0], dst, ticket); err != nil {
		return err
	}
	report := stapleReport{Path: dst, Stapled: true, TicketBytes: len(ticket), RecordName: record, Replaced: existing != nil}
	if opts.Output == "json" {
		return jsonOut(report)
	}
	progressf("stapled a %d-byte notarization ticket to %s", len(ticket), dst)
	return nil
}
