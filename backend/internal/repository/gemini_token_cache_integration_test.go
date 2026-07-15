//go:build integration

package repository

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type GeminiTokenCacheSuite struct {
	IntegrationRedisSuite
	cache service.GeminiTokenCache
}

func (s *GeminiTokenCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	encryptor, err := NewAESEncryptor(geminiTokenCacheIntegrationConfig())
	require.NoError(s.T(), err)
	cache, err := NewGeminiTokenCache(s.rdb, encryptor)
	require.NoError(s.T(), err)
	s.cache = cache
}

func (s *GeminiTokenCacheSuite) TestDeleteAccessToken() {
	cacheKey := "project-123"
	token := "token-value"
	require.NoError(s.T(), s.cache.SetAccessToken(s.ctx, cacheKey, token, time.Minute))

	got, err := s.cache.GetAccessToken(s.ctx, cacheKey)
	require.NoError(s.T(), err)
	require.Equal(s.T(), token, got)

	require.NoError(s.T(), s.cache.DeleteAccessToken(s.ctx, cacheKey))

	_, err = s.cache.GetAccessToken(s.ctx, cacheKey)
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil after delete")
}

func (s *GeminiTokenCacheSuite) TestAccessTokenIsEncryptedAtRest() {
	cacheKey := "encrypted-project"
	token := "ya29.integration-plaintext-token"
	require.NoError(s.T(), s.cache.SetAccessToken(s.ctx, cacheKey, token, time.Minute))

	raw, err := s.rdb.Get(s.ctx, oauthTokenKey(cacheKey)).Result()
	require.NoError(s.T(), err)
	require.NotEqual(s.T(), token, raw)
	require.True(s.T(), strings.HasPrefix(raw, secretDomainCiphertextPrefix))
	_, err = s.rdb.Get(s.ctx, oauthLegacyTokenKey(cacheKey)).Result()
	require.ErrorIs(s.T(), err, redis.Nil)

	got, err := s.cache.GetAccessToken(s.ctx, cacheKey)
	require.NoError(s.T(), err)
	require.Equal(s.T(), token, got)
}

func (s *GeminiTokenCacheSuite) TestGetAccessTokenDoesNotReadLegacyPlaintext() {
	cacheKey := "legacy-only"
	require.NoError(s.T(), s.rdb.Set(s.ctx, oauthLegacyTokenKey(cacheKey), "ya29.legacy-plaintext", time.Minute).Err())

	got, err := s.cache.GetAccessToken(s.ctx, cacheKey)
	require.Empty(s.T(), got)
	require.ErrorIs(s.T(), err, redis.Nil)
}

func (s *GeminiTokenCacheSuite) TestGetAccessTokenRejectsCorruptV2Ciphertext() {
	cacheKey := "corrupt-v2"
	require.NoError(s.T(), s.rdb.Set(s.ctx, oauthTokenKey(cacheKey), "not-domain-ciphertext", time.Minute).Err())

	got, err := s.cache.GetAccessToken(s.ctx, cacheKey)
	require.Empty(s.T(), got)
	require.Error(s.T(), err)
}

func (s *GeminiTokenCacheSuite) TestPurgeLegacyOAuthTokenCachePreservesV2AndLockNamespaces() {
	require.NoError(s.T(), s.rdb.Set(s.ctx, oauthLegacyTokenKey("legacy"), "plaintext", time.Minute).Err())
	require.NoError(s.T(), s.rdb.Set(s.ctx, oauthTokenKey("encrypted"), "protected", time.Minute).Err())
	require.NoError(s.T(), s.rdb.Set(s.ctx, oauthRefreshLockKeyPrefix+"lock", "owner", time.Minute).Err())

	removed, err := PurgeLegacyOAuthTokenCacheSecrets(s.ctx, s.rdb)
	require.NoError(s.T(), err)
	require.EqualValues(s.T(), 1, removed)
	_, err = s.rdb.Get(s.ctx, oauthLegacyTokenKey("legacy")).Result()
	require.ErrorIs(s.T(), err, redis.Nil)
	require.Equal(s.T(), "protected", s.rdb.Get(s.ctx, oauthTokenKey("encrypted")).Val())
	require.Equal(s.T(), "owner", s.rdb.Get(s.ctx, oauthRefreshLockKeyPrefix+"lock").Val())
}

func (s *GeminiTokenCacheSuite) TestDeleteAccessToken_MissingKey() {
	require.NoError(s.T(), s.cache.DeleteAccessToken(s.ctx, "missing-key"))
}

func (s *GeminiTokenCacheSuite) TestRefreshLockReleaseDoesNotDeleteForeignOwner() {
	cacheKey := "ownership-takeover"
	acquired, ownerA, err := s.cache.AcquireRefreshLock(s.ctx, cacheKey, time.Minute)
	require.NoError(s.T(), err)
	require.True(s.T(), acquired)
	require.NotEmpty(s.T(), ownerA)

	lockKey := oauthRefreshLockKeyPrefix + cacheKey
	require.NoError(s.T(), s.rdb.Set(s.ctx, lockKey, "owner-b", time.Minute).Err())
	require.NoError(s.T(), s.cache.ReleaseRefreshLock(s.ctx, cacheKey, ownerA))

	currentOwner, err := s.rdb.Get(s.ctx, lockKey).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), "owner-b", currentOwner)
}

func (s *GeminiTokenCacheSuite) TestRefreshLockOwnerCanReleaseOwnLock() {
	cacheKey := "ownership-release"
	acquired, owner, err := s.cache.AcquireRefreshLock(s.ctx, cacheKey, time.Minute)
	require.NoError(s.T(), err)
	require.True(s.T(), acquired)
	require.NotEmpty(s.T(), owner)

	require.NoError(s.T(), s.cache.ReleaseRefreshLock(s.ctx, cacheKey, owner))
	_, err = s.rdb.Get(s.ctx, oauthRefreshLockKeyPrefix+cacheKey).Result()
	require.ErrorIs(s.T(), err, redis.Nil)
}

func TestGeminiTokenCacheSuite(t *testing.T) {
	suite.Run(t, new(GeminiTokenCacheSuite))
}

func geminiTokenCacheIntegrationConfig() *config.Config {
	return &config.Config{
		SecretEncryption: config.SecretEncryptionConfig{
			TOTPSecretKey:        strings.Repeat("1", 64),
			TOTPCacheKey:         strings.Repeat("2", 64),
			AccountCredentialKey: strings.Repeat("3", 64),
			BackupS3Key:          strings.Repeat("4", 64),
			ContentModerationKey: strings.Repeat("5", 64),
			ChannelMonitorKey:    strings.Repeat("6", 64),
			PaymentProviderKey:   strings.Repeat("7", 64),
			ProxyCredentialKey:   strings.Repeat("8", 64),
			SchedulerCacheKey:    strings.Repeat("9", 64),
			OAuthTokenCacheKey:   strings.Repeat("b", 64),
			JWTHMACKey:           strings.Repeat("c", 64),
			SettingSecretKey:     strings.Repeat("d", 64),
		},
	}
}
