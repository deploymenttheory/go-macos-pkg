// Opening the package named on the command line, with the exit-code
// contract applied to the ways that can fail.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/deploymenttheory/go-macos-pkg/pkg/flatpkg"
	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// openPackage opens a flat package, returning exit 3 for anything that is
// not one: a missing file, a non-xar file, or a xar that is not a package.
func openPackage(path string) (*flatpkg.Package, error) {
	p, err := flatpkg.Open(path)
	if err != nil {
		switch {
		case os.IsNotExist(err):
			return nil, withCode(ExitBadPackage, fmt.Errorf("unable to open %s: no such file", path))
		case errors.Is(err, xar.ErrNotXar):
			return nil, withCode(ExitBadPackage, fmt.Errorf("%s is not a flat package (not a xar archive)", path))
		case errors.Is(err, flatpkg.ErrNotPackage):
			return nil, withCode(ExitBadPackage, fmt.Errorf("%s is a xar archive but not a flat package: no PackageInfo or Distribution", path))
		default:
			return nil, withCode(ExitBadPackage, fmt.Errorf("unable to open %s: %w", path, err))
		}
	}
	return p, nil
}

// openXAR opens a xar archive without requiring it to be a package, for
// the low-level inspect verbs.
func openXAR(path string) (*xar.Reader, error) {
	x, err := xar.OpenFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, withCode(ExitBadPackage, fmt.Errorf("unable to open %s: no such file", path))
		}
		if errors.Is(err, xar.ErrNotXar) {
			return nil, withCode(ExitBadPackage, fmt.Errorf("%s is not a xar archive", path))
		}
		return nil, withCode(ExitBadPackage, fmt.Errorf("unable to open %s: %w", path, err))
	}
	return x, nil
}
