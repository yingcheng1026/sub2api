package middleware

import (
	"context"
	"errors"
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
		ID:      100,
		UserID:  user.ID,
		Key:     "wallet-any-key",
		Name:    service.WalletUniversalAPIKeyName,
		Purpose: service.APIKeyPurposeWalletUniversal,
		Status:  service.StatusActive,
		User:    user,
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
		ExpiresAt:        service.MaxExpiresAt,
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
		ID:               3,
		Name:             "openai-default",
		Status:           service.StatusActive,
		Platform:         service.PlatformOpenAI,
		Hydrated:         true,
		SubscriptionType: service.SubscriptionTypeStandard,
	}
	modelRouter := &walletRouteModelRouterStub{groupID: targetGroup.ID}
	groupGetter := &walletRouteGroupGetterStub{group: targetGroup}

	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddlewareWithRouter(apiKeyService, subscriptionService, modelRouter, groupGetter, cfg)))
	body := `{"model":"gpt-5.6-high","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`
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
	require.Equal(t, "gpt-5.6-high", modelRouter.model)
}

func TestAPIKeyAuthWalletUniversalKeyRejectsUnsupportedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{ID: 7, Role: service.RoleUser, Status: service.StatusActive, Balance: 0, Concurrency: 3}
	apiKey := &service.APIKey{ID: 100, UserID: user.ID, Key: "wallet-any-key", Name: service.WalletUniversalAPIKeyName, Purpose: service.APIKeyPurposeWalletUniversal, Status: service.StatusActive, User: user}
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
				ExpiresAt:        service.MaxExpiresAt,
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

func TestAPIKeyAuthWalletUniversalKeyUsesOpenAIDefaultForMetadataAndResponsesWebSocket(t *testing.T) {
	user := &service.User{ID: 71, Role: service.RoleUser, Status: service.StatusActive, Concurrency: 3}
	apiKey := &service.APIKey{ID: 101, UserID: user.ID, Key: "wallet-metadata-key", Name: service.WalletUniversalAPIKeyName, Purpose: service.APIKeyPurposeWalletUniversal, Status: service.StatusActive, User: user}
	apiKeyService := service.NewAPIKeyService(fakeAPIKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
		clone := *apiKey
		return &clone, nil
	}}, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeStandard})
	balance := 100.0
	wallet := &service.UserSubscription{ID: 202, UserID: user.ID, Status: service.SubscriptionStatusActive, ExpiresAt: service.MaxExpiresAt, WalletBalanceUSD: &balance}
	subscriptionService := service.NewSubscriptionService(nil, fakeGoogleSubscriptionRepo{getActiveWallet: func(context.Context, int64) (*service.UserSubscription, error) {
		clone := *wallet
		return &clone, nil
	}}, nil, nil, &config.Config{RunMode: config.RunModeStandard})
	t.Cleanup(subscriptionService.Stop)
	openAI := &service.Group{ID: 3, Name: service.WalletDefaultOpenAIGroupName, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard}

	for _, path := range []string{"/v1/models", "/v1/usage", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			router := gin.New()
			router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddlewareWithRouter(
				apiKeyService, subscriptionService, &walletRouteModelRouterStub{groupID: openAI.ID}, &walletRouteGroupGetterStub{group: openAI}, &config.Config{RunMode: config.RunModeStandard},
			)))
			router.GET(path, func(c *gin.Context) {
				routedKey, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				require.NotNil(t, routedKey.GroupID)
				require.Equal(t, openAI.ID, *routedKey.GroupID)
				sub, ok := GetSubscriptionFromContext(c)
				require.True(t, ok)
				require.Equal(t, wallet.ID, sub.ID)
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("x-api-key", apiKey.Key)
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
		})
	}
}

func TestAPIKeyAuthUsageRejectsArbitraryNullGroupKey(t *testing.T) {
	user := &service.User{ID: 72, Role: service.RoleUser, Status: service.StatusActive, Balance: 100, Concurrency: 3}
	apiKey := &service.APIKey{ID: 102, UserID: user.ID, Key: "manual-null-usage-key", Name: "manual", Status: service.StatusActive, User: user}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	apiKeyService := service.NewAPIKeyService(fakeAPIKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
		clone := *apiKey
		return &clone, nil
	}}, nil, nil, nil, nil, nil, cfg)
	subscriptionService := service.NewSubscriptionService(nil, fakeGoogleSubscriptionRepo{}, nil, nil, cfg)
	t.Cleanup(subscriptionService.Stop)
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddlewareWithRouter(apiKeyService, subscriptionService, nil, nil, cfg)))
	router.GET("/v1/usage", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "WALLET_KEY_INVALID")
}

func TestAPIKeyAuthWalletUniversalKeyRequiresExplicitVIPGrant(t *testing.T) {
	for _, tt := range []struct {
		name          string
		allowedGroups []int64
		wantStatus    int
	}{
		{name: "missing grant is rejected", wantStatus: http.StatusForbidden},
		{name: "explicit grant is accepted", allowedGroups: []int64{22}, wantStatus: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			user := &service.User{
				ID:            77,
				Role:          service.RoleUser,
				Status:        service.StatusActive,
				Concurrency:   3,
				AllowedGroups: append([]int64(nil), tt.allowedGroups...),
			}
			apiKey := &service.APIKey{ID: 700, UserID: user.ID, Key: "wallet-vip-key", Name: service.WalletUniversalAPIKeyName, Purpose: service.APIKeyPurposeWalletUniversal, Status: service.StatusActive, User: user}
			vip := &service.Group{
				ID:               22,
				Name:             "vip",
				Status:           service.StatusActive,
				Platform:         service.PlatformAnthropic,
				Hydrated:         true,
				IsExclusive:      true,
				SubscriptionType: service.SubscriptionTypeStandard,
			}

			w := performWalletRouteRequest(t, apiKey, &service.UserSubscription{
				ID:               701,
				UserID:           user.ID,
				Status:           service.SubscriptionStatusActive,
				ExpiresAt:        service.MaxExpiresAt,
				WalletBalanceUSD: float64Ptr(50),
			}, &walletRouteModelRouterStub{groupID: vip.ID}, &walletRouteGroupGetterStub{group: vip}, `{"model":"claude-sonnet-4-6"}`, nil)

			require.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusForbidden {
				require.Contains(t, w.Body.String(), "GROUP_NOT_ALLOWED")
			}
		})
	}
}

func TestAPIKeyAuthWalletUniversalKeyRejectsOtherPublicGroups(t *testing.T) {
	user := &service.User{ID: 78, Role: service.RoleUser, Status: service.StatusActive, Concurrency: 3}
	apiKey := &service.APIKey{ID: 710, UserID: user.ID, Key: "wallet-public-key", Name: service.WalletUniversalAPIKeyName, Purpose: service.APIKeyPurposeWalletUniversal, Status: service.StatusActive, User: user}
	otherPublic := &service.Group{
		ID:               30,
		Name:             "some-public-group",
		Status:           service.StatusActive,
		Platform:         service.PlatformOpenAI,
		Hydrated:         true,
		SubscriptionType: service.SubscriptionTypeStandard,
	}

	w := performWalletRouteRequest(t, apiKey, &service.UserSubscription{
		ID:               711,
		UserID:           user.ID,
		Status:           service.SubscriptionStatusActive,
		ExpiresAt:        service.MaxExpiresAt,
		WalletBalanceUSD: float64Ptr(50),
	}, &walletRouteModelRouterStub{groupID: otherPublic.ID}, &walletRouteGroupGetterStub{group: otherPublic}, `{"model":"gpt-5.6-high"}`, nil)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "GROUP_NOT_ALLOWED")
}

func TestAPIKeyAuthWalletUniversalKeyRejectsNonVIPExclusiveGroupEvenWhenGranted(t *testing.T) {
	user := &service.User{ID: 781, Role: service.RoleUser, Status: service.StatusActive, Concurrency: 3, AllowedGroups: []int64{29}}
	apiKey := &service.APIKey{ID: 718, UserID: user.ID, Key: "wallet-wrong-vip-key", Name: service.WalletUniversalAPIKeyName, Purpose: service.APIKeyPurposeWalletUniversal, Status: service.StatusActive, User: user}
	wrongVIP := &service.Group{
		ID:               29,
		Name:             "Claude VIP",
		Status:           service.StatusActive,
		Platform:         service.PlatformAnthropic,
		Hydrated:         true,
		IsExclusive:      true,
		SubscriptionType: service.SubscriptionTypeStandard,
	}

	w := performWalletRouteRequest(t, apiKey, &service.UserSubscription{
		ID:               719,
		UserID:           user.ID,
		Status:           service.SubscriptionStatusActive,
		ExpiresAt:        service.MaxExpiresAt,
		WalletBalanceUSD: float64Ptr(50),
	}, &walletRouteModelRouterStub{groupID: wrongVIP.ID}, &walletRouteGroupGetterStub{group: wrongVIP}, `{"model":"claude-sonnet-4-6"}`, nil)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "GROUP_NOT_ALLOWED")
}

func TestAPIKeyAuthWalletUniversalKeyRejectsMissingPermanentCreditsWallet(t *testing.T) {
	for _, tt := range []struct {
		name   string
		wallet *service.UserSubscription
	}{
		{name: "no wallet"},
		{
			name: "finite legacy wallet",
			wallet: &service.UserSubscription{
				ID:               717,
				Status:           service.SubscriptionStatusActive,
				ExpiresAt:        time.Now().Add(30 * 24 * time.Hour),
				WalletBalanceUSD: float64Ptr(50),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			user := &service.User{ID: 782, Role: service.RoleUser, Status: service.StatusActive, Balance: 100, Concurrency: 3}
			apiKey := &service.APIKey{ID: 719, UserID: user.ID, Key: "stale-wallet-universal-key", Name: service.WalletUniversalAPIKeyName, Purpose: service.APIKeyPurposeWalletUniversal, Status: service.StatusActive, User: user}

			w := performWalletRouteRequest(t, apiKey, tt.wallet, nil, nil, `{"model":"gpt-5.6-high"}`, nil)

			require.Equal(t, http.StatusForbidden, w.Code)
			require.Contains(t, w.Body.String(), "WALLET_NOT_ACTIVE")
		})
	}
}

func TestAPIKeyAuthWalletUniversalKeyRejectsArbitraryNullGroupKey(t *testing.T) {
	user := &service.User{ID: 783, Role: service.RoleUser, Status: service.StatusActive, Balance: 100, Concurrency: 3}
	apiKey := &service.APIKey{ID: 720, UserID: user.ID, Key: "manual-null-group-key", Name: "manual unscoped key", Status: service.StatusActive, User: user}

	w := performWalletRouteRequest(t, apiKey, &service.UserSubscription{
		ID:               718,
		UserID:           user.ID,
		Status:           service.SubscriptionStatusActive,
		ExpiresAt:        service.MaxExpiresAt,
		WalletBalanceUSD: float64Ptr(50),
	}, &walletRouteModelRouterStub{groupID: 3}, &walletRouteGroupGetterStub{group: &service.Group{
		ID: 3, Name: service.WalletDefaultOpenAIGroupName, Platform: service.PlatformOpenAI,
		Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard,
	}}, `{"model":"gpt-5.6-high"}`, nil)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "WALLET_KEY_INVALID")
}

func TestAPIKeyAuthWalletUniversalKeyRejectsReservedNameWithoutPurpose(t *testing.T) {
	user := &service.User{ID: 784, Role: service.RoleUser, Status: service.StatusActive, Balance: 100, Concurrency: 3}
	apiKey := &service.APIKey{
		ID:      721,
		UserID:  user.ID,
		Key:     "forged-wallet-name-key",
		Name:    service.WalletUniversalAPIKeyName,
		Purpose: service.APIKeyPurposeStandard,
		Status:  service.StatusActive,
		User:    user,
	}

	w := performWalletRouteRequest(t, apiKey, &service.UserSubscription{
		ID:               719,
		UserID:           user.ID,
		Status:           service.SubscriptionStatusActive,
		ExpiresAt:        service.MaxExpiresAt,
		WalletBalanceUSD: float64Ptr(50),
	}, &walletRouteModelRouterStub{groupID: 3}, &walletRouteGroupGetterStub{group: &service.Group{
		ID: 3, Name: service.WalletDefaultOpenAIGroupName, Platform: service.PlatformOpenAI,
		Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard,
	}}, `{"model":"gpt-5.6-high"}`, nil)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "WALLET_KEY_INVALID")
}

func TestAPIKeyAuthWalletFixedGroupRejectsRevokedVIPAndDisabledGroup(t *testing.T) {
	for _, tt := range []struct {
		name  string
		group *service.Group
	}{
		{
			name:  "revoked vip grant",
			group: &service.Group{ID: 22, Name: "vip", Status: service.StatusActive, Platform: service.PlatformAnthropic, Hydrated: true, IsExclusive: true, SubscriptionType: service.SubscriptionTypeStandard},
		},
		{
			name:  "disabled default group",
			group: &service.Group{ID: 3, Name: "openai-default", Status: service.StatusDisabled, Platform: service.PlatformOpenAI, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			user := &service.User{ID: 79, Role: service.RoleUser, Status: service.StatusActive, Concurrency: 3}
			groupID := tt.group.ID
			apiKey := &service.APIKey{ID: 720, UserID: user.ID, Key: "wallet-fixed-key", Status: service.StatusActive, User: user, GroupID: &groupID, Group: tt.group}
			w := performWalletRouteRequest(t, apiKey, &service.UserSubscription{
				ID:               721,
				UserID:           user.ID,
				Status:           service.SubscriptionStatusActive,
				ExpiresAt:        service.MaxExpiresAt,
				WalletBalanceUSD: float64Ptr(50),
			}, nil, nil, `{"model":"claude-sonnet-4-6"}`, nil)

			require.Equal(t, http.StatusForbidden, w.Code)
			require.Contains(t, w.Body.String(), "GROUP_NOT_ALLOWED")
		})
	}
}

func TestAPIKeyAuthFixedExclusiveGroupRejectsRevokedGrantWithoutWallet(t *testing.T) {
	vip := &service.Group{
		ID: 22, Name: service.WalletDefaultVIPGroupName, Status: service.StatusActive,
		Platform: service.PlatformAnthropic, Hydrated: true, IsExclusive: true,
		SubscriptionType: service.SubscriptionTypeStandard,
	}
	user := &service.User{ID: 791, Role: service.RoleUser, Status: service.StatusActive, Balance: 100, Concurrency: 3}
	groupID := vip.ID
	apiKey := &service.APIKey{ID: 740, UserID: user.ID, Key: "revoked-vip-with-balance", Status: service.StatusActive, User: user, GroupID: &groupID, Group: vip}

	w := performWalletRouteRequest(t, apiKey, nil, nil, nil, `{"model":"claude-sonnet-4-6"}`, nil)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "GROUP_NOT_ALLOWED")
}

func TestAPIKeyAuthFixedReservedWalletGroupsRejectPolicyDriftWithoutWallet(t *testing.T) {
	tests := []struct {
		name          string
		group         *service.Group
		allowedGroups []int64
	}{
		{
			name: "vip made public",
			group: &service.Group{ID: 22, Name: service.WalletDefaultVIPGroupName, Status: service.StatusActive,
				Platform: service.PlatformAnthropic, Hydrated: true, IsExclusive: false, SubscriptionType: service.SubscriptionTypeStandard},
		},
		{
			name: "vip moved to wrong platform",
			group: &service.Group{ID: 22, Name: service.WalletDefaultVIPGroupName, Status: service.StatusActive,
				Platform: service.PlatformOpenAI, Hydrated: true, IsExclusive: true, SubscriptionType: service.SubscriptionTypeStandard},
			allowedGroups: []int64{22},
		},
		{
			name: "openai default moved to wrong platform",
			group: &service.Group{ID: 3, Name: service.WalletDefaultOpenAIGroupName, Status: service.StatusActive,
				Platform: service.PlatformAnthropic, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := &service.User{ID: 794, Role: service.RoleUser, Status: service.StatusActive, Balance: 100, Concurrency: 3, AllowedGroups: tt.allowedGroups}
			groupID := tt.group.ID
			apiKey := &service.APIKey{ID: 744, UserID: user.ID, Key: "reserved-group-drift-key", Status: service.StatusActive, User: user, GroupID: &groupID, Group: tt.group}
			w := performWalletRouteRequest(t, apiKey, nil, nil, nil, `{"model":"gpt-5.6-high"}`, nil)
			require.Equal(t, http.StatusForbidden, w.Code)
			require.Contains(t, w.Body.String(), "GROUP_NOT_ALLOWED")
		})
	}
}

func TestAPIKeyAuthFixedWalletGroupRequiresModelToMatchGroup(t *testing.T) {
	openAI := &service.Group{
		ID: 3, Name: service.WalletDefaultOpenAIGroupName, Status: service.StatusActive,
		Platform: service.PlatformOpenAI, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard,
	}
	vip := &service.Group{
		ID: 22, Name: service.WalletDefaultVIPGroupName, Status: service.StatusActive,
		Platform: service.PlatformAnthropic, Hydrated: true, IsExclusive: true,
		SubscriptionType: service.SubscriptionTypeStandard,
	}
	wallet := &service.UserSubscription{
		ID: 741, Status: service.SubscriptionStatusActive, ExpiresAt: service.MaxExpiresAt,
		WalletBalanceUSD: float64Ptr(50),
	}

	tests := []struct {
		name          string
		group         *service.Group
		allowedGroups []int64
		model         string
		routedGroupID int64
		wantStatus    int
	}{
		{name: "gpt on openai default", group: openAI, model: "gpt-5.6-high", routedGroupID: openAI.ID, wantStatus: http.StatusOK},
		{name: "claude on openai default is rejected", group: openAI, model: "claude-sonnet-4-6", routedGroupID: vip.ID, wantStatus: http.StatusForbidden},
		{name: "claude on granted vip", group: vip, allowedGroups: []int64{vip.ID}, model: "claude-sonnet-4-6", routedGroupID: vip.ID, wantStatus: http.StatusOK},
		{name: "gpt on granted vip is rejected", group: vip, allowedGroups: []int64{vip.ID}, model: "gpt-5.6-high", routedGroupID: openAI.ID, wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := &service.User{ID: 792, Role: service.RoleUser, Status: service.StatusActive, Balance: 0, Concurrency: 3, AllowedGroups: tt.allowedGroups}
			groupID := tt.group.ID
			apiKey := &service.APIKey{ID: 742, UserID: user.ID, Key: "fixed-wallet-model-key", Status: service.StatusActive, User: user, GroupID: &groupID, Group: tt.group}
			wallet.UserID = user.ID

			w := performWalletRouteRequest(t, apiKey, wallet, &walletRouteModelRouterStub{groupID: tt.routedGroupID}, nil, `{"model":"`+tt.model+`"}`, nil)

			require.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusForbidden {
				require.Contains(t, w.Body.String(), "GROUP_NOT_ALLOWED")
			}
		})
	}
}

func TestAPIKeyAuthFixedMonthlyGroupPrefersMonthlySubscriptionOverWallet(t *testing.T) {
	group := &service.Group{
		ID:               14,
		Name:             "paid-standard-v3",
		Status:           service.StatusActive,
		Platform:         service.PlatformOpenAI,
		Hydrated:         true,
		SubscriptionType: service.SubscriptionTypeSubscription,
	}
	user := &service.User{ID: 80, Role: service.RoleUser, Status: service.StatusActive, Concurrency: 3}
	groupID := group.ID
	apiKey := &service.APIKey{ID: 730, UserID: user.ID, Key: "monthly-key", Status: service.StatusActive, User: user, GroupID: &groupID, Group: group}
	wallet := &service.UserSubscription{ID: 731, UserID: user.ID, Status: service.SubscriptionStatusActive, ExpiresAt: service.MaxExpiresAt, WalletBalanceUSD: float64Ptr(50)}
	monthly := &service.UserSubscription{ID: 732, UserID: user.ID, GroupID: &groupID, Group: group, Status: service.SubscriptionStatusActive, ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}

	w := performWalletRouteRequest(t, apiKey, wallet, nil, nil, `{"model":"gpt-5.6-high"}`, func(c *gin.Context) {
		sub, ok := GetSubscriptionFromContext(c)
		require.True(t, ok)
		require.Equal(t, monthly.ID, sub.ID, "fixed monthly key must charge the monthly subscription before the credits wallet")
	})

	require.Equal(t, http.StatusOK, w.Code)
}

func TestAPIKeyAuthFailsClosedOnSubscriptionRepositoryErrors(t *testing.T) {
	sentinel := errors.New("injected subscription database failure")
	notFoundWallet := func(context.Context, int64) (*service.UserSubscription, error) {
		return nil, service.ErrSubscriptionNotFound
	}
	notFoundGroup := func(context.Context, int64, int64) (*service.UserSubscription, error) {
		return nil, service.ErrSubscriptionNotFound
	}

	tests := []struct {
		name string
		repo fakeGoogleSubscriptionRepo
	}{
		{
			name: "credits wallet lookup",
			repo: fakeGoogleSubscriptionRepo{
				getActiveWallet: func(context.Context, int64) (*service.UserSubscription, error) { return nil, sentinel },
				getActive:       notFoundGroup,
			},
		},
		{
			name: "exact subscription lookup",
			repo: fakeGoogleSubscriptionRepo{getActiveWallet: notFoundWallet, getActive: func(context.Context, int64, int64) (*service.UserSubscription, error) {
				return nil, sentinel
			}},
		},
		{
			name: "covering subscription lookup",
			repo: fakeGoogleSubscriptionRepo{getActiveWallet: notFoundWallet, getActive: notFoundGroup, getCovering: func(context.Context, int64, int64) (*service.UserSubscription, error) {
				return nil, sentinel
			}},
		},
		{
			name: "any subscription lookup",
			repo: fakeGoogleSubscriptionRepo{getActiveWallet: notFoundWallet, getActive: notFoundGroup, hasAny: func(context.Context, int64) (bool, error) {
				return false, sentinel
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group := &service.Group{
				ID: 3, Name: service.WalletDefaultOpenAIGroupName, Platform: service.PlatformOpenAI,
				Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard,
			}
			user := &service.User{ID: 793, Role: service.RoleUser, Status: service.StatusActive, Balance: 100, Concurrency: 3}
			groupID := group.ID
			apiKey := &service.APIKey{ID: 743, UserID: user.ID, Key: "subscription-error-key", Status: service.StatusActive, User: user, GroupID: &groupID, Group: group}

			w := performWalletRouteRequestWithSubscriptionRepo(t, apiKey, tt.repo, `{"model":"gpt-5.6-high"}`)

			require.Equal(t, http.StatusServiceUnavailable, w.Code)
			require.Contains(t, w.Body.String(), "BILLING_SERVICE_UNAVAILABLE")
		})
	}
}

func performWalletRouteRequestWithSubscriptionRepo(t *testing.T, apiKey *service.APIKey, repo fakeGoogleSubscriptionRepo, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeStandard}
	apiKeyService := service.NewAPIKeyService(fakeAPIKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
		clone := *apiKey
		return &clone, nil
	}}, nil, nil, nil, nil, nil, cfg)
	subscriptionService := service.NewSubscriptionService(nil, repo, nil, nil, cfg)
	t.Cleanup(subscriptionService.Stop)
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddlewareWithRouter(apiKeyService, subscriptionService, nil, nil, cfg)))
	router.POST("/t", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/t", strings.NewReader(body))
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)
	return w
}

func performWalletRouteRequest(
	t *testing.T,
	apiKey *service.APIKey,
	wallet *service.UserSubscription,
	modelRouter service.ModelRouter,
	groupGetter apiKeyAuthGroupGetter,
	body string,
	handler func(*gin.Context),
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeStandard}
	apiKeyService := service.NewAPIKeyService(fakeAPIKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
		clone := *apiKey
		return &clone, nil
	}}, nil, nil, nil, nil, nil, cfg)
	subscriptionService := service.NewSubscriptionService(nil, fakeGoogleSubscriptionRepo{
		getActiveWallet: func(context.Context, int64) (*service.UserSubscription, error) {
			if wallet == nil {
				return nil, service.ErrSubscriptionNotFound
			}
			clone := *wallet
			return &clone, nil
		},
		getActive: func(_ context.Context, userID, groupID int64) (*service.UserSubscription, error) {
			if apiKey.Group == nil || apiKey.GroupID == nil || groupID != *apiKey.GroupID || apiKey.Group.SubscriptionType != service.SubscriptionTypeSubscription {
				return nil, service.ErrSubscriptionNotFound
			}
			windowStart := time.Now()
			return &service.UserSubscription{
				ID:                 732,
				UserID:             userID,
				GroupID:            &groupID,
				Group:              apiKey.Group,
				Status:             service.SubscriptionStatusActive,
				ExpiresAt:          time.Now().Add(30 * 24 * time.Hour),
				DailyWindowStart:   &windowStart,
				WeeklyWindowStart:  &windowStart,
				MonthlyWindowStart: &windowStart,
			}, nil
		},
	}, nil, nil, cfg)
	t.Cleanup(subscriptionService.Stop)

	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddlewareWithRouter(apiKeyService, subscriptionService, modelRouter, groupGetter, cfg)))
	router.POST("/t", func(c *gin.Context) {
		if handler != nil {
			handler(c)
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/t", strings.NewReader(body))
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)
	return w
}

func float64Ptr(v float64) *float64 { return &v }
