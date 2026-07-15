//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type geminiTokenCacheStub struct {
	deletedKeys []string
	deleteErr   error
}

func (s *geminiTokenCacheStub) GetAccessToken(ctx context.Context, cacheKey string) (string, error) {
	return "", nil
}

func (s *geminiTokenCacheStub) SetAccessToken(ctx context.Context, cacheKey string, token string, ttl time.Duration) error {
	return nil
}

func (s *geminiTokenCacheStub) DeleteAccessToken(ctx context.Context, cacheKey string) error {
	s.deletedKeys = append(s.deletedKeys, cacheKey)
	return s.deleteErr
}

func (s *geminiTokenCacheStub) AcquireRefreshLock(ctx context.Context, cacheKey string, ttl time.Duration) (bool, string, error) {
	return true, "invalidator-owner", nil
}

func (s *geminiTokenCacheStub) ReleaseRefreshLock(ctx context.Context, cacheKey string, ownershipToken string) error {
	return nil
}

func TestCompositeTokenCacheInvalidator_Gemini(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache)
	account := &Account{
		ID:       10,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"project_id": "project-x",
		},
	}

	err := invalidator.InvalidateToken(context.Background(), account)
	require.NoError(t, err)
	// 新行为：同时删除基于 project_id 和 account_id 的缓存键
	// 这是为了处理：首次获取 token 时可能没有 project_id，之后自动检测到后会使用新 key
	require.Equal(t, []string{"gemini:project-x", "gemini:account:10"}, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_GeminiWithoutProjectID(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache)
	account := &Account{
		ID:       10,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "gemini-token",
		},
	}

	err := invalidator.InvalidateToken(context.Background(), account)
	require.NoError(t, err)
	// 删除当前凭据代际键，并兼容清理升级前的无代际键。
	require.Equal(t, []string{GeminiTokenCacheKey(account), "gemini:account:10"}, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_Antigravity(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache)
	account := &Account{
		ID:       99,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"project_id": "ag-project",
		},
	}

	err := invalidator.InvalidateToken(context.Background(), account)
	require.NoError(t, err)
	// 新行为：同时删除基于 project_id 和 account_id 的缓存键
	require.Equal(t, []string{"ag:ag-project", "ag:account:99"}, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_AntigravityWithoutProjectID(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache)
	account := &Account{
		ID:       99,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "ag-token",
		},
	}

	err := invalidator.InvalidateToken(context.Background(), account)
	require.NoError(t, err)
	// 删除当前凭据代际键，并兼容清理升级前的无代际键。
	require.Equal(t, []string{AntigravityTokenCacheKey(account), "ag:account:99"}, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_OpenAI(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache)
	account := &Account{
		ID:       500,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "openai-token",
		},
	}

	err := invalidator.InvalidateToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, []string{OpenAITokenCacheKey(account), "openai:account:500"}, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_Claude(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache)
	account := &Account{
		ID:       600,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "claude-token",
		},
	}

	err := invalidator.InvalidateToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, []string{ClaudeTokenCacheKey(account), "claude:account:600"}, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_SkipNonOAuth(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache)

	tests := []struct {
		name    string
		account *Account
	}{
		{
			name: "gemini_api_key",
			account: &Account{
				ID:       1,
				Platform: PlatformGemini,
				Type:     AccountTypeAPIKey,
			},
		},
		{
			name: "openai_api_key",
			account: &Account{
				ID:       2,
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
			},
		},
		{
			name: "claude_api_key",
			account: &Account{
				ID:       3,
				Platform: PlatformAnthropic,
				Type:     AccountTypeAPIKey,
			},
		},
		{
			name: "claude_setup_token",
			account: &Account{
				ID:       4,
				Platform: PlatformAnthropic,
				Type:     AccountTypeSetupToken,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache.deletedKeys = nil
			err := invalidator.InvalidateToken(context.Background(), tt.account)
			require.NoError(t, err)
			require.Empty(t, cache.deletedKeys)
		})
	}
}

func TestCompositeTokenCacheInvalidator_SkipUnsupportedPlatform(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache)
	account := &Account{
		ID:       100,
		Platform: "unknown-platform",
		Type:     AccountTypeOAuth,
	}

	err := invalidator.InvalidateToken(context.Background(), account)
	require.NoError(t, err)
	require.Empty(t, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_NilCache(t *testing.T) {
	invalidator := NewCompositeTokenCacheInvalidator(nil)
	account := &Account{
		ID:       2,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
	}

	err := invalidator.InvalidateToken(context.Background(), account)
	require.NoError(t, err)
}

func TestCompositeTokenCacheInvalidator_NilAccount(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache)

	err := invalidator.InvalidateToken(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_NilInvalidator(t *testing.T) {
	var invalidator *CompositeTokenCacheInvalidator
	account := &Account{
		ID:       5,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
	}

	err := invalidator.InvalidateToken(context.Background(), account)
	require.NoError(t, err)
}

func TestCompositeTokenCacheInvalidator_DeleteError(t *testing.T) {
	expectedErr := errors.New("redis connection failed")
	cache := &geminiTokenCacheStub{deleteErr: expectedErr}
	invalidator := NewCompositeTokenCacheInvalidator(cache)

	tests := []struct {
		name    string
		account *Account
	}{
		{
			name: "openai_delete_error",
			account: &Account{
				ID:       700,
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
			},
		},
		{
			name: "claude_delete_error",
			account: &Account{
				ID:       800,
				Platform: PlatformAnthropic,
				Type:     AccountTypeOAuth,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := invalidator.InvalidateToken(context.Background(), tt.account)
			require.ErrorIs(t, err, expectedErr)
		})
	}
}

func TestCompositeTokenCacheInvalidator_AttemptsEveryKeyWhenDeleteFails(t *testing.T) {
	expectedErr := errors.New("redis connection failed")
	tests := []struct {
		name     string
		account  *Account
		expected func(*Account) []string
	}{
		{
			name: "gemini",
			account: &Account{
				ID:       42,
				Platform: PlatformGemini,
				Type:     AccountTypeOAuth,
				Credentials: map[string]any{
					"project_id":    "project-42",
					"access_token":  "new-access-token",
					"refresh_token": "new-refresh-token",
				},
			},
			expected: func(account *Account) []string {
				return []string{
					GeminiTokenCacheKey(account),
					credentialBoundOAuthTokenCacheKey("gemini:account:42", account),
					"gemini:project-42",
					"gemini:account:42",
				}
			},
		},
		{
			name: "antigravity",
			account: &Account{
				ID:       43,
				Platform: PlatformAntigravity,
				Type:     AccountTypeOAuth,
				Credentials: map[string]any{
					"project_id":    "project-43",
					"access_token":  "new-ag-access-token",
					"refresh_token": "new-ag-refresh-token",
				},
			},
			expected: func(account *Account) []string {
				return []string{
					AntigravityTokenCacheKey(account),
					credentialBoundOAuthTokenCacheKey("ag:account:43", account),
					"ag:project-43",
					"ag:account:43",
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := &geminiTokenCacheStub{deleteErr: expectedErr}
			invalidator := NewCompositeTokenCacheInvalidator(cache)

			err := invalidator.InvalidateToken(context.Background(), tt.account)

			require.ErrorIs(t, err, expectedErr)
			require.Equal(t, tt.expected(tt.account), cache.deletedKeys)
		})
	}
}

func TestCompositeTokenCacheInvalidator_AllPlatformsIntegration(t *testing.T) {
	// 测试所有平台的缓存键生成和删除
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache)

	accounts := []*Account{
		{ID: 1, Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{"project_id": "gemini-proj"}},
		{ID: 2, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"project_id": "ag-proj"}},
		{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
	}

	// 新行为：Gemini 和 Antigravity 会同时删除基于 project_id 和 account_id 的键
	expectedKeys := []string{
		"gemini:gemini-proj",
		"gemini:account:1",
		"ag:ag-proj",
		"ag:account:2",
		"openai:account:3",
		"claude:account:4",
	}

	for _, acc := range accounts {
		err := invalidator.InvalidateToken(context.Background(), acc)
		require.NoError(t, err)
	}

	require.Equal(t, expectedKeys, cache.deletedKeys)
}

// ========== GetCredentialAsInt64 测试 ==========

func TestAccount_GetCredentialAsInt64(t *testing.T) {
	tests := []struct {
		name        string
		credentials map[string]any
		key         string
		expected    int64
	}{
		{
			name:        "int64_value",
			credentials: map[string]any{"_token_version": int64(1737654321000)},
			key:         "_token_version",
			expected:    1737654321000,
		},
		{
			name:        "float64_value",
			credentials: map[string]any{"_token_version": float64(1737654321000)},
			key:         "_token_version",
			expected:    1737654321000,
		},
		{
			name:        "int_value",
			credentials: map[string]any{"_token_version": 12345},
			key:         "_token_version",
			expected:    12345,
		},
		{
			name:        "string_value",
			credentials: map[string]any{"_token_version": "1737654321000"},
			key:         "_token_version",
			expected:    1737654321000,
		},
		{
			name:        "string_with_spaces",
			credentials: map[string]any{"_token_version": "  1737654321000  "},
			key:         "_token_version",
			expected:    1737654321000,
		},
		{
			name:        "nil_credentials",
			credentials: nil,
			key:         "_token_version",
			expected:    0,
		},
		{
			name:        "missing_key",
			credentials: map[string]any{"other_key": 123},
			key:         "_token_version",
			expected:    0,
		},
		{
			name:        "nil_value",
			credentials: map[string]any{"_token_version": nil},
			key:         "_token_version",
			expected:    0,
		},
		{
			name:        "invalid_string",
			credentials: map[string]any{"_token_version": "not_a_number"},
			key:         "_token_version",
			expected:    0,
		},
		{
			name:        "empty_string",
			credentials: map[string]any{"_token_version": ""},
			key:         "_token_version",
			expected:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{Credentials: tt.credentials}
			result := account.GetCredentialAsInt64(tt.key)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestAccount_GetCredentialAsInt64_NilAccount(t *testing.T) {
	var account *Account
	result := account.GetCredentialAsInt64("_token_version")
	require.Equal(t, int64(0), result)
}

// ========== CheckTokenVersion 测试 ==========

type tokenVersionAccountRepoStub struct {
	mockAccountRepoForGemini
	latest *Account
	err    error
}

func (r *tokenVersionAccountRepoStub) GetByID(context.Context, int64) (*Account, error) {
	return r.latest, r.err
}

func TestCheckTokenVersion(t *testing.T) {
	tests := []struct {
		name          string
		account       *Account
		latestAccount *Account
		repoErr       error
		expectedStale bool
		wantErr       bool
	}{
		{
			name:          "nil_account",
			account:       nil,
			latestAccount: nil,
			expectedStale: false,
			wantErr:       true,
		},
		{
			name: "no_version_in_account_but_db_has_version",
			account: &Account{
				ID:          1,
				Credentials: map[string]any{},
			},
			latestAccount: &Account{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			expectedStale: true, // 当前 account 无版本但 DB 有，说明已被异步刷新，当前已过时
		},
		{
			name: "both_no_version",
			account: &Account{
				ID:          1,
				Credentials: map[string]any{},
			},
			latestAccount: &Account{
				ID:          1,
				Credentials: map[string]any{},
			},
			expectedStale: false, // 两边都没有版本号，说明从未被异步刷新过，允许缓存
		},
		{
			name: "same_version",
			account: &Account{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			latestAccount: &Account{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			expectedStale: false,
		},
		{
			name: "same_version_but_token_material_changed",
			account: &Account{
				ID: 1,
				Credentials: map[string]any{
					"_token_version": int64(100),
					"access_token":   "old-token",
				},
			},
			latestAccount: &Account{
				ID: 1,
				Credentials: map[string]any{
					"_token_version": int64(100),
					"access_token":   "new-token",
				},
			},
			expectedStale: true,
		},
		{
			name: "legacy_zero_versions_but_token_material_changed",
			account: &Account{
				ID:          1,
				Credentials: map[string]any{"access_token": "legacy-old-token"},
			},
			latestAccount: &Account{
				ID:          1,
				Credentials: map[string]any{"access_token": "legacy-new-token"},
			},
			expectedStale: true,
		},
		{
			name: "current_version_newer",
			account: &Account{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(200)},
			},
			latestAccount: &Account{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			expectedStale: false,
		},
		{
			name: "current_version_older_stale",
			account: &Account{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			latestAccount: &Account{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(200)},
			},
			expectedStale: true, // 当前版本过时
		},
		{
			name: "repo_error",
			account: &Account{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			latestAccount: nil,
			repoErr:       errors.New("db error"),
			expectedStale: false,
			wantErr:       true,
		},
		{
			name: "repo_returns_nil",
			account: &Account{
				ID:          1,
				Credentials: map[string]any{"_token_version": int64(100)},
			},
			latestAccount: nil,
			repoErr:       nil,
			expectedStale: false,
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &tokenVersionAccountRepoStub{latest: tt.latestAccount, err: tt.repoErr}
			latest, isStale, err := CheckTokenVersion(context.Background(), tt.account, repo)
			require.Equal(t, tt.expectedStale, isStale)
			if tt.wantErr {
				require.Error(t, err)
				require.Nil(t, latest)
				return
			}
			require.NoError(t, err)
			require.Same(t, tt.latestAccount, latest)
		})
	}
}

func TestCheckTokenVersion_NilRepo(t *testing.T) {
	account := &Account{
		ID:          1,
		Credentials: map[string]any{"_token_version": int64(100)},
	}
	latest, isStale, err := CheckTokenVersion(context.Background(), account, nil)
	require.Error(t, err)
	require.Nil(t, latest)
	require.False(t, isStale)
}
