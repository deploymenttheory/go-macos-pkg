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
	"path"
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

// LargePayloadMinOS is the macOS major version a large payload needs. The
// format is not readable before macOS 12, and pkgbuild refuses to build one
// without being told so explicitly.
const LargePayloadMinOS = 12

// DefaultFilters are the paths pkgbuild leaves out of a payload when it is
// given no --filter of its own: any path component named exactly .svn, CVS
// or .DS_Store, whether it is a file or a directory. A directory that
// matches takes its whole subtree with it.
//
// The expressions are matched against the "./path" form, which is what
// pkgbuild matches: "^keep" and "^/.*keep" both miss where "^\./keep" and
// "/keep$" hit. The names are matched exactly, so CVSdir, notCVS.txt and
// .DS_Store_dir are all kept.
//
// Applying these is the caller's decision, not BuildComponent's: a library
// caller that sets no Exclude gets no filtering, while the command line
// applies them exactly as pkgbuild does.
var DefaultFilters = []string{`/\.svn$`, `/CVS$`, `/\.DS_Store$`}

// DefaultScriptTimeout is the timeout, in seconds, that current pkgbuild
// writes on every script it records in a PackageInfo. Versions before
// InstallCmds-860 wrote no timeout attribute at all; the fixtures record
// both shapes, and we write what the current tool writes.
const DefaultScriptTimeout = "600"

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
	// CompressionLZFSE and CompressionLZBitmap are the pbze and pbzb
	// containers. pkgbuild writes neither, but macOS reads both:
	// pkgutil --expand-full unpacks such a payload byte for byte,
	// single-chunk and multi-chunk alike, and installer installs it.
	// The acceptance suite pins that.
	CompressionLZFSE
	CompressionLZBitmap
	// CompressionNone stores the cpio with no container at all, which is
	// what productbuild --component-compression none writes. It suits a
	// payload that is already compressed, where a second pass buys little
	// and costs installation time.
	CompressionNone
)

// ParseCompression parses gzip, none, pbzx, latest, lzfse or lzbitmap.
//
// The pbz* family also has pbz4 (Apple-framed LZ4) and pbzz (zlib), and
// pkg/pbzx writes both: Apple's own aa reads what we produce. They are
// refused here because macOS will not read them in a package. pkgutil
// --expand-full fails with "cpio read error: bad file format" on a pbz4
// or pbzz Payload, so a package built with one could not be installed.
func ParseCompression(s string) (Compression, error) {
	switch strings.ToLower(s) {
	case "gzip", "legacy", "":
		return CompressionGzip, nil
	case "pbzx", "xz":
		return CompressionPBZX, nil
	case "latest":
		return CompressionLatest, nil
	case "lzfse", "pbze":
		return CompressionLZFSE, nil
	case "lzbitmap", "pbzb":
		return CompressionLZBitmap, nil
	case "none":
		return CompressionNone, nil
	case "lz4", "pbz4", "zlib", "pbzz":
		return 0, fmt.Errorf("compression %q writes a payload macOS cannot read: pkgutil refuses a pbz4 or pbzz Payload, so the package would not install (pkg/pbzx writes the container itself, if you need one outside a package)", s)
	}
	return 0, fmt.Errorf("unknown compression %q: want gzip, pbzx, latest, lzfse or lzbitmap", s)
}

func (c Compression) String() string {
	switch c {
	case CompressionPBZX, CompressionLatest:
		return "pbzx"
	case CompressionLZFSE:
		return "lzfse"
	case CompressionLZBitmap:
		return "lzbitmap"
	case CompressionNone:
		return "none"
	}
	return "gzip"
}

// Algorithm is the pbz* container algorithm the compression selects, and
// false for gzip, which is not a pbz container at all.
func (c Compression) Algorithm() (pbzx.Algorithm, bool) {
	switch c {
	case CompressionPBZX, CompressionLatest:
		return pbzx.XZ, true
	case CompressionLZFSE:
		return pbzx.LZFSE, true
	case CompressionLZBitmap:
		return pbzx.LZBitmap, true
	}
	return 0, false
}

// Encoding is the payload encoding the compression produces.
func (c Compression) Encoding() PayloadEncoding {
	if a, ok := c.Algorithm(); ok {
		return pbzEncoding(a)
	}
	if c == CompressionNone {
		return PayloadCPIO
	}
	return PayloadGzip
}

// XattrSource says where a build takes extended attributes from.
type XattrSource int

// Attribute sources.
const (
	// XattrsFromFS reads the attributes the host file system reports
	// (all of them on macOS, user.* on Linux, none on Windows), plus
	// XattrOverrides and any "._" sidecars already in the source tree.
	XattrsFromFS XattrSource = iota
	// XattrsNone carries no attributes, whatever the host reports;
	// XattrOverrides and sidecar files in the tree still apply.
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

// XattrOverride sets extended attributes on the payload paths it names,
// overriding what the tree and its "._" sidecars carry. Attributes are
// reapplied by default (unpacking and packing again reproduces the
// original package), and an override is how a path departs from that.
//
// A rule's own values are not subject to ExcludeXattr: naming a path and
// a value is more specific than filtering a name across the whole tree.
type XattrOverride struct {
	// Path is a payload path: "./usr/local/bin/tool", or "usr/local/bin"
	// and "/usr/local/bin", which mean the same. A path ending in "/"
	// matches that directory and everything beneath it; "./" is the whole
	// tree. It is an error for a rule to match nothing, so a typo does
	// not pass silently.
	Path string
	// Xattrs are the values to set, name → value.
	Xattrs map[string][]byte
	// Replace makes Xattrs the complete set for the paths matched,
	// discarding whatever they carried; with no Xattrs it removes them
	// all. Without it the values are merged over what is there, and a
	// name given here wins.
	Replace bool
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

	Identifier string
	Version    string
	// InstallLocation is written as the install-location attribute. Empty
	// leaves the attribute out, as pkgbuild does, which the Installer
	// reads as "/".
	InstallLocation string
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
	// KeepSidecarFiles carries "._" AppleDouble files through as the
	// files they are, rather than folding them into their owner's
	// extended attributes and deriving them again on the way out.
	//
	// A build wants the folding: it is what lets a tree exported from
	// macOS to a host with no extended attributes package the same way.
	// Flatten wants the opposite. Its contract is that the contents go
	// back as they stand, and folding then re-deriving gives a different
	// answer on a host that cannot store Apple's attribute names, which
	// would make a flattened package depend on where it was flattened.
	KeepSidecarFiles bool
	// ScriptTimeout is the timeout attribute written on every top-level
	// script, in seconds. Empty means DefaultScriptTimeout, which is what
	// current pkgbuild writes.
	ScriptTimeout string
	// ComponentPlist carries pkgbuild's --component-plist rules. When it
	// is set it is exhaustive: only the bundles it names, and their
	// children, are described. Empty means every bundle found takes the
	// defaults.
	ComponentPlist []ComponentBundle
	// LargePayload names the payload entry LargeSegmentedPayload rather
	// than Payload, as pkgbuild --large-payload does, and marks it in the
	// PackageInfo. Only macOS 12 and later reads such a package, so
	// MinOSVersion must be 12.0 or later, which is the precondition
	// pkgbuild enforces too.
	LargePayload bool
	// Components are bundle paths to package in place of a Root, as
	// pkgbuild --component does. They must share one directory, which
	// becomes the root. With exactly one, the identifier, version and
	// install location are inferred from it when they are not given.
	Components []string

	// Xattrs selects where extended attributes come from. They are
	// carried the way pkgbuild carries them: as AppleDouble "._" sidecar
	// entries beside their owners, in the payload and in Scripts.
	Xattrs XattrSource
	// ExcludeXattr, when set, drops attributes by name (for example
	// com.apple.provenance or com.apple.quarantine, which describe the
	// build host rather than the file).
	ExcludeXattr func(name string) bool
	// XattrOverrides set attributes on the paths they name, in order,
	// after the tree and its "._" sidecars have been read. A manifest can
	// give a Linux or Windows build the attributes a macOS build would
	// read from disk, or change what an unpacked tree carries before it
	// is packed again.
	XattrOverrides []XattrOverride
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

// componentPayloadCounts is what pkgbuild reports for a --component build,
// which is not what it reports for a --root build of the same payload.
//
// A --root build counts every archived entry and sizes the directories the
// way the bill of materials does. A --component build describes the bundle
// instead of the archive: it counts the entries other than the root and the
// AppleDouble sidecars, and adds up the bytes of the regular files alone,
// with no directory overhead. The same eleven-entry payload comes out as
// 11 files and 3 KB one way and 5 files and 0 KB the other, and 17 entries
// come out as 17 and 308 against 8 and 305.
func componentPayloadCounts(entries []payloadEntry) (files, kbytes int) {
	var bytes int64
	seen := map[uint64]bool{}
	for _, e := range entries {
		if e.rel == "." || e.sidecar != nil || isAppleDoubleName(e.rel) {
			continue
		}
		files++
		if e.isDir() {
			continue
		}
		if e.linkKey != 0 {
			if seen[e.linkKey] {
				continue
			}
			seen[e.linkKey] = true
		}
		bytes += e.size
	}
	return files, int(bytes / 1024)
}

// isAppleDoubleName reports whether a path names a "._" sidecar file.
func isAppleDoubleName(rel string) bool {
	return strings.HasPrefix(path.Base(rel), "._")
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
	if len(o.Components) > 0 {
		if o.Root != "" {
			return nil, fmt.Errorf("flatpkg: a component build takes no payload root")
		}
		root, keep, err := resolveComponents(o.Components)
		if err != nil {
			return nil, err
		}
		o.Root = root
		// Keep only the named bundles, and still honour whatever the
		// caller was excluding.
		outer := o.Exclude
		o.Exclude = func(rel string) bool {
			if !keep(rel) {
				return true
			}
			return outer != nil && outer(rel)
		}
		if len(o.Components) == 1 {
			id, err := InferFromBundle(o.Components[0])
			if err != nil {
				return nil, err
			}
			if o.Identifier == "" {
				o.Identifier = id.Identifier
			}
			if o.Version == "" {
				o.Version = id.Version
			}
			if o.InstallLocation == "" {
				o.InstallLocation = id.InstallLocation
			}
		} else if o.Identifier == "" {
			return nil, fmt.Errorf("flatpkg: an identifier is required unless there is exactly one component to take it from")
		}
	}
	if o.Identifier == "" {
		return nil, fmt.Errorf("flatpkg: an identifier is required")
	}
	if o.Version == "" {
		return nil, fmt.Errorf("flatpkg: a version is required")
	}
	if o.Root == "" && !o.NoPayload {
		return nil, fmt.Errorf("flatpkg: a payload root is required (or NoPayload)")
	}
	if o.LargePayload {
		// pkgbuild's own precondition, and it matters: the format is not
		// readable before macOS 12, so a package that did not say so
		// would fail to install rather than fail to build.
		if !MinOSVersionAtLeast(o.MinOSVersion, LargePayloadMinOS) {
			return nil, fmt.Errorf("flatpkg: a large payload needs --min-os-version 12.0 or later; macOS 11 and earlier cannot read one")
		}
	}
	if o.Ownership != OwnershipRecommended && runtime.GOOS == "windows" {
		return nil, fmt.Errorf("%w: preserving ownership (Windows has no uid or gid)", ErrUnsupportedOnPlatform)
	}
	// InstallLocation is deliberately not defaulted. pkgbuild writes the
	// attribute only when it is told one, and the Installer treats an
	// absent install-location as "/", so filling it in would both diverge
	// from pkgbuild's document and rewrite a package that had none when it
	// is expanded and built again.
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
	if o.Compression.Encoding().IsPBZ() {
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
	// Filled in from the component property list while the bundles are
	// walked, and written into <scripts> below.
	var bundleScripts []bundleScript
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
		if len(o.Components) > 0 {
			res.NumberOfFiles, res.InstallKBytes = componentPayloadCounts(entries)
		}
		info.Payload = &Payload{NumberOfFiles: res.NumberOfFiles, InstallKBytes: res.InstallKBytes}
		if o.LargePayload {
			info.Payload.LargeSegmented = "true"
		}

		bundles, err := findBundles(o.Root)
		if err != nil {
			return nil, fmt.Errorf("flatpkg: scanning for bundles: %w", err)
		}
		// A component property list is exhaustive: pkgbuild records only
		// the bundles it names, and drops any others from the payload's
		// description entirely. Without one, every bundle found is
		// recorded under the defaults.
		bundles, rules := resolveBundleRules(bundles, o.ComponentPlist)
		res.Bundles = bundles
		for _, b := range bundles {
			// pkgbuild's layout: details once, at the top level, then id
			// references in bundle-version (version checking),
			// upgrade-bundle or update-bundle, strict-identifier and
			// relocate.
			info.Bundles = append(info.Bundles, b)
			r, ok := rules[b.Path]
			if !ok {
				// Nested: described, never referenced. Its behaviour is
				// the containing bundle's.
				continue
			}
			ref := BundleRef{ID: b.ID}
			if r.versionChecked {
				info.BundleVersion.Bundles = append(info.BundleVersion.Bundles, Bundle{ID: b.ID})
			}
			switch r.overwriteAction {
			case OverwriteUpgrade:
				info.UpgradeBundle.Bundles = append(info.UpgradeBundle.Bundles, ref)
			case OverwriteUpdate:
				info.UpdateBundle.Bundles = append(info.UpdateBundle.Bundles, ref)
			}
			if r.strictIdentifier {
				info.StrictIdentifier.Bundles = append(info.StrictIdentifier.Bundles, ref)
			}
			if r.relocatable && !o.NoBundleRelocation {
				info.Relocate.Bundles = append(info.Relocate.Bundles, ref)
			}
			bundleScripts = append(bundleScripts, scriptsForBundle(b, r)...)
		}
	}

	if o.Scripts != "" {
		names, err := scriptNames(o.Scripts)
		if err != nil {
			return nil, err
		}
		if len(names) == 0 && len(bundleScripts) == 0 {
			return nil, fmt.Errorf("flatpkg: %s contains no install scripts (preinstall, postinstall, ...)", o.Scripts)
		}
		scriptsPath = filepath.Join(tmp, "Scripts")
		if err := writeScripts(o.Scripts, scriptsPath, o, epoch); err != nil {
			return nil, err
		}
		info.Scripts = &Scripts{}
		// pkgbuild writes the bundle-specific scripts first, in bundle
		// order, then the package's own.
		for _, bs := range bundleScripts {
			s := Script{File: "./" + bs.file, ComponentID: bs.componentID, Timeout: bs.timeout}
			switch bs.kind {
			case "preinstall":
				info.Scripts.Preinstall = append(info.Scripts.Preinstall, s)
			case "postinstall":
				info.Scripts.Postinstall = append(info.Scripts.Postinstall, s)
			}
		}
		timeout := o.ScriptTimeout
		if timeout == "" {
			timeout = DefaultScriptTimeout
		}
		for _, n := range names {
			s := Script{File: "./" + n, Timeout: timeout}
			switch n {
			case "preinstall":
				info.Scripts.Preinstall = append(info.Scripts.Preinstall, s)
			case "postinstall":
				info.Scripts.Postinstall = append(info.Scripts.Postinstall, s)
			case "preflight":
				info.Scripts.Preflight = append(info.Scripts.Preflight, s)
			case "postflight":
				info.Scripts.Postflight = append(info.Scripts.Postflight, s)
			case "preupgrade":
				info.Scripts.Preupgrade = append(info.Scripts.Preupgrade, s)
			case "postupgrade":
				info.Scripts.Postupgrade = append(info.Scripts.Postupgrade, s)
			}
		}
		res.Scripts = info.Scripts.Names()
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
		payloadEntryName := EntryPayload
		if o.LargePayload {
			payloadEntryName = EntryLargePayload
		}
		if err := addFileEntry(w, payloadEntryName, hdr, xar.EncodingNone, payloadPath); err != nil {
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
	// sourceHadChild records the directories that held anything on disk,
	// filtered or not, which is what tells an emptied directory apart from
	// one that was already empty. See pruneEmptiedDirs.
	sourceHadChild := map[string]bool{}
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
		if relSlash != "." {
			sourceHadChild[parentPath(relSlash)] = true
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
	entries = pruneEmptiedDirs(entries, childCount, sourceHadChild)
	if !o.KeepSidecarFiles {
		entries, err = liftSidecarFiles(entries, childCount, o.ExcludeXattr)
		if err != nil {
			return nil, err
		}
	}
	for _, ov := range o.XattrOverrides {
		if err := applyXattrOverride(entries, ov); err != nil {
			return nil, err
		}
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

// overridePath turns a rule's path into a payload path and reports
// whether it names a folder: "usr/bin", "/usr/bin" and "./usr/bin" all
// become "./usr/bin", and a trailing slash survives.
func overridePath(p string) (rel string, folder bool) {
	folder = strings.HasSuffix(p, "/")
	rel = "."
	if clean := path.Clean("/" + strings.TrimSuffix(p, "/")); clean != "/" {
		rel = "." + clean
	}
	return rel, folder
}

// applyXattrOverride sets one rule's attributes on the entries it names.
func applyXattrOverride(entries []payloadEntry, ov XattrOverride) error {
	rel, folder := overridePath(ov.Path)
	matched := 0
	for i := range entries {
		e := &entries[i]
		switch {
		case folder:
			if e.rel != rel && !strings.HasPrefix(e.rel, strings.TrimSuffix(rel, "/")+"/") {
				continue
			}
		case e.rel != rel:
			continue
		}
		matched++
		set := appledouble.FromXattrs(ov.Xattrs)
		if ov.Replace {
			e.xattrs = set
		} else {
			e.xattrs = mergeXattrs(e.xattrs, set, nil)
		}
		if e.xattrs != nil && e.xattrs.Empty() {
			e.xattrs = nil
		}
	}
	if matched == 0 {
		return fmt.Errorf("flatpkg: xattrs for %s: no such payload entry", ov.Path)
	}
	return nil
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
// pruneEmptiedDirs drops the directories that held something in the source
// tree but hold nothing once the filters have run, which is what pkgbuild
// does: a directory that was already empty on disk is packaged, and one the
// filters emptied is not. The prune cascades, so a parent left with nothing
// goes too.
//
// childCount is the live count of surviving children, so it doubles as the
// test and is kept correct as directories are dropped. Walk order puts a
// parent before its children, so one backwards pass reaches the deepest
// directory first and the cascade needs no second sweep.
func pruneEmptiedDirs(entries []payloadEntry, childCount map[string]int, sourceHadChild map[string]bool) []payloadEntry {
	drop := map[string]bool{}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.rel == "." || !e.isDir() || !sourceHadChild[e.rel] || childCount[e.rel] > 0 {
			continue
		}
		drop[e.rel] = true
		delete(childCount, e.rel)
		childCount[parentPath(e.rel)]--
	}
	if len(drop) == 0 {
		return entries
	}
	kept := make([]payloadEntry, 0, len(entries)-len(drop))
	for _, e := range entries {
		if !drop[e.rel] {
			kept = append(kept, e)
		}
	}
	return kept
}

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

// nopWriteCloser lets an uncompressed payload share the path a compressed
// one takes, without the container closing the file underneath it.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// writePayloadAndBom streams the entries into an odc cpio inside the
// chosen container and builds the bill of materials alongside.
func writePayloadAndBom(entries []payloadEntry, payloadPath, bomPath string, compression Compression, blockSize uint64, progress func(string)) error {
	pf, err := os.Create(payloadPath)
	if err != nil {
		return err
	}
	defer pf.Close()
	var container io.WriteCloser
	switch algo, ok := compression.Algorithm(); {
	case ok:
		container, err = pbzx.NewWriter(pf, algo, blockSize)
	case compression == CompressionNone:
		// The cpio is the payload. Closing it must not close the file,
		// which this function closes itself.
		container = nopWriteCloser{pf}
	default:
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
	return writeArchivedDir(dir, dst, o, epoch, true)
}

// writeArchivedDir packs a directory into the gzip cpio that a xar entry
// like Scripts or PlugIns carries.
//
// forceExecutable is what tells a component's scripts from a product's
// auxiliary directories. pkgbuild makes every file in a component's Scripts
// executable, because the Installer runs them; productbuild leaves the modes
// alone in a product's Scripts and PlugIns, where a directory holds data and
// bundles as well as anything runnable.
func writeArchivedDir(dir, dst string, o ComponentOptions, epoch time.Time, forceExecutable bool) error {
	so := ComponentOptions{
		Root:             dir,
		Ownership:        OwnershipRecommended,
		Xattrs:           o.Xattrs,
		ExcludeXattr:     o.ExcludeXattr,
		KeepSidecarFiles: o.KeepSidecarFiles,
		HardLinks:        HardLinksCopy,
		FileModes:        map[string]uint32{},
	}
	entries, err := collectPayload(so, epoch)
	if err != nil {
		return fmt.Errorf("flatpkg: scripts: %w", err)
	}
	for i := range entries {
		e := &entries[i]
		switch {
		// A sidecar, whether it was folded into its owner or is being
		// carried as the file it is, keeps the mode pkgbuild gives it.
		// Only the scripts themselves are made executable.
		case e.sidecar != nil || isAppleDoubleName(e.rel):
		case e.isDir():
			e.mode = cpio.ModeDir | 0o755
		case e.mode&cpio.ModeTypeMask == cpio.ModeRegular:
			if forceExecutable {
				e.mode = cpio.ModeRegular | 0o755
			}
		default:
			return fmt.Errorf("flatpkg: %s: %s is not a regular file", filepath.Base(dst), e.rel)
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
