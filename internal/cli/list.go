// macospkg list PKG: payload files from the bill of materials, or archive
// entries.
package cli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
	"github.com/spf13/cobra"
)

var (
	listArchive   bool
	listComponent string
	listLong      bool
	listScripts   bool
	listOnlyFiles bool
	listOnlyDirs  bool
	listPattern   string
)

var listCmd = &cobra.Command{
	Use:   "list PKG",
	Short: "List payload files (from the bill of materials) or archive entries",
	Long: `List the files a package installs, as recorded in each component's bill
of materials, without expanding anything. One line per file; -l adds mode,
owner, size and time.

--archive lists the xar entries instead (PackageInfo, Bom, Payload, nested
component directories, Distribution resources) with their stored sizes.
--scripts lists the entries of the Scripts archives.

With -o json, every line is one JSON object.

Examples:
  macospkg list Foo.pkg
  macospkg list -l Foo.pkg
  macospkg list --component foo.pkg Product.pkg
  macospkg list --archive Product.pkg
  macospkg list -o json Foo.pkg | jq -r 'select(.type=="file") | .path'`,
	Args: exactArgs(1, "PKG"),
	RunE: runList,
}

func init() {
	listCmd.Flags().BoolVar(&listArchive, "archive", false, "list xar archive entries rather than payload files")
	listCmd.Flags().StringVar(&listComponent, "component", "", "only this component of a product archive (e.g. foo.pkg)")
	listCmd.Flags().BoolVarP(&listLong, "long", "l", false, "long format: mode, uid/gid, size, modification time, path")
	listCmd.Flags().BoolVar(&listScripts, "scripts", false, "list the Scripts archive entries rather than payload files")
	listCmd.Flags().BoolVar(&listOnlyFiles, "only-files", false, "leave out directories, listing only the files a package installs")
	listCmd.Flags().BoolVar(&listOnlyDirs, "only-dirs", false, "leave out files, listing only the directories a package installs")
	listCmd.Flags().StringVar(&listPattern, "regexp", "", "only paths matching this regular expression")
}

// payloadEntry is the JSON schema for a payload line.
type payloadEntry struct {
	Path      string `json:"path"`
	Type      string `json:"type"`
	Mode      string `json:"mode"`
	UID       int    `json:"uid"`
	GID       int    `json:"gid"`
	Size      int64  `json:"size"`
	ModTime   string `json:"modTime,omitempty"`
	Checksum  string `json:"checksum,omitempty"`
	Target    string `json:"target,omitempty"`
	Component string `json:"component,omitempty"`
}

// archiveEntry is the JSON schema for an --archive line.
type archiveEntry struct {
	Entry    string `json:"entry"`
	Type     string `json:"type"`
	Encoding string `json:"encoding,omitempty"`
	Size     int64  `json:"size"`
	Length   int64  `json:"length"`
	Offset   int64  `json:"offset"`
	Mode     string `json:"mode,omitempty"`
}

func runList(cmd *cobra.Command, args []string) error {
	p, err := openPackage(args[0])
	if err != nil {
		return err
	}
	defer p.Close()

	if listArchive {
		return listArchiveEntries(p)
	}
	if listOnlyFiles && listOnlyDirs {
		return usageErrorf("--only-files and --only-dirs ask for different halves of the listing; pass neither to see both")
	}
	if listPattern != "" {
		re, err := regexp.Compile(listPattern)
		if err != nil {
			return usageErrorf("invalid --regexp: %v", err)
		}
		listMatch = re
	}
	components, err := selectComponents(p, listComponent)
	if err != nil {
		return err
	}
	if listScripts {
		return listScriptEntries(p, components)
	}
	return listPayload(p, components)
}

// selectComponents applies --component, which only means something for a
// product archive.
func selectComponents(p *flatpkg.Package, name string) ([]*flatpkg.Component, error) {
	if name == "" {
		return p.Components, nil
	}
	c := p.Component(strings.Trim(name, "/"))
	if c == nil {
		var have []string
		for _, c := range p.Components {
			have = append(have, c.Name)
		}
		return nil, usageErrorf("no component %q in %s (have: %s)", name, p.Path, strings.Join(have, ", "))
	}
	return []*flatpkg.Component{c}, nil
}

func listPayload(p *flatpkg.Package, components []*flatpkg.Component) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	defer func() { _ = w.Flush() }()
	multi := len(p.Components) > 1
	for _, c := range components {
		if c.Entry(flatpkg.EntryBom) == nil {
			verbosef("component %q has no bill of materials", c.Name)
			continue
		}
		b, err := c.Bom()
		if err != nil {
			return fmt.Errorf("%s: %w", componentLabel(c), err)
		}
		entries, err := b.Paths()
		if err != nil {
			return fmt.Errorf("%s: %w", componentLabel(c), err)
		}
		for _, e := range entries {
			if !listWanted(e.Path, e.Type == bom.TypeDirectory) {
				continue
			}
			if structured() {
				if err := jsonOut(payloadEntryOf(e, c, multi)); err != nil {
					return err
				}
				continue
			}
			printPayloadEntry(w, e, c, multi)
		}
	}
	return nil
}

func payloadEntryOf(e bom.Entry, c *flatpkg.Component, multi bool) payloadEntry {
	pe := payloadEntry{
		Path: e.Path,
		Type: e.Type.String(),
		Mode: fmt.Sprintf("%o", e.Mode&0o7777),
		UID:  int(e.UID),
		GID:  int(e.GID),
		Size: e.Size,
	}
	if !e.ModTime.IsZero() && e.ModTime.Unix() != 0 {
		pe.ModTime = e.ModTime.UTC().Format("2006-01-02T15:04:05Z")
	}
	if e.Type == bom.TypeFile || e.Type == bom.TypeLink {
		pe.Checksum = fmt.Sprintf("%08x", e.Checksum)
	}
	if e.Type == bom.TypeLink {
		pe.Target = e.LinkTarget
	}
	if multi {
		pe.Component = c.Name
	}
	return pe
}

func printPayloadEntry(w io.Writer, e bom.Entry, c *flatpkg.Component, multi bool) {
	prefix := ""
	if multi {
		prefix = c.Name + ":"
	}
	if !listLong {
		if e.Type == bom.TypeLink {
			fmt.Fprintf(w, "%s%s -> %s\n", prefix, e.Path, e.LinkTarget)
		} else {
			fmt.Fprintf(w, "%s%s\n", prefix, e.Path)
		}
		return
	}
	mtime := "-"
	if !e.ModTime.IsZero() && e.ModTime.Unix() != 0 {
		mtime = e.ModTime.UTC().Format("2006-01-02 15:04:05")
	}
	name := e.Path
	if e.Type == bom.TypeLink {
		name += " -> " + e.LinkTarget
	}
	fmt.Fprintf(w, "%s\t%d/%d\t%d\t%s\t%s%s\n", e.FileMode().String(), e.UID, e.GID, e.Size, mtime, prefix, name)
}

func listArchiveEntries(p *flatpkg.Package) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	defer func() { _ = w.Flush() }()
	for _, f := range p.XAR.Files() {
		ae := archiveEntry{Entry: f.Path(), Type: f.Type.Value, Mode: f.Mode}
		if f.Data != nil {
			ae.Encoding = f.Data.Encoding.Style
			ae.Size = f.Data.Size
			ae.Length = f.Data.Length
			ae.Offset = f.Data.Offset
		}
		if structured() {
			if err := jsonOut(ae); err != nil {
				return err
			}
			continue
		}
		if !listLong {
			name := f.Path()
			if f.IsDir() {
				name += "/"
			}
			fmt.Fprintln(w, name)
			continue
		}
		enc := "-"
		if f.Data != nil {
			enc = shortEncoding(f.Data.Encoding.Style)
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\n", f.Type.Value, enc, ae.Size, ae.Length, f.Path())
	}
	return nil
}

func shortEncoding(style string) string {
	style = strings.TrimPrefix(style, "application/")
	style = strings.TrimPrefix(style, "x-")
	if style == "octet-stream" {
		return "stored"
	}
	return style
}

func listScriptEntries(p *flatpkg.Package, components []*flatpkg.Component) error {
	multi := len(p.Components) > 1
	for _, c := range components {
		if !c.HasScripts() {
			continue
		}
		cr, _, closer, err := c.OpenScriptsCPIO()
		if err != nil {
			return fmt.Errorf("%s: %w", componentLabel(c), err)
		}
		err = func() error {
			defer closer.Close()
			for {
				h, err := cr.Next()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return fmt.Errorf("%s: Scripts: %w", componentLabel(c), err)
				}
				pe := payloadEntry{
					Path: h.Name, Type: cpioType(h), Mode: fmt.Sprintf("%o", h.Mode&0o7777),
					UID: int(h.UID), GID: int(h.GID), Size: h.Size,
					ModTime: h.ModTime.UTC().Format("2006-01-02T15:04:05Z"),
				}
				if multi {
					pe.Component = c.Name
				}
				if structured() {
					if err := jsonOut(pe); err != nil {
						return err
					}
					continue
				}
				prefix := ""
				if multi {
					prefix = c.Name + ":"
				}
				if listLong {
					fmt.Fprintf(os.Stdout, "%s %d/%d %d %s%s\n", h.FileMode().String(), h.UID, h.GID, h.Size, prefix, h.Name)
				} else {
					fmt.Fprintf(os.Stdout, "%s%s\n", prefix, h.Name)
				}
			}
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

func componentLabel(c *flatpkg.Component) string {
	if c.Name == "" {
		return "component package"
	}
	return "component " + c.Name
}

// listMatch is the compiled --regexp, or nil.
var listMatch *regexp.Regexp

// listWanted applies --only-files, --only-dirs and --regexp to one entry.
//
// The two --only flags are pkgutil's, where they narrow a receipt's file
// listing; they mean the same here for a package that is not installed yet.
// --regexp is pkgutil's name for matching, though pkgutil matches package
// identifiers with it rather than paths, which have no other way to be
// filtered.
func listWanted(path string, isDir bool) bool {
	switch {
	case listOnlyFiles && isDir:
		return false
	case listOnlyDirs && !isDir:
		return false
	}
	return listMatch == nil || listMatch.MatchString(path)
}
