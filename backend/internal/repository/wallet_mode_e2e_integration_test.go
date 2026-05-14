//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestWalletModeStandardPlanPurchaseKeyRoutingAndSharedDeduction(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("wallet-mode-e2e-%s@example.com", uuid.NewString()),
		Username:     "wallet-mode-e2e",
		PasswordHash: "hash",
	})

	gptGroup := mustCreateGroup(t, client, &service.Group{
		Name:             "gpt-5",
		Platform:         service.PlatformOpenAI,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   1.0,
	})
	sonnetGroup := mustCreateGroup(t, client, &service.Group{
		Name:             "claude-sonnet",
		Platform:         service.PlatformAnthropic,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   2.5,
	})
	geminiGroup := mustCreateGroup(t, client, &service.Group{
		Name:             "gemini-2-pro",
		Platform:         service.PlatformGemini,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   1.8,
	})

	walletQuota := 1500.0
	plan, err := client.SubscriptionPlan.Create().
		SetName("Standard Wallet E2E").
		SetPrice(299).
		SetWalletQuotaUsd(walletQuota).
		SetValidityDays(30).
		SetValidityUnit("day").
		Save(ctx)
	require.NoError(t, err)

	bindWalletPlanGroup(t, client, plan.ID, gptGroup.ID)
	bindWalletPlanGroup(t, client, plan.ID, sonnetGroup.ID)
	bindWalletPlanGroup(t, client, plan.ID, geminiGroup.ID)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(299).
		SetPayAmount(299).
		SetFeeRate(0).
		SetRechargeCode("WALLET-E2E-NO-CODE").
		SetOutTradeNo("wallet-e2e-" + uuid.NewString()).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-wallet-e2e-" + uuid.NewString()).
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(plan.ID).
		SetSubscriptionDays(30).
		SetStatus(service.OrderStatusPaid).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	require.Nil(t, order.SubscriptionGroupID)

	groupRepo := NewGroupRepository(client, integrationDB)
	userRepo := NewUserRepository(client, integrationDB)
	apiKeyRepo := NewAPIKeyRepository(client, integrationDB)
	subRepo := NewUserSubscriptionRepository(client)
	apiKeySvc := service.NewAPIKeyService(apiKeyRepo, userRepo, groupRepo, subRepo, nil, nil, &config.Config{})
	subSvc := service.NewSubscriptionService(groupRepo, subRepo, nil, client, nil)
	subSvc.SetWalletGroupKeyService(apiKeySvc)
	paymentSvc := service.NewPaymentService(client, nil, nil, nil, subSvc, nil, userRepo, groupRepo, nil)

	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, order.ID))

	sub, err := subRepo.GetActiveWalletByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.Nil(t, sub.GroupID)
	require.NotNil(t, sub.WalletInitialUSD)
	require.NotNil(t, sub.WalletBalanceUSD)
	require.InDelta(t, walletQuota, *sub.WalletInitialUSD, 0.000001)
	require.InDelta(t, walletQuota, *sub.WalletBalanceUSD, 0.000001)

	// 5/14 反转决策：钱包激活走单 key 路径，建 1 把通用 key（group_id=NULL），
	// 不再为每个 plan_group 建独立 key。跨平台调度靠 model_router (B1.1/B1.2)。
	// 参见 docs/plans/2026-05-14-wallet-single-key-reversal.md。
	keys, _, err := apiKeyRepo.ListByUserID(ctx, user.ID, defaultWalletE2EPagination(), service.APIKeyListFilters{Status: service.StatusAPIKeyActive})
	require.NoError(t, err)
	require.Len(t, keys, 1, "应只建 1 把 universal key，跨平台调度靠 model_router")

	universalKey := keys[0]
	require.True(t, service.IsWalletUniversalKeyName(universalKey.Name), "key 名应为 universal key 名，实际 %q", universalKey.Name)
	require.Equal(t, service.WalletUniversalAPIKeyName, universalKey.Name)
	require.Nil(t, universalKey.GroupID, "universal key 的 group_id 必须为 NULL（跨平台）")

	// 同一把 universal key 调 3 个不同平台模型，由 ModelRouter 在 gateway 层路由到对应 group；
	// 这里集成测试直传 WalletCost (= group.RateMultiplier) 模拟路由后的扣费金额。
	// 余额按 wallet_balance_usd 整体扣减，不分 group。
	billingRepo := NewUsageBillingRepository(client, integrationDB)
	requireWalletChargeApplied(t, billingRepo, user.ID, universalKey.ID, sub.ID, "gpt-5", gptGroup.RateMultiplier)
	requireWalletChargeApplied(t, billingRepo, user.ID, universalKey.ID, sub.ID, "claude-sonnet-4-6", sonnetGroup.RateMultiplier)
	requireWalletChargeApplied(t, billingRepo, user.ID, universalKey.ID, sub.ID, "gemini-2.5-pro", geminiGroup.RateMultiplier)

	expectedBalance := walletQuota - gptGroup.RateMultiplier - sonnetGroup.RateMultiplier - geminiGroup.RateMultiplier
	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT wallet_balance_usd FROM user_subscriptions WHERE id = $1", sub.ID).Scan(&balance))
	require.InDelta(t, expectedBalance, balance, 0.000001)

	var ledgerCount int
	var ledgerDeltaSum float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(delta_usd), 0)
		FROM subscription_wallet_ledger
		WHERE subscription_id = $1
	`, sub.ID).Scan(&ledgerCount, &ledgerDeltaSum))
	require.Equal(t, 3, ledgerCount)
	require.InDelta(t, -(gptGroup.RateMultiplier + sonnetGroup.RateMultiplier + geminiGroup.RateMultiplier), ledgerDeltaSum, 0.000001)

	reloadedOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, reloadedOrder.Status)
}

func bindWalletPlanGroup(t *testing.T, client *dbent.Client, planID, groupID int64) {
	t.Helper()
	_, err := client.SubscriptionPlanGroup.Create().
		SetPlanID(planID).
		SetGroupID(groupID).
		Save(context.Background())
	require.NoError(t, err)
}

func defaultWalletE2EPagination() pagination.PaginationParams {
	return pagination.PaginationParams{
		Page:      1,
		PageSize:  100,
		SortBy:    "created_at",
		SortOrder: "desc",
	}
}

func requireWalletChargeApplied(t *testing.T, repo service.UsageBillingRepository, userID, apiKeyID, subscriptionID int64, model string, walletCost float64) {
	t.Helper()
	result, err := repo.Apply(context.Background(), &service.UsageBillingCommand{
		RequestID:      "wallet-e2e-" + model + "-" + uuid.NewString(),
		APIKeyID:       apiKeyID,
		UserID:         userID,
		SubscriptionID: &subscriptionID,
		Model:          model,
		WalletCost:     walletCost,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Applied)
	require.False(t, result.WalletInsufficient)
	require.NotNil(t, result.NewWalletBalance)
}
