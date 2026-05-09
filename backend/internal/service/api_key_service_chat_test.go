//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyServiceDefaultChatAPIKeySkipsExpiredAndExhaustedKeys(t *testing.T) {
	expiredAt := time.Now().Add(-time.Hour)
	repo := &authRepoStub{
		listByUserID: func(_ context.Context, userID int64, params pagination.PaginationParams, filters APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error) {
			require.Equal(t, int64(42), userID)
			require.Equal(t, 1, params.Page)
			require.Equal(t, 10, params.PageSize)
			require.Equal(t, "created_at", params.SortBy)
			require.Equal(t, "desc", params.SortOrder)
			require.Equal(t, StatusAPIKeyActive, filters.Status)

			return []APIKey{
				{ID: 1, UserID: userID, Key: "sk-expired", Status: StatusAPIKeyActive, ExpiresAt: &expiredAt},
				{ID: 2, UserID: userID, Key: "sk-exhausted", Status: StatusAPIKeyActive, Quota: 10, QuotaUsed: 10},
				{ID: 3, UserID: userID, Key: "sk-usable", Status: StatusAPIKeyActive, Quota: 10, QuotaUsed: 2},
			}, &pagination.PaginationResult{Total: 3}, nil
		},
	}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{})

	key, err := svc.DefaultChatAPIKey(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, "sk-usable", key)
}

func TestAPIKeyServiceDefaultChatAPIKeyReturnsNotFoundWhenNoUsableKey(t *testing.T) {
	repo := &authRepoStub{
		listByUserID: func(_ context.Context, userID int64, _ pagination.PaginationParams, filters APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error) {
			require.Equal(t, int64(42), userID)
			require.Equal(t, StatusAPIKeyActive, filters.Status)
			return []APIKey{{ID: 1, UserID: userID, Key: "sk-exhausted", Status: StatusAPIKeyActive, Quota: 1, QuotaUsed: 1}}, &pagination.PaginationResult{Total: 1}, nil
		},
	}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{})

	key, err := svc.DefaultChatAPIKey(context.Background(), 42)

	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	require.Empty(t, key)
}
