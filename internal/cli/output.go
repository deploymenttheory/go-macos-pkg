// Output helpers: JSON encoding to stdout, TTY detection, byte formatting.
package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"os"

	"howett.net/plist"
)

// structured reports whether the output format is machine readable, which
// is what the many "print a report or print prose" branches ask.
func structured() bool { return opts.Output == "json" || opts.Output == "plist" }

// jsonOut writes one value to stdout in whichever machine-readable format
// was asked for.
//
// JSON is a line per value, so a listing streams and stays greppable. A
// property list cannot be: a plist file is one document, so a listing is
// collected and written as a single array when the command finishes. That
// is what plistOut and flushPlist are for.
func jsonOut(value any) error {
	if opts.Output == "plist" {
		plistPending = append(plistPending, value)
		return nil
	}
	encoder := json.NewEncoder(os.Stdout)
	return encoder.Encode(value)
}

// plistPending collects the values a command emitted, so they can be
// written as one document.
var plistPending []any

// flushPlist writes what a command emitted as a property list, and is
// called once when the command finishes.
//
// One value is written as itself; several are written as an array, which
// is the only shape a listing can take in a format that has no notion of a
// line per record.
func flushPlist() error {
	if opts.Output != "plist" || len(plistPending) == 0 {
		return nil
	}
	var value any = plistPending
	if len(plistPending) == 1 {
		value = plistPending[0]
	}
	plistPending = nil

	// Through JSON first. The report types carry json tags and nothing
	// else, so their field names, their omitempty rules and the JSON
	// schema this tool documents are all one thing. Encoding the structs
	// directly would use Go field names instead and would emit a key with
	// no value wherever a pointer was nil.
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var generic any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		return err
	}
	enc := plist.NewEncoderForFormat(os.Stdout, plist.XMLFormat)
	enc.Indent("\t")
	return enc.Encode(withoutNulls(generic))
}

// withoutNulls drops the nulls JSON allows and a property list has no way
// to express, and puts back the integers the trip through JSON turned into
// floats.
//
// Sizes, counts, modes and identifiers are whole numbers, and Apple's own
// property lists write them as integers. Decoding JSON makes every number
// a float64, so a whole one is turned back; anything genuinely fractional
// stays a real.
func withoutNulls(v any) any {
	switch t := v.(type) {
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1<<53 {
			return int64(t)
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if val == nil {
				continue
			}
			out[k] = withoutNulls(val)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, val := range t {
			if val == nil {
				continue
			}
			out = append(out, withoutNulls(val))
		}
		return out
	}
	return v
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
