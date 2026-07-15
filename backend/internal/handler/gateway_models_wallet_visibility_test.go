package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type walletModelsProviderStub struct {
	byGroup map[int64][]string
	calls   []int64
}

func (s *walletModelsProviderStub) GetAvailableModels(_ context.Context, groupID *int64, _ string) []string {
	if groupID == nil {
		return nil
	}
	s.calls = append(s.calls, *groupID)
	return append([]string(nil), s.byGroup[*groupID]...)
}

func TestGatewayModelsUniversalWalletMergesAuthorizedGroupsAndDeduplicates(t *testing.T) {
	openAI := service.Group{
		ID: 3, Name: service.WalletDefaultOpenAIGroupName, Platform: service.PlatformOpenAI,
		Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard,
	}
	vip := service.Group{
		ID: 22, Name: service.WalletDefaultVIPGroupName, Platform: service.PlatformAnthropic,
		Status: service.StatusActive, Hydrated: true, IsExclusive: true, SubscriptionType: service.SubscriptionTypeStandard,
	}
	provider := &walletModelsProviderStub{byGroup: map[int64][]string{
		openAI.ID: {"gpt-5.6-high", "claude-sonnet-4-6"},
		vip.ID:    {"claude-sonnet-4-6", "claude-sonnet-4-6", "fable5", "gpt-5.6-high"},
	}}
	h := &GatewayHandler{modelsProvider: provider}
	user := &service.User{ID: 91, AllowedGroups: []int64{vip.ID}}

	ids := performWalletModelsHandlerRequest(t, h, user, middleware.WalletModelVisibility{
		APIKeyID: 930,
		Groups:   []service.Group{openAI, vip, vip},
		Routes:   service.DefaultModelRoutes(),
	})

	require.Equal(t, []int64{openAI.ID, vip.ID}, provider.calls, "duplicate route groups must be fetched once")
	require.Equal(t, map[string]int{
		"gpt-5.6-high":      1,
		"claude-sonnet-4-6": 1,
		"fable5":            1,
	}, countModelIDs(ids))
}

func TestGatewayModelsUniversalWalletDoesNotLeakVIPModelsWithoutGrant(t *testing.T) {
	openAI := service.Group{
		ID: 3, Name: service.WalletDefaultOpenAIGroupName, Platform: service.PlatformOpenAI,
		Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard,
	}
	provider := &walletModelsProviderStub{byGroup: map[int64][]string{
		openAI.ID: {"gpt-5.6-high", "claude-opus-4-7", "fable5"},
	}}
	h := &GatewayHandler{modelsProvider: provider}
	user := &service.User{ID: 92}

	ids := performWalletModelsHandlerRequest(t, h, user, middleware.WalletModelVisibility{
		APIKeyID: 930,
		Groups:   []service.Group{openAI},
		Routes:   service.DefaultModelRoutes(),
	})

	require.Equal(t, []string{"gpt-5.6-high"}, ids)
}

func TestGatewayModelsReturnsControlledErrorWhenModelServiceIsMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)

	(&GatewayHandler{}).Models(c)

	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
}

func performWalletModelsHandlerRequest(t *testing.T, h *GatewayHandler, user *service.User, visibility middleware.WalletModelVisibility) []string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	groupID := int64(3)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID: visibility.APIKeyID, Name: service.WalletUniversalAPIKeyName, Purpose: service.APIKeyPurposeWalletUniversal,
		GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
		UserID: user.ID, User: user,
	})
	c.Set(string(middleware.ContextKeyWalletModelVisibility), visibility)

	h.Models(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	ids := make([]string, 0, len(response.Data))
	for _, model := range response.Data {
		ids = append(ids, model.ID)
	}
	return ids
}

func countModelIDs(ids []string) map[string]int {
	counts := make(map[string]int, len(ids))
	for _, id := range ids {
		counts[id]++
	}
	return counts
}
