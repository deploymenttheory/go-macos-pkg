// macospkg staple PKG: attach a notarization ticket; unstaple removes it.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/deploymenttheory/go-macos-pkg/pkg/staple"
	"github.com/spf13/cobra"
)

var (
	stapleCheck  bool
	stapleTicket string
)

var stapleCmd = &cobra.Command{
	Use:   "staple PKG|APP [OUT.pkg]",
	Short: "Attach a notarization ticket; unstaple removes one",
	Long: `Retrieve the notarization ticket Apple issued and attach it, so Gatekeeper
can verify notarization without going online.

Works on flat packages and application bundles (.app). The ticket is looked
up in Apple's public ticket database, so no credentials are needed: for a
package by its signature digest, for an application by its main executable's
CDHash. A package carries the ticket as a trailer; an application carries it
in Contents/CodeResources, and is always stapled in place.

--check reports whether a ticket is already stapled (exit 7 if not).
--ticket FILE staples a ticket from a file instead of looking it up, for
air-gapped builds where the ticket was fetched elsewhere.
OUT.pkg writes a stapled flat package to a new path; without it the package
is updated in place. It does not apply to an application bundle.

Examples:
  macospkg staple Foo.pkg
  macospkg staple --check Foo.pkg
  macospkg staple Foo.pkg Foo-stapled.pkg
  macospkg staple Foo.app`,
	Args: rangeArgs(1, 2, "PKG [OUT.pkg]"),
	RunE: runStaple,
}

var unstapleCmd = &cobra.Command{
	Use:   "unstaple PKG|APP [OUT.pkg]",
	Short: "Remove a stapled notarization ticket",
	Long: `Remove a notarization ticket from a package or application bundle.

For a package the ticket is a trailer appended after the archive, so removing
it returns the package to the bytes it had when it was signed. Re-signing does
this too, since a ticket cannot survive a change to what it covers. For an
application the ticket is Contents/CodeResources, which is removed in place.

OUT.pkg writes the result to a new path; without it the package is updated in
place (it does not apply to an application bundle). A target with no ticket is
left unchanged.

Examples:
  macospkg unstaple Foo.pkg
  macospkg unstaple Foo.pkg Foo-clean.pkg
  macospkg unstaple Foo.app`,
	Args: rangeArgs(1, 2, "PKG [OUT.pkg]"),
	RunE: func(cmd *cobra.Command, args []string) error {
		if staple.IsAppBundle(args[0]) {
			if err := staple.UnstapleApp(args[0]); err != nil {
				return err
			}
			progressf("removed any stapled ticket: %s", args[0])
			return nil
		}
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
	if staple.IsAppBundle(args[0]) {
		return runStapleApp(args)
	}
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
		case structured():
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
	if structured() {
		return jsonOut(report)
	}
	progressf("stapled a %d-byte notarization ticket to %s", len(ticket), dst)
	return nil
}

// runStapleApp staples an application bundle. Unlike a flat package, the
// ticket is a file (Contents/CodeResources), the lookup is keyed on the main
// executable's CDHash, and stapling is always in place.
func runStapleApp(args []string) error {
	bundle := args[0]
	if len(args) > 1 {
		return usageErrorf("an application bundle is stapled in place; OUT is only for flat packages")
	}
	has, err := staple.AppHasTicket(bundle)
	if err != nil {
		return err
	}
	if stapleCheck {
		report := stapleReport{Path: bundle, Stapled: has}
		if names, nerr := staple.AppRecordNames(bundle); nerr == nil && len(names) > 0 {
			report.RecordName = names[0]
		}
		switch {
		case structured():
			if err := jsonOut(report); err != nil {
				return err
			}
		case has:
			fmt.Printf("%s: notarization ticket stapled\n", bundle)
		default:
			fmt.Printf("%s: no notarization ticket\n", bundle)
		}
		if !has {
			return withCode(ExitSignature, fmt.Errorf("%s has no stapled ticket", bundle))
		}
		return nil
	}

	var ticket []byte
	var record string
	if stapleTicket != "" {
		ticket, err = os.ReadFile(stapleTicket)
		if err != nil {
			return usageErrorf("unable to read --ticket: %v", err)
		}
	} else {
		names, nerr := staple.AppRecordNames(bundle)
		if nerr != nil {
			return withCode(ExitSignature, nerr)
		}
		ticket, record, err = fetchAppTicket(names)
		if err != nil {
			if errors.Is(err, staple.ErrNoTicket) {
				return withCode(ExitSignature, fmt.Errorf("no notarization ticket on record for %s (tried %s): it has not been notarized, or the ticket is not published yet; retry shortly", bundle, strings.Join(names, ", ")))
			}
			return err
		}
	}
	if err := staple.StapleApp(bundle, ticket); err != nil {
		return err
	}
	report := stapleReport{Path: bundle, Stapled: true, TicketBytes: len(ticket), RecordName: record, Replaced: has}
	if structured() {
		return jsonOut(report)
	}
	progressf("stapled a %d-byte notarization ticket to %s", len(ticket), bundle)
	return nil
}

// fetchAppTicket tries each record name until one resolves. A universal
// binary offers one per architecture, and Apple's ticket answers to any.
func fetchAppTicket(names []string) (ticket []byte, record string, err error) {
	l := stapleLookup()
	noneFound := false
	for _, name := range names {
		t, e := l.Fetch(context.Background(), name)
		if e == nil {
			return t, name, nil
		}
		if errors.Is(e, staple.ErrNoTicket) {
			noneFound = true
			continue
		}
		return nil, name, e
	}
	if noneFound {
		return nil, "", staple.ErrNoTicket
	}
	return nil, "", fmt.Errorf("staple: no record names to look up")
}
