// Flatten: turning an expanded package directory back into a flat package,
// which is what pkgutil --flatten does.
//
// It is the inverse of Expand and deliberately literal. Every file in the
// directory becomes an archive entry with the bytes it has on disk, so a
// Payload that was never unpacked is carried straight through and a
// PackageInfo somebody edited is carried through as edited. The one
// exception is a directory named Scripts, which becomes the gzip cpio a
// package expects; pkgutil treats that name the same way, and leaves
// PlugIns packed because Expand never unpacked it.
//
// This is not a substitute for Build. Nothing here reads a PackageInfo,
// counts a payload or writes a bill of materials: a directory that was not
// a package before will not become a valid one by being flattened.
package flatpkg

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// FlattenOptions controls Flatten.
type FlattenOptions struct {
	// Dir is the expanded package directory to read.
	Dir string
	// Epoch pins the archive's timestamps, for reproducible output.
	Epoch time.Time
	// TempDir holds the scratch files. Default beside the output.
	TempDir string
	// Signer signs the finished archive, as for a build.
	Signer xar.Signer
	// Progress is called with each entry as it is added.
	Progress func(string)
}

// FlattenResult reports what was written.
type FlattenResult struct {
	Entries []string
	// Archived names the directories that were packed into a cpio rather
	// than copied through, which is Scripts and nothing else today.
	Archived []string
}

// Flatten writes the flat package an expanded directory describes.
func Flatten(o FlattenOptions, out io.Writer) (*FlattenResult, error) {
	st, err := os.Stat(o.Dir)
	if err != nil {
		return nil, fmt.Errorf("flatpkg: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("flatpkg: %s is not an expanded package directory", o.Dir)
	}

	archiveTime := time.Now()
	if !o.Epoch.IsZero() {
		archiveTime = o.Epoch
	}
	scratch := o.TempDir
	if scratch == "" {
		scratch = os.TempDir()
	}
	tmp, err := os.MkdirTemp(scratch, "macospkg-flatten-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	w, err := xar.NewWriter(out, xar.WriterOptions{
		ChecksumAlg:  xar.ChecksumSHA256,
		CreationTime: archiveTime,
		TempDir:      o.TempDir,
		Signer:       o.Signer,
	})
	if err != nil {
		return nil, err
	}
	hdr := xar.FileHeader{Mode: 0o644, User: "root", Group: "wheel", ModTime: archiveTime, CTime: archiveTime, ATime: archiveTime}
	dirHdr := xar.FileHeader{Mode: 0o755, User: "root", Group: "wheel", ModTime: archiveTime, CTime: archiveTime, ATime: archiveTime}

	res := &FlattenResult{}
	if err := flattenDir(w, o, res, tmp, o.Dir, "", hdr, dirHdr, archiveTime); err != nil {
		return nil, err
	}
	if len(res.Entries) == 0 {
		return nil, fmt.Errorf("flatpkg: %s holds nothing to flatten", o.Dir)
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return res, nil
}

// flattenDir adds one directory's entries, recursing into the ones that
// stay directories.
func flattenDir(w *xar.Writer, o FlattenOptions, res *FlattenResult, tmp, dir, prefix string, hdr, dirHdr xar.FileHeader, archiveTime time.Time) error {
	names, err := sortedDirNames(dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		entry := name
		if prefix != "" {
			entry = prefix + "/" + name
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir() && name == EntryScripts:
			// The one name Expand unpacks, so the one name to pack again.
			archive := filepath.Join(tmp, strings.ReplaceAll(entry, "/", "_"))
			if err := writeArchivedDir(path, archive, ComponentOptions{}, archiveTime, false); err != nil {
				return err
			}
			if err := addFileEntry(w, entry, hdr, xar.EncodingNone, archive); err != nil {
				return err
			}
			res.Archived = append(res.Archived, entry)
			res.Entries = append(res.Entries, entry)
		case info.IsDir():
			if err := w.AddDir(entry, dirHdr); err != nil {
				return err
			}
			res.Entries = append(res.Entries, entry)
			if err := flattenDir(w, o, res, tmp, path, entry, hdr, dirHdr, archiveTime); err != nil {
				return err
			}
			continue
		case info.Mode().IsRegular():
			// The bytes as they stand. A Payload left packed by Expand
			// goes back exactly as it came out.
			if err := addFileEntry(w, entry, hdr, encodingFor(name), path); err != nil {
				return err
			}
			res.Entries = append(res.Entries, entry)
		default:
			return fmt.Errorf("flatpkg: %s is neither a file nor a directory", entry)
		}
		if o.Progress != nil {
			o.Progress(entry)
		}
	}
	return nil
}

// encodingFor picks the xar encoding an entry is stored with.
//
// A Payload and a Scripts archive are already compressed, so they are
// stored as they are; the small XML and bill-of-materials entries are
// gzipped, which is what Apple's tools do.
func encodingFor(name string) string {
	switch name {
	case EntryPayload, EntryLargePayload, EntryScripts, EntryPlugins:
		return xar.EncodingNone
	}
	return xar.EncodingGzip
}

// sortedDirNames lists a directory in a stable order, so flattening the
// same tree twice gives the same archive.
func sortedDirNames(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}
