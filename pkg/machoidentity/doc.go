// Package machoidentity reads signing identifiers and code-directory hashes
// from Mach-O executables on any Go platform.
//
// # Design
//
// Read accepts a bounded random-access source and returns every architecture,
// preserving CPU and subtype values and every supported code directory. It
// handles thin and universal, 32-bit and 64-bit, little- and big-endian images.
// Unsupported hash algorithms and malformed signatures fail explicitly.
// Unsigned architectures have no code directories. Reads do not execute code.
//
// These are observations, not signature verification. Reading a TeamID does
// not establish certificate trust, code-page integrity, resource integrity,
// designated-requirement satisfaction or notarization. CDHash identifies the
// code directory, and does not verify the pages referenced by that directory.
// Callers choose the architecture and hash algorithm appropriate to their use.
//
// # References
//
//   - Apple code-signing hashes: https://developer.apple.com/documentation/technotes/tn3126-inside-code-signing-hashes
//   - Apple code-directory structures: https://github.com/apple-oss-distributions/Security/blob/main/OSX/libsecurity_codesigning/lib/codedirectory.h
package machoidentity
