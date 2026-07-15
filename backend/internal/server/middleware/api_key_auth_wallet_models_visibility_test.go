package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type walletModelsGroupRepoStub struct {
	groups []service.Group
}

func (s *walletModelsGroupRepoStub) ListActive(context.Context) ([]service.Group, error) {
	return append([]service.Group(nil), s.groups...), nil
}

type walletModelsGroupGetterStub struct {
	groups    map[int64]*service.Group
	requested []int64
}

func (s *walletModelsGroupGetterStub) GetByID(_ context.Context, id int64) (*service.Group, error) {
	s.requested = append(s.requested, id)
	group, ok := s.groups[id]
	if !ok {
		return nil, service.ErrGroupNotFound
	}
	clone := *group
	return &clone, nil
}

func TestAPIKeyAuthWalletUniversalModelsVisibilityUsesOnlyAuthorizedRouteGroups(t *testing.T) {
	openAI := walletModelsTestGroup(3, service.WalletDefaultOpenAIGroupName, service.PlatformOpenAI, false)
	vip := walletModelsTestGroup(22, service.WalletDefaultVIPGroupName, service.PlatformAnthropic, true)
	routes := []service.ModelRoute{
		{Pattern: "claude-sonnet-*", GroupName: service.WalletDefaultVIPGroupName, ExampleModel: "claude-sonnet-4-6"},
		{Pattern: "claude-opus-*", GroupName: service.WalletDefaultVIPGroupName, ExampleModel: "claude-opus-4-7"},
		{Pattern: "gpt-*", GroupName: service.WalletDefaultOpenAIGroupName, ExampleModel: "gpt-5.6-high"},
		{Pattern: "o1-*", GroupName: service.WalletDefaultOpenAIGroupName, ExampleModel: "o1-preview"},
	}

	for _, tt := range []struct {
		name          string
		allowedGroups []int64
		wantGroups    []string
	}{
		{
			name:       "ungranted user sees only openai default",
			wantGroups: []string{service.WalletDefaultOpenAIGroupName},
		},
		{
			name:          "explicit vip grant adds vip once",
			allowedGroups: []int64{vip.ID},
			wantGroups:    []string{service.WalletDefaultOpenAIGroupName, service.WalletDefaultVIPGroupName},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			user := &service.User{
				ID:            900,
				Role:          service.RoleUser,
				Status:        service.StatusActive,
				Concurrency:   2,
				AllowedGroups: append([]int64(nil), tt.allowedGroups...),
			}
			apiKey := &service.APIKey{
				ID: 901, UserID: user.ID, Key: "wallet-models-key",
				Name: service.WalletUniversalAPIKeyName, Purpose: service.APIKeyPurposeWalletUniversal, Status: service.StatusActive, User: user,
			}
			modelRouter := service.NewModelRouterServiceWithRoutes(
				&walletModelsGroupRepoStub{groups: []service.Group{openAI, vip}},
				routes,
			)
			groupGetter := &walletModelsGroupGetterStub{groups: map[int64]*service.Group{
				openAI.ID: &openAI,
				vip.ID:    &vip,
			}}

			router := walletModelsAuthTestRouter(t, apiKey, modelRouter, groupGetter, func(c *gin.Context) {
				visibility, ok := GetWalletModelVisibilityFromContext(c)
				require.True(t, ok)
				require.Equal(t, apiKey.ID, visibility.APIKeyID)
				actualNames := make([]string, 0, len(visibility.Groups))
				for _, group := range visibility.Groups {
					actualNames = append(actualNames, group.Name)
				}
				require.Equal(t, tt.wantGroups, actualNames)

				routedKey, routed := GetAPIKeyFromContext(c)
				require.True(t, routed)
				require.NotNil(t, routedKey.GroupID)
				require.Equal(t, openAI.ID, *routedKey.GroupID)
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req.Header.Set("x-api-key", apiKey.Key)
			router.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		})
	}
}

func TestAPIKeyAuthWalletUniversalUsageKeepsSingleDefaultRoute(t *testing.T) {
	openAI := walletModelsTestGroup(3, service.WalletDefaultOpenAIGroupName, service.PlatformOpenAI, false)
	vip := walletModelsTestGroup(22, service.WalletDefaultVIPGroupName, service.PlatformAnthropic, true)
	user := &service.User{
		ID: 910, Role: service.RoleUser, Status: service.StatusActive,
		Concurrency: 2, AllowedGroups: []int64{vip.ID},
	}
	apiKey := &service.APIKey{
		ID: 911, UserID: user.ID, Key: "wallet-usage-key",
		Name: service.WalletUniversalAPIKeyName, Purpose: service.APIKeyPurposeWalletUniversal, Status: service.StatusActive, User: user,
	}
	modelRouter := service.NewModelRouterServiceWithRoutes(
		&walletModelsGroupRepoStub{groups: []service.Group{openAI, vip}},
		service.DefaultModelRoutes(),
	)
	groupGetter := &walletModelsGroupGetterStub{groups: map[int64]*service.Group{
		openAI.ID: &openAI,
		vip.ID:    &vip,
	}}

	router := walletModelsAuthTestRouter(t, apiKey, modelRouter, groupGetter, func(c *gin.Context) {
		_, hasVisibility := GetWalletModelVisibilityFromContext(c)
		require.False(t, hasVisibility, "/v1/usage must not receive multi-group model visibility")
		routedKey, ok := GetAPIKeyFromContext(c)
		require.True(t, ok)
		require.NotNil(t, routedKey.GroupID)
		require.Equal(t, openAI.ID, *routedKey.GroupID)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []int64{openAI.ID}, groupGetter.requested, "usage metadata must not resolve or expose vip")
}

func walletModelsAuthTestRouter(
	t *testing.T,
	apiKey *service.APIKey,
	modelRouter service.ModelRouter,
	groupGetter apiKeyAuthGroupGetter,
	handler func(*gin.Context),
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeStandard}
	apiKeyService := service.NewAPIKeyService(fakeAPIKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
		clone := *apiKey
		return &clone, nil
	}}, nil, nil, nil, nil, nil, cfg)
	walletBalance := 100.0
	subscriptionService := service.NewSubscriptionService(nil, fakeGoogleSubscriptionRepo{
		getActiveWallet: func(context.Context, int64) (*service.UserSubscription, error) {
			return &service.UserSubscription{
				ID: 920, UserID: apiKey.UserID, Status: service.SubscriptionStatusActive,
				ExpiresAt: service.MaxExpiresAt, WalletBalanceUSD: &walletBalance,
			}, nil
		},
	}, nil, nil, cfg)
	t.Cleanup(subscriptionService.Stop)

	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddlewareWithRouter(
		apiKeyService,
		subscriptionService,
		modelRouter,
		groupGetter,
		cfg,
	)))
	router.GET("/v1/models", func(c *gin.Context) {
		handler(c)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	router.GET("/v1/usage", func(c *gin.Context) {
		handler(c)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

func walletModelsTestGroup(id int64, name, platform string, exclusive bool) service.Group {
	return service.Group{
		ID: id, Name: name, Platform: platform, IsExclusive: exclusive,
		Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard,
	}
}
