package service

import (
	"crypto/sha256"
	"encoding/hex"
)

const apiKeyPrefixStorageLength = 12

// HashAPIKey returns the deterministic SHA-256 digest used for API key lookup.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// APIKeyPrefixForStorage keeps a small non-secret prefix for admin search/display.
func APIKeyPrefixForStorage(key string) string {
	if len(key) <= apiKeyPrefixStorageLength {
		return key
	}
	return key[:apiKeyPrefixStorageLength]
}
