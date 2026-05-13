//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// B2.8 端到端回归 #1：链动小铺 credits SKU 走 PaymentOrder webhook 发货 →
// 创建钱包订阅、expires_at 锁到 MaxExpiresAt（永久），多 key 全开。
func TestWalletCreditsPlanPurchaseActivatesPermanentWallet(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("wallet-credits-buy-%s@example.com", uuid.NewString()),
		Username:     "wallet-credits-buy",
		PasswordHash: "hash",
	})

	gptGroup := mustCreateGroup(t, client, &service.Group{
		Name:             "gpt-5-b28-buy",
		Platform:         service.PlatformOpenAI,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   1.0,
	})
	sonnetGroup := mustCreateGroup(t, client, &service.Group{
		Name:             "claude-sonnet-b28-buy",
		Platform:         service.PlatformAnthropic,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   2.5,
	})

	creditsQuota := 100.0
	plan, err := client.SubscriptionPlan.Create().
		SetName("Credits 100 E2E Buy").
		SetPrice(30).
		SetWalletQuotaUsd(creditsQuota).
		SetValidityDays(36500).
		SetValidityUnit("day").
		SetPlanType(service.PlanTypeCredits).
		Save(ctx)
	require.NoError(t, err)

	bindWalletPlanGroup(t, client, plan.ID, gptGroup.ID)
	bindWalletPlanGroup(t, client, plan.ID, sonnetGroup.ID)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(30).
		SetPayAmount(30).
		SetFeeRate(0).
		SetRechargeCode("WALLET-CREDITS-E2E-BUY-NO-CODE").
		SetOutTradeNo("wallet-credits-buy-" + uuid.NewString()).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-credits-buy-" + uuid.NewString()).
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(plan.ID).
		SetSubscriptionDays(36500).
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
	walletRepo := NewWalletRepository(client, integrationDB)
	walletSvc := service.NewWalletService(walletRepo)
	apiKeySvc := service.NewAPIKeyService(apiKeyRepo, userRepo, groupRepo, subRepo, nil, nil, &config.Config{})
	subSvc := service.NewSubscriptionService(groupRepo, subRepo, nil, client, nil)
	subSvc.SetWalletGroupKeyService(apiKeySvc)
	subSvc.SetWalletTopupService(walletSvc)
	paymentSvc := service.NewPaymentService(client, nil, nil, nil, subSvc, nil, userRepo, groupRepo, nil)

	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, order.ID))

	sub, err := subRepo.GetActiveWalletByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.Nil(t, sub.GroupID)
	require.NotNil(t, sub.WalletInitialUSD)
	require.InDelta(t, creditsQuota, *sub.WalletInitialUSD, 0.000001)
	require.InDelta(t, creditsQuota, *sub.WalletBalanceUSD, 0.000001)
	require.True(t, sub.ExpiresAt.Equal(service.MaxExpiresAt),
		"额度卡 expires_at 必须 == MaxExpiresAt (2099-12-31)，实际 %v", sub.ExpiresAt)

	// 多 key 全开
	keys, _, err := apiKeyRepo.ListByUserID(ctx, user.ID, defaultWalletE2EPagination(), service.APIKeyListFilters{Status: service.StatusAPIKeyActive})
	require.NoError(t, err)
	require.Len(t, keys, 2, "credits plan 关联 2 个 group → 应建 2 把分组 key")
	for _, k := range keys {
		require.True(t, service.IsWalletGroupKeyName(k.Name), "key 名应带 钱包- 前缀，实际 %q", k.Name)
	}
}

// B2.8 端到端回归 #2：月卡用户再买额度卡 → topup 叠加（不新建 user_subscriptions 行）。
// 验证 §2.3 设计：wallet_balance_usd += quota；wallet_initial_usd += quota；
// 写一笔 ledger reason='topup'；多 key 不重建。
func TestWalletMonthlyUserBuysCreditsSKUStacksOntoExistingWallet(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("wallet-stack-%s@example.com", uuid.NewString()),
		Username:     "wallet-stack",
		PasswordHash: "hash",
	})

	gptGroup := mustCreateGroup(t, client, &service.Group{
		Name:             "gpt-5-b28-stack",
		Platform:         service.PlatformOpenAI,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   1.0,
	})

	// 月卡 plan
	monthlyQuota := 1500.0
	monthlyPlan, err := client.SubscriptionPlan.Create().
		SetName("Standard Monthly E2E Stack").
		SetPrice(299).
		SetWalletQuotaUsd(monthlyQuota).
		SetValidityDays(30).
		SetValidityUnit("day").
		SetPlanType(service.PlanTypeSubscription).
		Save(ctx)
	require.NoError(t, err)
	bindWalletPlanGroup(t, client, monthlyPlan.ID, gptGroup.ID)

	// 额度卡 plan
	creditsQuota := 100.0
	creditsPlan, err := client.SubscriptionPlan.Create().
		SetName("Credits 100 E2E Stack").
		SetPrice(30).
		SetWalletQuotaUsd(creditsQuota).
		SetValidityDays(36500).
		SetValidityUnit("day").
		SetPlanType(service.PlanTypeCredits).
		Save(ctx)
	require.NoError(t, err)
	bindWalletPlanGroup(t, client, creditsPlan.ID, gptGroup.ID)

	groupRepo := NewGroupRepository(client, integrationDB)
	userRepo := NewUserRepository(client, integrationDB)
	apiKeyRepo := NewAPIKeyRepository(client, integrationDB)
	subRepo := NewUserSubscriptionRepository(client)
	walletRepo := NewWalletRepository(client, integrationDB)
	walletSvc := service.NewWalletService(walletRepo)
	apiKeySvc := service.NewAPIKeyService(apiKeyRepo, userRepo, groupRepo, subRepo, nil, nil, &config.Config{})
	subSvc := service.NewSubscriptionService(groupRepo, subRepo, nil, client, nil)
	subSvc.SetWalletGroupKeyService(apiKeySvc)
	subSvc.SetWalletTopupService(walletSvc)
	paymentSvc := service.NewPaymentService(client, nil, nil, nil, subSvc, nil, userRepo, groupRepo, nil)

	// step 1: 月卡支付 → 建钱包 ($1500)
	monthlyOrder, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(299).
		SetPayAmount(299).
		SetFeeRate(0).
		SetRechargeCode("WALLET-MONTHLY-STACK-NO-CODE").
		SetOutTradeNo("wallet-monthly-stack-" + uuid.NewString()).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-monthly-stack-" + uuid.NewString()).
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(monthlyPlan.ID).
		SetSubscriptionDays(30).
		SetStatus(service.OrderStatusPaid).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, monthlyOrder.ID))

	subAfterMonthly, err := subRepo.GetActiveWalletByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, monthlyQuota, *subAfterMonthly.WalletBalanceUSD, 0.000001)
	monthlyExpiresAt := subAfterMonthly.ExpiresAt
	require.False(t, monthlyExpiresAt.Equal(service.MaxExpiresAt), "月卡不应永久有效")

	keysAfterMonthly, _, err := apiKeyRepo.ListByUserID(ctx, user.ID, defaultWalletE2EPagination(), service.APIKeyListFilters{Status: service.StatusAPIKeyActive})
	require.NoError(t, err)
	require.Len(t, keysAfterMonthly, 1, "monthly plan 关联 1 个 group → 应建 1 把 key")

	// step 2: 再来一张额度卡 → topup 叠加（不新建 sub，余额合并）
	creditsOrder, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(30).
		SetPayAmount(30).
		SetFeeRate(0).
		SetRechargeCode("WALLET-CREDITS-STACK-NO-CODE").
		SetOutTradeNo("wallet-credits-stack-" + uuid.NewString()).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-credits-stack-" + uuid.NewString()).
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(creditsPlan.ID).
		SetSubscriptionDays(36500).
		SetStatus(service.OrderStatusPaid).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, creditsOrder.ID))

	subAfterStack, err := subRepo.GetActiveWalletByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, subAfterMonthly.ID, subAfterStack.ID, "topup 必须复用同一条钱包订阅（不新建行）")

	expectedBalance := monthlyQuota + creditsQuota
	require.InDelta(t, expectedBalance, *subAfterStack.WalletBalanceUSD, 0.000001,
		"topup 后 wallet_balance_usd 必须 = $1500 + $100 = $1600")
	require.InDelta(t, expectedBalance, *subAfterStack.WalletInitialUSD, 0.000001,
		"topup 后 wallet_initial_usd 也叠加（用于「累计充值额度」展示）")
	// expires_at 由月卡持有，credits topup 不应把它拉到 MaxExpiresAt。
	require.True(t, subAfterStack.ExpiresAt.Equal(monthlyExpiresAt),
		"topup 不应改变月卡 expires_at（仍按 30 天到期）")

	// 多 key 不重建：仍是 1 把
	keysAfterStack, _, err := apiKeyRepo.ListByUserID(ctx, user.ID, defaultWalletE2EPagination(), service.APIKeyListFilters{Status: service.StatusAPIKeyActive})
	require.NoError(t, err)
	require.Len(t, keysAfterStack, 1, "topup 不应重复建 key")
	require.Equal(t, keysAfterMonthly[0].ID, keysAfterStack[0].ID)

	// ledger 校验：reason='topup' 一条 delta = +100
	var topupCount int
	var topupDeltaSum float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(delta_usd), 0)
		FROM subscription_wallet_ledger
		WHERE subscription_id = $1 AND reason = 'topup'
	`, subAfterStack.ID).Scan(&topupCount, &topupDeltaSum))
	require.Equal(t, 1, topupCount, "应写一条 reason='topup' 的 ledger")
	require.InDelta(t, creditsQuota, topupDeltaSum, 0.000001, "topup ledger delta_usd 必须 = +creditsQuota")
}
