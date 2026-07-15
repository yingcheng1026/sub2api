//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type tokenVersionRepoErrorStub struct {
	mockAccountRepoForGemini
}

func (r *tokenVersionRepoErrorStub) GetByID(context.Context, int64) (*Account, error) {
	return nil, errors.New("authoritative account read failed")
}

type tokenVersionCacheStub struct {
	setCalls atomic.Int32
}

type credentialBoundTokenCacheStub struct {
	tokens map[string]string
}

type lockHeldStaleTokenCacheStub struct {
	mu         sync.Mutex
	getCalls   int
	staleToken string
}

func (s *lockHeldStaleTokenCacheStub) GetAccessToken(context.Context, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCalls++
	if s.getCalls == 1 {
		return "", nil
	}
	return s.staleToken, nil
}

func (s *lockHeldStaleTokenCacheStub) SetAccessToken(context.Context, string, string, time.Duration) error {
	return nil
}

func (s *lockHeldStaleTokenCacheStub) DeleteAccessToken(context.Context, string) error {
	return nil
}

func (s *lockHeldStaleTokenCacheStub) AcquireRefreshLock(context.Context, string, time.Duration) (bool, string, error) {
	return false, "", nil
}

func (s *lockHeldStaleTokenCacheStub) ReleaseRefreshLock(context.Context, string, string) error {
	return nil
}

func (s *credentialBoundTokenCacheStub) GetAccessToken(_ context.Context, cacheKey string) (string, error) {
	return s.tokens[cacheKey], nil
}

func (s *credentialBoundTokenCacheStub) SetAccessToken(_ context.Context, cacheKey string, token string, _ time.Duration) error {
	s.tokens[cacheKey] = token
	return nil
}

func (s *credentialBoundTokenCacheStub) DeleteAccessToken(_ context.Context, cacheKey string) error {
	delete(s.tokens, cacheKey)
	return nil
}

func (s *credentialBoundTokenCacheStub) AcquireRefreshLock(context.Context, string, time.Duration) (bool, string, error) {
	return true, "credential-bound-owner", nil
}

func (s *credentialBoundTokenCacheStub) ReleaseRefreshLock(context.Context, string, string) error {
	return nil
}

func (s *tokenVersionCacheStub) GetAccessToken(context.Context, string) (string, error) {
	return "", nil
}

func (s *tokenVersionCacheStub) SetAccessToken(context.Context, string, string, time.Duration) error {
	s.setCalls.Add(1)
	return nil
}

func (s *tokenVersionCacheStub) DeleteAccessToken(context.Context, string) error {
	return nil
}

func (s *tokenVersionCacheStub) AcquireRefreshLock(context.Context, string, time.Duration) (bool, string, error) {
	return false, "", nil
}

func (s *tokenVersionCacheStub) ReleaseRefreshLock(context.Context, string, string) error {
	return nil
}

func TestTokenProvidersSkipCacheFillWhenAuthoritativeVersionReadFails(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour).Format(time.RFC3339)
	repo := &tokenVersionRepoErrorStub{}

	tests := []struct {
		name        string
		account     *Account
		newProvider func(*tokenVersionCacheStub) interface {
			GetAccessToken(context.Context, *Account) (string, error)
		}
	}{
		{
			name: "gemini",
			account: &Account{ID: 101, Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{
				"access_token": "gemini-token", "expires_at": expiresAt, "project_id": "gemini-project",
			}},
			newProvider: func(cache *tokenVersionCacheStub) interface {
				GetAccessToken(context.Context, *Account) (string, error)
			} {
				return NewGeminiTokenProvider(repo, cache, nil)
			},
		},
		{
			name: "openai",
			account: &Account{ID: 102, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
				"access_token": "openai-token", "expires_at": expiresAt,
			}},
			newProvider: func(cache *tokenVersionCacheStub) interface {
				GetAccessToken(context.Context, *Account) (string, error)
			} {
				return NewOpenAITokenProvider(repo, cache, nil)
			},
		},
		{
			name: "claude",
			account: &Account{ID: 103, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{
				"access_token": "claude-token", "expires_at": expiresAt,
			}},
			newProvider: func(cache *tokenVersionCacheStub) interface {
				GetAccessToken(context.Context, *Account) (string, error)
			} {
				return NewClaudeTokenProvider(repo, cache, nil)
			},
		},
		{
			name: "antigravity",
			account: &Account{ID: 104, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{
				"access_token": "antigravity-token", "expires_at": expiresAt, "project_id": "ag-project",
			}},
			newProvider: func(cache *tokenVersionCacheStub) interface {
				GetAccessToken(context.Context, *Account) (string, error)
			} {
				return NewAntigravityTokenProvider(repo, cache, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := &tokenVersionCacheStub{}
			provider := tt.newProvider(cache)
			token, err := provider.GetAccessToken(context.Background(), tt.account)
			require.NoError(t, err)
			require.NotEmpty(t, token)
			require.Zero(t, cache.setCalls.Load(), "an unverified token must not refill the shared cache")
		})
	}
}

func TestTokenProvidersDoNotReuseCacheEntryAfterOAuthCredentialReplacement(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour).Format(time.RFC3339)
	tests := []struct {
		name        string
		platform    string
		credentials map[string]any
		key         func(*Account) string
		newProvider func(AccountRepository, GeminiTokenCache) interface {
			GetAccessToken(context.Context, *Account) (string, error)
		}
	}{
		{
			name:        "gemini",
			platform:    PlatformGemini,
			credentials: map[string]any{"project_id": "gemini-project"},
			key:         GeminiTokenCacheKey,
			newProvider: func(repo AccountRepository, cache GeminiTokenCache) interface {
				GetAccessToken(context.Context, *Account) (string, error)
			} {
				return NewGeminiTokenProvider(repo, cache, nil)
			},
		},
		{
			name:        "openai",
			platform:    PlatformOpenAI,
			credentials: map[string]any{},
			key:         OpenAITokenCacheKey,
			newProvider: func(repo AccountRepository, cache GeminiTokenCache) interface {
				GetAccessToken(context.Context, *Account) (string, error)
			} {
				return NewOpenAITokenProvider(repo, cache, nil)
			},
		},
		{
			name:        "claude",
			platform:    PlatformAnthropic,
			credentials: map[string]any{},
			key:         ClaudeTokenCacheKey,
			newProvider: func(repo AccountRepository, cache GeminiTokenCache) interface {
				GetAccessToken(context.Context, *Account) (string, error)
			} {
				return NewClaudeTokenProvider(repo, cache, nil)
			},
		},
		{
			name:        "antigravity",
			platform:    PlatformAntigravity,
			credentials: map[string]any{"project_id": "ag-project"},
			key:         AntigravityTokenCacheKey,
			newProvider: func(repo AccountRepository, cache GeminiTokenCache) interface {
				GetAccessToken(context.Context, *Account) (string, error)
			} {
				return NewAntigravityTokenProvider(repo, cache, nil)
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := int64(200 + i)
			oldCredentials := cloneCredentials(tt.credentials)
			oldCredentials["access_token"] = "old-access-token-" + tt.name
			oldCredentials["refresh_token"] = "old-refresh-token-" + tt.name
			oldCredentials["expires_at"] = expiresAt
			newCredentials := cloneCredentials(tt.credentials)
			newCredentials["access_token"] = "new-access-token-" + tt.name
			newCredentials["refresh_token"] = "new-refresh-token-" + tt.name
			newCredentials["expires_at"] = expiresAt

			oldAccount := &Account{ID: id, Platform: tt.platform, Type: AccountTypeOAuth, Credentials: oldCredentials}
			newAccount := &Account{ID: id, Platform: tt.platform, Type: AccountTypeOAuth, Credentials: newCredentials}
			oldKey := tt.key(oldAccount)
			newKey := tt.key(newAccount)
			require.NotEqual(t, oldKey, newKey, "cache key must be bound to the current credential generation")
			require.False(t, strings.Contains(newKey, newAccount.GetCredential("access_token")))
			require.False(t, strings.Contains(newKey, newAccount.GetCredential("refresh_token")))

			cache := &credentialBoundTokenCacheStub{tokens: map[string]string{oldKey: "stale-cached-token"}}
			repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{id: newAccount}}
			provider := tt.newProvider(repo, cache)
			// Simulate a scheduler/request path that still holds the pre-update
			// account object while the database already contains the replacement.
			token, err := provider.GetAccessToken(context.Background(), oldAccount)
			require.NoError(t, err)
			require.Equal(t, newAccount.GetCredential("access_token"), token)
			require.NotEqual(t, "stale-cached-token", token)
		})
	}
}

func TestTokenProvidersRejectCacheHitWhenAuthoritativeVersionReadFails(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour).Format(time.RFC3339)
	tests := []struct {
		name        string
		platform    string
		credentials map[string]any
		key         func(*Account) string
		newProvider func(AccountRepository, GeminiTokenCache) interface {
			GetAccessToken(context.Context, *Account) (string, error)
		}
	}{
		{
			name:        "gemini",
			platform:    PlatformGemini,
			credentials: map[string]any{"project_id": "gemini-project"},
			key:         GeminiTokenCacheKey,
			newProvider: func(repo AccountRepository, cache GeminiTokenCache) interface {
				GetAccessToken(context.Context, *Account) (string, error)
			} {
				return NewGeminiTokenProvider(repo, cache, nil)
			},
		},
		{
			name:     "openai",
			platform: PlatformOpenAI,
			key:      OpenAITokenCacheKey,
			newProvider: func(repo AccountRepository, cache GeminiTokenCache) interface {
				GetAccessToken(context.Context, *Account) (string, error)
			} {
				return NewOpenAITokenProvider(repo, cache, nil)
			},
		},
		{
			name:     "claude",
			platform: PlatformAnthropic,
			key:      ClaudeTokenCacheKey,
			newProvider: func(repo AccountRepository, cache GeminiTokenCache) interface {
				GetAccessToken(context.Context, *Account) (string, error)
			} {
				return NewClaudeTokenProvider(repo, cache, nil)
			},
		},
		{
			name:        "antigravity",
			platform:    PlatformAntigravity,
			credentials: map[string]any{"project_id": "ag-project"},
			key:         AntigravityTokenCacheKey,
			newProvider: func(repo AccountRepository, cache GeminiTokenCache) interface {
				GetAccessToken(context.Context, *Account) (string, error)
			} {
				return NewAntigravityTokenProvider(repo, cache, nil)
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			credentials := cloneCredentials(tt.credentials)
			credentials["access_token"] = "account-token-" + tt.name
			credentials["refresh_token"] = "refresh-token-" + tt.name
			credentials["expires_at"] = expiresAt
			account := &Account{
				ID:          int64(300 + i),
				Platform:    tt.platform,
				Type:        AccountTypeOAuth,
				Credentials: credentials,
			}
			cache := &credentialBoundTokenCacheStub{tokens: map[string]string{
				tt.key(account): "unverified-cached-token",
			}}
			provider := tt.newProvider(&tokenVersionRepoErrorStub{}, cache)

			token, err := provider.GetAccessToken(context.Background(), account)

			require.Error(t, err)
			require.Empty(t, token)
		})
	}
}

func TestTokenProvidersRejectStaleCacheHitAfterRefreshLockWait(t *testing.T) {
	expiresAt := time.Now().Add(time.Minute).Format(time.RFC3339)
	tests := []struct {
		name        string
		platform    string
		credentials map[string]any
		newProvider func(AccountRepository, GeminiTokenCache) interface {
			GetAccessToken(context.Context, *Account) (string, error)
			SetRefreshAPI(*OAuthRefreshAPI, OAuthRefreshExecutor)
			SetRefreshPolicy(ProviderRefreshPolicy)
		}
	}{
		{
			name:        "gemini",
			platform:    PlatformGemini,
			credentials: map[string]any{"project_id": "gemini-project"},
			newProvider: func(repo AccountRepository, cache GeminiTokenCache) interface {
				GetAccessToken(context.Context, *Account) (string, error)
				SetRefreshAPI(*OAuthRefreshAPI, OAuthRefreshExecutor)
				SetRefreshPolicy(ProviderRefreshPolicy)
			} {
				return NewGeminiTokenProvider(repo, cache, nil)
			},
		},
		{
			name:     "openai",
			platform: PlatformOpenAI,
			newProvider: func(repo AccountRepository, cache GeminiTokenCache) interface {
				GetAccessToken(context.Context, *Account) (string, error)
				SetRefreshAPI(*OAuthRefreshAPI, OAuthRefreshExecutor)
				SetRefreshPolicy(ProviderRefreshPolicy)
			} {
				return NewOpenAITokenProvider(repo, cache, nil)
			},
		},
		{
			name:     "claude",
			platform: PlatformAnthropic,
			newProvider: func(repo AccountRepository, cache GeminiTokenCache) interface {
				GetAccessToken(context.Context, *Account) (string, error)
				SetRefreshAPI(*OAuthRefreshAPI, OAuthRefreshExecutor)
				SetRefreshPolicy(ProviderRefreshPolicy)
			} {
				return NewClaudeTokenProvider(repo, cache, nil)
			},
		},
		{
			name:        "antigravity",
			platform:    PlatformAntigravity,
			credentials: map[string]any{"project_id": "ag-project"},
			newProvider: func(repo AccountRepository, cache GeminiTokenCache) interface {
				GetAccessToken(context.Context, *Account) (string, error)
				SetRefreshAPI(*OAuthRefreshAPI, OAuthRefreshExecutor)
				SetRefreshPolicy(ProviderRefreshPolicy)
			} {
				return NewAntigravityTokenProvider(repo, cache, nil)
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := int64(400 + i)
			oldCredentials := cloneCredentials(tt.credentials)
			oldCredentials["access_token"] = "old-lock-token-" + tt.name
			oldCredentials["refresh_token"] = "old-lock-refresh-" + tt.name
			oldCredentials["expires_at"] = expiresAt
			newCredentials := cloneCredentials(tt.credentials)
			newCredentials["access_token"] = "new-lock-token-" + tt.name
			newCredentials["refresh_token"] = "new-lock-refresh-" + tt.name
			newCredentials["expires_at"] = expiresAt

			oldAccount := &Account{ID: id, Platform: tt.platform, Type: AccountTypeOAuth, Credentials: oldCredentials}
			newAccount := &Account{ID: id, Platform: tt.platform, Type: AccountTypeOAuth, Credentials: newCredentials}
			repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{id: newAccount}}
			cache := &lockHeldStaleTokenCacheStub{staleToken: "stale-lock-cache-token"}
			provider := tt.newProvider(repo, cache)
			provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), &refreshAPIExecutorStub{needsRefresh: true})
			policy := ProviderRefreshPolicy{
				OnRefreshError: ProviderRefreshErrorReturn,
				OnLockHeld:     ProviderLockHeldWaitForCache,
			}
			provider.SetRefreshPolicy(policy)

			token, err := provider.GetAccessToken(context.Background(), oldAccount)

			require.NoError(t, err)
			require.Equal(t, newAccount.GetCredential("access_token"), token)
			require.NotEqual(t, cache.staleToken, token)
		})
	}
}

func TestValidateOAuthTokenCacheHitRejectsRequestVersionAheadWithDifferentCredentials(t *testing.T) {
	requestAccount := &Account{
		ID:       901,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":   "request-old-token",
			"refresh_token":  "request-old-refresh",
			"_token_version": int64(200),
		},
	}
	latestAccount := &Account{
		ID:       requestAccount.ID,
		Platform: requestAccount.Platform,
		Type:     requestAccount.Type,
		Credentials: map[string]any{
			"access_token":   "authoritative-new-token",
			"refresh_token":  "authoritative-new-refresh",
			"_token_version": int64(100),
		},
	}
	repo := &tokenVersionAccountRepoStub{latest: latestAccount}

	validatedAccount, cacheHitIsCurrent, err := validateOAuthTokenCacheHit(
		context.Background(),
		requestAccount,
		repo,
	)

	require.Error(t, err)
	require.Nil(t, validatedAccount)
	require.False(t, cacheHitIsCurrent)
}

func TestAdvanceOAuthTokenVersionIsMonotonicAndOAuthOnly(t *testing.T) {
	oauth := &Account{
		Type: AccountTypeOAuth,
		Credentials: map[string]any{
			"_token_version": int64(9_999_999_999_999),
		},
	}
	AdvanceOAuthTokenVersion(oauth)
	require.EqualValues(t, 10_000_000_000_000, oauth.GetCredentialAsInt64("_token_version"))

	withoutCredentials := &Account{Type: AccountTypeOAuth}
	AdvanceOAuthTokenVersion(withoutCredentials)
	require.Greater(t, withoutCredentials.GetCredentialAsInt64("_token_version"), int64(0))

	apiKey := &Account{Type: AccountTypeAPIKey, Credentials: map[string]any{}}
	AdvanceOAuthTokenVersion(apiKey)
	require.NotContains(t, apiKey.Credentials, "_token_version")
}
