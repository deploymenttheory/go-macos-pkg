// macospkg flatten DIR OUT.pkg: reassemble an expanded package.
package cli

import (
	"crypto"
	"os"

	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
	"github.com/spf13/cobra"
)

var flattenCmd = &cobra.Command{
	Use:   "flatten DIR OUT.pkg",
	Short: "Reassemble an expanded package directory into a flat package",
	Long: `Turn a directory produced by expand back into a flat package, which is
what pkgutil --flatten does. It is the inverse of expand, and the way to
change one thing in a package without rebuilding it: expand it, edit the
PackageInfo or a script, flatten it again.

The contents are taken as they stand. Every file becomes an archive entry
with the bytes it has on disk, so a Payload expand left packed goes back
exactly as it came out, and an edited PackageInfo goes back as edited. A
directory named Scripts is packed into the archive a package expects,
which is the one name expand unpacks.

Nothing is recomputed. The bill of materials, the payload counts and the
install size are whatever the directory already says, so editing a payload
here will leave the package describing the old one. Use build for that.

A stapled ticket does not survive, and neither does a signature: both
cover bytes that have just changed. Sign again with --sign-*, or with
macospkg sign afterwards.

Examples:
  macospkg expand Foo.pkg ./expanded
  macospkg flatten ./expanded Foo-edited.pkg
  macospkg flatten ./expanded Foo.pkg --sign-p12 devid.p12`,
	Args: exactArgs(2, "DIR OUT.pkg"),
	RunE: runFlatten,
}

func init() {
	addSigningFlags(flattenCmd, "sign-")
}

// flattenReport is the JSON schema for macospkg flatten.
type flattenReport struct {
	Output   string   `json:"output"`
	Entries  []string `json:"entries"`
	Archived []string `json:"archived"`
	Size     int64    `json:"size"`
	SHA256   string   `json:"sha256"`
	Signed   bool     `json:"signed"`
}

func runFlatten(cmd *cobra.Command, args []string) error {
	dir, output := args[0], args[1]
	signer, err := signerFromFlags(cmd, nil, crypto.SHA256)
	if err != nil {
		return err
	}
	o := flatpkg.FlattenOptions{
		Dir:      dir,
		Epoch:    opts.SourceDateEpoch,
		TempDir:  opts.TempDir,
		Signer:   signer,
		Progress: func(e string) { verbosef("added %s", e) },
	}

	var res *flatpkg.FlattenResult
	if _, err := writePackage(output, func(w *os.File) (*flatpkg.BuildResult, error) {
		var ferr error
		res, ferr = flatpkg.Flatten(o, w)
		return nil, ferr
	}); err != nil {
		return buildError(err)
	}
	report := flattenReport{
		Output: output, Entries: res.Entries, Archived: res.Archived,
		Signed: signer != nil,
	}
	if st, serr := os.Stat(output); serr == nil {
		report.Size = st.Size()
	}
	report.SHA256, _ = sha256File(output)
	if report.Archived == nil {
		report.Archived = []string{}
	}
	if opts.Output == "json" {
		return jsonOut(report)
	}
	progressf("flattened %s -> %s (%d entries)", dir, output, len(res.Entries))
	return nil
}
