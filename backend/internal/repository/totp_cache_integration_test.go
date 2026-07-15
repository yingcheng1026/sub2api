//go:build integration

package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type totpCacheTestEncryptor struct{}

func (totpCacheTestEncryptor) Encrypt(plaintext string) (string, error) {
	return base64.RawStdEncoding.EncodeToString([]byte(plaintext)), nil
}

func (totpCacheTestEncryptor) Decrypt(ciphertext string) (string, error) {
	plaintext, err := base64.RawStdEncoding.DecodeString(ciphertext)
	return string(plaintext), err
}

func (totpCacheTestEncryptor) EncryptForDomain(domain, plaintext string) (string, error) {
	return base64.RawStdEncoding.EncodeToString([]byte(domain + "\x00" + plaintext)), nil
}

func (totpCacheTestEncryptor) DecryptForDomain(domain, ciphertext string) (string, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	prefix := domain + "\x00"
	if !strings.HasPrefix(string(decoded), prefix) {
		return "", errors.New("ciphertext domain mismatch")
	}
	return strings.TrimPrefix(string(decoded), prefix), nil
}

func TestTotpHighSensitivityCodeCannotReplayAcrossPurposes(t *testing.T) {
	rdb := testRedis(t)
	cache := &TotpCache{rdb: rdb}
	ctx := context.Background()

	consumed, err := cache.ConsumeScopedVerification(ctx, "api_key_reveal", 91, "same-code-fingerprint", 2*time.Minute)
	require.NoError(t, err)
	require.True(t, consumed)

	consumed, err = cache.ConsumeScopedVerification(ctx, "api_key_create", 91, "same-code-fingerprint", 2*time.Minute)
	require.NoError(t, err)
	require.False(t, consumed, "one TOTP code must be single-use across every API-key step-up purpose")
}

func TestTotpVerifyLimiterDoesNotSlideItsTTL(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		increment func(*TotpCache) (int, error)
	}{
		{
			name: "login",
			key:  fmt.Sprintf("%s%d", totpAttemptsKeyPrefix, 92),
			increment: func(cache *TotpCache) (int, error) {
				return cache.IncrementVerifyAttempts(context.Background(), 92)
			},
		},
		{
			name: "api key step-up",
			key:  fmt.Sprintf("%s%s:%d", totpAttemptsKeyPrefix, "api_key_reveal", 92),
			increment: func(cache *TotpCache) (int, error) {
				return cache.IncrementScopedVerifyAttempts(context.Background(), "api_key_reveal", 92)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rdb := testRedis(t)
			cache := &TotpCache{rdb: rdb}
			ctx := context.Background()

			count, err := tt.increment(cache)
			require.NoError(t, err)
			require.Equal(t, 1, count)
			require.NoError(t, rdb.Expire(ctx, tt.key, 2*time.Second).Err())

			count, err = tt.increment(cache)
			require.NoError(t, err)
			require.Equal(t, 2, count)
			ttl, err := rdb.TTL(ctx, tt.key).Result()
			require.NoError(t, err)
			require.Positive(t, ttl)
			require.LessOrEqual(t, ttl, 2*time.Second, "a retry must not extend the original limiter window")
		})
	}
}

func TestTotpLoginSessionIsEncryptedAndConsumedOnce(t *testing.T) {
	rdb := testRedis(t)
	cache := &TotpCache{rdb: rdb, encryptor: totpCacheTestEncryptor{}}
	ctx := context.Background()
	const tempToken = "temporary-login-token"
	session := &service.TotpLoginSession{
		UserID:      93,
		Email:       "oauth@example.com",
		TokenExpiry: time.Now().Add(5 * time.Minute),
		PendingOAuthBind: &service.PendingOAuthBindLoginSession{
			PendingSessionToken: "pending-session-secret",
			BrowserSessionKey:   "browser-session-secret",
		},
	}

	require.NoError(t, cache.SetLoginSession(ctx, tempToken, session, 5*time.Minute))
	raw, err := rdb.Get(ctx, totpLoginKeyPrefix+tempToken).Result()
	require.NoError(t, err)
	require.Contains(t, raw, totpLoginEncryptedV2Prefix)
	require.NotContains(t, raw, session.PendingOAuthBind.PendingSessionToken)
	require.NotContains(t, raw, session.PendingOAuthBind.BrowserSessionKey)

	got, err := cache.GetLoginSession(ctx, tempToken)
	require.NoError(t, err)
	require.Equal(t, session.UserID, got.UserID)
	require.Equal(t, session.PendingOAuthBind, got.PendingOAuthBind)
	consumed, err := cache.ConsumeLoginSession(ctx, tempToken)
	require.NoError(t, err)
	require.NotNil(t, consumed)
	require.Equal(t, session.UserID, consumed.UserID)
	require.NoError(t, cache.SetLoginSession(ctx, tempToken, session, 5*time.Minute))

	const workers = 16
	winners := make(chan *service.TotpLoginSession, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			consumed, consumeErr := cache.ConsumeLoginSession(ctx, tempToken)
			winners <- consumed
			errs <- consumeErr
		}()
	}
	winnerCount := 0
	for i := 0; i < workers; i++ {
		require.NoError(t, <-errs)
		if <-winners != nil {
			winnerCount++
		}
	}
	require.Equal(t, 1, winnerCount, "GETDEL must allow exactly one successful login continuation")
}

func TestTotpCacheRejectsLegacyUnboundSessionEnvelope(t *testing.T) {
	rdb := testRedis(t)
	encryptor := totpCacheTestEncryptor{}
	cache := &TotpCache{rdb: rdb, encryptor: encryptor}
	ctx := context.Background()
	session := &service.TotpLoginSession{UserID: 94, Email: "legacy@example.test"}
	plaintext, err := json.Marshal(session)
	require.NoError(t, err)
	legacy, err := encryptor.Encrypt(string(plaintext))
	require.NoError(t, err)
	require.NoError(t, rdb.Set(ctx, totpLoginKeyPrefix+"legacy", totpLoginEncryptedV1Prefix+legacy, time.Minute).Err())

	_, err = cache.GetLoginSession(ctx, "legacy")
	require.Error(t, err)
}
