// macospkg - cross-platform CLI for macOS flat installer packages.
package main

import (
	"os"

	"github.com/deploymenttheory/go-macos-pkg/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
