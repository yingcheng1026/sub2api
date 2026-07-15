package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/redis/go-redis/v9"
)

const (
	oauthLegacyTokenKeyPrefix = "oauth:token:"
	oauthTokenKeyPrefix       = "oauth:v2:token:"
	oauthRefreshLockKeyPrefix = "oauth:refresh_lock:"
)

var oauthRefreshLockReleaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
    return redis.call('DEL', KEYS[1])
end
return 0
`)

type geminiTokenCache struct {
	rdb       *redis.Client
	encryptor service.SecretEncryptor
}

func NewGeminiTokenCache(rdb *redis.Client, encryptor service.SecretEncryptor) (service.GeminiTokenCache, error) {
	if rdb == nil {
		return nil, fmt.Errorf("oauth token cache Redis client is required")
	}
	if domainEncryptor, ok := encryptor.(service.DomainSecretEncryptor); !ok || domainEncryptor == nil {
		return nil, fmt.Errorf("domain secret encryptor is required for oauth token cache")
	}
	return &geminiTokenCache{rdb: rdb, encryptor: encryptor}, nil
}

func (c *geminiTokenCache) GetAccessToken(ctx context.Context, cacheKey string) (string, error) {
	encoded, err := c.rdb.Get(ctx, oauthTokenKey(cacheKey)).Result()
	if err != nil {
		return "", err
	}
	return decodeOAuthAccessToken(encoded, c.encryptor)
}

func (c *geminiTokenCache) SetAccessToken(ctx context.Context, cacheKey string, token string, ttl time.Duration) error {
	encoded, err := encodeOAuthAccessToken(token, c.encryptor)
	if err != nil {
		return fmt.Errorf("encrypt oauth access token: %w", err)
	}
	if err := c.rdb.Set(ctx, oauthTokenKey(cacheKey), encoded, ttl).Err(); err != nil {
		return err
	}
	if err := c.rdb.Del(ctx, oauthLegacyTokenKey(cacheKey)).Err(); err != nil {
		return fmt.Errorf("remove legacy plaintext oauth access token: %w", err)
	}
	return nil
}

func (c *geminiTokenCache) DeleteAccessToken(ctx context.Context, cacheKey string) error {
	return c.rdb.Del(ctx, oauthTokenKey(cacheKey), oauthLegacyTokenKey(cacheKey)).Err()
}

func (c *geminiTokenCache) AcquireRefreshLock(ctx context.Context, cacheKey string, ttl time.Duration) (bool, string, error) {
	ownershipToken, err := newOAuthRefreshLockToken()
	if err != nil {
		return false, "", fmt.Errorf("generate oauth refresh lock token: %w", err)
	}
	key := fmt.Sprintf("%s%s", oauthRefreshLockKeyPrefix, cacheKey)
	acquired, err := c.rdb.SetNX(ctx, key, ownershipToken, ttl).Result()
	if err != nil || !acquired {
		return acquired, "", err
	}
	return true, ownershipToken, nil
}

func (c *geminiTokenCache) ReleaseRefreshLock(ctx context.Context, cacheKey string, ownershipToken string) error {
	if ownershipToken == "" {
		return nil
	}
	key := fmt.Sprintf("%s%s", oauthRefreshLockKeyPrefix, cacheKey)
	if _, err := oauthRefreshLockReleaseScript.Run(ctx, c.rdb, []string{key}, ownershipToken).Result(); err != nil {
		return fmt.Errorf("release oauth refresh lock: %w", err)
	}
	return nil
}

func newOAuthRefreshLockToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func oauthTokenKey(cacheKey string) string {
	return fmt.Sprintf("%s%s", oauthTokenKeyPrefix, cacheKey)
}

func oauthLegacyTokenKey(cacheKey string) string {
	return fmt.Sprintf("%s%s", oauthLegacyTokenKeyPrefix, cacheKey)
}

func encodeOAuthAccessToken(token string, encryptor service.SecretEncryptor) (string, error) {
	encoded, err := service.EncryptForSecretDomain(encryptor, service.SecretDomainOAuthTokenCache, token)
	if err != nil {
		return "", err
	}
	if encoded == "" || encoded == token || !strings.HasPrefix(encoded, secretDomainCiphertextPrefix) {
		return "", fmt.Errorf("oauth access token encryption did not produce protected ciphertext")
	}
	return encoded, nil
}

func decodeOAuthAccessToken(encoded string, encryptor service.SecretEncryptor) (string, error) {
	return service.DecryptForSecretDomain(encryptor, service.SecretDomainOAuthTokenCache, encoded)
}

// PurgeLegacyOAuthTokenCacheSecrets removes only the pre-v2 plaintext access
// token namespace. New encrypted tokens and refresh locks use non-overlapping
// prefixes and are never touched. Old application nodes must be drained before
// this startup preflight runs, otherwise they can recreate plaintext keys.
func PurgeLegacyOAuthTokenCacheSecrets(ctx context.Context, rdb *redis.Client) (int64, error) {
	if rdb == nil {
		return 0, fmt.Errorf("oauth token cache Redis client is required")
	}
	pattern := oauthLegacyTokenKeyPrefix + "*"
	var removed int64
	for pass := 0; pass < 3; pass++ {
		var cursor uint64
		for {
			keys, next, err := rdb.Scan(ctx, cursor, pattern, 256).Result()
			if err != nil {
				return removed, fmt.Errorf("scan legacy oauth token cache: %w", err)
			}
			if len(keys) > 0 {
				count, err := rdb.Unlink(ctx, keys...).Result()
				if err != nil {
					return removed, fmt.Errorf("remove legacy oauth token cache: %w", err)
				}
				removed += count
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
		remaining, _, err := rdb.Scan(ctx, 0, pattern, 1).Result()
		if err != nil {
			return removed, fmt.Errorf("verify legacy oauth token cache: %w", err)
		}
		if len(remaining) == 0 {
			return removed, nil
		}
	}
	return removed, fmt.Errorf("legacy oauth token cache keys remain")
}
