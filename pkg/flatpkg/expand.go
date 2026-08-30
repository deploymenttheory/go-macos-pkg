// Expanding a package to a directory, the way pkgutil --expand does.
package flatpkg

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// ExpandOptions configures Expand.
type ExpandOptions struct {
	// Full also unpacks each Payload into a directory of the same name,
	// as pkgutil --expand-full does. Scripts are always unpacked, as
	// pkgutil --expand does.
	Full bool
	// Verify checks every archive entry's stored checksums while reading.
	Verify bool
	// Symlinks selects symbolic link handling for Full extractions.
	Symlinks SymlinkMode
	// Xattrs and NoHardLinks are passed to the Full extractions.
	Xattrs      XattrMode
	NoHardLinks bool
	// Progress, when set, is called for every entry written.
	Progress func(path string)
}

// ExpandResult reports what an expansion did.
type ExpandResult struct {
	Entries  int
	Payloads []*ExtractResult
	Skipped  []Skip
}

// Partial reports whether anything was skipped.
func (r *ExpandResult) Partial() bool {
	if len(r.Skipped) > 0 {
		return true
	}
	for _, p := range r.Payloads {
		if p.Partial() {
			return true
		}
	}
	return false
}

// Expand writes the package's archive entries, decoded, under dir. dir
// must not already exist, as with pkgutil --expand, so an expansion never
// merges into something else.
func (p *Package) Expand(dir string, o ExpandOptions) (*ExpandResult, error) {
	if _, err := os.Lstat(dir); err == nil {
		return nil, fmt.Errorf("flatpkg: %s already exists; expand into a new directory", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	res := &ExpandResult{}
	x := p.XAR

	// Payload and Scripts entries are handled per component below; every
	// other entry is written as a file.
	special := map[*xar.File]bool{}
	for _, c := range p.Components {
		if f := c.Entry(EntryPayload); f != nil {
			special[f] = true
		}
		if f := c.Entry(EntryScripts); f != nil {
			special[f] = true
		}
	}

	for _, f := range x.Files() {
		if special[f] {
			continue
		}
		rel, renamed, reason := SafeRelPath(f.Path())
		if reason != "" {
			res.Skipped = append(res.Skipped, Skip{Path: f.Path(), Reason: reason})
			continue
		}
		if renamed != "" {
			res.Skipped = append(res.Skipped, Skip{Path: renamed, Reason: "renamed to " + rel})
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		switch {
		case f.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return res, err
			}
		case f.Type.Value == xar.TypeSymlink:
			if err := writeSymlink(target, f.SymlinkTarget(), o.Symlinks); err != nil {
				res.Skipped = append(res.Skipped, Skip{Path: f.Path(), Reason: err.Error()})
				continue
			}
		case f.Data != nil:
			if o.Verify {
				if err := x.Verify(f); err != nil {
					return res, err
				}
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return res, err
			}
			if err := copyEntry(x, f, target); err != nil {
				return res, err
			}
		default:
			res.Skipped = append(res.Skipped, Skip{Path: f.Path(), Reason: "entry has no data"})
			continue
		}
		res.Entries++
		if o.Progress != nil {
			o.Progress(rel)
		}
	}

	for _, c := range p.Components {
		base := dir
		if c.Name != "" {
			base = filepath.Join(dir, filepath.FromSlash(c.Name))
		}
		if c.HasScripts() {
			sr, err := c.ExtractScripts(filepath.Join(base, EntryScripts), ExtractOptions{Symlinks: o.Symlinks, Xattrs: o.Xattrs, NoHardLinks: o.NoHardLinks, Progress: o.Progress})
			if err != nil {
				return res, fmt.Errorf("%s Scripts: %w", componentName(c), err)
			}
			res.Payloads = append(res.Payloads, sr)
		}
		if !c.HasPayload() {
			continue
		}
		entry := c.Entry(EntryPayload)
		target := filepath.Join(base, filepath.FromSlash(entry.Name()))
		if !o.Full {
			if o.Verify {
				if err := x.Verify(entry); err != nil {
					return res, err
				}
			}
			if err := copyEntry(x, entry, target); err != nil {
				return res, err
			}
			res.Entries++
			continue
		}
		pr, _, err := c.ExtractPayload(target, ExtractOptions{Symlinks: o.Symlinks, Xattrs: o.Xattrs, NoHardLinks: o.NoHardLinks, Progress: o.Progress})
		if err != nil {
			return res, fmt.Errorf("%s Payload: %w", componentName(c), err)
		}
		res.Payloads = append(res.Payloads, pr)
	}
	return res, nil
}

func componentName(c *Component) string {
	if c.Name == "" {
		return "package"
	}
	return c.Name
}

// copyEntry writes one archive entry, decoded, to target.
func copyEntry(x *xar.Reader, f *xar.File, target string) error {
	rc, err := x.Open(f)
	if err != nil {
		return err
	}
	defer rc.Close()
	mode := os.FileMode(0o644)
	if m := f.ModeBits(); m != 0 {
		mode = os.FileMode(m & 0o777)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, rc)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("unable to write %s: %w", strings.TrimPrefix(target, "./"), err)
	}
	if t := f.ModTime(); !t.IsZero() {
		_ = os.Chtimes(target, t, t)
	}
	return nil
}
