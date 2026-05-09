package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type internalChatAPIKeyRepoStub struct {
	keys []service.APIKey
}

func (s *internalChatAPIKeyRepoStub) Create(context.Context, *service.APIKey) error {
	panic("unexpected Create call")
}
func (s *internalChatAPIKeyRepoStub) GetByID(context.Context, int64) (*service.APIKey, error) {
	panic("unexpected GetByID call")
}
func (s *internalChatAPIKeyRepoStub) GetKeyAndOwnerID(context.Context, int64) (string, int64, error) {
	panic("unexpected GetKeyAndOwnerID call")
}
func (s *internalChatAPIKeyRepoStub) GetByKey(context.Context, string) (*service.APIKey, error) {
	panic("unexpected GetByKey call")
}
func (s *internalChatAPIKeyRepoStub) GetByKeyForAuth(context.Context, string) (*service.APIKey, error) {
	panic("unexpected GetByKeyForAuth call")
}
func (s *internalChatAPIKeyRepoStub) Update(context.Context, *service.APIKey) error {
	panic("unexpected Update call")
}
func (s *internalChatAPIKeyRepoStub) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}
func (s *internalChatAPIKeyRepoStub) ListByUserID(_ context.Context, userID int64, _ pagination.PaginationParams, filters service.APIKeyListFilters) ([]service.APIKey, *pagination.PaginationResult, error) {
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
func (s *internalChatAPIKeyRepoStub) VerifyOwnership(context.Context, int64, []int64) ([]int64, error) {
	panic("unexpected VerifyOwnership call")
}
func (s *internalChatAPIKeyRepoStub) CountByUserID(context.Context, int64) (int64, error) {
	panic("unexpected CountByUserID call")
}
func (s *internalChatAPIKeyRepoStub) ExistsByKey(context.Context, string) (bool, error) {
	panic("unexpected ExistsByKey call")
}
func (s *internalChatAPIKeyRepoStub) ListByGroupID(context.Context, int64, pagination.PaginationParams) ([]service.APIKey, *pagination.PaginationResult, error) {
	panic("unexpected ListByGroupID call")
}
func (s *internalChatAPIKeyRepoStub) SearchAPIKeys(context.Context, int64, string, int) ([]service.APIKey, error) {
	panic("unexpected SearchAPIKeys call")
}
func (s *internalChatAPIKeyRepoStub) ClearGroupIDByGroupID(context.Context, int64) (int64, error) {
	panic("unexpected ClearGroupIDByGroupID call")
}
func (s *internalChatAPIKeyRepoStub) UpdateGroupIDByUserAndGroup(context.Context, int64, int64, int64) (int64, error) {
	panic("unexpected UpdateGroupIDByUserAndGroup call")
}
func (s *internalChatAPIKeyRepoStub) CountByGroupID(context.Context, int64) (int64, error) {
	panic("unexpected CountByGroupID call")
}
func (s *internalChatAPIKeyRepoStub) ListKeysByUserID(context.Context, int64) ([]string, error) {
	panic("unexpected ListKeysByUserID call")
}
func (s *internalChatAPIKeyRepoStub) ListKeysByGroupID(context.Context, int64) ([]string, error) {
	panic("unexpected ListKeysByGroupID call")
}
func (s *internalChatAPIKeyRepoStub) IncrementQuotaUsed(context.Context, int64, float64) (float64, error) {
	panic("unexpected IncrementQuotaUsed call")
}
func (s *internalChatAPIKeyRepoStub) UpdateLastUsed(context.Context, int64, time.Time) error {
	panic("unexpected UpdateLastUsed call")
}
func (s *internalChatAPIKeyRepoStub) IncrementRateLimitUsage(context.Context, int64, float64) error {
	panic("unexpected IncrementRateLimitUsage call")
}
func (s *internalChatAPIKeyRepoStub) ResetRateLimitWindows(context.Context, int64) error {
	panic("unexpected ResetRateLimitWindows call")
}
func (s *internalChatAPIKeyRepoStub) GetRateLimitData(context.Context, int64) (*service.APIKeyRateLimitData, error) {
	panic("unexpected GetRateLimitData call")
}

func TestInternalChatRoutesRequireServiceTokenAndForwardUserAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("HFC_CHAT_INTERNAL_TOKEN", "service-token")

	router := gin.New()
	apiKeyService := service.NewAPIKeyService(&internalChatAPIKeyRepoStub{
		keys: []service.APIKey{{ID: 9, UserID: 42, Key: "sk-chat-user", Status: service.StatusAPIKeyActive}},
	}, nil, nil, nil, nil, nil, &config.Config{})

	var jwtCalled int
	RegisterInternalChatRoutes(router, &handler.Handlers{
		APIKey: handler.NewAPIKeyHandler(apiKeyService),
	}, servermiddleware.JWTAuthMiddleware(func(c *gin.Context) {
		jwtCalled++
		require.Equal(t, "Bearer user-jwt", c.GetHeader("Authorization"))
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 42})
		c.Next()
	}))

	denied := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodGet, "/internal/v1/chat/default-api-key", nil)
	badReq.Header.Set("X-HFC-Internal-Token", "wrong")
	router.ServeHTTP(denied, badReq)
	require.Equal(t, http.StatusUnauthorized, denied.Code)
	require.Zero(t, jwtCalled)

	allowed := httptest.NewRecorder()
	goodReq := httptest.NewRequest(http.MethodGet, "/internal/v1/chat/default-api-key", nil)
	goodReq.Header.Set("X-HFC-Internal-Token", "service-token")
	goodReq.Header.Set("X-HFC-User-Authorization", "Bearer user-jwt")
	router.ServeHTTP(allowed, goodReq)
	require.Equal(t, http.StatusOK, allowed.Code)
	require.Contains(t, allowed.Body.String(), "sk-chat-user")
	require.Equal(t, 1, jwtCalled)
}
