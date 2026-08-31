// Reading the receipt database: the record macOS keeps of what each
// installed package put on a volume.
//
// A receipt is two files in <volume>/var/db/receipts, named for the
// package: a property list of what was installed and when, and a bill of
// materials listing every path. The Installer writes them; nothing else
// should, which is why this package only reads.
//
// It reads a directory rather than asking the system, so it works against a
// volume mounted anywhere, from any operating system. That is also its
// limit. On a running macOS, "pkgutil --pkgs" reports more than this
// directory holds: the packages that make up the system itself are recorded
// in a sealed database that pkgutil reaches through a private interface,
// and no directory on disk lists them. What is here is what was installed
// onto the volume, which is the part anyone auditing a machine is asking
// about.
package receipts

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/bom"
	"howett.net/plist"
)

// DirName is where a volume keeps its receipts, relative to its root.
const DirName = "var/db/receipts"

// Receipt is one installed package's record, as the Installer wrote it.
type Receipt struct {
	// PackageIdentifier is the identifier the package was built with, and
	// the name both of its files carry.
	PackageIdentifier string `plist:"PackageIdentifier"`
	PackageVersion    string `plist:"PackageVersion"`
	// InstallPrefixPath is the install location, without a leading slash:
	// "usr/local" rather than "/usr/local". An empty string is the volume
	// root, which is what a package installing at "/" leaves behind.
	InstallPrefixPath string `plist:"InstallPrefixPath"`
	// InstallProcessName is what performed the install, usually
	// "installer" or "Installer".
	InstallProcessName string    `plist:"InstallProcessName"`
	InstallDate        time.Time `plist:"InstallDate"`
	// PackageFileName is the file the package arrived as, where the
	// Installer recorded one.
	PackageFileName string `plist:"PackageFileName"`

	// Volume is the volume the receipt was read from, "/" for the boot
	// volume. It is not stored in the property list; pkgutil reports it
	// and so do we.
	Volume string `plist:"-"`
}

// DB is a volume's receipt directory.
type DB struct {
	// Dir is the receipts directory itself.
	Dir string
	// Volume is what to report as the volume, "/" for the boot volume.
	Volume string
}

// Open finds the receipt directory on a volume. The volume is a mount
// point, or "/" for the running system.
func Open(volume string) (*DB, error) {
	if volume == "" {
		volume = "/"
	}
	dir := filepath.Join(volume, filepath.FromSlash(DirName))
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("receipts: %s holds no receipt database at %s: %w", volume, DirName, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("receipts: %s is not a directory", dir)
	}
	return &DB{Dir: dir, Volume: volume}, nil
}

// List returns the package identifiers on the volume, in order.
//
// An identifier is the name of a .plist in the directory. A .bom beside it
// is what Files reads, and a package with no payload leaves none, so the
// property list alone is what counts as a receipt.
func (db *DB) List() ([]string, error) {
	names, err := os.ReadDir(db.Dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if n.IsDir() || !strings.HasSuffix(n.Name(), ".plist") {
			continue
		}
		out = append(out, strings.TrimSuffix(n.Name(), ".plist"))
	}
	sort.Strings(out)
	return out, nil
}

// Match returns the identifiers matching a regular expression, or all of
// them where the expression is empty.
func (db *DB) Match(pattern string) ([]string, error) {
	all, err := db.List()
	if err != nil || pattern == "" {
		return all, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("receipts: %q: %w", pattern, err)
	}
	var out []string
	for _, id := range all {
		if re.MatchString(id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// Get reads one package's receipt.
func (db *DB) Get(id string) (*Receipt, error) {
	path, err := db.pathFor(id, ".plist")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("receipts: %s: %w", id, err)
	}
	var r Receipt
	if _, err := plist.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("receipts: %s: unable to parse the receipt: %w", id, err)
	}
	if r.PackageIdentifier == "" {
		// Older receipts leave it out and let the file name carry it.
		r.PackageIdentifier = id
	}
	r.Volume = db.Volume
	return &r, nil
}

// Files returns the bill of materials a package left behind, which is every
// path it installed.
//
// The paths are as the bill of materials records them, "./"-prefixed and
// relative to the install location. InstallLocation on the receipt says
// where that is.
func (db *DB) Files(id string) ([]bom.Entry, error) {
	path, err := db.pathFor(id, ".bom")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("receipts: %s: %w", id, err)
	}
	b, err := bom.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("receipts: %s: %w", id, err)
	}
	return b.Paths()
}

// HasFiles reports whether a package left a bill of materials. One that
// installed nothing, a scripts-only package say, leaves none.
func (db *DB) HasFiles(id string) bool {
	_, err := db.pathFor(id, ".bom")
	return err == nil
}

// pathFor resolves one of a package's two files, refusing an identifier
// that would reach outside the directory.
func (db *DB) pathFor(id, ext string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return "", fmt.Errorf("receipts: %q is not a package identifier", id)
	}
	path := filepath.Join(db.Dir, id+ext)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("receipts: no %s receipt for %s on %s", strings.TrimPrefix(ext, "."), id, db.Volume)
	}
	return path, nil
}
