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
	// sidecars whose attributes were applied to their owners.
	HardLinks int
	Xattrs    int
	// Renamed lists entries written under a different name because the
	// host cannot store the original (Windows).
	Renamed []Skip
	// Skipped lists entries that were not written: device nodes, sockets,
	// unsafe paths, and links the host refused.
	Skipped []Skip
	// Mismatched lists files whose content did not match the bill of
	// materials, when checksums were supplied.
	Mismatched []string
}

// Partial reports whether anything was skipped or failed verification.
func (r *ExtractResult) Partial() bool {
	return len(r.Skipped) > 0 || len(r.Mismatched) > 0
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
	// Directory times are applied after their contents, since writing a
	// file into a directory updates the directory's mtime.
	type dirTime struct {
		path string
		t    time.Time
	}
	var dirTimes []dirTime
	xattrMode := o.Xattrs.resolve()
	// The first extracted path of each hard-link set, by cpio inode.
	linked := map[uint64]string{}

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
		target := filepath.Join(dir, filepath.FromSlash(rel))

		switch {
		case h.IsRegular() && appledouble.IsSidecarName(h.Name) && xattrMode != XattrFile:
			handled, raw, err := applySidecar(cr, h, dir, xattrMode, res)
			if err != nil {
				return res, err
			}
			if handled {
				continue
			}
			// Not AppleDouble after all: an ordinary file whose name
			// starts with "._"; it has been buffered, so write it out.
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return res, err
			}
			sum, err := writeFile(target, bytes.NewReader(raw), h)
			if err != nil {
				return res, fmt.Errorf("unable to write %s: %w", rel, err)
			}
			if want, ok := o.Checksums[h.Name]; ok && sum != want {
				res.Mismatched = append(res.Mismatched, h.Name)
			}
			res.Files++
		case h.IsRegular() && !o.NoHardLinks && h.NLink > 1 && !appledouble.IsSidecarName(h.Name) && linked[h.Inode] != "":
			// A later member of a hard-link set: link it to the first.
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return res, err
			}
			_ = os.Remove(target)
			if err := os.Link(linked[h.Inode], target); err == nil {
				if _, err := io.Copy(io.Discard, cr); err != nil {
					return res, fmt.Errorf("unable to read %s: %w", rel, err)
				}
				res.HardLinks++
				res.Files++
				break
			}
			// The host refused the link; write a copy.
			sum, err := writeFile(target, cr, h)
			if err != nil {
				return res, fmt.Errorf("unable to write %s: %w", rel, err)
			}
			if want, ok := o.Checksums[h.Name]; ok && sum != want {
				res.Mismatched = append(res.Mismatched, h.Name)
			}
			res.Files++
		case h.IsDir():
			if rel != "." {
				if err := os.MkdirAll(target, 0o755); err != nil {
					return res, err
				}
			}
			applyMode(target, h.Mode)
			dirTimes = append(dirTimes, dirTime{target, h.ModTime})
			res.Dirs++
		case h.IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return res, err
			}
			sum, err := writeFile(target, cr, h)
			if err != nil {
				return res, fmt.Errorf("unable to write %s: %w", rel, err)
			}
			if want, ok := o.Checksums[h.Name]; ok && sum != want {
				res.Mismatched = append(res.Mismatched, h.Name)
			}
			res.Files++
			if h.NLink > 1 && !appledouble.IsSidecarName(h.Name) {
				linked[h.Inode] = target
			}
		case h.IsSymlink():
			linkTarget, err := io.ReadAll(io.LimitReader(cr, 65536))
			if err != nil {
				return res, fmt.Errorf("unable to read link %s: %w", rel, err)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return res, err
			}
			if err := writeSymlink(target, string(linkTarget), o.Symlinks); err != nil {
				if errors.Is(err, errSymlinkRefused) {
					res.Skipped = append(res.Skipped, Skip{Path: h.Name, Reason: "symlink not created: " + err.Error()})
					continue
				}
				return res, fmt.Errorf("unable to create link %s: %w", rel, err)
			}
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
			_ = os.Chtimes(dirTimes[i].path, dirTimes[i].t, dirTimes[i].t)
		}
	}
	return res, nil
}

var errSymlinkRefused = errors.New("host refused")

// applySidecar reads a "._" entry and applies (or skips) its attributes.
// It reports false, and the bytes it read, when the entry is not an
// AppleDouble file after all and the caller should write it as a file.
func applySidecar(r io.Reader, h *cpio.Header, dir string, mode XattrMode, res *ExtractResult) (bool, []byte, error) {
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
	target := filepath.Join(dir, filepath.FromSlash(rel))
	if _, err := os.Lstat(target); err != nil {
		res.Skipped = append(res.Skipped, Skip{Path: h.Name, Reason: "owner " + owner + " was not extracted"})
		return true, nil, nil
	}
	if err := setHostXattrs(target, f.Xattrs()); err != nil {
		res.Skipped = append(res.Skipped, Skip{Path: h.Name, Reason: "attribute not set: " + err.Error()})
		return true, nil, nil
	}
	res.Xattrs++
	return true, nil, nil
}

func writeFile(target string, r io.Reader, h *cpio.Header) (uint32, error) {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
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
	applyMode(target, h.Mode)
	if !h.ModTime.IsZero() {
		_ = os.Chtimes(target, h.ModTime, h.ModTime)
	}
	return crc.Sum32(), nil
}

// applyMode sets the permission bits. On Windows only the read-only bit
// exists and os.Chmod handles the mapping.
func applyMode(target string, mode uint32) {
	perm := os.FileMode(mode & 0o777)
	if runtime.GOOS == "windows" {
		return
	}
	_ = os.Chmod(target, perm)
}

func writeSymlink(target, linkTarget string, mode SymlinkMode) error {
	_ = os.Remove(target)
	if mode == SymlinkFile {
		return os.WriteFile(target, []byte(linkTarget), 0o644)
	}
	err := os.Symlink(linkTarget, target)
	if err == nil {
		return nil
	}
	if mode == SymlinkReal {
		return err
	}
	// Auto: fall back to a file holding the target.
	if werr := os.WriteFile(target, []byte(linkTarget), 0o644); werr != nil {
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
