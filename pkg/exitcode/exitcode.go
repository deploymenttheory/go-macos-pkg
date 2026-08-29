// Package exitcode defines the exit-code contract of the macospkg command.
//
// These values are stable: scripts, pipelines and the acceptance suite depend
// on them, so a code's meaning must not change once released. Add new codes
// rather than repurposing existing ones. Codes 0-6 carry the same meaning as
// in the sister apfs command so the two tools compose in one pipeline.
package exitcode

// Exit codes returned by the macospkg command.
const (
	// OK indicates the operation completed successfully.
	OK = 0
	// Error indicates a generic runtime error.
	Error = 1
	// Usage indicates bad flags or arguments.
	Usage = 2
	// BadPackage indicates the package was not found, is not a xar archive,
	// or is a xar archive that is not a flat installer package.
	BadPackage = 3
	// Auth indicates credentials were required and were missing or wrong: a
	// PKCS#12 password, a key that does not match its certificate, or notary
	// credentials Apple rejected.
	Auth = 4
	// Unsupported indicates the requested feature is not supported on this
	// platform or for this package, for example an Apple Archive payload or
	// preserving ownership on Windows.
	Unsupported = 5
	// Partial indicates the operation completed partially, for example an
	// extraction in which some entries were skipped.
	Partial = 6
	// Signature indicates a signature or ticket check failed: the package is
	// unsigned, its digest does not match, the chain is not trusted, or no
	// notarization ticket exists for it.
	Signature = 7
	// NotaryRejected indicates Apple's notary service returned Invalid or
	// Rejected for the submission.
	NotaryRejected = 8
	// Timeout indicates a wait deadline passed while the submission was still
	// in progress.
	Timeout = 9
)

// Name returns a short human-readable name for an exit code, for use in test
// failures and diagnostics. Unknown codes are rendered as "Unknown".
func Name(code int) string {
	switch code {
	case OK:
		return "OK"
	case Error:
		return "Error"
	case Usage:
		return "Usage"
	case BadPackage:
		return "BadPackage"
	case Auth:
		return "Auth"
	case Unsupported:
		return "Unsupported"
	case Partial:
		return "Partial"
	case Signature:
		return "Signature"
	case NotaryRejected:
		return "NotaryRejected"
	case Timeout:
		return "Timeout"
	default:
		return "Unknown"
	}
}
