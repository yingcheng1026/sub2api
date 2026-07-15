package repository

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestNewAPIKeyProtectorRequiresExplicitIndependentKey(t *testing.T) {
	_, err := NewAPIKeyProtector(&config.Config{APIKey: config.APIKeyConfig{
		EncryptionKey: strings.Repeat("11", 32),
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "API_KEY_ENCRYPTION_KEY")
}

func TestAPIKeyProtectorUsesKeyedLocatorAndAuthenticatedContext(t *testing.T) {
	protector, err := NewAPIKeyProtector(&config.Config{APIKey: config.APIKeyConfig{
		EncryptionKey:           strings.Repeat("22", 32),
		EncryptionKeyConfigured: true,
	}})
	require.NoError(t, err)

	plaintext := "sk-short-but-historical"
	locator := protector.LookupLocator(plaintext)
	require.Len(t, locator, 64)
	require.NotEqual(t, service.HashAPIKey(plaintext), locator, "lookup must use HMAC, not bare SHA-256")

	aad := apiKeyAssociatedData(7, service.APIKeyPurposeStandard, locator)
	ciphertext, err := protector.EncryptAPIKey(plaintext, aad)
	require.NoError(t, err)
	require.NotContains(t, ciphertext, plaintext)

	decrypted, err := protector.DecryptAPIKey(ciphertext, aad)
	require.NoError(t, err)
	require.Equal(t, plaintext, decrypted)

	_, err = protector.DecryptAPIKey(ciphertext, apiKeyAssociatedData(8, service.APIKeyPurposeStandard, locator))
	require.Error(t, err, "ciphertext copied to another user must fail AAD authentication")
}
