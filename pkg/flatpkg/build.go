// Building a component package from a directory: what pkgbuild does.
package flatpkg

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/appledouble"
	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
	"github.com/deploymenttheory/go-macos-pkg/pkg/cpio"
	"github.com/deploymenttheory/go-macos-pkg/pkg/pbzx"
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

// Compression selects the payload container, as pkgbuild's --compression
// does.
type Compression int

// Payload compressions.
const (
	// CompressionGzip is pkgbuild's default: gzip cpio, readable by every
	// macOS.
	CompressionGzip Compression = iota
	// CompressionPBZX is the pbzx container (xz chunks) that pkgbuild
	// --compression latest has written on every macOS from 12 to 26.
	// Packages using it need macOS 12 or later.
	CompressionPBZX
	// CompressionLatest is whatever pkgbuild --compression latest means
	// today: pbzx.
	CompressionLatest
)

// ParseCompression parses gzip, pbzx or latest.
func ParseCompression(s string) (Compression, error) {
	switch strings.ToLower(s) {
	case "gzip", "legacy", "":
		return CompressionGzip, nil
	case "pbzx", "xz":
		return CompressionPBZX, nil
	case "latest":
		return CompressionLatest, nil
	}
	return 0, fmt.Errorf("unknown compression %q: want gzip, pbzx or latest", s)
}

func (c Compression) String() string {
	switch c {
	case CompressionPBZX, CompressionLatest:
		return "pbzx"
	}
	return "gzip"
}

// Encoding is the payload encoding the compression produces.
func (c Compression) Encoding() PayloadEncoding {
	if c == CompressionPBZX || c == CompressionLatest {
		return PayloadPBZX
	}
	return PayloadGzip
}

// XattrSource says where a build takes extended attributes from.
type XattrSource int

// Attribute sources.
const (
	// XattrsFromFS reads the attributes the host file system reports
	// (all of them on macOS, user.* on Linux, none on Windows), plus
	// ExtraXattrs and any "._" sidecar files already in the source tree.
	XattrsFromFS XattrSource = iota
	// XattrsNone carries no attributes, whatever the host reports;
	// ExtraXattrs and sidecar files in the tree still apply.
	XattrsNone
)

// ParseXattrSource parses fs or none.
func ParseXattrSource(s string) (XattrSource, error) {
	switch strings.ToLower(s) {
	case "fs", "":
		return XattrsFromFS, nil
	case "none":
		return XattrsNone, nil
	}
	return 0, fmt.Errorf("unknown xattrs source %q: want fs or none", s)
}

// HardLinkMode says how hard links are packaged.
type HardLinkMode int

// Hard-link modes.
const (
	// HardLinksAuto packages files that share an inode as one hard-link
	// set, as pkgbuild does: the same cpio inode and link count on every
	// member, each carrying the data, one bill-of-materials index entry.
	// Hosts that expose no inode (Windows) fall back to copies.
	HardLinksAuto HardLinkMode = iota
	// HardLinksCopy packages every path as a separate file.
	HardLinksCopy
)

// ParseHardLinkMode parses auto or copy.
func ParseHardLinkMode(s string) (HardLinkMode, error) {
	switch strings.ToLower(s) {
	case "auto", "":
		return HardLinksAuto, nil
	case "copy":
		return HardLinksCopy, nil
	}
	return 0, fmt.Errorf("unknown hard-links mode %q: want auto or copy", s)
}

// pbzxMinimumOS is the oldest macOS that reads a pbzx payload; pkgbuild
// refuses --compression latest without a --min-os-version of at least
// this, and so does this builder.
const pbzxMinimumOS = "12.0"

// ErrCompressionNeedsMinOS reports a pbzx package with too old a minimum
// system version.
var ErrCompressionNeedsMinOS = errors.New("flatpkg: pbzx payloads need a minimum system version of 12.0 or later")

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
	// PreserveXattr sets preserve-xattr on the PackageInfo, as pkgbuild
	// --preserve-xattr does.
	PreserveXattr bool

	// Xattrs selects where extended attributes come from. They are
	// carried the way pkgbuild carries them: as AppleDouble "._" sidecar
	// entries beside their owners, in the payload and in Scripts.
	Xattrs XattrSource
	// ExcludeXattr, when set, drops attributes by name (for example
	// com.apple.provenance or com.apple.quarantine, which describe the
	// build host rather than the file).
	ExcludeXattr func(name string) bool
	// ExtraXattrs adds attributes by "./" path, name → value, on top of
	// what the host reports; a manifest can give a Linux or Windows build
	// the attributes a macOS build would read from disk.
	ExtraXattrs map[string]map[string][]byte
	// HardLinks selects how hard links are packaged.
	HardLinks HardLinkMode

	// Compression selects the payload container. Scripts are always gzip,
	// as pkgbuild leaves them.
	Compression Compression
	// PBZXBlockSize is the pbzx block size; 0 selects pkgbuild's 16 MiB.
	PBZXBlockSize uint64

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
	seen := map[uint64]bool{}
	for _, e := range entries {
		if e.rel == "." || e.sidecar != nil {
			continue
		}
		// A hard-link set occupies its blocks once (pkgbuild's
		// installKBytes for the links fixture counts each inode once).
		if e.linkKey != 0 {
			if seen[e.linkKey] {
				continue
			}
			seen[e.linkKey] = true
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
	// linkKey groups the members of a hard-link set (0: not linked);
	// nlink is the host's link count.
	linkKey uint64
	nlink   uint32
	// xattrs is what the entry's sidecar will carry, if anything.
	xattrs *appledouble.File
	// sidecar is set on the synthesised "._" entries: the encoded
	// AppleDouble bytes; owner is the index of the entry they belong to.
	sidecar []byte
	owner   int
}

func (e *payloadEntry) isDir() bool { return e.mode&cpio.ModeTypeMask == cpio.ModeDir }

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
	if o.Compression.Encoding() == PayloadPBZX {
		if o.MinOSVersion == "" {
			o.MinOSVersion = pbzxMinimumOS
		} else if versionLess(o.MinOSVersion, pbzxMinimumOS) {
			return nil, fmt.Errorf("%w (got %s)", ErrCompressionNeedsMinOS, o.MinOSVersion)
		}
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
		if err := writePayloadAndBom(entries, payloadPath, bomPath, o.Compression, o.PBZXBlockSize, o.Progress); err != nil {
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
		if err := writeScripts(o.Scripts, scriptsPath, o, epoch); err != nil {
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
	linkKeys := &linkKeySet{}
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
		if dev, ino, nlink, ok := fileIdentity(fi); ok {
			e.nlink = nlink
			if o.HardLinks == HardLinksAuto && !fi.IsDir() && nlink > 1 {
				e.linkKey = linkKeys.key(dev, ino)
			}
		}
		if o.Xattrs == XattrsFromFS {
			attrs, err := hostXattrs(p)
			if err != nil {
				return fmt.Errorf("%s: reading extended attributes: %w", relSlash, err)
			}
			e.xattrs = filterXattrs(attrs, o.ExcludeXattr)
		}
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
	entries, err = liftSidecarFiles(entries, childCount, o.ExcludeXattr)
	if err != nil {
		return nil, err
	}
	for rel, attrs := range o.ExtraXattrs {
		i := indexOf(entries, rel)
		if i < 0 {
			return nil, fmt.Errorf("flatpkg: xattrs for %s: no such payload entry", rel)
		}
		entries[i].xattrs = mergeXattrs(entries[i].xattrs, appledouble.FromXattrs(attrs), o.ExcludeXattr)
	}
	for i := range entries {
		if entries[i].isDir() {
			entries[i].children = childCount[entries[i].rel]
			// Apple's Bom records the directory's size as APFS reports it:
			// 32 bytes per entry plus two. Sidecars are not counted.
			entries[i].size = int64(32 * (entries[i].children + 2))
		}
		if entries[i].xattrs != nil && entries[i].xattrs.Empty() {
			entries[i].xattrs = nil
		}
	}
	// Hard-link sets: the link count written is the number of members
	// packaged, so a reader waiting for the last link is never left
	// waiting for one outside the tree.
	members := map[uint64]uint32{}
	for _, e := range entries {
		if e.linkKey != 0 {
			members[e.linkKey]++
		}
	}
	for i := range entries {
		if k := entries[i].linkKey; k != 0 {
			if members[k] < 2 {
				entries[i].linkKey = 0
			} else {
				entries[i].nlink = members[k]
			}
		}
	}
	return withSidecars(entries)
}

// linkKeySet maps host (device, inode) pairs to small dense keys. One
// set belongs to one build: the keys are indexes into that build's
// entries, and sharing a set between builds would race.
type linkKeySet struct{ m map[[2]uint64]uint64 }

func (s *linkKeySet) key(dev, ino uint64) uint64 {
	if s.m == nil {
		s.m = map[[2]uint64]uint64{}
	}
	k, ok := s.m[[2]uint64{dev, ino}]
	if !ok {
		k = uint64(len(s.m) + 1)
		s.m[[2]uint64{dev, ino}] = k
	}
	return k
}

func indexOf(entries []payloadEntry, rel string) int {
	for i := range entries {
		if entries[i].rel == rel {
			return i
		}
	}
	return -1
}

// filterXattrs turns host attributes into sidecar content, dropping the
// excluded names; nil when nothing remains.
func filterXattrs(attrs map[string][]byte, exclude func(string) bool) *appledouble.File {
	if len(attrs) == 0 {
		return nil
	}
	kept := make(map[string][]byte, len(attrs))
	for name, value := range attrs {
		if exclude != nil && exclude(name) {
			continue
		}
		kept[name] = value
	}
	if len(kept) == 0 {
		return nil
	}
	return appledouble.FromXattrs(kept)
}

// mergeXattrs overlays extra on base; extra wins on a name clash.
func mergeXattrs(base, extra *appledouble.File, exclude func(string) bool) *appledouble.File {
	merged := map[string][]byte{}
	if base != nil {
		for k, v := range base.Xattrs() {
			merged[k] = v
		}
	}
	if extra != nil {
		for k, v := range extra.Xattrs() {
			merged[k] = v
		}
	}
	return filterXattrs(merged, exclude)
}

// liftSidecarFiles takes "._name" files that sit beside their owner and
// decode as AppleDouble out of the tree and into the owner's attributes,
// so a tree exported from macOS to a host without extended attributes
// packages the same way. A "._" file with no owner, or that is not
// AppleDouble, stays an ordinary file. exclude prunes the lifted names
// as it prunes the ones read from the host, so the two hosts agree.
func liftSidecarFiles(entries []payloadEntry, childCount map[string]int, exclude func(string) bool) ([]payloadEntry, error) {
	index := map[string]int{}
	for i, e := range entries {
		index[e.rel] = i
	}
	drop := map[int]bool{}
	for i, e := range entries {
		if e.mode&cpio.ModeTypeMask != cpio.ModeRegular || !appledouble.IsSidecarName(e.rel) {
			continue
		}
		owner, _ := appledouble.OwnerName(e.rel)
		oi, ok := index[owner]
		if !ok || e.size > appledouble.MaxHeader*16 {
			continue
		}
		raw, err := os.ReadFile(e.src)
		if err != nil {
			return nil, err
		}
		f, err := appledouble.Decode(raw)
		if err != nil {
			continue
		}
		entries[oi].xattrs = mergeXattrs(entries[oi].xattrs, f, exclude)
		drop[i] = true
		childCount[parentPath(e.rel)]--
	}
	if len(drop) == 0 {
		return entries, nil
	}
	kept := entries[:0]
	for i, e := range entries {
		if !drop[i] {
			kept = append(kept, e)
		}
	}
	return kept, nil
}

// withSidecars inserts the "._" entries where pkgbuild puts them: a
// file's right after the file, a directory's after its whole subtree,
// none for the root.
func withSidecars(entries []payloadEntry) ([]payloadEntry, error) {
	any := false
	for _, e := range entries {
		if e.xattrs != nil && e.rel != "." {
			any = true
			break
		}
	}
	if !any {
		return entries, nil
	}
	out := make([]payloadEntry, 0, len(entries)*2)
	// Directories with a sidecar pending, outermost first, as indexes
	// into out (owner indexes refer to the expanded list).
	var open []int
	flush := func(upTo string) error {
		for len(open) > 0 {
			at := open[len(open)-1]
			d := out[at]
			if upTo != "" && strings.HasPrefix(upTo, d.rel+"/") {
				return nil
			}
			se, err := sidecarEntry(d, at)
			if err != nil {
				return err
			}
			out = append(out, se)
			open = open[:len(open)-1]
		}
		return nil
	}
	for _, e := range entries {
		if err := flush(e.rel); err != nil {
			return nil, err
		}
		out = append(out, e)
		if e.xattrs == nil || e.rel == "." {
			continue
		}
		at := len(out) - 1
		if e.isDir() {
			open = append(open, at)
			continue
		}
		se, err := sidecarEntry(e, at)
		if err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	if err := flush(""); err != nil {
		return nil, err
	}
	return out, nil
}

// sidecarEntry synthesises the "._" entry for an owner, with pkgbuild's
// header: mode 100644, the owner's owner, time and link count. Encode
// fails when the attributes do not fit AppleDouble's 64 KiB header,
// which a tree on disk or a manifest can both ask for.
func sidecarEntry(owner payloadEntry, index int) (payloadEntry, error) {
	raw, err := owner.xattrs.Encode()
	if err != nil {
		return payloadEntry{}, fmt.Errorf("flatpkg: %s: %w", owner.rel, err)
	}
	return payloadEntry{
		rel:     appledouble.SidecarName(owner.rel),
		mode:    cpio.ModeRegular | 0o644,
		uid:     owner.uid,
		gid:     owner.gid,
		mtime:   owner.mtime,
		size:    int64(len(raw)),
		nlink:   owner.nlink,
		sidecar: raw,
		owner:   index,
	}, nil
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

// writePayloadAndBom streams the entries into an odc cpio inside the
// chosen container and builds the bill of materials alongside.
func writePayloadAndBom(entries []payloadEntry, payloadPath, bomPath string, compression Compression, blockSize uint64, progress func(string)) error {
	pf, err := os.Create(payloadPath)
	if err != nil {
		return err
	}
	defer pf.Close()
	var container io.WriteCloser
	if compression.Encoding() == PayloadPBZX {
		container, err = pbzx.NewWriter(pf, pbzx.XZ, blockSize)
	} else {
		container, err = gzip.NewWriterLevel(pf, gzip.DefaultCompression)
	}
	if err != nil {
		return err
	}
	cw := cpio.NewWriter(container)
	b := bom.NewBuilder()
	if err := writeEntries(cw, b, entries, progress); err != nil {
		return err
	}
	if err := cw.Close(); err != nil {
		return err
	}
	if err := container.Close(); err != nil {
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

// writeEntries streams entries into a cpio and, when b is set, records
// them in the bill of materials. Inode numbers are assigned in order;
// the members of a hard-link set share one, and so does the sidecar of a
// hard-linked file (pkgbuild's layout).
func writeEntries(cw *cpio.Writer, b *bom.Builder, entries []payloadEntry, progress func(string)) error {
	nextIno := uint64(1)
	setIno := map[uint64]uint64{}
	inos := make([]uint64, len(entries))
	for i, e := range entries {
		switch {
		case e.sidecar != nil:
			owner := entries[e.owner]
			if owner.linkKey != 0 {
				inos[i] = inos[e.owner]
				continue
			}
		case e.linkKey != 0:
			if ino, ok := setIno[e.linkKey]; ok {
				inos[i] = ino
				continue
			}
			setIno[e.linkKey] = nextIno
		}
		inos[i] = nextIno
		nextIno++
	}
	for i, e := range entries {
		hdr := &cpio.Header{
			Name:    e.rel,
			Inode:   inos[i],
			Mode:    e.mode,
			UID:     e.uid,
			GID:     e.gid,
			NLink:   1,
			ModTime: e.mtime,
			Size:    e.size,
		}
		switch {
		case e.isDir():
			hdr.Size = 0
			hdr.NLink = uint32(e.children + 2)
		case e.sidecar != nil:
			owner := entries[e.owner]
			if owner.isDir() {
				hdr.NLink = uint32(owner.children + 2)
			} else if owner.linkKey != 0 {
				hdr.NLink = owner.nlink
			}
		case e.linkKey != 0:
			hdr.NLink = e.nlink
		}
		if err := cw.WriteHeader(hdr); err != nil {
			return err
		}
		if e.sidecar != nil {
			if _, err := cw.Write(e.sidecar); err != nil {
				return err
			}
			if b != nil {
				owner := entries[e.owner]
				be := bom.Entry{
					Path:         e.rel,
					Type:         bom.TypeFile,
					Sidecar:      true,
					Architecture: 1,
					Mode:         uint16(owner.mode),
					HardLinkKey:  owner.linkKey,
				}
				if err := b.Add(be); err != nil {
					return err
				}
			}
			if progress != nil {
				progress(e.rel)
			}
			continue
		}
		be := bom.Entry{
			Path:         e.rel,
			Architecture: 15,
			Mode:         uint16(e.mode),
			UID:          e.uid,
			GID:          e.gid,
			ModTime:      e.mtime,
			Size:         e.size,
			HardLinkKey:  e.linkKey,
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
		if b != nil {
			if err := b.Add(be); err != nil {
				return err
			}
		}
		if progress != nil {
			progress(e.rel)
		}
	}
	return nil
}

// versionLess reports whether dotted version a is older than b.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
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
// Extended attributes travel as sidecars, as in the payload.
func writeScripts(dir, dst string, o ComponentOptions, epoch time.Time) error {
	so := ComponentOptions{
		Root:         dir,
		Ownership:    OwnershipRecommended,
		Xattrs:       o.Xattrs,
		ExcludeXattr: o.ExcludeXattr,
		HardLinks:    HardLinksCopy,
		FileModes:    map[string]uint32{},
	}
	entries, err := collectPayload(so, epoch)
	if err != nil {
		return fmt.Errorf("flatpkg: scripts: %w", err)
	}
	for i := range entries {
		e := &entries[i]
		switch {
		case e.sidecar != nil:
		case e.isDir():
			e.mode = cpio.ModeDir | 0o755
		case e.mode&cpio.ModeTypeMask == cpio.ModeRegular:
			e.mode = cpio.ModeRegular | 0o755
		default:
			return fmt.Errorf("flatpkg: scripts: %s is not a regular file", e.rel)
		}
		e.uid, e.gid = 0, 0
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	cw := cpio.NewWriter(gz)
	if err := writeEntries(cw, nil, entries, nil); err != nil {
		return err
	}
	if err := cw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Close()
}
