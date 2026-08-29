// Extracting payload files to the local file system.
package flatpkg

import (
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
type ExtractOptions struct {
	// Pattern, when set, limits extraction to entries whose path matches.
	Pattern *regexp.Regexp
	// Symlinks selects symbolic link handling.
	Symlinks SymlinkMode
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
