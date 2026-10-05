package crawler

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// GenerateFingerprint normalizes inputs to prevent false duplicates
func GenerateFingerprint(artist, title string) string {
	cleanArtist := strings.TrimSpace(strings.ToLower(artist))
	cleanTitle := strings.TrimSpace(strings.ToLower(title))

	raw := cleanArtist + "|" + cleanTitle
	hash := sha256.Sum256([]byte(raw))

	return hex.EncodeToString(hash[:])
}
