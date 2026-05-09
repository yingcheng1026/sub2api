//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type chatInternalAPIKeyRepoStub struct {
	keys []service.APIKey
}

func (s *chatInternalAPIKeyRepoStub) Create(context.Context, *service.APIKey) error {
	panic("unexpected Create call")
}
func (s *chatInternalAPIKeyRepoStub) GetByID(context.Context, int64) (*service.APIKey, error) {
	panic("unexpected GetByID call")
}
func (s *chatInternalAPIKeyRepoStub) GetKeyAndOwnerID(context.Context, int64) (string, int64, error) {
	panic("unexpected GetKeyAndOwnerID call")
}
func (s *chatInternalAPIKeyRepoStub) GetByKey(context.Context, string) (*service.APIKey, error) {
	panic("unexpected GetByKey call")
}
func (s *chatInternalAPIKeyRepoStub) GetByKeyForAuth(context.Context, string) (*service.APIKey, error) {
	panic("unexpected GetByKeyForAuth call")
}
func (s *chatInternalAPIKeyRepoStub) Update(context.Context, *service.APIKey) error {
	panic("unexpected Update call")
}
func (s *chatInternalAPIKeyRepoStub) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}
func (s *chatInternalAPIKeyRepoStub) ListByUserID(_ context.Context, userID int64, _ pagination.PaginationParams, filters service.APIKeyListFilters) ([]service.APIKey, *pagination.PaginationResult, error) {
	if filters.Status != service.StatusAPIKeyActive {
		return nil, &pagination.PaginationResult{}, nil
	}
	out := make([]service.APIKey, 0, len(s.keys))
	for _, key := range s.keys {
		if key.UserID == userID && key.Status == service.StatusAPIKeyActive {
			out = append(out, key)
		}
	}
	return out, &pagination.PaginationResult{Total: int64(len(out))}, nil
}
func (s *chatInternalAPIKeyRepoStub) VerifyOwnership(context.Context, int64, []int64) ([]int64, error) {
	panic("unexpected VerifyOwnership call")
}
func (s *chatInternalAPIKeyRepoStub) CountByUserID(context.Context, int64) (int64, error) {
	panic("unexpected CountByUserID call")
}
func (s *chatInternalAPIKeyRepoStub) ExistsByKey(context.Context, string) (bool, error) {
	panic("unexpected ExistsByKey call")
}
func (s *chatInternalAPIKeyRepoStub) ListByGroupID(context.Context, int64, pagination.PaginationParams) ([]service.APIKey, *pagination.PaginationResult, error) {
	panic("unexpected ListByGroupID call")
}
func (s *chatInternalAPIKeyRepoStub) SearchAPIKeys(context.Context, int64, string, int) ([]service.APIKey, error) {
	panic("unexpected SearchAPIKeys call")
}
func (s *chatInternalAPIKeyRepoStub) ClearGroupIDByGroupID(context.Context, int64) (int64, error) {
	panic("unexpected ClearGroupIDByGroupID call")
}
func (s *chatInternalAPIKeyRepoStub) UpdateGroupIDByUserAndGroup(context.Context, int64, int64, int64) (int64, error) {
	panic("unexpected UpdateGroupIDByUserAndGroup call")
}
func (s *chatInternalAPIKeyRepoStub) CountByGroupID(context.Context, int64) (int64, error) {
	panic("unexpected CountByGroupID call")
}
func (s *chatInternalAPIKeyRepoStub) ListKeysByUserID(context.Context, int64) ([]string, error) {
	panic("unexpected ListKeysByUserID call")
}
func (s *chatInternalAPIKeyRepoStub) ListKeysByGroupID(context.Context, int64) ([]string, error) {
	panic("unexpected ListKeysByGroupID call")
}
func (s *chatInternalAPIKeyRepoStub) IncrementQuotaUsed(context.Context, int64, float64) (float64, error) {
	panic("unexpected IncrementQuotaUsed call")
}
func (s *chatInternalAPIKeyRepoStub) UpdateLastUsed(context.Context, int64, time.Time) error {
	panic("unexpected UpdateLastUsed call")
}
func (s *chatInternalAPIKeyRepoStub) IncrementRateLimitUsage(context.Context, int64, float64) error {
	panic("unexpected IncrementRateLimitUsage call")
}
func (s *chatInternalAPIKeyRepoStub) ResetRateLimitWindows(context.Context, int64) error {
	panic("unexpected ResetRateLimitWindows call")
}
func (s *chatInternalAPIKeyRepoStub) GetRateLimitData(context.Context, int64) (*service.APIKeyRateLimitData, error) {
	panic("unexpected GetRateLimitData call")
}

func TestAPIKeyHandlerGetChatDefaultAPIKeyReturnsPlainInternalPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := &chatInternalAPIKeyRepoStub{
		keys: []service.APIKey{{ID: 7, UserID: 42, Key: "sk-chat-user", Status: service.StatusAPIKeyActive}},
	}
	h := NewAPIKeyHandler(service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{}))

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/internal/v1/chat/default-api-key", nil)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})

	h.GetChatDefaultAPIKey(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload map[string]string
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "sk-chat-user", payload["api_key"])
}
