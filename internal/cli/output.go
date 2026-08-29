// Output helpers: JSON encoding to stdout, TTY detection, byte formatting.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
)

// jsonOut writes one value as a JSON line to stdout.
func jsonOut(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	return encoder.Encode(value)
}

// verbosef writes a diagnostic line to stderr when --verbose is set.
func verbosef(format string, args ...any) {
	if opts.Verbose {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

// progressf writes a progress line to stderr unless --quiet is set.
func progressf(format string, args ...any) {
	if !opts.Quiet {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

// formatSize converts bytes to a human-readable string.
func formatSize(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	return fmt.Sprintf("%.1f %s", float64(bytes)/float64(div), units[exp])
}
