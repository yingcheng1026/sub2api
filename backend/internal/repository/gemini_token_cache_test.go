//go:build unit

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGeminiTokenCache_DeleteAccessToken_RedisError(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() {
		_ = rdb.Close()
	})

	encryptor, err := NewAESEncryptor(domainKeyTestConfig())
	require.NoError(t, err)
	cache, err := NewGeminiTokenCache(rdb, encryptor)
	require.NoError(t, err)
	err = cache.DeleteAccessToken(context.Background(), "broken")
	require.Error(t, err)
}

func TestNewOAuthRefreshLockTokenIsOpaqueAndUnique(t *testing.T) {
	first, err := newOAuthRefreshLockToken()
	require.NoError(t, err)
	second, err := newOAuthRefreshLockToken()
	require.NoError(t, err)

	require.Len(t, first, 32)
	require.Len(t, second, 32)
	require.NotEqual(t, first, second)
}

func TestGeminiTokenCache_ReleaseRefreshLockEmptyOwnerIsNoop(t *testing.T) {
	cache := &geminiTokenCache{}
	require.NoError(t, cache.ReleaseRefreshLock(context.Background(), "key", ""))
}

func TestOAuthAccessTokenCodecEncryptsAtRest(t *testing.T) {
	encryptor, err := NewAESEncryptor(domainKeyTestConfig())
	require.NoError(t, err)

	encoded, err := encodeOAuthAccessToken("ya29.plaintext-token", encryptor)
	require.NoError(t, err)
	require.NotEqual(t, "ya29.plaintext-token", encoded)
	require.True(t, strings.HasPrefix(encoded, secretDomainCiphertextPrefix))

	decoded, err := decodeOAuthAccessToken(encoded, encryptor)
	require.NoError(t, err)
	require.Equal(t, "ya29.plaintext-token", decoded)

	_, err = service.DecryptForSecretDomain(encryptor, service.SecretDomainSchedulerCache, encoded)
	require.Error(t, err)
}

func TestNewGeminiTokenCacheRequiresDomainEncryptor(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = rdb.Close() })

	_, err := NewGeminiTokenCache(rdb, nil)
	require.Error(t, err)
}

func TestNewGeminiTokenCacheRequiresRedisClient(t *testing.T) {
	encryptor, err := NewAESEncryptor(domainKeyTestConfig())
	require.NoError(t, err)

	_, err = NewGeminiTokenCache(nil, encryptor)
	require.Error(t, err)
}

func TestOAuthAccessTokenCodecRejectsLegacyPlaintext(t *testing.T) {
	encryptor, err := NewAESEncryptor(domainKeyTestConfig())
	require.NoError(t, err)

	_, err = decodeOAuthAccessToken("ya29.legacy-plaintext-token", encryptor)
	require.Error(t, err)
}

func TestPurgeLegacyOAuthTokenCacheRequiresRedisClient(t *testing.T) {
	_, err := PurgeLegacyOAuthTokenCacheSecrets(context.Background(), nil)
	require.Error(t, err)
}

func TestOAuthTokenCacheNamespacesDoNotOverlap(t *testing.T) {
	newKey := oauthTokenKey("account-1")
	legacyKey := oauthLegacyTokenKey("account-1")
	lockKey := oauthRefreshLockKeyPrefix + "account-1"

	require.True(t, strings.HasPrefix(legacyKey, oauthLegacyTokenKeyPrefix))
	require.False(t, strings.HasPrefix(newKey, oauthLegacyTokenKeyPrefix))
	require.False(t, strings.HasPrefix(lockKey, oauthLegacyTokenKeyPrefix))
	require.NotEqual(t, newKey, legacyKey)
	require.NotEqual(t, newKey, lockKey)
}
