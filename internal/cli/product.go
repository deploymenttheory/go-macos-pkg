// macospkg product -o OUT.pkg --package X.pkg...: build a product archive.
package cli

import (
	"crypto"
	"os"
	"strings"

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
)

var productCmd = &cobra.Command{
	Use:   "product OUT.pkg --package X.pkg [--package Y.pkg ...]",
	Short: "Build a product archive (distribution) from component packages",
	Long: `Build a product archive (what productbuild makes) from one or more
component packages. A product archive carries a Distribution script that
the Installer runs to present choices and decide what to install; it is
the form to distribute, and the form notarization expects.

Without --distribution a Distribution is synthesised that installs every
package, as productbuild --synthesize does; --title, --min-os-version and
--host-architectures shape it. With --distribution the document is used as
given and must refer to the packages by their file names (#Foo.pkg).
--resources embeds a directory the Distribution's welcome, license and
background elements refer to.

Examples:
  macospkg product Foo-1.0.pkg --package Foo.pkg --title "Foo 1.0"
  macospkg product Suite.pkg --package A.pkg --package B.pkg \
      --distribution Distribution.xml --resources ./resources`,
	Args: exactArgs(1, "OUT.pkg"),
	RunE: runProduct,
}

func init() {
	f := productCmd.Flags()
	f.StringArrayVar(&productPackages, "package", nil, "component package to embed; repeatable (required)")
	f.StringVar(&productDistribution, "distribution", "", "Distribution XML to use instead of synthesising one")
	f.StringArrayVar(&productPackagePaths, "package-path", nil, "directory to search for the packages a --distribution names; repeatable, and the working directory is searched too")
	f.BoolVar(&productSynthesize, "synthesize", false, "write the synthesised Distribution to the output path instead of building an archive")
	f.StringVar(&productResources, "resources", "", "directory to embed as Resources/")
	f.StringVar(&productTitle, "title", "", "title for the synthesised Distribution")
	f.StringVar(&productMinOS, "min-os-version", "", "minimum macOS version for the synthesised Distribution")
	f.StringVar(&productArchs, "host-architectures", "", "comma-separated hostArchitectures, e.g. arm64,x86_64")
	f.StringVar(&productID, "product-id", "", "product identifier for the synthesised Distribution")
	f.StringVar(&productVersion, "product-version", "", "product version for the synthesised Distribution")
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
	if len(productPackages) == 0 {
		return usageErrorf("at least one --package is required (or a --distribution that names them)")
	}
	o := flatpkg.ProductOptions{
		Packages:       productPackages,
		Resources:      productResources,
		Title:          productTitle,
		MinOSVersion:   productMinOS,
		ProductID:      productID,
		ProductVersion: productVersion,
		Epoch:          opts.SourceDateEpoch,
		TempDir:        opts.TempDir,
		Progress:       func(p string) { verbosef("packaged %s", p) },
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
	if opts.Output == "json" {
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
	data, err := flatpkg.SynthesizeDistribution(flatpkg.ProductOptions{
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
	if opts.Output == "json" {
		return jsonOut(report)
	}
	progressf("wrote %s for %d package(s)", out, len(productPackages))
	return nil
}
