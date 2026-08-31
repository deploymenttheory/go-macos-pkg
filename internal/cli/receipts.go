// macospkg receipts: what a volume records about the packages installed on
// it, which is pkgutil's receipt database side.
package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
	"github.com/deploymenttheory/go-macos-pkg/pkg/receipts"
	"github.com/spf13/cobra"
)

var (
	receiptsVolume    string
	receiptsRegexp    string
	receiptsOnlyFiles bool
	receiptsOnlyDirs  bool
)

var receiptsCmd = &cobra.Command{
	Use:   "receipts",
	Short: "Read what a volume records about the packages installed on it",
	Long: `Read a volume's receipt database: the record macOS keeps of what each
installed package put there. A receipt is two files the Installer wrote,
a property list of what was installed and when, and a bill of materials
listing every path.

This reads the directory rather than asking the system, so it works
against a volume mounted anywhere, from any operating system: point
--volume at a disk and see what a Mac has installed on it.

That is also its limit. On a running macOS, "pkgutil --pkgs" lists more
than this: the packages that make up the system itself are held in a
sealed database that pkgutil reaches through a private interface, and no
directory on disk lists them. What you see here is what was installed
onto the volume, which is the part worth auditing.

Nothing here writes. pkgutil can forget a receipt or relearn one; both
change what the system believes it installed, need root, and are not
something this tool should be doing behind the Installer's back.

Examples:
  macospkg receipts list
  macospkg receipts list --regexp '^com\.apple\.'
  macospkg receipts info com.example.tool
  macospkg receipts files com.example.tool --only-files
  macospkg receipts list --volume /Volumes/Macintosh\ HD`,
}

var receiptsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the package identifiers recorded on the volume",
	Args:  exactArgs(0, "no arguments"),
	RunE:  runReceiptsList,
}

var receiptsInfoCmd = &cobra.Command{
	Use:   "info PKGID",
	Short: "Show what a package's receipt records",
	Args:  exactArgs(1, "PKGID"),
	RunE:  runReceiptsInfo,
}

var receiptsFilesCmd = &cobra.Command{
	Use:   "files PKGID",
	Short: "List the paths a package installed",
	Args:  exactArgs(1, "PKGID"),
	RunE:  runReceiptsFiles,
}

func init() {
	receiptsCmd.PersistentFlags().StringVar(&receiptsVolume, "volume", "", "volume to read, a mount point; the running system's root by default")
	receiptsListCmd.Flags().StringVar(&receiptsRegexp, "regexp", "", "only identifiers matching this regular expression")
	receiptsFilesCmd.Flags().BoolVar(&receiptsOnlyFiles, "only-files", false, "leave out directories")
	receiptsFilesCmd.Flags().BoolVar(&receiptsOnlyDirs, "only-dirs", false, "leave out files")
	receiptsCmd.AddCommand(receiptsListCmd, receiptsInfoCmd, receiptsFilesCmd)
}

// receiptJSON is the JSON schema for macospkg receipts info.
type receiptJSON struct {
	PackageID       string `json:"packageId"`
	Version         string `json:"version"`
	Volume          string `json:"volume"`
	InstallLocation string `json:"installLocation"`
	InstallTime     int64  `json:"installTime,omitempty"`
	InstalledBy     string `json:"installedBy,omitempty"`
	PackageFileName string `json:"packageFileName,omitempty"`
	HasFiles        bool   `json:"hasFiles"`
}

// openReceipts opens the volume's receipt database.
func openReceipts() (*receipts.DB, error) {
	db, err := receipts.Open(receiptsVolume)
	if err != nil {
		return nil, withCode(ExitBadPackage, err)
	}
	return db, nil
}

func runReceiptsList(cmd *cobra.Command, args []string) error {
	db, err := openReceipts()
	if err != nil {
		return err
	}
	ids, err := db.Match(receiptsRegexp)
	if err != nil {
		return usageErrorf("%v", err)
	}
	for _, id := range ids {
		if structured() {
			if err := jsonOut(struct {
				PackageID string `json:"packageId"`
			}{id}); err != nil {
				return err
			}
			continue
		}
		fmt.Println(id)
	}
	return nil
}

func runReceiptsInfo(cmd *cobra.Command, args []string) error {
	db, err := openReceipts()
	if err != nil {
		return err
	}
	r, err := db.Get(args[0])
	if err != nil {
		return withCode(ExitBadPackage, err)
	}
	out := receiptJSON{
		PackageID:       r.PackageIdentifier,
		Version:         r.PackageVersion,
		Volume:          r.Volume,
		InstallLocation: r.InstallPrefixPath,
		InstalledBy:     r.InstallProcessName,
		PackageFileName: r.PackageFileName,
		HasFiles:        db.HasFiles(args[0]),
	}
	if !r.InstallDate.IsZero() {
		out.InstallTime = r.InstallDate.Unix()
	}
	if structured() {
		return jsonOut(out)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	defer func() { _ = w.Flush() }()
	fmt.Fprintf(w, "package-id:\t%s\n", out.PackageID)
	fmt.Fprintf(w, "version:\t%s\n", out.Version)
	fmt.Fprintf(w, "volume:\t%s\n", out.Volume)
	fmt.Fprintf(w, "location:\t%s\n", out.InstallLocation)
	if out.InstallTime != 0 {
		fmt.Fprintf(w, "install-time:\t%d\n", out.InstallTime)
	}
	if out.PackageFileName != "" {
		fmt.Fprintf(w, "package-file:\t%s\n", out.PackageFileName)
	}
	if out.InstalledBy != "" {
		fmt.Fprintf(w, "installed-by:\t%s\n", out.InstalledBy)
	}
	return nil
}

func runReceiptsFiles(cmd *cobra.Command, args []string) error {
	if receiptsOnlyFiles && receiptsOnlyDirs {
		return usageErrorf("--only-files and --only-dirs ask for different halves of the listing; pass neither to see both")
	}
	db, err := openReceipts()
	if err != nil {
		return err
	}
	entries, err := db.Files(args[0])
	if err != nil {
		return withCode(ExitBadPackage, err)
	}
	for _, e := range entries {
		isDir := e.Type == bom.TypeDirectory
		if (receiptsOnlyFiles && isDir) || (receiptsOnlyDirs && !isDir) {
			continue
		}
		// pkgutil --files prints the path relative to the install
		// location, without the "./" the bill of materials carries and
		// without the root entry itself.
		path := strings.TrimPrefix(e.Path, "./")
		if path == "." || path == "" {
			continue
		}
		if structured() {
			if err := jsonOut(struct {
				Path string `json:"path"`
				Type string `json:"type"`
			}{path, e.Type.String()}); err != nil {
				return err
			}
			continue
		}
		fmt.Println(path)
	}
	return nil
}
