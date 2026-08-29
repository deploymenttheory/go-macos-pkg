// Package cli provides the macospkg command-line interface. All cobra/viper
// wiring lives here; format handling lives in pkg/xar, pkg/bom, pkg/cpio,
// pkg/flatpkg, pkg/pkgsign, pkg/notary and pkg/staple.
//
// Configuration precedence: flag > MACOSPKG_* environment variable > config
// file (~/.config/macospkg/config.yaml, optional).
//
// SOURCE_DATE_EPOCH is the one deliberate exception, resolved as
// --source-date-epoch > SOURCE_DATE_EPOCH > MACOSPKG_SOURCE_DATE_EPOCH > config
// file. The bare, unprefixed variable is the ecosystem standard that build
// systems set, so a stale MACOSPKG_ value in a shell profile must not defeat it.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/internal/tools"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// globalOptions are the persistent flags shared by all commands, resolved
// through viper so environment variables and the config file apply.
type globalOptions struct {
	Output  string // text | json
	Quiet   bool
	Verbose bool
	// SourceDateEpoch is the fixed build timestamp for reproducible output. The
	// zero value means unset, leaving each writer to use the current time.
	SourceDateEpoch time.Time
	// TempDir is where the scratch file for a build goes. Empty means beside
	// the output, which is the safer default: the system temporary directory is
	// often small, and on many Linux images it is RAM-backed.
	TempDir string
}

var opts globalOptions

var rootCmd = &cobra.Command{
	Use:   "macospkg",
	Short: "Inspect, extract, build, sign and notarize macOS installer packages",
	Long: `A cross-platform, self-contained toolkit for macOS flat installer packages
(.pkg). It reads and writes the xar container, the bill of materials, the cpio
payload, PackageInfo and Distribution directly - without pkgbuild, productbuild,
productsign, pkgutil or a Mac - so packages can be inspected, built, signed,
notarized and stapled on Linux and Windows CI runners as well as on macOS.

Read commands:
  info     Package summary: kind, components, payload, signature, staple
  list     List payload files (from the bill of materials) or archive entries
  cat      Write one archive entry or payload file to stdout
  inspect  Low-level structural inspection (header, TOC, bom, signature)
  expand   Unpack the archive like pkgutil --expand / --expand-full
  extract  Extract payload files to the local file system

Write commands:
  build    Build a component package from a directory
  product  Build a product archive (distribution) from component packages

Signing and notarization:
  sign      Sign a package with a Developer ID Installer certificate
  verify    Verify a package signature and, optionally, its stapled ticket
  notarize  Submit a package to Apple's notary service; wait and staple
  staple    Attach a notarization ticket; unstaple removes one

Packages are auto-detected by content; every command takes the package as its
first argument. Data goes to stdout, diagnostics and progress to stderr.

Configuration precedence: flag > MACOSPKG_<FLAG> environment variable >
~/.config/macospkg/config.yaml.

Reproducible output: build and product produce byte-identical packages for
identical input. Set --source-date-epoch (or SOURCE_DATE_EPOCH) to pin every
timestamp the package carries. SOURCE_DATE_EPOCH resolves as
--source-date-epoch > SOURCE_DATE_EPOCH > MACOSPKG_SOURCE_DATE_EPOCH > config
file: the bare variable is the ecosystem standard, so it outranks the
MACOSPKG_ form.

Exit codes:
  0 success        3 not a package          6 partial result
  1 error          4 credentials missing    7 signature or ticket invalid
  2 usage error      or rejected            8 notarization rejected
                   5 unsupported            9 wait timed out`,
	Version:           tools.Version(),
	SilenceUsage:      true,
	SilenceErrors:     true,
	DisableAutoGenTag: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return resolveGlobalOptions(cmd)
	},
}

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringP("output", "o", "text", "output format: text or json")
	flags.BoolP("quiet", "q", false, "suppress progress and non-essential messages")
	flags.Bool("verbose", false, "verbose diagnostics on stderr")
	flags.String("source-date-epoch", "", "fixed build timestamp (decimal seconds since 1970 UTC) for reproducible output")
	flags.String("temp-dir", "", "directory for the scratch file used while building a package (default: beside the output)")

	rootCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return withCode(ExitUsage, err)
	})

	rootCmd.AddCommand(infoCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(catCmd)
	rootCmd.AddCommand(inspectCmd)
	rootCmd.AddCommand(expandCmd)
	rootCmd.AddCommand(extractCmd)
	rootCmd.AddCommand(buildCmd)
	rootCmd.AddCommand(productCmd)
	rootCmd.AddCommand(signCmd)
	rootCmd.AddCommand(verifyCmd)
	rootCmd.AddCommand(notarizeCmd)
	rootCmd.AddCommand(stapleCmd)
	rootCmd.AddCommand(unstapleCmd)
}

// resolveGlobalOptions binds flags into viper and resolves the effective
// configuration from flags, MACOSPKG_* environment variables and the optional
// config file.
func resolveGlobalOptions(cmd *cobra.Command) error {
	v := viper.New()
	v.SetEnvPrefix("MACOSPKG")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()

	if err := v.BindPFlags(cmd.Root().PersistentFlags()); err != nil {
		return err
	}

	if home, err := os.UserHomeDir(); err == nil {
		v.SetConfigFile(filepath.Join(home, ".config", "macospkg", "config.yaml"))
		// The config file is optional; only complain if it exists but is invalid.
		if err := v.ReadInConfig(); err != nil {
			if _, ok := err.(*os.PathError); !ok && !os.IsNotExist(err) {
				if _, notFound := err.(viper.ConfigFileNotFoundError); !notFound {
					return fmt.Errorf("unable to read config file: %w", err)
				}
			}
		}
	}

	opts = globalOptions{
		Output:  v.GetString("output"),
		Quiet:   v.GetBool("quiet"),
		Verbose: v.GetBool("verbose"),
		TempDir: v.GetString("temp-dir"),
	}

	// SOURCE_DATE_EPOCH deliberately departs from the flag > MACOSPKG_<FLAG> >
	// config order used by everything else: the bare, unprefixed variable is
	// the ecosystem standard that build systems set, and a stale MACOSPKG_
	// value left in a shell profile must not defeat it.
	raw := ""
	if f := cmd.Root().PersistentFlags().Lookup("source-date-epoch"); f != nil && f.Changed {
		raw = f.Value.String()
	} else if env := os.Getenv("SOURCE_DATE_EPOCH"); env != "" {
		raw = env
	} else {
		raw = v.GetString("source-date-epoch")
	}
	if raw != "" {
		epoch, err := parseSourceDateEpoch(raw)
		if err != nil {
			return err
		}
		opts.SourceDateEpoch = epoch
	}

	switch opts.Output {
	case "text", "json":
	default:
		return usageErrorf("invalid --output %q: must be text or json", opts.Output)
	}

	return nil
}

// parseSourceDateEpoch parses the SOURCE_DATE_EPOCH convention: decimal
// seconds since 1970 UTC, no sign, no fraction.
func parseSourceDateEpoch(raw string) (time.Time, error) {
	seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, usageErrorf("invalid source date epoch %q: must be decimal seconds since 1970 UTC", raw)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

// exactArgs is like cobra.ExactArgs but returns a usage-coded error.
func exactArgs(n int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return usageErrorf("expected %s (got %d arguments)", usage, len(args))
		}
		return nil
	}
}

// rangeArgs is like cobra.RangeArgs but returns a usage-coded error.
func rangeArgs(min, max int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < min || len(args) > max {
			return usageErrorf("expected %s (got %d arguments)", usage, len(args))
		}
		return nil
	}
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return exitCodeFor(err)
	}
	return ExitOK
}
