package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// InvalidateAuthCacheByKey 清除指定 API Key 的认证缓存
func (s *APIKeyService) InvalidateAuthCacheByKey(ctx context.Context, key string) {
	if key == "" {
		return
	}
	cacheKey := s.authCacheKey(key)
	s.deleteAuthCache(ctx, cacheKey)
}

// InvalidateAuthCacheByUserID 清除用户相关的 API Key 认证缓存
func (s *APIKeyService) InvalidateAuthCacheByUserID(ctx context.Context, userID int64) {
	_ = s.InvalidateAuthCacheByUserIDReliable(ctx, userID)
}

// InvalidateAuthCacheByUserIDReliable invalidates every local and distributed
// auth cache entry and reports repository, Redis delete, and pub/sub failures.
// Durable billing replay uses this form so a quota exhaustion cannot be
// acknowledged while stale authorization remains active.
func (s *APIKeyService) InvalidateAuthCacheByUserIDReliable(ctx context.Context, userID int64) error {
	if userID <= 0 {
		return nil
	}
	keys, err := s.apiKeyRepo.ListKeysByUserID(ctx, userID)
	if err != nil {
		return err
	}
	return s.deleteAuthCacheByKeysReliable(ctx, keys)
}

// InvalidateAuthCacheByLocatorReliable invalidates one frozen cache locator
// without consulting active API-key rows. This remains reliable after the key
// has been soft-deleted and its raw database value replaced by a tombstone.
func (s *APIKeyService) InvalidateAuthCacheByLocatorReliable(ctx context.Context, locator string) error {
	locator = strings.ToLower(strings.TrimSpace(locator))
	if !validSHA256(locator) {
		return fmt.Errorf("invalid API key auth cache locator")
	}
	return s.deleteAuthCacheReliable(ctx, locator)
}

// InvalidateAuthCacheByGroupID 清除分组相关的 API Key 认证缓存
func (s *APIKeyService) InvalidateAuthCacheByGroupID(ctx context.Context, groupID int64) {
	if groupID <= 0 {
		return
	}
	keys, err := s.apiKeyRepo.ListKeysByGroupID(ctx, groupID)
	if err != nil {
		return
	}
	s.deleteAuthCacheByKeys(ctx, keys)
}

func (s *APIKeyService) deleteAuthCacheByKeys(ctx context.Context, keys []string) {
	_ = s.deleteAuthCacheByKeysReliable(ctx, keys)
}

func (s *APIKeyService) deleteAuthCacheByKeysReliable(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	var invalidateErrors []error
	for _, key := range keys {
		if key == "" {
			continue
		}
		if err := s.deleteAuthCacheReliable(ctx, s.authCacheKey(key)); err != nil {
			invalidateErrors = append(invalidateErrors, err)
		}
	}
	return errors.Join(invalidateErrors...)
}
