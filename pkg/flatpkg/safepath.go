// Turning archive entry names into paths it is safe to write under a
// directory the user chose.
package flatpkg

import (
	"path"
	"runtime"
	"strings"
)

// windowsReserved are the device names Windows refuses as file names, in
// any case and with any extension.
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SafeRelPath converts an archive entry name to a clean, relative,
// slash-separated path with no traversal, or reports why it cannot. The
// second result is a renamed form for the host (Windows cannot represent
// every macOS file name), empty when no renaming was needed.
//
// Names refused outright: absolute paths, paths that climb above the root,
// and names with NUL bytes. Everything else is written, possibly renamed.
func SafeRelPath(name string) (rel string, renamedFrom string, reason string) {
	if strings.ContainsRune(name, 0) {
		return "", "", "name contains a NUL byte"
	}
	// Payload entries are "./a/b"; archive entries "a/b". Normalise.
	n := strings.ReplaceAll(name, "\\", "/")
	n = strings.TrimPrefix(n, "./")
	if strings.HasPrefix(n, "/") {
		return "", "", "absolute path"
	}
	clean := path.Clean(n)
	if clean == "." || clean == "" {
		return ".", "", ""
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", "path escapes the destination directory"
	}
	if runtime.GOOS != "windows" {
		return clean, "", ""
	}
	// Windows: strip characters and names it cannot store.
	parts := strings.Split(clean, "/")
	changed := false
	for i, part := range parts {
		fixed := sanitizeWindowsName(part)
		if fixed != part {
			changed = true
			parts[i] = fixed
		}
	}
	if changed {
		return strings.Join(parts, "/"), clean, ""
	}
	return clean, "", ""
}

// sanitizeWindowsName replaces what NTFS refuses: the characters
// <>:"|?* and control characters, trailing dots and spaces, and the
// reserved device names.
func sanitizeWindowsName(part string) string {
	var b strings.Builder
	for _, r := range part {
		switch {
		case r < 0x20, r == '<', r == '>', r == ':', r == '"', r == '|', r == '?', r == '*':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimRight(b.String(), ". ")
	if out == "" {
		out = "_"
	}
	base := out
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if windowsReserved[strings.ToUpper(base)] {
		out = "_" + out
	}
	return out
}
