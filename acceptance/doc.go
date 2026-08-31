// Package acceptance holds the acceptance tests for this repository. It has no
// production code in it, only tests.
//
// A test belongs here if something outside our own code decides whether it
// passed: either the real macospkg command, run as a subprocess, or a tool
// that ships with the operating system. A test that only checks our code
// against itself is a unit test and stays in the package it tests.
//
// That distinction matters most for the packages we write. If we build a
// package and then read it back with our own reader, both halves can share
// the same misunderstanding of the format and the test still passes. Handing
// the package to Apple's pkgutil, lsbom, xar and installer removes that blind
// spot, and it is the only way to know a package built on Linux or Windows
// actually installs on a Mac.
//
// # The three kinds of test here
//
// Command tests build ./cmd/macospkg and run it against the small packages
// committed in testdata/cli, checking what it prints, what it extracts and
// what exit code it returns. Those fixtures were produced once, on macOS, by
// Apple's own pkgbuild, productbuild and productsign (see
// scripts/gen-fixtures.sh), so on Linux and Windows the suite is still
// checking our reader against Apple's bytes. They compare file contents
// against the checksums in testdata/cli/manifest.json. Nothing in these tests
// imports internal/cli, so they exercise the command the way a shell script
// would. They need nothing installed:
//
//	go test ./acceptance/
//
// Pipeline tests build packages with macospkg itself from a deterministic
// source tree generated in the test, then sign, verify, expand and extract
// them, again with only the macospkg binary. These run everywhere and are
// deliberately not skippable: they are the proof the tool works end to end
// where Apple's tools do not exist.
//
// reference tests hand what we built to the tools that come with macOS: pkgutil,
// lsbom, xar, installer, stapler and spctl. Each one skips if its tool is
// missing, so passing these on a machine without them proves nothing: the
// macOS CI runner is where they count. They never run in production code:
// the binary itself never calls an Apple tool.
//
// # Environment
//
//	MACOSPKG_ACCEPTANCE_PKG      path to a real, Apple-signed and stapled .pkg
//	                             for the signature, staple and round-trip
//	                             checks; unset skips
//	MACOSPKG_ACCEPTANCE_INSTALL  set to run the installer references outside CI;
//	                             they need passwordless sudo and run a
//	                             package's scripts as root
//
// # Recorded results
//
// Tests that observe something worth knowing (file counts, checksums,
// what pkgutil reported) log it through attest, which writes to the test
// output and, in CI, to the GitHub Actions step summary. A passing run
// therefore states the numbers it observed rather than only that it
// passed.
package acceptance
