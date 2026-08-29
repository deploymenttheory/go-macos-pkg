// File hashing for build reports and notary submissions.
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// sha256File returns the hex SHA-256 of a file.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
