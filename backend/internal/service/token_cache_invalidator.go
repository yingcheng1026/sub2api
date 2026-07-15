package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
)

type TokenCacheInvalidator interface {
	InvalidateToken(ctx context.Context, account *Account) error
}

type CompositeTokenCacheInvalidator struct {
	cache GeminiTokenCache // 统一使用一个缓存接口，通过缓存键前缀区分平台
}

func NewCompositeTokenCacheInvalidator(cache GeminiTokenCache) *CompositeTokenCacheInvalidator {
	return &CompositeTokenCacheInvalidator{
		cache: cache,
	}
}

func (c *CompositeTokenCacheInvalidator) InvalidateToken(ctx context.Context, account *Account) error {
	if c == nil || c.cache == nil || account == nil {
		return nil
	}
	if account.Type != AccountTypeOAuth {
		return nil
	}

	var keysToDelete []string
	accountIDKey := "account:" + strconv.FormatInt(account.ID, 10)

	switch account.Platform {
	case PlatformGemini:
		// Gemini 可能有两种缓存键：project_id 或 account_id
		// 首次获取 token 时可能没有 project_id，之后自动检测到 project_id 后会使用新 key
		// 刷新时需要同时删除两种可能的 key，确保不会遗留旧缓存
		primaryBaseKey := geminiTokenCacheBaseKey(account)
		accountBaseKey := "gemini:" + accountIDKey
		keysToDelete = append(keysToDelete,
			credentialBoundOAuthTokenCacheKey(primaryBaseKey, account),
			credentialBoundOAuthTokenCacheKey(accountBaseKey, account),
			primaryBaseKey,
			accountBaseKey,
		)
	case PlatformAntigravity:
		// Antigravity 同样可能有两种缓存键
		primaryBaseKey := antigravityTokenCacheBaseKey(account)
		accountBaseKey := "ag:" + accountIDKey
		keysToDelete = append(keysToDelete,
			credentialBoundOAuthTokenCacheKey(primaryBaseKey, account),
			credentialBoundOAuthTokenCacheKey(accountBaseKey, account),
			primaryBaseKey,
			accountBaseKey,
		)
	case PlatformOpenAI:
		baseKey := openAITokenCacheBaseKey(account)
		keysToDelete = append(keysToDelete, credentialBoundOAuthTokenCacheKey(baseKey, account), baseKey)
	case PlatformAnthropic:
		baseKey := claudeTokenCacheBaseKey(account)
		keysToDelete = append(keysToDelete, credentialBoundOAuthTokenCacheKey(baseKey, account), baseKey)
	default:
		return nil
	}

	// 删除所有可能的缓存键（去重后）
	seen := make(map[string]bool)
	var deleteErrors []error
	for _, key := range keysToDelete {
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := c.cache.DeleteAccessToken(ctx, key); err != nil {
			slog.Warn("token_cache_delete_failed", "key", key, "account_id", account.ID, "error", err)
			deleteErrors = append(deleteErrors, fmt.Errorf("delete token cache key %q: %w", key, err))
		}
	}

	return errors.Join(deleteErrors...)
}

// validateOAuthTokenCacheHit checks the authoritative account after a shared
// cache hit and before the cached token can be returned. A failed authority
// read fails closed: callers must not use a token whose credential generation
// can no longer be proven current.
func validateOAuthTokenCacheHit(ctx context.Context, account *Account, repo AccountRepository) (*Account, bool, error) {
	latestAccount, isStale, err := CheckTokenVersion(ctx, account, repo)
	if err != nil {
		return nil, false, fmt.Errorf("validate oauth token cache hit: %w", err)
	}
	if latestAccount.Type != account.Type || latestAccount.Platform != account.Platform {
		return nil, false, fmt.Errorf(
			"validate oauth token cache hit for account %d: authoritative account kind changed",
			account.ID,
		)
	}
	if isStale {
		return latestAccount, false, nil
	}
	if oauthTokenCacheGeneration(account) != oauthTokenCacheGeneration(latestAccount) {
		return nil, false, fmt.Errorf(
			"validate oauth token cache hit for account %d: request version is ahead of authority with a different credential generation",
			account.ID,
		)
	}
	return latestAccount, true, nil
}

// CheckTokenVersion 检查 account 的 token 版本是否已过时，并返回最新的 account
// 用于解决异步刷新任务与请求线程的竞态条件：
// 如果刷新任务已更新 token 并删除缓存，此时请求线程的旧 account 对象不应写入缓存
//
// 返回值:
//   - latestAccount: 从 DB 获取的最新 account
//   - isStale: true 表示 token 已过时（应使用 latestAccount），false 表示可以使用当前 account
//   - err: 权威读取不可用；调用方必须跳过共享缓存写入
func CheckTokenVersion(ctx context.Context, account *Account, repo AccountRepository) (latestAccount *Account, isStale bool, err error) {
	if account == nil {
		return nil, false, fmt.Errorf("check token version: account is nil")
	}
	if repo == nil {
		return nil, false, fmt.Errorf("check token version: account repository is nil")
	}

	currentVersion := account.GetCredentialAsInt64("_token_version")

	latestAccount, err = repo.GetByID(ctx, account.ID)
	if err != nil {
		return nil, false, fmt.Errorf("check token version for account %d: %w", account.ID, err)
	}
	if latestAccount == nil {
		return nil, false, fmt.Errorf("check token version for account %d: authoritative account not found", account.ID)
	}

	latestVersion := latestAccount.GetCredentialAsInt64("_token_version")

	// A larger authoritative version always supersedes the request snapshot.
	if latestVersion > currentVersion {
		slog.Debug("token_version_stale",
			"account_id", account.ID,
			"current_version", currentVersion,
			"latest_version", latestVersion)
		return latestAccount, true, nil
	}
	// A request-local account may be the freshly persisted refresh result while
	// a lagging read still exposes the previous version. Do not roll it back.
	if currentVersion > latestVersion {
		return latestAccount, false, nil
	}

	// Equal versions (including legacy zero/zero rows) still require the token
	// material to match. This closes generic/bulk credential replacement paths
	// that predate _token_version stamping.
	if oauthTokenCacheGeneration(account) != oauthTokenCacheGeneration(latestAccount) {
		slog.Debug("token_credential_generation_stale",
			"account_id", account.ID,
			"current_version", currentVersion,
			"latest_version", latestVersion)
		return latestAccount, true, nil
	}

	return latestAccount, false, nil
}
