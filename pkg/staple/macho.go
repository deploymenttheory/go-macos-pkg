package staple

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/deploymenttheory/go-macos-pkg/pkg/machoidentity"
)

// CDHashes returns each architecture's SHA-256 code-directory hash for
// notarization ticket lookup. Reading hashes does not verify code signatures.
// Architectures without a SHA-256 code directory cause the entire call to fail.
func CDHashes(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	architectures, err := machoidentity.Read(context.Background(), f, info.Size())
	if err != nil {
		return nil, err
	}
	var hashes [][]byte
	for _, arch := range architectures {
		found := false
		for _, directory := range arch.CodeDirectories {
			if directory.HashType != 2 {
				continue
			}
			hash, err := hex.DecodeString(directory.CDHash)
			if err != nil {
				return nil, err
			}
			hashes = append(hashes, hash)
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("staple: %s has no SHA-256 code directory", arch.Name)
		}
	}
	return hashes, nil
}
