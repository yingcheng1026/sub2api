package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type walletRouteModelRouterStub struct {
	groupID int64
	err     error
	model   string
	userID  int64
}

func (s *walletRouteModelRouterStub) ResolveGroupID(ctx context.Context, userID int64, modelName string) (int64, error) {
	s.userID = userID
	s.model = modelName
	if s.err != nil {
		return 0, s.err
	}
	return s.groupID, nil
}

type walletRouteGroupGetterStub struct {
	group *service.Group
	err   error
}

func (s *walletRouteGroupGetterStub) GetByID(ctx context.Context, id int64) (*service.Group, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.group == nil || s.group.ID != id {
		return nil, service.ErrGroupNotFound
	}
	clone := *s.group
	return &clone, nil
}

func TestAPIKeyAuthWalletUniversalKeyRoutesByModelAndRestoresBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     0,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     100,
		UserID: user.ID,
		Key:    "wallet-any-key",
		Status: service.StatusActive,
		User:   user,
	}
	apiKeyRepo := fakeAPIKeyRepo{getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
		if key != apiKey.Key {
			return nil, service.ErrAPIKeyNotFound
		}
		clone := *apiKey
		return &clone, nil
	}}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)

	balance := 100.0
	walletSub := &service.UserSubscription{
		ID:               201,
		UserID:           user.ID,
		GroupID:          nil,
		Status:           service.SubscriptionStatusActive,
		ExpiresAt:        time.Now().Add(24 * time.Hour),
		WalletBalanceUSD: &balance,
	}
	subscriptionService := service.NewSubscriptionService(nil, fakeGoogleSubscriptionRepo{
		getActiveWallet: func(ctx context.Context, userID int64) (*service.UserSubscription, error) {
			if userID != user.ID {
				return nil, service.ErrSubscriptionNotFound
			}
			clone := *walletSub
			return &clone, nil
		},
	}, nil, nil, cfg)
	t.Cleanup(subscriptionService.Stop)

	targetGroup := &service.Group{
		ID:       3,
		Name:     "claude-sonnet",
		Status:   service.StatusActive,
		Platform: service.PlatformAnthropic,
		Hydrated: true,
	}
	modelRouter := &walletRouteModelRouterStub{groupID: targetGroup.ID}
	groupGetter := &walletRouteGroupGetterStub{group: targetGroup}

	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddlewareWithRouter(apiKeyService, subscriptionService, modelRouter, groupGetter, cfg)))
	body := `{"model":"claude-sonnet-4-6","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`
	router.POST("/t", func(c *gin.Context) {
		routedAPIKey, ok := GetAPIKeyFromContext(c)
		require.True(t, ok)
		require.NotNil(t, routedAPIKey.GroupID)
		require.Equal(t, targetGroup.ID, *routedAPIKey.GroupID)

		groupFromCtx, ok := c.Request.Context().Value(ctxkey.Group).(*service.Group)
		require.True(t, ok)
		require.Equal(t, targetGroup.ID, groupFromCtx.ID)

		restored, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.JSONEq(t, body, string(restored))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/t", strings.NewReader(body))
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, user.ID, modelRouter.userID)
	require.Equal(t, "claude-sonnet-4-6", modelRouter.model)
}

func TestAPIKeyAuthWalletUniversalKeyRejectsUnsupportedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{ID: 7, Role: service.RoleUser, Status: service.StatusActive, Balance: 0, Concurrency: 3}
	apiKey := &service.APIKey{ID: 100, UserID: user.ID, Key: "wallet-any-key", Status: service.StatusActive, User: user}
	apiKeyService := service.NewAPIKeyService(fakeAPIKeyRepo{getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
		clone := *apiKey
		return &clone, nil
	}}, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeStandard})

	balance := 100.0
	subscriptionService := service.NewSubscriptionService(nil, fakeGoogleSubscriptionRepo{
		getActiveWallet: func(ctx context.Context, userID int64) (*service.UserSubscription, error) {
			return &service.UserSubscription{
				ID:               201,
				UserID:           user.ID,
				Status:           service.SubscriptionStatusActive,
				ExpiresAt:        time.Now().Add(24 * time.Hour),
				WalletBalanceUSD: &balance,
			}, nil
		},
	}, nil, nil, &config.Config{RunMode: config.RunModeStandard})
	t.Cleanup(subscriptionService.Stop)

	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddlewareWithRouter(
		apiKeyService,
		subscriptionService,
		&walletRouteModelRouterStub{err: service.ErrModelUnsupported},
		&walletRouteGroupGetterStub{},
		&config.Config{RunMode: config.RunModeStandard},
	)))
	router.POST("/t", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/t", strings.NewReader(`{"model":"unknown-model-xyz"}`))
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "model_unsupported")
}
