package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const apiKeyPrefixStorageLength = 8

// HashAPIKey returns the deterministic SHA-256 digest used for API key lookup.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// APIKeyStoredAuthCacheLocator returns the repository-provided keyed locator.
// The SHA-256 fallback exists only for test doubles and legacy in-memory
// objects; production repository reads always populate KeyHash with HMAC.
func APIKeyStoredAuthCacheLocator(key *APIKey) string {
	if key == nil {
		return ""
	}
	if locator := strings.ToLower(strings.TrimSpace(key.KeyHash)); len(locator) == 64 {
		return locator
	}
	if strings.TrimSpace(key.Key) == "" {
		return ""
	}
	return APIKeyAuthCacheLocator(key.Key)
}

// APIKeyPrefixForStorage keeps a small non-secret prefix for admin search/display.
func APIKeyPrefixForStorage(key string) string {
	if len(key) <= apiKeyPrefixStorageLength {
		return key
	}
	return key[:apiKeyPrefixStorageLength]
}
