// Building a component package from a directory: what pkgbuild does.
package flatpkg

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
	"github.com/deploymenttheory/go-macos-pkg/pkg/cpio"
	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// Ownership selects how payload owners are recorded, as pkgbuild's
// --ownership does.
type Ownership int

// Ownership policies.
const (
	// OwnershipRecommended records every entry as root:wheel (0:0), which
	// is what pkgbuild --ownership recommended writes and what an
	// installer package should carry.
	OwnershipRecommended Ownership = iota
	// OwnershipPreserve records the source tree's owners. Not available on
	// Windows, which has no uid or gid.
	OwnershipPreserve
	// OwnershipPreserveOther preserves owners other than the user running
	// the build, and records the rest as root:wheel.
	OwnershipPreserveOther
)

// ParseOwnership parses recommended, preserve or preserve-other.
func ParseOwnership(s string) (Ownership, error) {
	switch strings.ToLower(s) {
	case "recommended", "":
		return OwnershipRecommended, nil
	case "preserve":
		return OwnershipPreserve, nil
	case "preserve-other":
		return OwnershipPreserveOther, nil
	}
	return 0, fmt.Errorf("unknown ownership %q: want recommended, preserve or preserve-other", s)
}

// ErrUnsupportedOnPlatform reports an option the host cannot honour.
var ErrUnsupportedOnPlatform = fmt.Errorf("flatpkg: not supported on %s", runtime.GOOS)

// ComponentOptions configures BuildComponent.
type ComponentOptions struct {
	// Root is the payload root: the directory whose contents are installed
	// at InstallLocation. Empty with NoPayload for a scripts-only package.
	Root string
	// Scripts is a directory of install scripts (preinstall, postinstall,
	// ...) and their resources; optional.
	Scripts string
	// NoPayload builds a package with no Payload, like pkgbuild --nopayload.
	NoPayload bool

	Identifier      string
	Version         string
	InstallLocation string // default "/"
	Ownership       Ownership
	MinOSVersion    string
	// PostinstallAction is none, logout, restart or shutdown.
	PostinstallAction string
	// Auth is root (default) or none.
	Auth string
	// Relocatable and OverwritePermissions default to pkgbuild's values
	// (false and true).
	Relocatable          bool
	OverwritePermissions *bool
	// NoBundleRelocation omits the <relocate> references so bundles are
	// always installed at their packaged path.
	NoBundleRelocation bool
	// PreserveXattr sets preserve-xattr on the PackageInfo. Extended
	// attributes are not carried in the payload by this builder, so the
	// flag only matters to what the Installer expects.
	PreserveXattr bool

	// Epoch, when set, is written as every timestamp (payload, bill of
	// materials, archive) so the package is reproducible. Zero preserves
	// the source tree's modification times and uses the current time for
	// the archive.
	Epoch time.Time
	// TempDir holds the scratch files. Default beside the output.
	TempDir string
	// GeneratorVersion is written as PackageInfo's generator-version.
	GeneratorVersion string

	// Exclude, when set, drops payload entries whose "./" path it matches.
	Exclude func(relPath string) bool
	// Executable, when set, decides which files get the execute bits on
	// hosts whose file system does not record them (Windows). Files it
	// returns true for get 0755, the rest 0644.
	Executable func(relPath string) bool
	// FileModes overrides the mode of specific "./" paths.
	FileModes map[string]uint32

	// Signer, when set, signs the archive as it is written.
	Signer xar.Signer

	// Progress, when set, is called for every payload entry.
	Progress func(relPath string)
}

// BuildResult reports what BuildComponent wrote.
type BuildResult struct {
	NumberOfFiles int
	InstallKBytes int
	Bundles       []Bundle
	Scripts       []string
	PackageInfo   *PackageInfo
}

// installKBytes computes pkgbuild's installKBytes: every entry except the
// root, rounded up to 512-byte blocks, summed, and rounded up to whole
// kilobytes. Directories count with the size APFS reports for them
// (32 bytes per child plus two), symlinks with their target length. Found
// by building trees of known content and reading back what pkgbuild
// wrote, then checked against every fixture.
func installKBytes(entries []payloadEntry) int {
	var blocks int64
	for _, e := range entries {
		if e.rel == "." {
			continue
		}
		blocks += (e.size + 511) / 512
	}
	return int((blocks*512 + 1023) / 1024)
}

// payloadEntry is one path in the payload, collected before writing.
type payloadEntry struct {
	rel      string // "./a/b"
	src      string // host path
	mode     uint32 // st_mode with type bits
	uid, gid uint32
	mtime    time.Time
	size     int64
	link     string
	children int
}

// BuildComponent writes a component package to out.
func BuildComponent(o ComponentOptions, out io.Writer) (*BuildResult, error) {
	if o.Identifier == "" {
		return nil, fmt.Errorf("flatpkg: an identifier is required")
	}
	if o.Version == "" {
		return nil, fmt.Errorf("flatpkg: a version is required")
	}
	if o.Root == "" && !o.NoPayload {
		return nil, fmt.Errorf("flatpkg: a payload root is required (or NoPayload)")
	}
	if o.Ownership != OwnershipRecommended && runtime.GOOS == "windows" {
		return nil, fmt.Errorf("%w: preserving ownership (Windows has no uid or gid)", ErrUnsupportedOnPlatform)
	}
	if o.InstallLocation == "" {
		o.InstallLocation = "/"
	}
	if o.Auth == "" {
		o.Auth = "root"
	}
	if o.PostinstallAction == "" {
		o.PostinstallAction = "none"
	}
	switch o.PostinstallAction {
	case "none", "logout", "restart", "shutdown":
	default:
		return nil, fmt.Errorf("flatpkg: unknown postinstall action %q", o.PostinstallAction)
	}
	if o.GeneratorVersion == "" {
		o.GeneratorVersion = "go-macos-pkg"
	}
	epoch := o.Epoch
	archiveTime := time.Now()
	if !epoch.IsZero() {
		archiveTime = epoch
	}

	scratchDir := o.TempDir
	if scratchDir == "" {
		scratchDir = os.TempDir()
	}
	tmp, err := os.MkdirTemp(scratchDir, "macospkg-build-*")
	if err != nil {
		return nil, fmt.Errorf("flatpkg: unable to create scratch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	res := &BuildResult{}
	info := &PackageInfo{
		FormatVersion:        2,
		Identifier:           o.Identifier,
		Version:              o.Version,
		InstallLocation:      o.InstallLocation,
		Auth:                 o.Auth,
		OverwritePermissions: BoolPtr(true),
		Relocatable:          BoolPtr(o.Relocatable),
		GeneratorVersion:     o.GeneratorVersion,
		PostinstallAction:    o.PostinstallAction,
		MinimumSystemVersion: o.MinOSVersion,
		BundleVersion:        &BundleList{},
		UpgradeBundle:        &BundleRefs{},
		UpdateBundle:         &BundleRefs{},
		AtomicUpdateBundle:   &BundleRefs{},
		StrictIdentifier:     &BundleRefs{},
		Relocate:             &BundleRefs{},
	}
	if o.OverwritePermissions != nil {
		info.OverwritePermissions = o.OverwritePermissions
	}
	if o.PreserveXattr {
		info.PreserveXattr = BoolPtr(true)
	}

	var payloadPath, bomPath, scriptsPath string
	if !o.NoPayload {
		entries, err := collectPayload(o, epoch)
		if err != nil {
			return nil, err
		}
		payloadPath = filepath.Join(tmp, "Payload")
		bomPath = filepath.Join(tmp, "Bom")
		if err := writePayloadAndBom(entries, payloadPath, bomPath, o.Progress); err != nil {
			return nil, err
		}
		res.NumberOfFiles = len(entries)
		res.InstallKBytes = installKBytes(entries)
		info.Payload = &Payload{NumberOfFiles: res.NumberOfFiles, InstallKBytes: res.InstallKBytes}

		bundles, err := findBundles(o.Root)
		if err != nil {
			return nil, fmt.Errorf("flatpkg: scanning for bundles: %w", err)
		}
		res.Bundles = bundles
		for _, b := range bundles {
			// pkgbuild's layout: details once, at the top level, then id
			// references in bundle-version (version checking),
			// upgrade-bundle, strict-identifier and relocate.
			info.Bundles = append(info.Bundles, b)
			ref := BundleRef{ID: b.ID}
			info.BundleVersion.Bundles = append(info.BundleVersion.Bundles, Bundle{ID: b.ID})
			info.UpgradeBundle.Bundles = append(info.UpgradeBundle.Bundles, ref)
			info.StrictIdentifier.Bundles = append(info.StrictIdentifier.Bundles, ref)
			if !o.NoBundleRelocation {
				info.Relocate.Bundles = append(info.Relocate.Bundles, ref)
			}
		}
	}

	if o.Scripts != "" {
		names, err := scriptNames(o.Scripts)
		if err != nil {
			return nil, err
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("flatpkg: %s contains no install scripts (preinstall, postinstall, ...)", o.Scripts)
		}
		scriptsPath = filepath.Join(tmp, "Scripts")
		if err := writeScripts(o.Scripts, scriptsPath, epoch); err != nil {
			return nil, err
		}
		info.Scripts = &Scripts{}
		for _, n := range names {
			s := &Script{File: "./" + n}
			switch n {
			case "preinstall":
				info.Scripts.Preinstall = s
			case "postinstall":
				info.Scripts.Postinstall = s
			case "preflight":
				info.Scripts.Preflight = s
			case "postflight":
				info.Scripts.Postflight = s
			case "preupgrade":
				info.Scripts.Preupgrade = s
			case "postupgrade":
				info.Scripts.Postupgrade = s
			}
		}
		res.Scripts = names
	}

	res.PackageInfo = info
	pi, err := info.Marshal()
	if err != nil {
		return nil, err
	}

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
	// pkgbuild's entry order: Bom, Payload, Scripts, PackageInfo.
	if bomPath != "" {
		if err := addFileEntry(w, EntryBom, hdr, xar.EncodingGzip, bomPath); err != nil {
			return nil, err
		}
		if err := addFileEntry(w, EntryPayload, hdr, xar.EncodingNone, payloadPath); err != nil {
			return nil, err
		}
	}
	if scriptsPath != "" {
		if err := addFileEntry(w, EntryScripts, hdr, xar.EncodingNone, scriptsPath); err != nil {
			return nil, err
		}
	}
	if err := w.AddFile(EntryPackageInfo, hdr, xar.EncodingGzip, bytes.NewReader(pi)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return res, nil
}

func addFileEntry(w *xar.Writer, name string, hdr xar.FileHeader, encoding, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	return w.AddFile(name, hdr, encoding, f)
}

// collectPayload walks the root in sorted order, directories before
// their contents, applying the ownership, mode and time policies.
func collectPayload(o ComponentOptions, epoch time.Time) ([]payloadEntry, error) {
	root := filepath.Clean(o.Root)
	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("flatpkg: payload root: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("flatpkg: payload root %s is not a directory", root)
	}
	var entries []payloadEntry
	childCount := map[string]int{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		relSlash := "."
		if rel != "." {
			relSlash = "./" + filepath.ToSlash(rel)
		}
		if relSlash != "." && o.Exclude != nil && o.Exclude(relSlash) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			fi, err = os.Lstat(p)
			if err != nil {
				return err
			}
		}
		e := payloadEntry{rel: relSlash, src: p, mtime: fi.ModTime().UTC().Truncate(time.Second)}
		if !epoch.IsZero() {
			e.mtime = epoch
		}
		switch {
		case fi.IsDir():
			e.mode = cpio.ModeDir | permBits(fi, relSlash, o, 0o755)
		case fi.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			e.link = filepath.ToSlash(target)
			e.mode = cpio.ModeSymlink | 0o755
			e.size = int64(len(e.link))
		case fi.Mode().IsRegular():
			e.mode = cpio.ModeRegular | permBits(fi, relSlash, o, 0o644)
			e.size = fi.Size()
		default:
			return fmt.Errorf("flatpkg: %s: %s entries cannot be packaged", relSlash, fi.Mode().Type())
		}
		if m, ok := o.FileModes[relSlash]; ok {
			e.mode = e.mode&cpio.ModeTypeMask | m&0o7777
		}
		e.uid, e.gid = owners(fi, o.Ownership)
		if relSlash != "." {
			// Not path.Dir: it cleans "./a" to ".", and "./a/b" to "a",
			// which would never match the "./"-prefixed entry names.
			childCount[parentPath(relSlash)]++
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("flatpkg: walking payload root: %w", err)
	}
	for i := range entries {
		if entries[i].mode&cpio.ModeTypeMask == cpio.ModeDir {
			entries[i].children = childCount[entries[i].rel]
			// Apple's Bom records the directory's size as APFS reports it:
			// 32 bytes per entry plus two.
			entries[i].size = int64(32 * (entries[i].children + 2))
		}
	}
	return entries, nil
}

// parentPath returns the parent of a "./a/b" payload path, "." for a
// top-level entry.
func parentPath(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i > 1 {
		return rel[:i]
	}
	return "."
}

// permBits returns the permission bits to record for an entry. Unix hosts
// report them; on Windows the caller's Executable rule decides.
func permBits(fi os.FileInfo, rel string, o ComponentOptions, def uint32) uint32 {
	if runtime.GOOS == "windows" {
		if fi.IsDir() {
			return 0o755
		}
		if o.Executable != nil && o.Executable(rel) {
			return 0o755
		}
		return def
	}
	perm := uint32(fi.Mode().Perm())
	if fi.Mode()&os.ModeSetuid != 0 {
		perm |= cpio.ModeSetUID
	}
	if fi.Mode()&os.ModeSetgid != 0 {
		perm |= cpio.ModeSetGID
	}
	if fi.Mode()&os.ModeSticky != 0 {
		perm |= cpio.ModeSticky
	}
	return perm
}

// writePayloadAndBom streams the entries into a gzip odc cpio and builds
// the bill of materials alongside.
func writePayloadAndBom(entries []payloadEntry, payloadPath, bomPath string, progress func(string)) error {
	pf, err := os.Create(payloadPath)
	if err != nil {
		return err
	}
	defer pf.Close()
	gz, err := gzip.NewWriterLevel(pf, gzip.DefaultCompression)
	if err != nil {
		return err
	}
	cw := cpio.NewWriter(gz)
	b := bom.NewBuilder()

	for i, e := range entries {
		hdr := &cpio.Header{
			Name:    e.rel,
			Inode:   uint64(i + 1),
			Mode:    e.mode,
			UID:     e.uid,
			GID:     e.gid,
			NLink:   1,
			ModTime: e.mtime,
			Size:    e.size,
		}
		if e.mode&cpio.ModeTypeMask == cpio.ModeDir {
			hdr.Size = 0
			hdr.NLink = uint32(e.children + 2)
		}
		if err := cw.WriteHeader(hdr); err != nil {
			return err
		}
		be := bom.Entry{
			Path:         e.rel,
			Architecture: 15,
			Mode:         uint16(e.mode),
			UID:          e.uid,
			GID:          e.gid,
			ModTime:      e.mtime,
			Size:         e.size,
		}
		switch e.mode & cpio.ModeTypeMask {
		case cpio.ModeDir:
			be.Type = bom.TypeDirectory
		case cpio.ModeSymlink:
			be.Type = bom.TypeLink
			be.LinkTarget = e.link
			be.Checksum = bom.CksumBytes([]byte(e.link))
			if _, err := io.WriteString(cw, e.link); err != nil {
				return err
			}
		default:
			be.Type = bom.TypeFile
			f, err := os.Open(e.src)
			if err != nil {
				return err
			}
			ck := bom.NewCksum()
			n, err := io.Copy(io.MultiWriter(cw, ck), f)
			f.Close()
			if err != nil {
				return fmt.Errorf("flatpkg: %s: %w", e.rel, err)
			}
			if n != e.size {
				return fmt.Errorf("flatpkg: %s changed size while being packaged (%d, was %d)", e.rel, n, e.size)
			}
			be.Checksum = ck.Sum32()
		}
		if err := b.Add(be); err != nil {
			return err
		}
		if progress != nil {
			progress(e.rel)
		}
	}
	if err := cw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := pf.Close(); err != nil {
		return err
	}
	bf, err := os.Create(bomPath)
	if err != nil {
		return err
	}
	defer bf.Close()
	if err := b.Build(bf); err != nil {
		return err
	}
	return bf.Close()
}

// knownScripts are the script names pkgbuild recognises.
var knownScripts = []string{"preflight", "preinstall", "preupgrade", "postinstall", "postupgrade", "postflight"}

// scriptNames returns the recognised scripts present in dir, in Apple's
// order.
func scriptNames(dir string) ([]string, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("flatpkg: scripts directory: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("flatpkg: scripts path %s is not a directory", dir)
	}
	var out []string
	for _, n := range knownScripts {
		if fi, err := os.Stat(filepath.Join(dir, n)); err == nil && fi.Mode().IsRegular() {
			out = append(out, n)
		}
	}
	return out, nil
}

// writeScripts packs the scripts directory as a gzip odc cpio, forcing
// the execute bits on so a script committed from Windows still runs.
func writeScripts(dir, dst string, epoch time.Time) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	cw := cpio.NewWriter(gz)
	var paths []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return fmt.Errorf("flatpkg: walking scripts: %w", err)
	}
	sort.Strings(paths)
	for i, p := range paths {
		rel, _ := filepath.Rel(dir, p)
		name := "."
		if rel != "." {
			name = "./" + filepath.ToSlash(rel)
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		mtime := fi.ModTime().UTC().Truncate(time.Second)
		if !epoch.IsZero() {
			mtime = epoch
		}
		hdr := &cpio.Header{Name: name, Inode: uint64(i + 1), NLink: 1, ModTime: mtime}
		switch {
		case fi.IsDir():
			hdr.Mode = cpio.ModeDir | 0o755
			if err := cw.WriteHeader(hdr); err != nil {
				return err
			}
		case fi.Mode().IsRegular():
			hdr.Mode = cpio.ModeRegular | 0o755
			hdr.Size = fi.Size()
			if err := cw.WriteHeader(hdr); err != nil {
				return err
			}
			src, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(cw, src)
			src.Close()
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("flatpkg: scripts: %s is not a regular file", name)
		}
	}
	if err := cw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Close()
}
