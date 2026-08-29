// macospkg build SRC -o OUT.pkg — build a component package from a directory.
package cli

import (
	"crypto"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/deploymenttheory/go-macos-pkg/internal/tools"
	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
	"github.com/spf13/cobra"
)

var (
	buildIdentifier         string
	buildVersion            string
	buildInstallLocation    string
	buildScripts            string
	buildOwnership          string
	buildMinOS              string
	buildPostinstallAction  string
	buildAuth               string
	buildNoPayload          bool
	buildRelocatable        bool
	buildNoBundleRelocation bool
	buildPreserveXattr      bool
	buildExclude            []string
	buildExecutable         []string
	buildManifest           string
)

var buildCmd = &cobra.Command{
	Use:   "build SRC [OUT.pkg]",
	Short: "Build a component package from a directory",
	Long: `Build a component package — what pkgbuild makes — from SRC, the directory
whose contents are to be installed at --install-location.

SRC may instead be a project directory holding a build-info.yaml (or .json,
.plist) in munkipkg's format next to payload/ and scripts/ directories; the
manifest supplies identifier, version and the other options, and flags
given here override it. --manifest names such a file explicitly.

The package is reproducible: with --source-date-epoch (or SOURCE_DATE_EPOCH)
set, the same input produces byte-identical output on every platform.

On Windows the file system records no execute bits; --executable names the
payload paths (regular expressions) that should be 0755. Ownership other
than "recommended" (root:wheel) needs uid/gid and is not available there.

OUT.pkg may be omitted for a project whose manifest names the package; it
is then written to the project's build/ directory.

Examples:
  macospkg build ./root Foo.pkg --identifier com.example.foo --version 1.2.0
  macospkg build ./root Foo.pkg --identifier com.example.foo --version 1.2.0 \
      --install-location /usr/local --scripts ./scripts
  macospkg build ./project                     # project/build-info.yaml
  macospkg build ./root Foo.pkg --identifier com.example.foo --version 1 \
      --executable '^\./usr/local/bin/'`,
	Args: rangeArgs(1, 2, "SRC [OUT.pkg]"),
	RunE: runBuild,
}

func init() {
	f := buildCmd.Flags()
	f.StringVar(&buildIdentifier, "identifier", "", "package identifier, e.g. com.example.foo")
	f.StringVar(&buildVersion, "version", "", "package version, e.g. 1.2.0")
	f.StringVar(&buildInstallLocation, "install-location", "", "where the payload is installed (default /)")
	f.StringVar(&buildScripts, "scripts", "", "directory of install scripts (preinstall, postinstall, ...)")
	f.StringVar(&buildOwnership, "ownership", "", "payload ownership: recommended, preserve or preserve-other")
	f.StringVar(&buildMinOS, "min-os-version", "", "minimum macOS version, e.g. 12.0")
	f.StringVar(&buildPostinstallAction, "postinstall-action", "", "none, logout, restart or shutdown")
	f.StringVar(&buildAuth, "auth", "", "root (default) or none")
	f.BoolVar(&buildNoPayload, "nopayload", false, "build a scripts-only package with no payload")
	f.BoolVar(&buildRelocatable, "relocatable", false, "mark the package relocatable")
	f.BoolVar(&buildNoBundleRelocation, "no-bundle-relocation", false, "always install bundles at their packaged paths")
	f.BoolVar(&buildPreserveXattr, "preserve-xattr", false, "set preserve-xattr on the package")
	f.StringArrayVar(&buildExclude, "exclude", nil, "payload paths to leave out (regular expression on ./path); repeatable")
	f.StringArrayVar(&buildExecutable, "executable", nil, "payload paths that are executable, for hosts without execute bits (regular expression); repeatable")
	f.StringVar(&buildManifest, "manifest", "", "build-info.yaml/.json/.plist to read options from")
	addSigningFlags(buildCmd, "sign-")
	addNotarizeFlags(buildCmd)
}

// buildReport is the JSON schema for macospkg build.
type buildReport struct {
	Output          string   `json:"output"`
	Kind            string   `json:"kind"`
	Identifier      string   `json:"identifier"`
	Version         string   `json:"version"`
	InstallLocation string   `json:"installLocation"`
	NumberOfFiles   int      `json:"numberOfFiles"`
	InstallKBytes   int      `json:"installKBytes"`
	Scripts         []string `json:"scripts"`
	Bundles         []string `json:"bundles"`
	Size            int64    `json:"size"`
	SHA256          string   `json:"sha256"`
	Signed          bool     `json:"signed"`
	Notarized       bool     `json:"notarized"`
}

func runBuild(cmd *cobra.Command, args []string) error {
	src := args[0]
	buildOutput := ""
	if len(args) > 1 {
		buildOutput = args[1]
	}
	m, err := loadManifestFor(src, buildManifest)
	if err != nil {
		return err
	}

	// Flags override the manifest; the manifest fills in what flags left
	// empty.
	pick := func(flag, manifest string) string {
		if flag != "" {
			return flag
		}
		return manifest
	}
	o := flatpkg.ComponentOptions{
		Root:               m.payloadRoot(src),
		Scripts:            pick(buildScripts, m.scriptsDir(src)),
		NoPayload:          buildNoPayload || m.NoPayload,
		Identifier:         pick(buildIdentifier, m.Identifier),
		Version:            pick(buildVersion, m.Version),
		InstallLocation:    pick(buildInstallLocation, m.InstallLocation),
		MinOSVersion:       pick(buildMinOS, m.MinimumOSVersion),
		PostinstallAction:  pick(buildPostinstallAction, m.PostinstallAction),
		Auth:               buildAuth,
		Relocatable:        buildRelocatable,
		NoBundleRelocation: buildNoBundleRelocation || m.SuppressBundleRelocation,
		PreserveXattr:      buildPreserveXattr || m.PreserveXattr,
		Epoch:              opts.SourceDateEpoch,
		TempDir:            opts.TempDir,
		GeneratorVersion:   "go-macos-pkg " + tools.Version(),
		Progress:           func(rel string) { verbosef("packaged %s", rel) },
	}
	if o.Identifier == "" {
		return usageErrorf("--identifier is required (or identifier in a build-info manifest)")
	}
	if o.Version == "" {
		return usageErrorf("--version is required (or version in a build-info manifest)")
	}
	output := pick(buildOutput, m.outputPath(src, o.Version))
	if output == "" {
		return usageErrorf("an output path is required: build SRC OUT.pkg (or a manifest with a name)")
	}
	ownership, err := flatpkg.ParseOwnership(pick(buildOwnership, m.Ownership))
	if err != nil {
		return usageErrorf("%v", err)
	}
	o.Ownership = ownership

	excludes, err := compilePatterns(append(buildExclude, m.Exclude...))
	if err != nil {
		return usageErrorf("invalid --exclude: %v", err)
	}
	if len(excludes) > 0 {
		o.Exclude = func(rel string) bool { return anyMatch(excludes, rel) }
	}
	executables, err := compilePatterns(append(buildExecutable, m.ExecutablePatterns...))
	if err != nil {
		return usageErrorf("invalid --executable: %v", err)
	}
	if len(executables) > 0 {
		o.Executable = func(rel string) bool { return anyMatch(executables, rel) }
	}
	if len(m.Files) > 0 {
		o.FileModes = m.fileModes()
	}

	signer, err := signerFromFlags(cmd, m, crypto.SHA256)
	if err != nil {
		return err
	}
	o.Signer = signer

	res, err := writePackage(output, func(w *os.File) (*flatpkg.BuildResult, error) {
		return flatpkg.BuildComponent(o, w)
	})
	if err != nil {
		return buildError(err)
	}

	if err := notarizeAfterBuild(cmd, m, output, signer != nil); err != nil {
		return err
	}
	report := buildReport{
		Output:          output,
		Kind:            string(flatpkg.KindComponent),
		Identifier:      o.Identifier,
		Version:         o.Version,
		InstallLocation: res.PackageInfo.InstallLocation,
		NumberOfFiles:   res.NumberOfFiles,
		InstallKBytes:   res.InstallKBytes,
		Scripts:         []string{},
		Bundles:         []string{},
		Signed:          signer != nil,
		Notarized:       buildNotarize,
	}
	if res.Scripts != nil {
		report.Scripts = res.Scripts
	}
	for _, b := range res.Bundles {
		report.Bundles = append(report.Bundles, b.ID)
	}
	if st, err := os.Stat(output); err == nil {
		report.Size = st.Size()
	}
	report.SHA256, _ = sha256File(output)

	if opts.Output == "json" {
		return jsonOut(report)
	}
	progressf("built %s: %s %s, %d files, %d KB installed%s", output, report.Identifier, report.Version, report.NumberOfFiles, report.InstallKBytes, signedLabel(signer != nil))
	return nil
}

// writePackage writes a package to path through a temporary file beside
// it, so an interrupted build never leaves a half-written package under
// the final name.
func writePackage(path string, build func(*os.File) (*flatpkg.BuildResult, error)) (*flatpkg.BuildResult, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.partial")
	if err != nil {
		return nil, fmt.Errorf("unable to create %s: %w", path, err)
	}
	res, err := build(tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return nil, fmt.Errorf("unable to write %s: %w", path, err)
	}
	return res, nil
}

// buildError maps builder failures onto the exit-code contract.
func buildError(err error) error {
	switch {
	case strings.Contains(err.Error(), flatpkg.ErrUnsupportedOnPlatform.Error()):
		return withCode(ExitUnsupported, err)
	case os.IsNotExist(err):
		return withCode(ExitUsage, err)
	}
	return err
}

func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, p := range patterns {
		if p == "" {
			continue
		}
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func anyMatch(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func signedLabel(signed bool) string {
	if signed {
		return ", signed"
	}
	return ""
}
