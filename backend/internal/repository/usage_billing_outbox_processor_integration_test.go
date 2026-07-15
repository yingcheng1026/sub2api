//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	if input.RequestPayloadHash == "" {
		input.RequestPayloadHash = integrationUsageRequestPayloadHash
	}
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
		AuthCacheLocator:    apiKey.KeyHash,
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

	t.Run("monthly plan coverage", func(t *testing.T) {
		_, _ = integrationDB.ExecContext(ctx, "TRUNCATE usage_billing_outbox RESTART IDENTITY")
		user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("outbox-monthly-coverage-%s@example.com", uuid.NewString())})
		anchorGroup := mustCreateGroup(t, client, &service.Group{
			Name:             "outbox-monthly-anchor-" + uuid.NewString(),
			SubscriptionType: service.SubscriptionTypeSubscription,
		})
		routingGroup := mustCreateGroup(t, client, &service.Group{
			Name:             "outbox-monthly-routing-" + uuid.NewString(),
			SubscriptionType: service.SubscriptionTypeStandard,
		})
		plan, err := client.SubscriptionPlan.Create().
			SetName("outbox-monthly-coverage-" + uuid.NewString()).
			SetPrice(99).
			SetGroupID(anchorGroup.ID).
			SetValidityDays(30).
			SetValidityUnit("day").
			SetPlanType(service.PlanTypeSubscription).
			Save(ctx)
		require.NoError(t, err)
		for _, groupID := range []int64{anchorGroup.ID, routingGroup.ID} {
			_, err = client.SubscriptionPlanGroup.Create().SetPlanID(plan.ID).SetGroupID(groupID).Save(ctx)
			require.NoError(t, err)
		}
		apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &routingGroup.ID, Key: "sk-outbox-monthly-coverage-" + uuid.NewString()})
		account := mustCreateAccount(t, client, &service.Account{Name: "outbox-monthly-coverage-account-" + uuid.NewString(), Type: service.AccountTypeOAuth})
		mustBindAccountToGroup(t, client, account.ID, routingGroup.ID, 1)
		subscription := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, GroupID: &anchorGroup.ID})
		envelope := integrationOutboxEnvelope(t, service.UsageBillingEnvelopeInput{
			RequestID:               "outbox-monthly-coverage-" + uuid.NewString(),
			APIKeyID:                apiKey.ID,
			UserID:                  user.ID,
			AccountID:               account.ID,
			SubscriptionID:          &subscription.ID,
			GroupID:                 &routingGroup.ID,
			EffectiveBillingGroupID: &anchorGroup.ID,
			AccountType:             service.AccountTypeOAuth,
			BillingModel:            "gpt-5.6-sol",
			BillingType:             service.BillingTypeSubscription,
			SubscriptionCost:        2.5,
		})

		processIntegrationEnvelopeAfterEnqueue(t, ctx, envelope, nil)

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
		group := mustGetOrCreateWalletBusinessGroup(t, client,
			service.WalletDefaultOpenAIGroupName, service.PlatformOpenAI, false, 1)
		wallet := mustCreateCreditsWallet(t, client, user.ID, 10)
		apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Name: service.WalletUniversalAPIKeyName, Purpose: service.APIKeyPurposeWalletUniversal, Key: "sk-outbox-wallet-" + uuid.NewString()})
		account := mustCreateAccount(t, client, &service.Account{Name: "outbox-wallet-account-" + uuid.NewString(), Type: service.AccountTypeOAuth})
		mustBindAccountToGroup(t, client, account.ID, group.ID, 1)
		walletID := wallet.ID
		requestID := "outbox-wallet-" + uuid.NewString()
		ownerToken := strings.Repeat("a", 32)
		attemptID := strings.Repeat("b", 32)
		admission, err := service.NewUsageBillingAdmission(service.UsageBillingAdmissionInput{
			RequestID: requestID, RequestPayloadHash: integrationUsageRequestPayloadHash,
			APIKeyID: apiKey.ID, AuthCacheLocator: apiKey.KeyHash,
			UserID: user.ID, AccountID: account.ID, SubscriptionID: &walletID,
			GroupID: &group.ID, EffectiveBillingGroupID: &group.ID,
			AccountType: account.Type, BillingType: service.BillingTypeSubscription,
			OwnerToken: ownerToken, AttemptID: attemptID, BillingModel: "gpt-5.6-sol",
			PricingSource: service.PricingSourceBuiltinGPT56, PricingRevision: service.GPT56PricingRevision,
			PricingHash:    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			RateMultiplier: 1, WorstCaseCostUSD: 2.5,
		})
		require.NoError(t, err)
		admissionRepo := NewUsageBillingOutboxRepository(integrationDB)
		require.NoError(t, admissionRepo.Admit(ctx, admission))
		require.NoError(t, admissionRepo.MarkDispatched(ctx, service.UsageBillingAdmissionAttemptRef{
			RequestID: admission.RequestID(), APIKeyID: admission.APIKeyID(),
			OwnerToken: admission.OwnerToken(), AttemptID: admission.AttemptID(),
		}))
		envelope := integrationOutboxEnvelope(t, service.UsageBillingEnvelopeInput{
			RequestID:          requestID,
			RequestPayloadHash: integrationUsageRequestPayloadHash,
			AdmissionAttemptID: attemptID,
			APIKeyID:           apiKey.ID,
			AuthCacheLocator:   apiKey.KeyHash,
			UserID:             user.ID,
			AccountID:          account.ID,
			SubscriptionID:     &walletID,
			GroupID:            &group.ID,
			AccountType:        service.AccountTypeOAuth,
			BillingModel:       "gpt-5.6-sol",
			BillingType:        service.BillingTypeSubscription,
			WalletCost:         2.5,
		})
		processIntegrationEnvelopeAfterEnqueue(t, ctx, envelope, nil)

		var balance float64
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT wallet_balance_usd FROM user_subscriptions WHERE id = $1", walletID).Scan(&balance))
		require.InDelta(t, 7.5, balance, 0.000001)
		var ledgerCount int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM subscription_wallet_ledger WHERE subscription_id = $1 AND reason = 'usage'", walletID).Scan(&ledgerCount))
		require.Equal(t, 1, ledgerCount)
	})
}

func TestUsageBillingOutboxProcessorIntegration_PendingEventSurvivesWorkerRestart(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE usage_billing_outbox RESTART IDENTITY")
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("outbox-restart-%s@example.com", uuid.NewString()), Balance: 100})
	group := mustCreateGroup(t, client, &service.Group{Name: "outbox-restart-" + uuid.NewString()})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-outbox-restart-" + uuid.NewString()})
	account := mustCreateAccount(t, client, &service.Account{Name: "outbox-restart-" + uuid.NewString(), Type: service.AccountTypeAPIKey})
	mustBindAccountToGroup(t, client, account.ID, group.ID, 1)
	billingModel := "gpt-5.6-sol"
	upstreamModel := billingModel
	pricingSource := service.PricingSourceBuiltinGPT56
	pricingRevision := service.GPT56PricingRevision
	pricingHash := "abababababababababababababababababababababababababababababababab"
	accountRate := 1.0
	requestID := "outbox-restart-" + uuid.NewString()
	usageLog := &service.UsageLog{
		UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID, RequestID: requestID,
		Model: billingModel, RequestedModel: "claude-sonnet-4-6", UpstreamModel: &upstreamModel,
		BillingModel: &billingModel, PricingSource: &pricingSource, PricingRevision: &pricingRevision,
		PricingHash: &pricingHash, GroupID: &group.ID, InputTokens: 100, OutputTokens: 10,
		InputCost: 1, OutputCost: 0.25, TotalCost: 1.25, ActualCost: 1.25,
		RateMultiplier: 1, AccountRateMultiplier: &accountRate, BillingType: service.BillingTypeBalance,
		RequestType: service.RequestTypeStream, CreatedAt: time.Now().UTC(),
	}
	cmd := &service.UsageBillingCommand{
		RequestID: requestID, APIKeyID: apiKey.ID, UserID: user.ID, AccountID: account.ID,
		RequestPayloadHash: integrationUsageRequestPayloadHash,
		AccountType:        service.AccountTypeAPIKey, Model: billingModel, BillingType: service.BillingTypeBalance,
		InputTokens: 100, OutputTokens: 10, BalanceCost: 1.25,
	}
	envelope, err := service.NewUsageBillingEnvelopeFromUsageLog(usageLog, cmd)
	require.NoError(t, err)

	firstProcessRepo := NewUsageBillingOutboxRepository(integrationDB)
	_, inserted, err := firstProcessRepo.Enqueue(ctx, envelope)
	require.NoError(t, err)
	require.True(t, inserted)
	// Simulate process exit before the in-memory wake hint or worker can run.
	firstProcessRepo = nil

	restartedOutboxRepo := NewUsageBillingOutboxRepository(integrationDB)
	billingRepo := NewUsageBillingRepository(client, integrationDB)
	usageRepo := NewUsageLogRepository(client, integrationDB)
	processor := service.NewUsageBillingOutboxProcessor(
		restartedOutboxRepo,
		restartedOutboxRepo,
		billingRepo,
		service.NewUsageBillingReplayWriter(usageRepo),
	)
	processed, err := processor.ProcessBatch(ctx, "restart-worker", 10)
	require.NoError(t, err)
	require.Equal(t, 1, processed)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 98.75, balance, 0.000001)
	var count int
	var model, requested, storedBilling string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*), MAX(model), MAX(requested_model), MAX(billing_model)
		FROM usage_logs WHERE request_id = $1 AND api_key_id = $2
	`, requestID, apiKey.ID).Scan(&count, &model, &requested, &storedBilling))
	require.Equal(t, 1, count)
	require.Equal(t, billingModel, model)
	require.Equal(t, "claude-sonnet-4-6", requested)
	require.Equal(t, billingModel, storedBilling)
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
