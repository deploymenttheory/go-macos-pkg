// macospkg extract PKG DIR — extract payload files to the local file system.
package cli

import (
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
	"github.com/spf13/cobra"
)

var (
	extractComponent string
	extractScripts   bool
	extractPattern   string
	extractSymlinks  string
	extractVerify    bool
	extractXattrs    string
	extractHardLinks bool
)

var extractCmd = &cobra.Command{
	Use:   "extract PKG DIR",
	Short: "Extract payload files to the local file system",
	Long: `Extract the files a package installs into DIR, as they would land under
the install location. Permission bits and modification times are applied;
ownership is not, since that is the Installer's job and needs root.

A product archive's components are extracted side by side under
DIR/<component>/ unless --component names one, in which case its payload
lands in DIR directly.

--scripts extracts the Scripts archives instead of the payloads.
--pattern limits extraction to payload paths matching a regular expression
(paths look like ./usr/local/bin/tool).
--verify checks every file against the CRC-32 in the bill of materials.

Exit code 6 means some entries were skipped: device nodes, sockets, paths
that would escape DIR, or links the host would not create.

Examples:
  macospkg extract Foo.pkg ./out
  macospkg extract --pattern '^\./Applications/' Foo.pkg ./out
  macospkg extract --component foo.pkg Product.pkg ./out
  macospkg extract --scripts Foo.pkg ./scripts`,
	Args: exactArgs(2, "PKG DIR"),
	RunE: runExtract,
}

func init() {
	extractCmd.Flags().StringVar(&extractComponent, "component", "", "only this component of a product archive (e.g. foo.pkg)")
	extractCmd.Flags().BoolVar(&extractScripts, "scripts", false, "extract the Scripts archives rather than the payloads")
	extractCmd.Flags().StringVar(&extractPattern, "pattern", "", "only payload paths matching this regular expression")
	extractCmd.Flags().StringVar(&extractSymlinks, "symlinks", "auto", "symbolic links: auto, real or file")
	extractCmd.Flags().StringVar(&extractXattrs, "xattrs", "auto", "\"._\" sidecars: apply (set the attributes on the owner), file (write them as files) or skip; auto applies what the host takes and keeps the rest as files")
	extractCmd.Flags().BoolVar(&extractHardLinks, "hard-links", true, "recreate hard links; --hard-links=false writes copies")
	extractCmd.Flags().BoolVar(&extractVerify, "verify", false, "verify each file's CRC-32 against the bill of materials")
}

// extractReport is the JSON schema for macospkg extract.
type extractReport struct {
	Package    string            `json:"package"`
	Dir        string            `json:"dir"`
	Components []componentReport `json:"components"`
	Partial    bool              `json:"partial"`
}

type componentReport struct {
	Component  string   `json:"component"`
	Dir        string   `json:"dir"`
	Encoding   string   `json:"encoding,omitempty"`
	Files      int      `json:"files"`
	Dirs       int      `json:"dirs"`
	Symlinks   int      `json:"symlinks"`
	HardLinks  int      `json:"hardLinks"`
	Xattrs     int      `json:"xattrs"`
	XattrFiles int      `json:"xattrFiles"`
	Renamed    []string `json:"renamed"`
	Skipped    []string `json:"skipped"`
	Mismatched []string `json:"mismatched"`
}

func runExtract(cmd *cobra.Command, args []string) error {
	mode, err := flatpkg.ParseSymlinkMode(extractSymlinks)
	if err != nil {
		return usageErrorf("%v", err)
	}
	xattrMode, err := flatpkg.ParseXattrMode(extractXattrs)
	if err != nil {
		return usageErrorf("%v", err)
	}
	var pattern *regexp.Regexp
	if extractPattern != "" {
		pattern, err = regexp.Compile(extractPattern)
		if err != nil {
			return usageErrorf("invalid --pattern: %v", err)
		}
	}
	p, err := openPackage(args[0])
	if err != nil {
		return err
	}
	defer p.Close()
	components, err := selectComponents(p, extractComponent)
	if err != nil {
		return err
	}

	report := extractReport{Package: p.Path, Dir: args[1], Components: []componentReport{}}
	total := 0
	for _, c := range components {
		dir := args[1]
		if len(components) > 1 && c.Name != "" {
			dir = filepath.Join(args[1], filepath.FromSlash(c.Name))
		}
		o := flatpkg.ExtractOptions{
			Pattern:     pattern,
			Symlinks:    mode,
			Xattrs:      xattrMode,
			NoHardLinks: !extractHardLinks,
			Progress:    func(path string) { verbosef("wrote %s", path) },
		}
		var res *flatpkg.ExtractResult
		var enc flatpkg.PayloadEncoding
		switch {
		case extractScripts:
			if !c.HasScripts() {
				verbosef("%s has no scripts", componentLabel(c))
				continue
			}
			res, err = c.ExtractScripts(dir, o)
		default:
			if !c.HasPayload() {
				verbosef("%s has no payload", componentLabel(c))
				continue
			}
			if extractVerify {
				b, err := c.Bom()
				if err != nil {
					return fmt.Errorf("%s: %w", componentLabel(c), err)
				}
				o.Checksums, err = flatpkg.ChecksumMap(b)
				if err != nil {
					return fmt.Errorf("%s: %w", componentLabel(c), err)
				}
			}
			res, enc, err = c.ExtractPayload(dir, o)
		}
		if err != nil {
			return payloadOpenError(fmt.Errorf("%s: %w", componentLabel(c), err))
		}
		cr := componentReport{
			Component: c.Name, Dir: dir, Encoding: string(enc),
			Files: res.Files, Dirs: res.Dirs, Symlinks: res.Symlinks, HardLinks: res.HardLinks,
			Xattrs: res.Xattrs, XattrFiles: res.XattrFiles,
			Renamed: []string{}, Skipped: []string{}, Mismatched: res.Mismatched,
		}
		if cr.Mismatched == nil {
			cr.Mismatched = []string{}
		}
		for _, r := range res.Renamed {
			cr.Renamed = append(cr.Renamed, r.Path+": "+r.Reason)
		}
		for _, s := range res.Skipped {
			cr.Skipped = append(cr.Skipped, s.Path+": "+s.Reason)
		}
		if res.Partial() {
			report.Partial = true
		}
		report.Components = append(report.Components, cr)
		total += res.Files + res.Symlinks
	}

	if opts.Output == "json" {
		if err := jsonOut(report); err != nil {
			return err
		}
	} else {
		for _, cr := range report.Components {
			label := "payload"
			if cr.Component != "" {
				label = cr.Component
			}
			progressf("extracted %s into %s: %d files, %d directories, %d symlinks", label, cr.Dir, cr.Files, cr.Dirs, cr.Symlinks)
			for _, s := range cr.Skipped {
				fmt.Fprintln(cmd.ErrOrStderr(), "skipped:", s)
			}
			for _, m := range cr.Mismatched {
				fmt.Fprintln(cmd.ErrOrStderr(), "checksum mismatch:", m)
			}
		}
	}
	if report.Partial {
		return withCode(ExitPartial, fmt.Errorf("extraction incomplete: some entries were skipped or failed verification"))
	}
	if total == 0 && pattern != nil {
		return withCode(ExitPartial, fmt.Errorf("no payload entries matched --pattern %q", extractPattern))
	}
	return nil
}
