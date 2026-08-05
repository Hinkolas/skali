package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// ShortChecksum renders a content checksum or digest for humans: the
// sha256: prefix dropped and the first 12 hex characters kept.
func ShortChecksum(checksum string) string {
	checksum = strings.TrimPrefix(checksum, "sha256:")
	if len(checksum) > 12 {
		return checksum[:12]
	}
	return checksum
}

// FileSHA256 streams the file and returns its hex digest along with the
// byte count it hashed.
func FileSHA256(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
