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
// 创建钱包订阅、expires_at 锁到 MaxExpiresAt（永久），单 key 模式建 1 把 universal key。
// （5/14 反转决策后从「多 key」改回「单 key + ModelRouter」）。
func TestWalletCreditsPlanPurchaseActivatesPermanentWallet(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("wallet-credits-buy-%s@example.com", uuid.NewString()),
		Username:     "wallet-credits-buy",
		PasswordHash: "hash",
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

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	order, err := tx.Client().PaymentOrder.Create().
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
		SetPaidAt(time.Now()).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	insertCreditsPlanFulfillmentSnapshot(t, ctx, tx.Client(), order.ID, user.ID, plan.ID, 30, 36500, creditsQuota)
	require.NoError(t, tx.Commit())
	require.Nil(t, order.SubscriptionGroupID)

	groupRepo := NewGroupRepository(client, integrationDB)
	userRepo := NewUserRepository(client, integrationDB)
	apiKeyRepo := NewAPIKeyRepository(client, integrationDB, strictAPIKeyTestProtector{})
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

	// 5/14 反转决策：单 key 路径建 1 把 universal key (group_id=NULL)，跨平台调度靠 model_router
	keys, _, err := apiKeyRepo.ListByUserID(ctx, user.ID, defaultWalletE2EPagination(), service.APIKeyListFilters{Status: service.StatusAPIKeyActive})
	require.NoError(t, err)
	require.Len(t, keys, 1, "credits plan 应只建 1 把 universal key，不分 group")
	require.True(t, service.IsWalletUniversalKeyName(keys[0].Name), "key 名应为 universal key 名，实际 %q", keys[0].Name)
	require.Equal(t, service.WalletUniversalAPIKeyName, keys[0].Name)
	require.Nil(t, keys[0].GroupID, "universal key 的 group_id 必须为 NULL")
}

// Monthly entitlements and credits wallets are separate products, rows,
// authorization paths, and ledgers. Buying credits after monthly access must
// never turn the monthly row into a wallet or merge their money.
func TestMonthlySubscriptionAndCreditsWalletRemainSeparate(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("wallet-stack-%s@example.com", uuid.NewString()),
		Username:     "wallet-stack",
		PasswordHash: "hash",
	})

	gptGroup := mustCreateGroup(t, client, &service.Group{
		Name:             "monthly-openai-" + uuid.NewString(),
		Platform:         service.PlatformOpenAI,
		SubscriptionType: service.SubscriptionTypeSubscription,
		RateMultiplier:   1.0,
	})

	monthlyPlan, err := client.SubscriptionPlan.Create().
		SetName("Monthly Group E2E Separate").
		SetPrice(299).
		SetGroupID(gptGroup.ID).
		SetValidityDays(30).
		SetValidityUnit("day").
		SetPlanType(service.PlanTypeSubscription).
		Save(ctx)
	require.NoError(t, err)
	bindWalletPlanGroup(t, client, monthlyPlan.ID, gptGroup.ID)

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

	groupRepo := NewGroupRepository(client, integrationDB)
	userRepo := NewUserRepository(client, integrationDB)
	apiKeyRepo := NewAPIKeyRepository(client, integrationDB, strictAPIKeyTestProtector{})
	subRepo := NewUserSubscriptionRepository(client)
	walletRepo := NewWalletRepository(client, integrationDB)
	walletSvc := service.NewWalletService(walletRepo)
	apiKeySvc := service.NewAPIKeyService(apiKeyRepo, userRepo, groupRepo, subRepo, nil, nil, &config.Config{})
	subSvc := service.NewSubscriptionService(groupRepo, subRepo, nil, client, nil)
	subSvc.SetWalletGroupKeyService(apiKeySvc)
	subSvc.SetWalletTopupService(walletSvc)
	paymentSvc := service.NewPaymentService(client, nil, nil, nil, subSvc, nil, userRepo, groupRepo, nil)

	monthlyTx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = monthlyTx.Rollback() }()
	monthlyOrder, err := monthlyTx.Client().PaymentOrder.Create().
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
		SetSubscriptionGroupID(gptGroup.ID).
		SetSubscriptionDays(30).
		SetStatus(service.OrderStatusPaid).
		SetPaidAt(time.Now()).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	insertMonthlyPlanFulfillmentSnapshot(t, ctx, monthlyTx.Client(), monthlyOrder.ID, user.ID, monthlyPlan.ID, gptGroup.ID, 299, 30, gptGroup.RateMultiplier)
	require.NoError(t, monthlyTx.Commit())
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, monthlyOrder.ID))

	monthlySub, err := subRepo.GetActiveByUserIDAndGroupID(ctx, user.ID, gptGroup.ID)
	require.NoError(t, err)
	require.Nil(t, monthlySub.WalletBalanceUSD)
	require.Nil(t, monthlySub.WalletInitialUSD)
	require.False(t, monthlySub.ExpiresAt.Equal(service.MaxExpiresAt))
	_, err = subRepo.GetActiveWalletByUserID(ctx, user.ID)
	require.ErrorIs(t, err, service.ErrSubscriptionNotFound,
		"monthly group access must not create a hidden wallet")

	keysAfterMonthly, _, err := apiKeyRepo.ListByUserID(ctx, user.ID, defaultWalletE2EPagination(), service.APIKeyListFilters{Status: service.StatusAPIKeyActive})
	require.NoError(t, err)
	require.Empty(t, keysAfterMonthly, "monthly entitlement assignment must not mint a credits-wallet key")

	creditsTx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = creditsTx.Rollback() }()
	creditsOrder, err := creditsTx.Client().PaymentOrder.Create().
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
		SetPaidAt(time.Now()).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	insertCreditsPlanFulfillmentSnapshot(t, ctx, creditsTx.Client(), creditsOrder.ID, user.ID, creditsPlan.ID, 30, 36500, creditsQuota)
	require.NoError(t, creditsTx.Commit())
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, creditsOrder.ID))

	walletSub, err := subRepo.GetActiveCreditsWalletByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.NotEqual(t, monthlySub.ID, walletSub.ID)
	require.Nil(t, walletSub.GroupID)
	require.InDelta(t, creditsQuota, *walletSub.WalletBalanceUSD, 0.000001)
	require.InDelta(t, creditsQuota, *walletSub.WalletInitialUSD, 0.000001)
	require.True(t, walletSub.ExpiresAt.Equal(service.MaxExpiresAt))

	monthlyReloaded, err := subRepo.GetByID(ctx, monthlySub.ID)
	require.NoError(t, err)
	require.Nil(t, monthlyReloaded.WalletBalanceUSD)
	require.Nil(t, monthlyReloaded.WalletInitialUSD)
	var subscriptionRows int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM user_subscriptions
		WHERE user_id = $1 AND deleted_at IS NULL
	`, user.ID).Scan(&subscriptionRows))
	require.Equal(t, 2, subscriptionRows)

	keysAfterCredits, _, err := apiKeyRepo.ListByUserID(ctx, user.ID, defaultWalletE2EPagination(), service.APIKeyListFilters{Status: service.StatusAPIKeyActive})
	require.NoError(t, err)
	require.Len(t, keysAfterCredits, 1)
	require.True(t, service.IsWalletUniversalKeyName(keysAfterCredits[0].Name))

	var activationCount, topupCount int
	var ledgerTotal float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE reason = 'activation'),
			COUNT(*) FILTER (WHERE reason = 'topup'),
			COALESCE(SUM(delta_usd), 0)
		FROM subscription_wallet_ledger WHERE subscription_id = $1
	`, walletSub.ID).Scan(&activationCount, &topupCount, &ledgerTotal))
	require.Equal(t, 1, activationCount)
	require.Zero(t, topupCount, "a monthly row must never be reused as a credits top-up target")
	require.InDelta(t, creditsQuota, ledgerTotal, 0.000001)
}
