// macospkg expand PKG DIR: unpack the archive like pkgutil --expand.
package cli

import (
	"fmt"

	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
	"github.com/spf13/cobra"
)

var (
	expandFull      bool
	expandVerify    bool
	expandSymlinks  string
	expandXattrs    string
	expandHardLinks bool
)

var expandCmd = &cobra.Command{
	Use:   "expand PKG DIR",
	Short: "Unpack the archive like pkgutil --expand / --expand-full",
	Long: `Write the package's archive entries, decoded, into a new directory DIR:
PackageInfo, Bom, Distribution and Resources as files, each Scripts archive
unpacked into a Scripts directory, and each Payload left as the gzip cpio
stream it is, exactly the layout pkgutil --expand produces.

--full also unpacks every Payload into a directory of the same name, as
pkgutil --expand-full does.

DIR must not exist: an expansion never merges into something else.

Examples:
  macospkg expand Foo.pkg foo-expanded
  macospkg expand --full Product.pkg product-expanded
  macospkg expand --verify Foo.pkg out    # check every entry's checksums`,
	Args: exactArgs(2, "PKG DIR"),
	RunE: runExpand,
}

func init() {
	expandCmd.Flags().BoolVar(&expandFull, "full", false, "also unpack each Payload into a directory (pkgutil --expand-full)")
	expandCmd.Flags().BoolVar(&expandVerify, "verify", false, "verify every archive entry's stored checksums")
	expandCmd.Flags().StringVar(&expandSymlinks, "symlinks", "auto", "symbolic links: auto, real or file")
	expandCmd.Flags().StringVar(&expandXattrs, "xattrs", "auto", "\"._\" sidecars: apply (set the attributes on the owner), file (write them as files) or skip; auto applies what the host takes and keeps the rest as files")
	expandCmd.Flags().BoolVar(&expandHardLinks, "hard-links", true, "recreate hard links; --hard-links=false writes copies")
}

// expandReport is the JSON schema for macospkg expand.
type expandReport struct {
	Package    string   `json:"package"`
	Dir        string   `json:"dir"`
	Full       bool     `json:"full"`
	Entries    int      `json:"entries"`
	Files      int      `json:"files"`
	Dirs       int      `json:"dirs"`
	Symlinks   int      `json:"symlinks"`
	HardLinks  int      `json:"hardLinks"`
	Xattrs     int      `json:"xattrs"`
	XattrFiles int      `json:"xattrFiles"`
	Skipped    []string `json:"skipped"`
	Partial    bool     `json:"partial"`
}

func runExpand(cmd *cobra.Command, args []string) error {
	mode, err := flatpkg.ParseSymlinkMode(expandSymlinks)
	if err != nil {
		return usageErrorf("%v", err)
	}
	xattrMode, err := flatpkg.ParseXattrMode(expandXattrs)
	if err != nil {
		return usageErrorf("%v", err)
	}
	p, err := openPackage(args[0])
	if err != nil {
		return err
	}
	defer p.Close()

	res, err := p.Expand(args[1], flatpkg.ExpandOptions{
		Full:        expandFull,
		Verify:      expandVerify,
		Symlinks:    mode,
		Xattrs:      xattrMode,
		NoHardLinks: !expandHardLinks,
		Progress:    func(path string) { verbosef("wrote %s", path) },
	})
	if err != nil {
		return payloadOpenError(err)
	}
	report := expandReport{Package: p.Path, Dir: args[1], Full: expandFull, Entries: res.Entries, Skipped: []string{}}
	for _, pr := range res.Payloads {
		report.Files += pr.Files
		report.Dirs += pr.Dirs
		report.Symlinks += pr.Symlinks
		report.HardLinks += pr.HardLinks
		report.Xattrs += pr.Xattrs
		report.XattrFiles += pr.XattrFiles
		for _, s := range pr.Skipped {
			report.Skipped = append(report.Skipped, s.Path+": "+s.Reason)
		}
		for _, m := range pr.Mismatched {
			report.Skipped = append(report.Skipped, m+": checksum mismatch")
		}
	}
	for _, s := range res.Skipped {
		report.Skipped = append(report.Skipped, s.Path+": "+s.Reason)
	}
	report.Partial = res.Partial()

	if opts.Output == "json" {
		if err := jsonOut(report); err != nil {
			return err
		}
	} else {
		progressf("expanded %s into %s: %d entries, %d files, %d directories, %d symlinks", p.Path, args[1], report.Entries, report.Files, report.Dirs, report.Symlinks)
		for _, s := range report.Skipped {
			fmt.Fprintln(cmd.ErrOrStderr(), "skipped:", s)
		}
	}
	if report.Partial {
		return withCode(ExitPartial, fmt.Errorf("%d entries were skipped", len(report.Skipped)))
	}
	return nil
}
