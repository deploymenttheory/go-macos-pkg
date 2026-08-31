// macospkg product -o OUT.pkg --package X.pkg...: build a product archive.
package cli

import (
	"crypto"
	"os"
	"strings"

	"github.com/deploymenttheory/go-macos-pkg/internal/tools"
	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
	"github.com/spf13/cobra"
)

var (
	productPackages     []string
	productDistribution string
	productResources    string
	productTitle        string
	productMinOS        string
	productArchs        string
	productID           string
	productVersion      string
	productSynthesize   bool
	productPackagePaths []string
	productRequirements string
	productScripts      string
	productUI           string
	productRoot         string
	productContent      string
	productComponents   []string

	productComponentCompression string
	productLargePayload         bool
	productPlugins              string
)

var productCmd = &cobra.Command{
	Use:   "product OUT.pkg [--package X.pkg | --root DIR[:INSTALL_PATH] | --content DIR | --component B.app]",
	Short: "Build a product archive from component packages",
	Long: `Build a product archive for the macOS Installer or the Mac App Store.

A product archive is the flat file you distribute. It holds one or more
component packages and a distribution: the document the Installer runs to
present choices and decide what to install. Notarization expects this form
rather than a bare component package.

product has five modes:

  1. From component packages already built. --package names each one, and
     they go in as they are, keeping whatever payload container they carry.

  2. From a destination root. --root DIR:INSTALL_PATH packages a directory
     tree as its own component package.

  3. From in-app content. --content packages the contents of a directory.

  4. From a bundle. --component packages a bundle, reading the identifier,
     version and install location out of its Info.plist. PATH:INSTALL_PATH
     overrides the location. This is the mode the Mac App Store accepts.

  5. Synthesize a distribution (--synthesize). Instead of an archive, write
     the document that would have been generated, so it can be edited and
     passed back with --distribution.

Without --distribution a distribution is synthesized that installs everything
with no customization. --title, --identifier, --version, --min-os-version,
--host-architectures and --ui shape it, and --product supplies the pre-install
requirements the Installer checks first.

With --distribution the document is used as given, and the packages it names
are found with --package-path. Its references are rewritten to point inside the
archive, so it can name them as plain file names.

Examples:
  macospkg product Foo-1.0.pkg --package Foo.pkg --title "Foo 1.0"
  macospkg product Foo.pkg --component Foo.app:/Applications
  macospkg product dist.xml --package A.pkg --package B.pkg --synthesize
  macospkg product Suite.pkg --distribution dist.xml --package-path ./out \
      --resources ./resources`,
	Args: exactArgs(1, "OUT.pkg"),
	RunE: runProduct,
}

func init() {
	f := productCmd.Flags()

	// What goes in. Each of these adds a component package to the archive
	// and a reference to the synthesized Distribution; the last three
	// build that component here rather than taking one already built.
	f.StringArrayVar(&productPackages, "package", nil, "component package to add to the archive and to the synthesized Distribution; repeatable")
	f.StringVar(&productRoot, "root", "", "destination root to add as its own component package; DIR:INSTALL_PATH gives it a default install location")
	f.StringVar(&productContent, "content", "", "directory whose contents are added as their own component package, for in-app content")
	f.StringArrayVar(&productComponents, "component", nil, "bundle to add as its own component package; repeatable, and PATH:INSTALL_PATH gives it a default install location")
	f.BoolVar(&productLargePayload, "large-payload", false, "build the components from --root, --content and --component with the payload format that carries files of 8 GiB and over; only macOS 12 and later can read one, which the Distribution then requires")
	f.StringVar(&productComponentCompression, "component-compression", "", "payload container for the components built by --component: legacy or gzip (default), none for no compression at all, or pbzx, lzfse or lzbitmap. A package given with --package keeps whatever container it was built with")

	// The Distribution: the document the Installer runs to decide what to
	// show and what to install. Supply one, or shape the synthesized one.
	f.StringVar(&productDistribution, "distribution", "", "Distribution defining the presentation, choices and packages to install, used instead of synthesizing one")
	f.StringArrayVar(&productPackagePaths, "package-path", nil, "directory to search for the component packages a Distribution names; repeatable, and the working directory is always searched")
	f.BoolVar(&productSynthesize, "synthesize", false, "write the synthesized Distribution to the output path instead of building an archive with it")
	f.StringVar(&productRequirements, "product", "", "requirements property list the synthesized Distribution takes its os, arch, ram, bundle, graphics and sysctl checks from")
	f.StringVar(&productTitle, "title", "", "title the synthesized Distribution shows")
	f.StringVar(&productID, "identifier", "", "unique product identifier the synthesized Distribution carries")
	f.StringVar(&productVersion, "version", "", "product version the synthesized Distribution carries")
	f.StringVar(&productMinOS, "min-os-version", "", "oldest macOS the synthesized Distribution allows; a shorthand for a --product list carrying only os")
	f.StringVar(&productArchs, "host-architectures", "", "architectures the synthesized Distribution allows, comma separated (default x86_64,arm64)")
	f.StringVar(&productUI, "ui", "", "value for the synthesized choices-outline's ui attribute, which also namespaces its choices; \"mas\" marks one meant for the Mac App Store")

	// Everything else the archive carries alongside the packages.
	f.StringVar(&productResources, "resources", "", "directory of resources to copy in: images, and lproj directories of localized strings, that the Distribution's welcome, license and background elements name")
	f.StringVar(&productScripts, "scripts", "", "directory to carry for the system.run() commands a Distribution invokes; the macOS Installer reads these, the App Store does not")
	f.StringVar(&productPlugins, "plugins", "", "directory to carry for the Installer's plug-in mechanism, normally an InstallerSections.plist and one or more plug-in bundles")

	addSigningFlags(productCmd, "sign-")
	addNotarizeFlags(productCmd)
}

// productReport is the JSON schema for macospkg product.
type productReport struct {
	Output     string   `json:"output"`
	Kind       string   `json:"kind"`
	Components []string `json:"components"`
	Resources  []string `json:"resources"`
	Size       int64    `json:"size"`
	SHA256     string   `json:"sha256"`
	Signed     bool     `json:"signed"`
	Notarized  bool     `json:"notarized"`
}

func runProduct(cmd *cobra.Command, args []string) error {
	productOutput := args[0]
	if productSynthesize {
		return runSynthesize(productOutput)
	}
	// A distribution names its packages; --package-path says where to look
	// for them, as productbuild does.
	if len(productPackages) == 0 && productDistribution != "" {
		data, err := os.ReadFile(productDistribution)
		if err != nil {
			return usageErrorf("unable to read --distribution: %v", err)
		}
		found, err := flatpkg.ResolvePackagePaths(data, productPackagePaths)
		if err != nil {
			return buildError(err)
		}
		productPackages = found
	}
	inlineComponents, err := parseInlineComponents(productComponents)
	if err != nil {
		return err
	}
	componentCompression, err := flatpkg.ParseCompression(productComponentCompression)
	if err != nil {
		return usageErrorf("--component-compression: %v", err)
	}
	if productComponentCompression != "" && len(inlineComponents) == 0 {
		return usageErrorf("--component-compression applies to the components --component builds; a package given with --package keeps the container it was built with")
	}
	productRoot, productRootInstall := splitInstallPath(productRoot)
	if len(productPackages) == 0 && productRoot == "" && productContent == "" && len(inlineComponents) == 0 {
		return usageErrorf("at least one --package, --root, --content or --component is required (or a --distribution that names the packages)")
	}
	requirements, err := loadProductRequirements()
	if err != nil {
		return err
	}
	o := flatpkg.ProductOptions{
		Output:               productOutput,
		GeneratorVersion:     "go-macos-pkg " + tools.Version(),
		Root:                 productRoot,
		RootInstallPath:      productRootInstall,
		Content:              productContent,
		Components:           inlineComponents,
		ComponentCompression: componentCompression,
		LargePayload:         productLargePayload,
		Requirements:         requirements,
		Packages:             productPackages,
		Resources:            productResources,
		Scripts:              productScripts,
		Plugins:              productPlugins,
		UI:                   productUI,
		Title:                productTitle,
		MinOSVersion:         productMinOS,
		ProductID:            productID,
		ProductVersion:       productVersion,
		Epoch:                opts.SourceDateEpoch,
		TempDir:              opts.TempDir,
		Progress:             func(p string) { verbosef("packaged %s", p) },
	}
	if productArchs != "" {
		for _, a := range strings.Split(productArchs, ",") {
			if a = strings.TrimSpace(a); a != "" {
				o.HostArchitectures = append(o.HostArchitectures, a)
			}
		}
	}
	if productDistribution != "" {
		data, err := os.ReadFile(productDistribution)
		if err != nil {
			return usageErrorf("unable to read --distribution: %v", err)
		}
		if _, err := flatpkg.ParseDistribution(data); err != nil {
			return usageErrorf("--distribution: %v", err)
		}
		o.Distribution = data
	}
	signer, err := signerFromFlags(cmd, nil, crypto.SHA256)
	if err != nil {
		return err
	}
	o.Signer = signer

	var res *flatpkg.ProductResult
	_, err = writePackage(productOutput, func(w *os.File) (*flatpkg.BuildResult, error) {
		var err error
		res, err = flatpkg.BuildProduct(o, w)
		return nil, err
	})
	if err != nil {
		return buildError(err)
	}
	if err := notarizeAfterBuild(cmd, nil, productOutput, signer != nil); err != nil {
		return err
	}
	report := productReport{
		Output: productOutput, Kind: string(flatpkg.KindProduct),
		Components: res.Components, Resources: []string{}, Signed: signer != nil, Notarized: buildNotarize,
	}
	if res.Resources != nil {
		report.Resources = res.Resources
	}
	if st, err := os.Stat(productOutput); err == nil {
		report.Size = st.Size()
	}
	report.SHA256, _ = sha256File(productOutput)
	if structured() {
		return jsonOut(report)
	}
	progressf("built %s: product archive with %d component(s)%s", productOutput, len(report.Components), signedLabel(signer != nil))
	return nil
}

// synthesizeReport is the JSON schema for macospkg product --synthesize.
type synthesizeReport struct {
	Output   string   `json:"output"`
	Packages []string `json:"packages"`
}

// runSynthesize writes the Distribution productbuild --synthesize writes,
// which is a starting point for one you then edit and pass to
// --distribution. It is not the document a product archive carries; see
// flatpkg.SynthesizeDistribution.
func runSynthesize(out string) error {
	if len(productPackages) == 0 {
		return usageErrorf("--synthesize needs at least one --package")
	}
	requirements, err := loadProductRequirements()
	if err != nil {
		return err
	}
	if productUI != "" && productDistribution != "" {
		return usageErrorf("--ui shapes a synthesized Distribution; it has no effect on one given with --distribution")
	}
	data, err := flatpkg.SynthesizeDistribution(flatpkg.ProductOptions{
		Requirements:   requirements,
		UI:             productUI,
		Packages:       productPackages,
		Title:          productTitle,
		MinOSVersion:   productMinOS,
		ProductID:      productID,
		ProductVersion: productVersion,
	})
	if err != nil {
		return buildError(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil { //nolint:gosec // the argument names the output document on purpose
		return buildError(err)
	}
	report := synthesizeReport{Output: out, Packages: productPackages}
	if structured() {
		return jsonOut(report)
	}
	progressf("wrote %s for %d package(s)", out, len(productPackages))
	return nil
}

// loadProductRequirements reads the --product property list, if there is
// one. It is a usage error rather than a build failure: the file is named on
// the command line and a bad one is a mistake in the invocation.
func loadProductRequirements() (*flatpkg.ProductRequirements, error) {
	if productRequirements == "" {
		return nil, nil
	}
	data, err := os.ReadFile(productRequirements)
	if err != nil {
		return nil, usageErrorf("unable to read --product: %v", err)
	}
	r, err := flatpkg.ParseProductRequirements(data)
	if err != nil {
		return nil, usageErrorf("--product: %v", err)
	}
	return r, nil
}

// splitInstallPath separates PATH:INSTALL_PATH. productbuild takes the
// install path as a second positional argument, which cobra has no way to
// express, so --root and --component both spell it with a colon.
//
// A Windows drive letter is not a separator, so the split is on the last
// colon and only when what follows looks like an install path.
func splitInstallPath(a string) (path, installPath string) {
	if i := strings.LastIndex(a, ":"); i > 1 && strings.HasPrefix(a[i+1:], "/") {
		return a[:i], a[i+1:]
	}
	return a, ""
}

// parseInlineComponents reads the --component arguments.
func parseInlineComponents(args []string) ([]flatpkg.ProductComponent, error) {
	var out []flatpkg.ProductComponent
	for _, a := range args {
		path, install := splitInstallPath(a)
		if path == "" {
			return nil, usageErrorf("--component needs a bundle path")
		}
		out = append(out, flatpkg.ProductComponent{Path: path, InstallPath: install})
	}
	return out, nil
}
