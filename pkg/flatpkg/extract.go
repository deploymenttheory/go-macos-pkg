// Extracting payload files to the local file system.
package flatpkg

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/appledouble"
	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
	"github.com/deploymenttheory/go-macos-pkg/pkg/cpio"
)

// SymlinkMode says what to do with symbolic links in a payload.
type SymlinkMode int

// Symlink modes.
const (
	// SymlinkAuto creates real links where the host allows it and falls
	// back to writing the target as a small file where it does not (a
	// Windows account without the symlink privilege).
	SymlinkAuto SymlinkMode = iota
	// SymlinkReal creates real links or fails.
	SymlinkReal
	// SymlinkFile always writes the target as a file, never a link.
	SymlinkFile
)

// ParseSymlinkMode parses "auto", "real" or "file".
func ParseSymlinkMode(s string) (SymlinkMode, error) {
	switch strings.ToLower(s) {
	case "auto", "":
		return SymlinkAuto, nil
	case "real":
		return SymlinkReal, nil
	case "file":
		return SymlinkFile, nil
	}
	return 0, fmt.Errorf("unknown symlink mode %q: want auto, real or file", s)
}

// ExtractOptions configures ExtractCPIO.
// XattrMode says what extract does with the AppleDouble "._" sidecar
// entries that carry extended attributes.
type XattrMode int

// Sidecar handling.
const (
	// XattrDefault applies attributes where the host supports them
	// (macOS, Linux) and writes sidecar files elsewhere.
	XattrDefault XattrMode = iota
	// XattrApply sets the attributes on the extracted owner and writes no
	// sidecar file. Attributes the host refuses are recorded as skipped.
	XattrApply
	// XattrFile writes the sidecars as files, beside their owners.
	XattrFile
	// XattrSkip drops the sidecars.
	XattrSkip
)

// ParseXattrMode parses apply, file or skip.
func ParseXattrMode(s string) (XattrMode, error) {
	switch strings.ToLower(s) {
	case "", "auto":
		return XattrDefault, nil
	case "apply":
		return XattrApply, nil
	case "file":
		return XattrFile, nil
	case "skip":
		return XattrSkip, nil
	}
	return 0, fmt.Errorf("unknown xattrs mode %q: want apply, file or skip", s)
}

func (m XattrMode) resolve() XattrMode {
	if m == XattrDefault {
		if hostXattrsSupported {
			return XattrApply
		}
		return XattrFile
	}
	return m
}

// setXattr is the host setter, in a variable so that tests can stand in
// a host that refuses names this one does not.
var setXattr = setHostXattr

// maxSidecar bounds a sidecar read into memory: the attribute header is
// at most 64 KiB, the resource fork after it is not, and 64 MiB covers
// any resource fork worth carrying.
const maxSidecar = 64 << 20

type ExtractOptions struct {
	// Pattern, when set, limits extraction to entries whose path matches.
	Pattern *regexp.Regexp
	// Symlinks selects symbolic link handling.
	Symlinks SymlinkMode
	// Xattrs says what to do with "._" sidecar entries.
	Xattrs XattrMode
	// NoHardLinks writes every member of a hard-link set as a separate
	// file instead of linking the later members to the first.
	NoHardLinks bool
	// Checksums, when set, verifies each regular file against the bill of
	// materials' cksum (the map is keyed by payload path, "./a/b").
	Checksums map[string]uint32
	// Progress, when set, is called for every entry written.
	Progress func(path string)
}

// Skip records an entry that was not written and why.
type Skip struct {
	Path   string
	Reason string
}

// ExtractResult reports what an extraction did.
type ExtractResult struct {
	Files    int
	Dirs     int
	Symlinks int
	// HardLinks counts the entries recreated as hard links; Xattrs the
	// sidecars whose attributes were applied to their owners; XattrFiles
	// the sidecars kept as files because the host refused some of their
	// attributes (nothing is lost: a build reads them back).
	HardLinks  int
	Xattrs     int
	XattrFiles int
	// Renamed lists entries written under a different name because the
	// host cannot store the original (Windows).
	Renamed []Skip
	// Skipped lists entries that were not written: device nodes, sockets,
	// unsafe paths, and links the host refused.
	Skipped []Skip
	// Mismatched lists files whose content did not match the bill of
	// materials, when checksums were supplied.
	Mismatched []string
	// Unlisted lists regular files the payload carried that the bill of
	// materials does not describe, and Absent the files it describes that
	// the payload never delivered. Both are set only when checksums were
	// supplied. A checksum can only be compared for a file named in both,
	// so without these two a payload could deliver entirely different
	// files and satisfy every comparison that was made.
	Unlisted []string
	Absent   []string
}

// Partial reports whether anything was skipped or failed verification.
func (r *ExtractResult) Partial() bool {
	return len(r.Skipped) > 0 || len(r.Mismatched) > 0 ||
		len(r.Unlisted) > 0 || len(r.Absent) > 0
}

// ExtractCPIO writes the entries of a cpio stream under dir. Entries are
// written in stream order, directories first as pkgbuild emits them;
// permission bits and modification times are applied, ownership is not
// (that is the Installer's job, and needs root).
func ExtractCPIO(cr *cpio.Reader, dir string, o ExtractOptions) (*ExtractResult, error) {
	res := &ExtractResult{}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// Every write goes through a root anchored at dir. SafeRelPath rejects
	// names that climb out lexically, but it cannot know what is already
	// on disk: a payload can write a symlink and then a path that traverses
	// it, and plain os.MkdirAll and os.OpenFile would follow the link out
	// of the destination. A root refuses to traverse a link that leaves it,
	// while still allowing one to be created, so packages that legitimately
	// contain absolute symlinks still extract.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	// Directory times are applied after their contents, since writing a
	// file into a directory updates the directory's mtime.
	type dirTime struct {
		path string
		t    time.Time
	}
	var dirTimes []dirTime
	xattrMode := o.Xattrs.resolve()
	// "auto" may keep what a host refuses; an explicit --xattrs apply
	// reports it instead.
	autoXattrs := o.Xattrs == XattrDefault
	// The first extracted path of each hard-link set, by cpio inode.
	linked := map[uint64]string{}
	// Directories that are really symbolic links we wrote. A payload can
	// name a link and then name entries beneath it, which would write
	// through the link and out of the destination. os.Root refuses that,
	// but it refuses it as a hard error, which would abandon the rest of
	// a payload over one hostile entry. Tracking the links lets those
	// entries be skipped and reported like any other unsafe path.
	links := map[string]bool{}
	// Which of the bill of materials' files the payload delivered. Only
	// meaningful over a whole payload, so it is skipped when a pattern
	// selects part of one.
	var seen map[string]bool
	if o.Checksums != nil && o.Pattern == nil {
		seen = make(map[string]bool, len(o.Checksums))
	}
	// verify compares one extracted file against the bill of materials.
	// A file the Bom does not name is recorded rather than passed over:
	// silently skipping it is how a payload could deliver entirely
	// different files and still satisfy every comparison that was made.
	// Sidecars are exempt because pkgbuild records them with no checksum.
	// sum is nil for a later member of a hard-link set, whose bytes are
	// never read: it shares the first member's inode and content, and that
	// member was compared when it was written.
	verify := func(name string, sum *uint32) {
		if o.Checksums == nil || appledouble.IsSidecarName(name) {
			return
		}
		want, ok := o.Checksums[name]
		if !ok {
			res.Unlisted = append(res.Unlisted, name)
			return
		}
		if seen != nil {
			seen[name] = true
		}
		if sum != nil && *sum != want {
			res.Mismatched = append(res.Mismatched, name)
		}
	}

	for {
		h, err := cr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, fmt.Errorf("payload: %w", err)
		}
		if o.Pattern != nil && !o.Pattern.MatchString(h.Name) {
			continue
		}
		rel, renamedFrom, reason := SafeRelPath(h.Name)
		if reason != "" {
			res.Skipped = append(res.Skipped, Skip{Path: h.Name, Reason: reason})
			continue
		}
		if renamedFrom != "" {
			res.Renamed = append(res.Renamed, Skip{Path: renamedFrom, Reason: "renamed to " + rel})
		}
		target := filepath.FromSlash(rel)
		if parent := linkAncestor(links, rel); parent != "" {
			res.Skipped = append(res.Skipped, Skip{
				Path:   h.Name,
				Reason: "would be written through the symbolic link " + parent,
			})
			continue
		}

		switch {
		case h.IsRegular() && appledouble.IsSidecarName(h.Name) && xattrMode != XattrFile:
			handled, raw, err := applySidecar(cr, h, root, dir, target, xattrMode, autoXattrs, res)
			if err != nil {
				return res, err
			}
			if handled {
				continue
			}
			// Not AppleDouble after all: an ordinary file whose name
			// starts with "._"; it has been buffered, so write it out.
			if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return res, err
			}
			sum, err := writeFile(root, target, bytes.NewReader(raw), h)
			if err != nil {
				return res, fmt.Errorf("unable to write %s: %w", rel, err)
			}
			verify(h.Name, &sum)
			res.Files++
		case h.IsRegular() && !o.NoHardLinks && h.NLink > 1 && !appledouble.IsSidecarName(h.Name) && linked[h.Inode] != "":
			// A later member of a hard-link set: link it to the first.
			if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return res, err
			}
			_ = root.Remove(target)
			if err := root.Link(linked[h.Inode], target); err == nil {
				if _, err := io.Copy(io.Discard, cr); err != nil {
					return res, fmt.Errorf("unable to read %s: %w", rel, err)
				}
				verify(h.Name, nil)
				res.HardLinks++
				res.Files++
				break
			}
			// The host refused the link; write a copy.
			sum, err := writeFile(root, target, cr, h)
			if err != nil {
				return res, fmt.Errorf("unable to write %s: %w", rel, err)
			}
			verify(h.Name, &sum)
			res.Files++
		case h.IsDir():
			if rel != "." {
				if err := root.MkdirAll(target, 0o755); err != nil {
					return res, err
				}
			}
			applyMode(root, target, h.Mode)
			dirTimes = append(dirTimes, dirTime{target, h.ModTime})
			res.Dirs++
		case h.IsRegular():
			if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return res, err
			}
			sum, err := writeFile(root, target, cr, h)
			if err != nil {
				return res, fmt.Errorf("unable to write %s: %w", rel, err)
			}
			verify(h.Name, &sum)
			res.Files++
			if h.NLink > 1 && !appledouble.IsSidecarName(h.Name) {
				linked[h.Inode] = target
			}
		case h.IsSymlink():
			linkTarget, err := io.ReadAll(io.LimitReader(cr, 65536))
			if err != nil {
				return res, fmt.Errorf("unable to read link %s: %w", rel, err)
			}
			if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return res, err
			}
			if err := writeSymlink(root, target, string(linkTarget), o.Symlinks); err != nil {
				if errors.Is(err, errSymlinkRefused) {
					res.Skipped = append(res.Skipped, Skip{Path: h.Name, Reason: "symlink not created: " + err.Error()})
					continue
				}
				return res, fmt.Errorf("unable to create link %s: %w", rel, err)
			}
			links[rel] = true
			res.Symlinks++
		default:
			res.Skipped = append(res.Skipped, Skip{Path: h.Name, Reason: fmt.Sprintf("%s entries are not extracted", typeName(h))})
			continue
		}
		if o.Progress != nil {
			o.Progress(rel)
		}
	}
	for i := len(dirTimes) - 1; i >= 0; i-- {
		if !dirTimes[i].t.IsZero() {
			_ = root.Chtimes(dirTimes[i].path, dirTimes[i].t, dirTimes[i].t)
		}
	}
	// Files the bill of materials describes that the payload never
	// delivered. Sorted so the report is stable.
	if seen != nil {
		for name := range o.Checksums {
			if !seen[name] {
				res.Absent = append(res.Absent, name)
			}
		}
		sort.Strings(res.Absent)
	}
	sort.Strings(res.Unlisted)
	return res, nil
}

var errSymlinkRefused = errors.New("host refused")

// linkAncestor reports the nearest ancestor of rel that was extracted as a
// symbolic link, or "" when none was. Writing under such a path follows the
// link, which is how a payload reaches outside the directory it was given.
func linkAncestor(links map[string]bool, rel string) string {
	if len(links) == 0 {
		return ""
	}
	for i := 0; i < len(rel); i++ {
		if rel[i] != '/' {
			continue
		}
		if prefix := rel[:i]; links[prefix] {
			return prefix
		}
	}
	return ""
}

// applySidecar reads a "._" entry and applies its attributes to the
// entry's owner. It reports false, and the bytes it read, when the entry
// is not an AppleDouble file after all and the caller should write it as
// a file.
//
// Attributes are set one at a time, because a host may take some and
// refuse others: Linux accepts only user.*, so a package built on macOS
// carries com.apple.* names that no Linux file system will store. Under
// "auto" the refused ones are kept in a sidecar file beside their owner
// (the same "._" name and bytes a build reads back), so unpacking on a
// host without Apple's attributes never loses them, and repacking the
// unpacked tree restores exactly what was there. An explicit
// --xattrs apply reports them as skipped instead, since the caller asked
// for them to be applied and they were not.
// target is the sidecar's own path, already checked by SafeRelPath, and
// relative to root. dir is the same directory as an absolute path, needed
// because extended attributes are set by name and root has no method for
// them; the setters do not follow symlinks, so a link cannot redirect one.
func applySidecar(r io.Reader, h *cpio.Header, root *os.Root, dir, target string, mode XattrMode, auto bool, res *ExtractResult) (bool, []byte, error) {
	if h.Size > maxSidecar {
		return false, nil, fmt.Errorf("%s: sidecar of %d bytes is larger than %d", h.Name, h.Size, maxSidecar)
	}
	b, err := io.ReadAll(io.LimitReader(r, h.Size))
	if err != nil {
		return false, nil, fmt.Errorf("unable to read %s: %w", h.Name, err)
	}
	f, err := appledouble.Decode(b)
	if err != nil {
		return false, b, nil
	}
	if mode == XattrSkip {
		res.Skipped = append(res.Skipped, Skip{Path: h.Name, Reason: "extended attributes skipped"})
		return true, nil, nil
	}
	owner, _ := appledouble.OwnerName(h.Name)
	rel, _, reason := SafeRelPath(owner)
	if reason != "" {
		res.Skipped = append(res.Skipped, Skip{Path: h.Name, Reason: reason})
		return true, nil, nil
	}
	if _, err := root.Lstat(filepath.FromSlash(rel)); err != nil {
		res.Skipped = append(res.Skipped, Skip{Path: h.Name, Reason: "owner " + owner + " was not extracted"})
		return true, nil, nil
	}
	ownerPath := filepath.Join(dir, filepath.FromSlash(rel))
	attrs := f.Xattrs()
	refused := map[string][]byte{}
	for name, value := range attrs {
		if err := setXattr(ownerPath, name, value); err != nil {
			refused[name] = value
		}
	}
	if len(refused) < len(attrs) {
		res.Xattrs++
	}
	if len(refused) == 0 {
		return true, nil, nil
	}
	names := make([]string, 0, len(refused))
	for name := range refused {
		names = append(names, name)
	}
	sort.Strings(names)
	if !auto {
		res.Skipped = append(res.Skipped, Skip{Path: h.Name, Reason: "host refused " + strings.Join(names, ", ")})
		return true, nil, nil
	}
	// Keep them. The file is a sidecar in its own right, so a later
	// build lifts it back into the owner's attributes.
	kept, err := appledouble.FromXattrs(refused).Encode()
	if err != nil {
		return true, nil, fmt.Errorf("%s: %w", h.Name, err)
	}
	if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return true, nil, err
	}
	if _, err := writeFile(root, target, bytes.NewReader(kept), h); err != nil {
		return true, nil, fmt.Errorf("unable to write %s: %w", h.Name, err)
	}
	res.Files++
	res.XattrFiles++
	return true, nil, nil
}

func writeFile(root *os.Root, target string, r io.Reader, h *cpio.Header) (uint32, error) {
	// Remove first: an existing entry here may be a symlink the payload
	// planted, and truncating one writes through it. The root refuses a
	// link that leaves the destination, but one pointing inside it would
	// still be followed.
	_ = root.Remove(target)
	f, err := root.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	crc := bom.NewCksum()
	_, err = io.Copy(io.MultiWriter(f, crc), r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	applyMode(root, target, h.Mode)
	if !h.ModTime.IsZero() {
		_ = root.Chtimes(target, h.ModTime, h.ModTime)
	}
	return crc.Sum32(), nil
}

// applyMode sets the permission bits. On Windows only the read-only bit
// exists and os.Chmod handles the mapping.
func applyMode(root *os.Root, target string, mode uint32) {
	perm := os.FileMode(mode & 0o777)
	if runtime.GOOS == "windows" {
		return
	}
	_ = root.Chmod(target, perm)
}

func writeSymlink(root *os.Root, target, linkTarget string, mode SymlinkMode) error {
	_ = root.Remove(target)
	if mode == SymlinkFile {
		return root.WriteFile(target, []byte(linkTarget), 0o644)
	}
	// Root.Symlink does not resolve the target, so a package may still
	// carry an absolute or climbing link, as real ones do. What it stops
	// is a later entry traversing that link out of the destination.
	err := root.Symlink(linkTarget, target)
	if err == nil {
		return nil
	}
	if mode == SymlinkReal {
		return err
	}
	// Auto: fall back to a file holding the target.
	if werr := root.WriteFile(target, []byte(linkTarget), 0o644); werr != nil {
		return werr
	}
	return fmt.Errorf("%w (%v); wrote the target as a file instead", errSymlinkRefused, err)
}

func typeName(h *cpio.Header) string {
	switch h.Mode & cpio.ModeTypeMask {
	case cpio.ModeCharDev:
		return "character device"
	case cpio.ModeBlockDev:
		return "block device"
	case cpio.ModeFIFO:
		return "fifo"
	case cpio.ModeSocket:
		return "socket"
	}
	return "unknown"
}

// ChecksumMap builds the payload path to cksum map from a bill of
// materials, for ExtractOptions.Checksums. AppleDouble ._ sidecars are
// left out: pkgbuild records them with size 0 and checksum 0 because they
// describe extended attributes, not the bytes it puts in the payload.
func ChecksumMap(b *bom.BOM) (map[string]uint32, error) {
	entries, err := b.Paths()
	if err != nil {
		return nil, err
	}
	m := make(map[string]uint32, len(entries))
	for _, e := range entries {
		if e.Type == bom.TypeFile && !strings.HasPrefix(path.Base(e.Path), "._") {
			m[e.Path] = e.Checksum
		}
	}
	return m, nil
}

// ExtractPayload extracts a component's payload under dir.
func (c *Component) ExtractPayload(dir string, o ExtractOptions) (*ExtractResult, PayloadEncoding, error) {
	cr, enc, closer, err := c.OpenPayloadCPIO()
	if err != nil {
		return nil, enc, err
	}
	defer closer.Close()
	res, err := ExtractCPIO(cr, dir, o)
	return res, enc, err
}

// ExtractScripts extracts a component's Scripts archive under dir.
func (c *Component) ExtractScripts(dir string, o ExtractOptions) (*ExtractResult, error) {
	cr, _, closer, err := c.OpenScriptsCPIO()
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return ExtractCPIO(cr, dir, o)
}
