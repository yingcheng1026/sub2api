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

func TestCreditsWalletDefaultsToOpenAIAndVIPRequiresExplicitGrant(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("wallet-mode-e2e-%s@example.com", uuid.NewString()),
		Username:     "wallet-mode-e2e",
		PasswordHash: "hash",
	})

	gptGroup := mustGetOrCreateWalletBusinessGroup(t, client,
		service.WalletDefaultOpenAIGroupName, service.PlatformOpenAI, false, 1)
	vipGroup := mustGetOrCreateWalletBusinessGroup(t, client,
		service.WalletDefaultVIPGroupName, service.PlatformAnthropic, true, 2.5)

	walletQuota := 1500.0
	plan, err := client.SubscriptionPlan.Create().
		SetName("Credits Wallet E2E").
		SetPrice(299).
		SetWalletQuotaUsd(walletQuota).
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
		SetAmount(299).
		SetPayAmount(299).
		SetFeeRate(0).
		SetRechargeCode("WALLET-E2E-NO-CODE").
		SetOutTradeNo("wallet-e2e-" + uuid.NewString()).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-wallet-e2e-" + uuid.NewString()).
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
	insertCreditsPlanFulfillmentSnapshot(t, ctx, tx.Client(), order.ID, user.ID, plan.ID, 299, 36500, walletQuota)
	require.NoError(t, tx.Commit())
	require.Nil(t, order.SubscriptionGroupID)

	groupRepo := NewGroupRepository(client, integrationDB)
	userRepo := NewUserRepository(client, integrationDB)
	apiKeyRepo := NewAPIKeyRepository(client, integrationDB, strictAPIKeyTestProtector{})
	subRepo := NewUserSubscriptionRepository(client)
	walletRepo := NewWalletRepository(client, integrationDB)
	apiKeySvc := service.NewAPIKeyService(apiKeyRepo, userRepo, groupRepo, subRepo, nil, nil, &config.Config{})
	subSvc := service.NewSubscriptionService(groupRepo, subRepo, nil, client, nil)
	subSvc.SetWalletGroupKeyService(apiKeySvc)
	subSvc.SetWalletTopupService(service.NewWalletService(walletRepo))
	paymentSvc := service.NewPaymentService(client, nil, nil, nil, subSvc, nil, userRepo, groupRepo, nil)

	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, order.ID))

	sub, err := subRepo.GetActiveWalletByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.Nil(t, sub.GroupID)
	require.NotNil(t, sub.WalletInitialUSD)
	require.NotNil(t, sub.WalletBalanceUSD)
	require.InDelta(t, walletQuota, *sub.WalletInitialUSD, 0.000001)
	require.InDelta(t, walletQuota, *sub.WalletBalanceUSD, 0.000001)

	keys, _, err := apiKeyRepo.ListByUserID(ctx, user.ID, defaultWalletE2EPagination(), service.APIKeyListFilters{Status: service.StatusAPIKeyActive})
	require.NoError(t, err)
	require.Len(t, keys, 1, "credits wallet must create exactly one universal key")

	universalKey := keys[0]
	require.True(t, service.IsWalletUniversalKeyName(universalKey.Name), "key 名应为 universal key 名，实际 %q", universalKey.Name)
	require.Equal(t, service.WalletUniversalAPIKeyName, universalKey.Name)
	require.Nil(t, universalKey.GroupID, "universal key 的 group_id 必须为 NULL（跨平台）")

	routePolicy := []service.ModelRoute{
		{Pattern: "gpt-*", GroupName: service.WalletDefaultOpenAIGroupName, ExampleModel: "gpt-5.6-high"},
		{Pattern: "claude-*", GroupName: service.WalletDefaultVIPGroupName, ExampleModel: "claude-sonnet-4-6"},
	}
	routes, err := apiKeySvc.GetWalletModelRoutes(ctx, user.ID, routePolicy)
	require.NoError(t, err)
	require.Len(t, routes, 1, "credits users must not see Claude before an explicit vip grant")
	require.Equal(t, gptGroup.ID, routes[0].GroupID)

	billingRepo := NewUsageBillingRepository(client, integrationDB)
	requireWalletChargeApplied(t, billingRepo, user.ID, universalKey.ID, sub.ID, "gpt-5", gptGroup.RateMultiplier)

	require.NoError(t, userRepo.AddGroupToAllowedGroups(ctx, user.ID, vipGroup.ID),
		"the explicit admin-style vip grant must persist before Claude becomes visible")
	routes, err = apiKeySvc.GetWalletModelRoutes(ctx, user.ID, routePolicy)
	require.NoError(t, err)
	require.Len(t, routes, 2)
	requireWalletChargeApplied(t, billingRepo, user.ID, universalKey.ID, sub.ID, "claude-sonnet-4-6", vipGroup.RateMultiplier)

	expectedBalance := walletQuota - gptGroup.RateMultiplier - vipGroup.RateMultiplier
	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT wallet_balance_usd FROM user_subscriptions WHERE id = $1", sub.ID).Scan(&balance))
	require.InDelta(t, expectedBalance, balance, 0.000001)

	var activationCount, usageCount int
	var ledgerDeltaSum, usageDeltaSum float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE reason = 'activation'),
			COUNT(*) FILTER (WHERE reason = 'usage'),
			COALESCE(SUM(delta_usd), 0),
			COALESCE(SUM(delta_usd) FILTER (WHERE reason = 'usage'), 0)
		FROM subscription_wallet_ledger
		WHERE subscription_id = $1
	`, sub.ID).Scan(&activationCount, &usageCount, &ledgerDeltaSum, &usageDeltaSum))
	require.Equal(t, 1, activationCount)
	require.Equal(t, 2, usageCount)
	require.InDelta(t, expectedBalance, ledgerDeltaSum, 0.000001)
	require.InDelta(t, -(gptGroup.RateMultiplier + vipGroup.RateMultiplier), usageDeltaSum, 0.000001)

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
