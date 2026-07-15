//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestWalletDeductConcurrentUsageReplayDebitsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("wallet-usage-replay-%s@example.com", uuid.NewString()),
		PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-wallet-usage-replay-" + uuid.NewString(),
		Name:   "wallet replay integration",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "wallet-usage-replay-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
	})
	usageLog, err := client.UsageLog.Create().
		SetUserID(user.ID).
		SetAPIKeyID(apiKey.ID).
		SetAccountID(account.ID).
		SetRequestID(uuid.NewString()).
		SetModel("wallet-replay-test").
		Save(ctx)
	require.NoError(t, err)
	wallet := mustCreateCreditsWallet(t, client, user.ID, 100)
	repo := NewWalletRepository(client, integrationDB)

	entries := make([]service.WalletLedgerEntry, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for index := range entries {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			entries[index], errs[index] = repo.Deduct(ctx, service.WalletDeductCommand{
				SubscriptionID: wallet.ID,
				CostUSD:        10,
				UsageLogID:     &usageLog.ID,
			})
		}(index)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, entries[0].ID, entries[1].ID)
	require.NotZero(t, entries[0].ID)

	replay, err := repo.Deduct(ctx, service.WalletDeductCommand{
		SubscriptionID: wallet.ID,
		CostUSD:        50,
		UsageLogID:     &usageLog.ID,
	})
	require.NoError(t, err)
	require.Equal(t, entries[0].ID, replay.ID)
	require.InDelta(t, -10, replay.DeltaUSD, 0.000001)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT wallet_balance_usd
		FROM user_subscriptions
		WHERE id = $1
	`, wallet.ID).Scan(&balance))
	require.InDelta(t, 90, balance, 0.000001)

	var ledgerCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM subscription_wallet_ledger
		WHERE usage_log_id = $1 AND reason = 'usage'
	`, usageLog.ID).Scan(&ledgerCount))
	require.Equal(t, 1, ledgerCount)

	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO subscription_wallet_ledger
			(subscription_id, delta_usd, balance_after, reason, usage_log_id)
		VALUES ($1, -10, 80, 'usage', $2)
	`, wallet.ID, usageLog.ID)
	requirePostgresCode(t, err, "23505")
}
