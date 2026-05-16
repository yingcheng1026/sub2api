//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestAPIKeyAuth_GroupSwitchCoverage 验证 2026-05-16 方案 C：
// 用户在 admin 把 api_key 切到非订阅主 group 时，middleware 通过
// subscription_plan_groups 间接 lookup，找到覆盖该 group 的月卡订阅，
// 走月卡 quota 而不是静默扣 user.balance。
//
// 见 docs/plans/2026-05-16-wallet-v4-group-switch-billing-fix.md。
func TestAPIKeyAuth_GroupSwitchCoverage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 用户买了月卡 paid-trial-v3-30d，订阅 sub.group_id = 13 (paid-trial-v3, 'subscription' 类型);
	// 月卡 plan_groups 关联 [claude-Max pool=2, openai-default=3, gemini-default=4, cc-antigravity=5]，
	// 都是 'standard' 类型。
	//
	// 用户把 api_key.group_id 切到 3 (openai-default standard 类型)。
	standardGroup := &service.Group{
		ID:               3,
		Name:             "openai-default",
		Status:           service.StatusActive,
		Hydrated:         true,
		SubscriptionType: service.SubscriptionTypeStandard,
	}
	user := &service.User{
		ID:          42,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     0, // 主余额 0；中间件不能静默扣 balance
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     500,
		UserID: user.ID,
		Key:    "switched-key",
		Status: service.StatusActive,
		User:   user,
		Group:  standardGroup,
	}
	apiKey.GroupID = &standardGroup.ID

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}

	makeRequest := func(repo *stubUserSubscriptionRepo) *httptest.ResponseRecorder {
		cfg := &config.Config{RunMode: config.RunModeStandard}
		cfg.SubscriptionMaintenance.WorkerCount = 1
		cfg.SubscriptionMaintenance.QueueSize = 1

		apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
		subscriptionService := service.NewSubscriptionService(nil, repo, nil, nil, cfg)
		t.Cleanup(subscriptionService.Stop)

		router := gin.New()
		router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, subscriptionService, cfg)))
		router.GET("/t", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("x-api-key", apiKey.Key)
		router.ServeHTTP(w, req)
		return w
	}

	t.Run("link_inside_uses_monthly_quota", func(t *testing.T) {
		// 月卡覆盖 openai-default → middleware 走月卡 quota，不扣主余额
		monthlySub := &service.UserSubscription{
			ID:        700,
			UserID:    user.ID,
			GroupID:   int64Ptr(13), // 订阅主 group = paid-trial-v3
			Status:    service.SubscriptionStatusActive,
			ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
			// 注意：没有 wallet_balance_usd → 月卡模式
		}

		coverCalled := false
		repo := &stubUserSubscriptionRepo{
			getActive: func(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
				// exact (user_id, group_id=3) 没匹配（订阅是 group 13）
				return nil, service.ErrSubscriptionNotFound
			},
			getActiveByPlanCover: func(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
				coverCalled = true
				require.Equal(t, user.ID, userID)
				require.Equal(t, standardGroup.ID, groupID)
				clone := *monthlySub
				return &clone, nil
			},
		}

		w := makeRequest(repo)
		require.Equal(t, http.StatusOK, w.Code,
			"月卡 plan_groups 覆盖切换的 group → 应放行走月卡 quota")
		require.True(t, coverCalled, "应调用 GetActiveByPlanCoveringGroup 做间接 lookup")
	})

	t.Run("link_outside_rejects_with_403", func(t *testing.T) {
		// 用户有月卡，但 plan_groups 不包含 openai-default → 拒绝，不能静默扣主余额
		repo := &stubUserSubscriptionRepo{
			getActive: func(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
				return nil, service.ErrSubscriptionNotFound
			},
			getActiveByPlanCover: func(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
				return nil, service.ErrSubscriptionNotFound
			},
			hasAnyActive: func(ctx context.Context, userID int64) (bool, error) {
				// 用户有月卡但不覆盖
				return true, nil
			},
		}

		w := makeRequest(repo)
		require.Equal(t, http.StatusForbidden, w.Code,
			"用户有订阅但当前 group 不在 plan_groups 链内 → 403，不再 fallback balance")
		require.Contains(t, w.Body.String(), "GROUP_NOT_IN_SUBSCRIPTION",
			"错误码应为 GROUP_NOT_IN_SUBSCRIPTION")
	})

	t.Run("balance_only_user_still_works", func(t *testing.T) {
		// 无任何订阅 + balance > 0 → 兼容老 balance 用户，放行
		balUser := &service.User{
			ID:          43,
			Role:        service.RoleUser,
			Status:      service.StatusActive,
			Balance:     50, // 老充值余额
			Concurrency: 3,
		}
		balKey := &service.APIKey{
			ID:     501,
			UserID: balUser.ID,
			Key:    "balance-user-key",
			Status: service.StatusActive,
			User:   balUser,
			Group:  standardGroup,
		}
		balKey.GroupID = &standardGroup.ID

		balApiKeyRepo := &stubApiKeyRepo{
			getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
				if key != balKey.Key {
					return nil, service.ErrAPIKeyNotFound
				}
				clone := *balKey
				return &clone, nil
			},
		}

		cfg := &config.Config{RunMode: config.RunModeStandard}
		cfg.SubscriptionMaintenance.WorkerCount = 1
		cfg.SubscriptionMaintenance.QueueSize = 1
		apiKeyService := service.NewAPIKeyService(balApiKeyRepo, nil, nil, nil, nil, nil, cfg)
		repo := &stubUserSubscriptionRepo{
			getActive: func(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
				return nil, service.ErrSubscriptionNotFound
			},
			getActiveByPlanCover: func(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
				return nil, service.ErrSubscriptionNotFound
			},
			hasAnyActive: func(ctx context.Context, userID int64) (bool, error) {
				return false, nil // 纯余额用户
			},
		}
		subscriptionService := service.NewSubscriptionService(nil, repo, nil, nil, cfg)
		t.Cleanup(subscriptionService.Stop)

		router := gin.New()
		router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, subscriptionService, cfg)))
		router.GET("/t", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("x-api-key", balKey.Key)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code,
			"纯余额用户(无任何订阅) + balance > 0 → 应放行(兼容老逻辑)")
	})

	t.Run("no_subscription_no_balance_rejects", func(t *testing.T) {
		// 既无订阅也无余额 → INSUFFICIENT_BALANCE
		repo := &stubUserSubscriptionRepo{
			hasAnyActive: func(ctx context.Context, userID int64) (bool, error) {
				return false, nil
			},
		}
		w := makeRequest(repo)
		require.Equal(t, http.StatusForbidden, w.Code)
		require.Contains(t, w.Body.String(), "INSUFFICIENT_BALANCE")
	})
}

func int64Ptr(v int64) *int64 {
	return &v
}
