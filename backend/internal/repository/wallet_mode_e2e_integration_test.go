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

	// B2.2 多 key 模式：plan 关联 3 个 group → 自动建 3 把分组 key，命名 "钱包-{group.Name}"
	keys, _, err := apiKeyRepo.ListByUserID(ctx, user.ID, defaultWalletE2EPagination(), service.APIKeyListFilters{Status: service.StatusAPIKeyActive})
	require.NoError(t, err)
	require.Len(t, keys, 3, "应建 3 把分组 key（GPT / Claude Sonnet / Gemini）")

	keysByGroupID := make(map[int64]service.APIKey, len(keys))
	for _, k := range keys {
		require.True(t, service.IsWalletGroupKeyName(k.Name), "key 名应带 钱包- 前缀，实际 %q", k.Name)
		require.NotNil(t, k.GroupID, "钱包多 key 模式 group_id 必须非空")
		keysByGroupID[*k.GroupID] = k
	}
	require.Contains(t, keysByGroupID, gptGroup.ID)
	require.Contains(t, keysByGroupID, sonnetGroup.ID)
	require.Contains(t, keysByGroupID, geminiGroup.ID)
	require.Equal(t, "钱包-gpt-5", keysByGroupID[gptGroup.ID].Name)
	require.Equal(t, "钱包-claude-sonnet", keysByGroupID[sonnetGroup.ID].Name)
	require.Equal(t, "钱包-gemini-2-pro", keysByGroupID[geminiGroup.ID].Name)

	// 各 group 的 key 独立扣款（不再依赖 ModelRouter）：用各自的 key 直接命中对应 group
	billingRepo := NewUsageBillingRepository(client, integrationDB)
	requireWalletChargeApplied(t, billingRepo, user.ID, keysByGroupID[gptGroup.ID].ID, sub.ID, "gpt-5", 1.0*gptGroup.RateMultiplier)
	requireWalletChargeApplied(t, billingRepo, user.ID, keysByGroupID[sonnetGroup.ID].ID, sub.ID, "claude-sonnet-4-6", 1.0*sonnetGroup.RateMultiplier)
	requireWalletChargeApplied(t, billingRepo, user.ID, keysByGroupID[geminiGroup.ID].ID, sub.ID, "gemini-2.5-pro", 1.0*geminiGroup.RateMultiplier)

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
