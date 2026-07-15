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
	locators, err := s.apiKeyRepo.ListAuthCacheLocatorsByUserID(ctx, userID)
	if err != nil {
		return err
	}
	return s.deleteAuthCacheByLocatorsReliable(ctx, locators)
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
	locators, err := s.apiKeyRepo.ListAuthCacheLocatorsByGroupID(ctx, groupID)
	if err != nil {
		return
	}
	_ = s.deleteAuthCacheByLocatorsReliable(ctx, locators)
}

func (s *APIKeyService) deleteAuthCacheByLocatorsReliable(ctx context.Context, locators []string) error {
	if len(locators) == 0 {
		return nil
	}
	var invalidateErrors []error
	for _, locator := range locators {
		locator = strings.ToLower(strings.TrimSpace(locator))
		if !validSHA256(locator) {
			invalidateErrors = append(invalidateErrors, fmt.Errorf("invalid API key auth cache locator"))
			continue
		}
		if err := s.deleteAuthCacheReliable(ctx, locator); err != nil {
			invalidateErrors = append(invalidateErrors, err)
		}
	}
	return errors.Join(invalidateErrors...)
}
