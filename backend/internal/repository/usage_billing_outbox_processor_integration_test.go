//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type failOnceCompleteOutboxRepository struct {
	service.UsageBillingOutboxRepository
	failed bool
}

func (r *failOnceCompleteOutboxRepository) Complete(ctx context.Context, id int64, owner, leaseToken, resultCode string) error {
	if !r.failed {
		r.failed = true
		return errors.New("simulated ack crash")
	}
	return r.UsageBillingOutboxRepository.Complete(ctx, id, owner, leaseToken, resultCode)
}

type integrationUsageBillingReplayWriter struct {
	repo service.UsageLogRepository
}

func (w *integrationUsageBillingReplayWriter) WriteUsageBillingReplay(ctx context.Context, envelope service.UsageBillingEnvelope) error {
	cmd := envelope.Command()
	actualCost := cmd.BalanceCost
	if cmd.SubscriptionCost > 0 {
		actualCost = cmd.SubscriptionCost
	}
	if cmd.WalletCost > 0 {
		actualCost = cmd.WalletCost
	}
	billingModel := cmd.Model
	_, err := w.repo.Create(ctx, &service.UsageLog{
		UserID:         cmd.UserID,
		APIKeyID:       cmd.APIKeyID,
		AccountID:      cmd.AccountID,
		RequestID:      cmd.RequestID,
		Model:          cmd.Model,
		BillingModel:   &billingModel,
		InputTokens:    cmd.InputTokens,
		OutputTokens:   cmd.OutputTokens,
		ActualCost:     actualCost,
		TotalCost:      actualCost,
		RateMultiplier: 1,
		BillingType:    cmd.BillingType,
		SubscriptionID: cmd.SubscriptionID,
		CreatedAt:      time.Now().UTC(),
	})
	return err
}

func integrationOutboxEnvelope(t *testing.T, input service.UsageBillingEnvelopeInput) service.UsageBillingEnvelope {
	t.Helper()
	input.PricingSource = service.PricingSourceBuiltinGPT56
	input.PricingRevision = service.GPT56PricingRevision
	input.PricingHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	input.RateMultiplier = 1
	input.AccountRateMultiplier = 1
	envelope, err := service.NewUsageBillingEnvelope(input)
	require.NoError(t, err)
	return envelope
}

func TestUsageBillingOutboxProcessorIntegration_BalanceAckCrashReplayChargesAndLogsOnce(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE usage_billing_outbox RESTART IDENTITY")
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("outbox-balance-%s@example.com", uuid.NewString()), Balance: 100})
	group := mustCreateGroup(t, client, &service.Group{Name: "outbox-balance-" + uuid.NewString()})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-outbox-balance-" + uuid.NewString()})
	account := mustCreateAccount(t, client, &service.Account{Name: "outbox-balance-" + uuid.NewString(), Type: service.AccountTypeAPIKey})
	mustBindAccountToGroup(t, client, account.ID, group.ID, 1)

	outboxRepo := NewUsageBillingOutboxRepository(integrationDB)
	billingRepo := NewUsageBillingRepository(client, integrationDB)
	usageRepo := NewUsageLogRepository(client, integrationDB)
	envelope := integrationOutboxEnvelope(t, service.UsageBillingEnvelopeInput{
		RequestID:           "outbox-ack-" + uuid.NewString(),
		APIKeyID:            apiKey.ID,
		UserID:              user.ID,
		AccountID:           account.ID,
		GroupID:             &group.ID,
		AccountType:         service.AccountTypeAPIKey,
		BillingModel:        "gpt-5.6-sol",
		BillingType:         service.BillingTypeBalance,
		InputTokens:         100,
		OutputTokens:        10,
		BalanceCost:         1.25,
		APIKeyQuotaCost:     1.25,
		APIKeyRateLimitCost: 1.25,
		AccountQuotaCost:    1.25,
	})
	event, _, err := outboxRepo.Enqueue(ctx, envelope)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE users SET deleted_at = NOW() WHERE id = $1", user.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE api_keys SET deleted_at = NOW() WHERE id = $1", apiKey.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET deleted_at = NOW() WHERE id = $1", account.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE usage_billing_outbox SET attempt_count = max_attempts - 1 WHERE id = $1", event.ID)
	require.NoError(t, err)
	claimed, err := outboxRepo.Claim(ctx, "worker-one", 1, service.UsageBillingOutboxLeaseDuration)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, claimed[0].MaxAttempts, claimed[0].AttemptCount)

	failingAckRepo := &failOnceCompleteOutboxRepository{UsageBillingOutboxRepository: outboxRepo}
	processor := service.NewUsageBillingOutboxProcessor(failingAckRepo, outboxRepo, billingRepo, &integrationUsageBillingReplayWriter{repo: usageRepo})
	require.Error(t, processor.ProcessEvent(ctx, claimed[0]))

	_, err = integrationDB.ExecContext(ctx, "UPDATE usage_billing_outbox SET locked_at = NOW() - INTERVAL '31 seconds' WHERE id = $1", claimed[0].ID)
	require.NoError(t, err)
	replayed, err := outboxRepo.Claim(ctx, "worker-two", 1, service.UsageBillingOutboxLeaseDuration)
	require.NoError(t, err)
	require.Len(t, replayed, 1)
	processor = service.NewUsageBillingOutboxProcessor(outboxRepo, outboxRepo, billingRepo, &integrationUsageBillingReplayWriter{repo: usageRepo})
	require.NoError(t, processor.ProcessEvent(ctx, replayed[0]))

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 98.75, balance, 0.000001)
	var usageCount, dedupCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs WHERE request_id = $1 AND api_key_id = $2", envelope.RequestID(), apiKey.ID).Scan(&usageCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2", envelope.RequestID(), apiKey.ID).Scan(&dedupCount))
	require.Equal(t, 1, usageCount)
	require.Equal(t, 1, dedupCount)
	var status, resultCode string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT status, result_code FROM usage_billing_outbox WHERE id = $1", claimed[0].ID).Scan(&status, &resultCode))
	require.Equal(t, service.UsageBillingOutboxStatusCompleted, status)
	require.Equal(t, service.UsageBillingOutboxResultDuplicate, resultCode)
}

func TestUsageBillingOutboxProcessorIntegration_MonthlyAndWalletPreserveFrozenSubscription(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	t.Run("monthly", func(t *testing.T) {
		_, _ = integrationDB.ExecContext(ctx, "TRUNCATE usage_billing_outbox RESTART IDENTITY")
		user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("outbox-monthly-%s@example.com", uuid.NewString())})
		group := mustCreateGroup(t, client, &service.Group{Name: "outbox-monthly-" + uuid.NewString(), SubscriptionType: service.SubscriptionTypeSubscription})
		apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-outbox-monthly-" + uuid.NewString()})
		account := mustCreateAccount(t, client, &service.Account{Name: "outbox-monthly-account-" + uuid.NewString(), Type: service.AccountTypeOAuth})
		mustBindAccountToGroup(t, client, account.ID, group.ID, 1)
		subscription := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, GroupID: &group.ID})
		envelope := integrationOutboxEnvelope(t, service.UsageBillingEnvelopeInput{
			RequestID:        "outbox-monthly-" + uuid.NewString(),
			APIKeyID:         apiKey.ID,
			UserID:           user.ID,
			AccountID:        account.ID,
			SubscriptionID:   &subscription.ID,
			GroupID:          &group.ID,
			AccountType:      service.AccountTypeOAuth,
			BillingModel:     "gpt-5.6-sol",
			BillingType:      service.BillingTypeSubscription,
			SubscriptionCost: 2.5,
		})
		processIntegrationEnvelopeAfterEnqueue(t, ctx, envelope, func() {
			_, err := integrationDB.ExecContext(ctx, "UPDATE user_subscriptions SET deleted_at = NOW() WHERE id = $1", subscription.ID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET deleted_at = NOW() WHERE id = $1", group.ID)
			require.NoError(t, err)
		})

		var daily, weekly, monthly float64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd
			FROM user_subscriptions WHERE id = $1
		`, subscription.ID).Scan(&daily, &weekly, &monthly))
		require.InDelta(t, 2.5, daily, 0.000001)
		require.InDelta(t, 2.5, weekly, 0.000001)
		require.InDelta(t, 2.5, monthly, 0.000001)
	})

	t.Run("wallet", func(t *testing.T) {
		_, _ = integrationDB.ExecContext(ctx, "TRUNCATE usage_billing_outbox RESTART IDENTITY")
		user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("outbox-wallet-%s@example.com", uuid.NewString())})
		group := mustCreateGroup(t, client, &service.Group{Name: "outbox-wallet-" + uuid.NewString()})
		apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Name: service.WalletUniversalAPIKeyName, Key: "sk-outbox-wallet-" + uuid.NewString()})
		account := mustCreateAccount(t, client, &service.Account{Name: "outbox-wallet-account-" + uuid.NewString(), Type: service.AccountTypeOAuth})
		mustBindAccountToGroup(t, client, account.ID, group.ID, 1)
		now := time.Now().UTC()
		wallet, err := client.UserSubscription.Create().
			SetUserID(user.ID).
			SetStartsAt(now.Add(-time.Hour)).
			SetExpiresAt(now.Add(24 * time.Hour)).
			SetStatus(service.SubscriptionStatusActive).
			SetWalletBalanceUsd(10).
			SetWalletInitialUsd(10).
			SetAssignedAt(now).
			Save(ctx)
		require.NoError(t, err)
		walletID := wallet.ID
		envelope := integrationOutboxEnvelope(t, service.UsageBillingEnvelopeInput{
			RequestID:      "outbox-wallet-" + uuid.NewString(),
			APIKeyID:       apiKey.ID,
			UserID:         user.ID,
			AccountID:      account.ID,
			SubscriptionID: &walletID,
			GroupID:        &group.ID,
			AccountType:    service.AccountTypeOAuth,
			BillingModel:   "gpt-5.6-sol",
			BillingType:    service.BillingTypeSubscription,
			WalletCost:     2.5,
		})
		processIntegrationEnvelopeAfterEnqueue(t, ctx, envelope, func() {
			_, err := integrationDB.ExecContext(ctx, "UPDATE user_subscriptions SET deleted_at = NOW() WHERE id = $1", walletID)
			require.NoError(t, err)
		})

		var balance float64
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT wallet_balance_usd FROM user_subscriptions WHERE id = $1", walletID).Scan(&balance))
		require.InDelta(t, 7.5, balance, 0.000001)
		var ledgerCount int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM subscription_wallet_ledger WHERE subscription_id = $1 AND reason = 'usage'", walletID).Scan(&ledgerCount))
		require.Equal(t, 1, ledgerCount)
	})
}

func processIntegrationEnvelopeAfterEnqueue(t *testing.T, ctx context.Context, envelope service.UsageBillingEnvelope, afterEnqueue func()) {
	t.Helper()
	entClient := testEntClient(t)
	outboxRepo := NewUsageBillingOutboxRepository(integrationDB)
	billingRepo := NewUsageBillingRepository(entClient, integrationDB)
	event, _, err := outboxRepo.Enqueue(ctx, envelope)
	require.NoError(t, err)
	if afterEnqueue != nil {
		afterEnqueue()
	}
	claimed, err := outboxRepo.Claim(ctx, "integration-worker", 1, service.UsageBillingOutboxLeaseDuration)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, event.ID, claimed[0].ID)
	processor := service.NewUsageBillingOutboxProcessor(outboxRepo, outboxRepo, billingRepo, nil)
	require.NoError(t, processor.ProcessEvent(ctx, claimed[0]))
}
