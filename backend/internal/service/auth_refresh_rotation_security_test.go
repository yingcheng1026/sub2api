//go:build unit

package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type refreshRotationBarrierCache struct {
	mu          sync.Mutex
	tokens      map[string]*service.RefreshTokenData
	getCount    int
	bothRead    chan struct{}
	releaseRead chan struct{}
}

type refreshTokenIndexFailureCache struct {
	*refreshRotationBarrierCache
	userIndexErr   error
	familyIndexErr error
}

func (c *refreshTokenIndexFailureCache) AddToUserTokenSet(context.Context, int64, string, time.Duration) error {
	return c.userIndexErr
}

func (c *refreshTokenIndexFailureCache) AddToFamilyTokenSet(context.Context, string, string, time.Duration) error {
	return c.familyIndexErr
}

func newRefreshRotationBarrierCache() *refreshRotationBarrierCache {
	return &refreshRotationBarrierCache{
		tokens:      make(map[string]*service.RefreshTokenData),
		bothRead:    make(chan struct{}),
		releaseRead: make(chan struct{}),
	}
}

func (c *refreshRotationBarrierCache) StoreRefreshToken(_ context.Context, tokenHash string, data *service.RefreshTokenData, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cloned := *data
	c.tokens[tokenHash] = &cloned
	return nil
}

func (c *refreshRotationBarrierCache) GetRefreshToken(_ context.Context, tokenHash string) (*service.RefreshTokenData, error) {
	c.mu.Lock()
	data, ok := c.tokens[tokenHash]
	if !ok {
		c.mu.Unlock()
		return nil, service.ErrRefreshTokenNotFound
	}
	cloned := *data
	c.getCount++
	if c.getCount == 2 {
		close(c.bothRead)
	}
	c.mu.Unlock()

	<-c.releaseRead
	return &cloned, nil
}

func (c *refreshRotationBarrierCache) ConsumeRefreshToken(_ context.Context, tokenHash string) (*service.RefreshTokenData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.tokens[tokenHash]
	if !ok {
		return nil, service.ErrRefreshTokenNotFound
	}
	delete(c.tokens, tokenHash)
	cloned := *data
	return &cloned, nil
}

func (c *refreshRotationBarrierCache) DeleteRefreshToken(_ context.Context, tokenHash string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tokens, tokenHash)
	return nil
}

func (c *refreshRotationBarrierCache) DeleteUserRefreshTokens(context.Context, int64) error {
	return nil
}

func (c *refreshRotationBarrierCache) DeleteTokenFamily(context.Context, string) error {
	return nil
}

func (c *refreshRotationBarrierCache) AddToUserTokenSet(context.Context, int64, string, time.Duration) error {
	return nil
}

func (c *refreshRotationBarrierCache) AddToFamilyTokenSet(context.Context, string, string, time.Duration) error {
	return nil
}

func (c *refreshRotationBarrierCache) GetUserTokenHashes(context.Context, int64) ([]string, error) {
	return nil, nil
}

func (c *refreshRotationBarrierCache) GetFamilyTokenHashes(context.Context, string) ([]string, error) {
	return nil, nil
}

func (c *refreshRotationBarrierCache) IsTokenInFamily(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestRefreshTokenPairConsumesParentExactlyOnceUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	cache := newRefreshRotationBarrierCache()
	user := &service.User{
		ID:                   91,
		Email:                "refresh-race@example.com",
		PasswordHash:         "test-password-hash",
		Role:                 service.RoleUser,
		Status:               service.StatusActive,
		TokenVersion:         3,
		TokenVersionResolved: true,
	}
	userRepo := newEmailBindUserRepoStub(user)
	cfg := &config.Config{JWT: config.JWTConfig{
		Secret:                   "test-refresh-race-secret",
		ExpireHour:               1,
		AccessTokenExpireMinutes: 60,
		RefreshTokenExpireDays:   7,
	}}
	svc := service.NewAuthService(nil, userRepo, nil, cache, cfg, nil, nil, nil, nil, nil, nil, nil)

	parent, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)

	type result struct {
		pair *service.TokenPairWithUser
		err  error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			pair, refreshErr := svc.RefreshTokenPair(ctx, parent.RefreshToken)
			results <- result{pair: pair, err: refreshErr}
		}()
	}

	select {
	case <-cache.bothRead:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent refresh calls did not both reach the vulnerable read boundary")
	}
	close(cache.releaseRead)

	first := <-results
	second := <-results
	successes := 0
	for _, got := range []result{first, second} {
		if got.err == nil {
			successes++
			require.NotNil(t, got.pair)
			require.NotEmpty(t, got.pair.AccessToken)
			require.NotEmpty(t, got.pair.RefreshToken)
			continue
		}
		require.True(t, errors.Is(got.err, service.ErrRefreshTokenReused) || errors.Is(got.err, service.ErrRefreshTokenInvalid))
	}
	require.Equal(t, 1, successes, "one parent refresh token must mint at most one child token pair")
}

func TestGenerateTokenPairRejectsAndCleansUpWhenRefreshTokenIndexingFails(t *testing.T) {
	t.Parallel()

	for name, indexFailure := range map[string]func(*refreshTokenIndexFailureCache){
		"user index": func(cache *refreshTokenIndexFailureCache) {
			cache.userIndexErr = errors.New("user index unavailable")
		},
		"family index": func(cache *refreshTokenIndexFailureCache) {
			cache.familyIndexErr = errors.New("family index unavailable")
		},
	} {
		t.Run(name, func(t *testing.T) {
			cache := &refreshTokenIndexFailureCache{refreshRotationBarrierCache: newRefreshRotationBarrierCache()}
			indexFailure(cache)
			user := &service.User{ID: 92, Email: "refresh-index@example.com", PasswordHash: "test-password-hash", Role: service.RoleUser, Status: service.StatusActive, TokenVersion: 1, TokenVersionResolved: true}
			svc := service.NewAuthService(nil, newEmailBindUserRepoStub(user), nil, cache, &config.Config{JWT: config.JWTConfig{Secret: "test-refresh-index-secret", ExpireHour: 1, AccessTokenExpireMinutes: 60, RefreshTokenExpireDays: 7}}, nil, nil, nil, nil, nil, nil, nil)

			pair, err := svc.GenerateTokenPair(context.Background(), user, "family-92")

			require.Error(t, err)
			require.Nil(t, pair)
			cache.mu.Lock()
			require.Empty(t, cache.tokens, "a token with incomplete revocation indexes must not remain usable")
			cache.mu.Unlock()
		})
	}
}
