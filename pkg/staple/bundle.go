// Stapling application bundles.
//
// A .app is not an archive with a trailer; its ticket is a file. stapler
// writes the same s8ch ticket a package carries to Contents/CodeResources
// (not _CodeSignature/CodeResources, which is the code signature's resource
// list and an unrelated file that shares a name). The ticket is keyed on the
// main executable's CDHash rather than a package's table-of-contents digest,
// so the lookup differs; attaching is a plain, atomic file write.
package staple

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
	"howett.net/plist"
)

// IsAppBundle reports whether path is an application bundle: a directory
// with a Contents/Info.plist. It is how the CLI tells a bundle to staple
// from a flat package to staple.
func IsAppBundle(path string) bool {
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return false
	}
	fi, err := os.Stat(filepath.Join(path, "Contents", "Info.plist"))
	return err == nil && fi.Mode().IsRegular()
}

// BundleExecutable returns the path to a bundle's main executable, named by
// CFBundleExecutable in Contents/Info.plist and living in Contents/MacOS.
func BundleExecutable(bundle string) (string, error) {
	f, err := os.Open(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		return "", fmt.Errorf("staple: reading Info.plist: %w", err)
	}
	defer f.Close()
	var info struct {
		Executable string `plist:"CFBundleExecutable"`
	}
	// howett.net/plist decodes both the binary and the XML forms.
	if err := plist.NewDecoder(f).Decode(&info); err != nil {
		return "", fmt.Errorf("staple: parsing Info.plist: %w", err)
	}
	if info.Executable == "" {
		return "", fmt.Errorf("staple: Info.plist names no CFBundleExecutable")
	}
	return filepath.Join(bundle, "Contents", "MacOS", info.Executable), nil
}

// AppRecordNames returns the CloudKit ticket record names to try for a
// bundle: one per architecture's SHA-256 CDHash. A universal binary has one
// CDHash per architecture; Apple's ticket covers all of them and its
// database answers to any, so the caller tries each until one resolves.
func AppRecordNames(bundle string) ([]string, error) {
	exe, err := BundleExecutable(bundle)
	if err != nil {
		return nil, err
	}
	hashes, err := CDHashes(exe)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var names []string
	for _, h := range hashes {
		name, err := RecordName(xar.ChecksumSHA256, h)
		if err != nil {
			return nil, err
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}

// appTicketPath is where a bundle's stapled ticket lives.
func appTicketPath(bundle string) string {
	return filepath.Join(bundle, "Contents", "CodeResources")
}

// AppHasTicket reports whether a bundle carries a stapled ticket: a
// Contents/CodeResources that begins with the s8ch ticket magic.
func AppHasTicket(bundle string) (bool, error) {
	f, err := os.Open(appTicketPath(bundle))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	head := make([]byte, len(ticketMagic))
	n, _ := f.Read(head)
	return bytes.Equal(head[:n], ticketMagic), nil
}

// StapleApp writes ticket into the bundle at Contents/CodeResources,
// replacing any ticket already there. The write is atomic: a temp file in
// the same directory, then a rename, so a reader never sees a half-written
// ticket.
func StapleApp(bundle string, ticket []byte) error {
	if !bytes.HasPrefix(ticket, ticketMagic) {
		return fmt.Errorf("staple: the ticket does not begin with %q", ticketMagic)
	}
	dst := appTicketPath(bundle)
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".CodeResources-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(ticket)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmpName)
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// UnstapleApp removes a bundle's stapled ticket. A bundle with none, or
// whose Contents/CodeResources is not a ticket, is left untouched.
func UnstapleApp(bundle string) error {
	has, err := AppHasTicket(bundle)
	if err != nil {
		return err
	}
	if !has {
		return nil
	}
	if err := os.Remove(appTicketPath(bundle)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
