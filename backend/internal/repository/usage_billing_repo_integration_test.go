//go:build integration

package repository

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestUsageBillingRepositoryApply_DeduplicatesBalanceBilling(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-" + uuid.NewString(),
		Name:   "billing",
		Quota:  1,
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "usage-billing-account-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:           requestID,
		APIKeyID:            apiKey.ID,
		UserID:              user.ID,
		AccountID:           account.ID,
		AccountType:         service.AccountTypeAPIKey,
		BalanceCost:         1.25,
		APIKeyQuotaCost:     1.25,
		APIKeyRateLimitCost: 1.25,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.NotNil(t, result1)
	require.True(t, result1.Applied)
	require.True(t, result1.APIKeyQuotaExhausted)

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.NotNil(t, result2)
	require.False(t, result2.Applied)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 98.75, balance, 0.000001)

	var quotaUsed float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used FROM api_keys WHERE id = $1", apiKey.ID).Scan(&quotaUsed))
	require.InDelta(t, 1.25, quotaUsed, 0.000001)

	var usage5h float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT usage_5h FROM api_keys WHERE id = $1", apiKey.ID).Scan(&usage5h))
	require.InDelta(t, 1.25, usage5h, 0.000001)

	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT status FROM api_keys WHERE id = $1", apiKey.ID).Scan(&status))
	require.Equal(t, service.StatusAPIKeyQuotaExhausted, status)

	var dedupCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2", requestID, apiKey.ID).Scan(&dedupCount))
	require.Equal(t, 1, dedupCount)
}

func TestUsageBillingRepositoryApply_DeduplicatesSubscriptionBilling(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-sub-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "usage-billing-group-" + uuid.NewString(),
		Platform:         service.PlatformAnthropic,
		SubscriptionType: service.SubscriptionTypeSubscription,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-sub-" + uuid.NewString(),
		Name:    "billing-sub",
	})
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:  user.ID,
		GroupID: repositoryInt64Ptr(group.ID),
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:        requestID,
		APIKeyID:         apiKey.ID,
		UserID:           user.ID,
		AccountID:        0,
		SubscriptionID:   &subscription.ID,
		SubscriptionCost: 2.5,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result1.Applied)

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result2.Applied)

	var dailyUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1", subscription.ID).Scan(&dailyUsage))
	require.InDelta(t, 2.5, dailyUsage, 0.000001)
}

func TestUsageBillingRepositoryApply_RequestFingerprintConflict(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-conflict-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-conflict-" + uuid.NewString(),
		Name:   "billing-conflict",
	})

	requestID := uuid.NewString()
	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:   requestID,
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		BalanceCost: 1.25,
	})
	require.NoError(t, err)

	_, err = repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:   requestID,
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		BalanceCost: 2.50,
	})
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
}

func TestUsageBillingRepositoryApply_UpdatesAccountQuota(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-account-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-account-" + uuid.NewString(),
		Name:   "billing-account",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "usage-billing-account-quota-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
		Extra: map[string]any{
			"quota_limit": 100.0,
		},
	})

	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:        uuid.NewString(),
		APIKeyID:         apiKey.ID,
		UserID:           user.ID,
		AccountID:        account.ID,
		AccountType:      service.AccountTypeAPIKey,
		AccountQuotaCost: 3.5,
	})
	require.NoError(t, err)

	var quotaUsed float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COALESCE((extra->>'quota_used')::numeric, 0) FROM accounts WHERE id = $1", account.ID).Scan(&quotaUsed))
	require.InDelta(t, 3.5, quotaUsed, 0.000001)
}

func TestUsageBillingRepositoryApply_EnqueuesSchedulerOutboxOnQuotaCrossing(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	newFixture := func(t *testing.T, extra map[string]any) (int64, int64) {
		t.Helper()
		user := mustCreateUser(t, client, &service.User{
			Email:        fmt.Sprintf("usage-billing-outbox-user-%d-%s@example.com", time.Now().UnixNano(), uuid.NewString()),
			PasswordHash: "hash",
		})
		apiKey := mustCreateApiKey(t, client, &service.APIKey{
			UserID: user.ID,
			Key:    "sk-usage-billing-outbox-" + uuid.NewString(),
			Name:   "billing-outbox",
		})
		account := mustCreateAccount(t, client, &service.Account{
			Name:  "usage-billing-outbox-" + uuid.NewString(),
			Type:  service.AccountTypeAPIKey,
			Extra: extra,
		})
		return apiKey.ID, account.ID
	}

	outboxCountFor := func(t *testing.T, accountID int64) int {
		t.Helper()
		var count int
		require.NoError(t, integrationDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND account_id = $2",
			service.SchedulerOutboxEventAccountChanged, accountID,
		).Scan(&count))
		return count
	}

	t.Run("daily_first_crossing_enqueues", func(t *testing.T) {
		apiKeyID, accountID := newFixture(t, map[string]any{
			"quota_daily_limit": 10.0,
		})
		// 第一次低于日限额：不应入队 outbox
		_, err := repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID:        uuid.NewString(),
			APIKeyID:         apiKeyID,
			AccountID:        accountID,
			AccountType:      service.AccountTypeAPIKey,
			AccountQuotaCost: 4,
		})
		require.NoError(t, err)
		require.Equal(t, 0, outboxCountFor(t, accountID), "below limit should not enqueue")

		// 第二次跨越日限额：应入队一次 outbox
		_, err = repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID:        uuid.NewString(),
			APIKeyID:         apiKeyID,
			AccountID:        accountID,
			AccountType:      service.AccountTypeAPIKey,
			AccountQuotaCost: 8,
		})
		require.NoError(t, err)
		require.Equal(t, 1, outboxCountFor(t, accountID), "crossing daily limit should enqueue once")

		// 再次递增（已超）：不应重复入队
		_, err = repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID:        uuid.NewString(),
			APIKeyID:         apiKeyID,
			AccountID:        accountID,
			AccountType:      service.AccountTypeAPIKey,
			AccountQuotaCost: 2,
		})
		require.NoError(t, err)
		require.Equal(t, 1, outboxCountFor(t, accountID), "subsequent increments beyond limit should not re-enqueue")
	})

	t.Run("weekly_first_crossing_enqueues", func(t *testing.T) {
		apiKeyID, accountID := newFixture(t, map[string]any{
			"quota_weekly_limit": 10.0,
		})
		_, err := repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID:        uuid.NewString(),
			APIKeyID:         apiKeyID,
			AccountID:        accountID,
			AccountType:      service.AccountTypeAPIKey,
			AccountQuotaCost: 15, // 单次即跨越
		})
		require.NoError(t, err)
		require.Equal(t, 1, outboxCountFor(t, accountID), "single-shot crossing weekly limit should enqueue once")
	})
}

func TestDashboardAggregationRepositoryCleanupUsageBillingDedup_BatchDeletesOldRows(t *testing.T) {
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(integrationDB)

	oldRequestID := "dedup-old-" + uuid.NewString()
	newRequestID := "dedup-new-" + uuid.NewString()
	oldCreatedAt := time.Now().UTC().AddDate(0, 0, -400)
	newCreatedAt := time.Now().UTC().Add(-time.Hour)

	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO usage_billing_dedup (request_id, api_key_id, request_fingerprint, created_at)
		VALUES ($1, 1, $2, $3), ($4, 1, $5, $6)
	`,
		oldRequestID, strings.Repeat("a", 64), oldCreatedAt,
		newRequestID, strings.Repeat("b", 64), newCreatedAt,
	)
	require.NoError(t, err)

	require.NoError(t, repo.CleanupUsageBillingDedup(ctx, time.Now().UTC().AddDate(0, 0, -365)))

	var oldCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1", oldRequestID).Scan(&oldCount))
	require.Equal(t, 0, oldCount)

	var newCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1", newRequestID).Scan(&newCount))
	require.Equal(t, 1, newCount)

	var archivedCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup_archive WHERE request_id = $1", oldRequestID).Scan(&archivedCount))
	require.Equal(t, 1, archivedCount)
}

func TestUsageBillingRepositoryApply_DeduplicatesAgainstArchivedKey(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	aggRepo := newDashboardAggregationRepositoryWithSQL(integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-archive-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-archive-" + uuid.NewString(),
		Name:   "billing-archive",
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:   requestID,
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		BalanceCost: 1.25,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result1.Applied)

	_, err = integrationDB.ExecContext(ctx, `
		UPDATE usage_billing_dedup
		SET created_at = $1
		WHERE request_id = $2 AND api_key_id = $3
	`, time.Now().UTC().AddDate(0, 0, -400), requestID, apiKey.ID)
	require.NoError(t, err)
	require.NoError(t, aggRepo.CleanupUsageBillingDedup(ctx, time.Now().UTC().AddDate(0, 0, -365)))

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result2.Applied)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 98.75, balance, 0.000001)
}

func TestUsageLogInsert_WritesBillingUsageLedgerEntry(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-ledger-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-ledger-" + uuid.NewString(),
		Name:   "billing-ledger",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "usage-ledger-account-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
	})

	var usageLogID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO usage_logs (
			user_id, api_key_id, account_id, request_id, model,
			input_tokens, output_tokens, total_cost, actual_cost, billing_type, created_at
		)
		VALUES ($1, $2, $3, $4, 'gpt-5.4-mini', 12, 4, 1.25, 0.75, $5, NOW())
		RETURNING id
	`, user.ID, apiKey.ID, account.ID, "usage-ledger-"+uuid.NewString(), service.BillingTypeBalance).Scan(&usageLogID))

	var (
		ledgerUserID     int64
		ledgerAPIKeyID   int64
		ledgerType       int16
		ledgerApplied    bool
		ledgerDeltaUSD   float64
		ledgerEntryCount int
	)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT user_id, api_key_id, billing_type, applied, delta_usd::double precision
		FROM billing_usage_entries
		WHERE usage_log_id = $1
	`, usageLogID).Scan(&ledgerUserID, &ledgerAPIKeyID, &ledgerType, &ledgerApplied, &ledgerDeltaUSD))
	require.Equal(t, user.ID, ledgerUserID)
	require.Equal(t, apiKey.ID, ledgerAPIKeyID)
	require.Equal(t, int16(service.BillingTypeBalance), ledgerType)
	require.True(t, ledgerApplied)
	require.InDelta(t, 0.75, ledgerDeltaUSD, 0.000001)

	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM billing_usage_entries WHERE usage_log_id = $1
	`, usageLogID).Scan(&ledgerEntryCount))
	require.Equal(t, 1, ledgerEntryCount)
}

func TestUsageBillingLedgerMigration_BackfillsMissingEntryIdempotently(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-ledger-backfill-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-ledger-backfill-" + uuid.NewString(),
		Name:   "billing-ledger-backfill",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "usage-ledger-backfill-account-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
	})

	var usageLogID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO usage_logs (
			user_id, api_key_id, account_id, request_id, model,
			input_tokens, output_tokens, total_cost, actual_cost, billing_type, created_at
		)
		VALUES ($1, $2, $3, $4, 'gpt-5.4-mini', 12, 4, 1.25, 0.75, $5, NOW())
		RETURNING id
	`, user.ID, apiKey.ID, account.ID, "usage-ledger-backfill-"+uuid.NewString(), service.BillingTypeBalance).Scan(&usageLogID))

	_, err := integrationDB.ExecContext(ctx, "DELETE FROM billing_usage_entries WHERE usage_log_id = $1", usageLogID)
	require.NoError(t, err)

	migrationSQL, err := fs.ReadFile(migrations.FS, "134_usage_billing_ledger_trigger.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	var (
		ledgerEntryCount int
		ledgerDeltaUSD   float64
	)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(delta_usd), 0)::double precision
		FROM billing_usage_entries
		WHERE usage_log_id = $1
	`, usageLogID).Scan(&ledgerEntryCount, &ledgerDeltaUSD))
	require.Equal(t, 1, ledgerEntryCount)
	require.InDelta(t, 0.75, ledgerDeltaUSD, 0.000001)
}

func TestBillingUsageEntry_PostsBalancedDoubleEntryLedger(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("double-entry-usage-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-double-entry-usage-" + uuid.NewString(),
		Name:   "double-entry-usage",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "double-entry-usage-account-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
	})

	var usageLogID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO usage_logs (
			user_id, api_key_id, account_id, request_id, model,
			input_tokens, output_tokens, total_cost, actual_cost, billing_type, created_at
		)
		VALUES ($1, $2, $3, $4, 'gpt-5.4-mini', 12, 4, 1.25, 0.75, $5, NOW())
		RETURNING id
	`, user.ID, apiKey.ID, account.ID, "double-entry-usage-"+uuid.NewString(), service.BillingTypeBalance).Scan(&usageLogID))

	var (
		billingEntryID int64
		transactionID  int64
		txType         string
		txAmount       float64
	)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT b.id, lt.id, lt.transaction_type, lt.amount_usd::double precision
		FROM billing_usage_entries b
		JOIN ledger_transactions lt
		  ON lt.idempotency_key = 'billing_usage_entry:' || b.id::text
		WHERE b.usage_log_id = $1
	`, usageLogID).Scan(&billingEntryID, &transactionID, &txType, &txAmount))
	require.NotZero(t, billingEntryID)
	require.NotZero(t, transactionID)
	require.Equal(t, "usage_charge", txType)
	require.InDelta(t, 0.75, txAmount, 0.000001)

	assertBalancedLedgerTransaction(t, ctx, transactionID, user.ID, 0.75, "liability", "revenue:api_usage")
}

func TestRedeemCodeBalance_PostsWalletDoubleEntryLedger(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("double-entry-redeem-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})

	var redeemCodeID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO redeem_codes (code, type, value, status, used_by, used_at, created_at)
		VALUES ($1, 'admin_balance', 12.34, 'used', $2, NOW(), NOW())
		RETURNING id
	`, "DOUBLE-ENTRY-"+uuid.NewString()[:12], user.ID).Scan(&redeemCodeID))

	var transactionID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT id
		FROM ledger_transactions
		WHERE idempotency_key = 'redeem_code:' || $1::bigint::text || ':wallet'
	`, redeemCodeID).Scan(&transactionID))

	assertBalancedLedgerTransaction(t, ctx, transactionID, user.ID, 12.34, "expense", "user_wallet")
}

func TestPaymentRefundAudit_PostsWalletDebitDoubleEntryLedger(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("double-entry-refund-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})

	var orderID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO payment_orders (
			user_id, user_email, user_name, amount, pay_amount, recharge_code,
			order_type, status, expires_at, paid_at, completed_at, created_at, updated_at
		)
		VALUES ($1, $2, '', 20.00, 20.00, $3, 'balance', 'COMPLETED', NOW() + INTERVAL '1 hour', NOW(), NOW(), NOW(), NOW())
		RETURNING id
	`, user.ID, user.Email, "REFUND-"+uuid.NewString()[:12]).Scan(&orderID))

	var auditID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO payment_audit_logs (order_id, action, detail, operator, created_at)
		VALUES ($1, 'REFUND_SUCCESS', '{"refundAmount":5.00,"balanceDeducted":3.21,"force":false}', 'admin', NOW())
		RETURNING id
	`, fmt.Sprintf("%d", orderID)).Scan(&auditID))

	var transactionID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT id
		FROM ledger_transactions
		WHERE idempotency_key = 'payment_audit_log:' || $1::bigint::text || ':refund_balance_deduction'
	`, auditID).Scan(&transactionID))

	assertBalancedLedgerTransaction(t, ctx, transactionID, user.ID, 3.21, "liability", "asset:payment_cash_clearing")
}

func TestDoubleEntryLedgerMigration_BackfillsExistingSourcesIdempotently(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("double-entry-backfill-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-double-entry-backfill-" + uuid.NewString(),
		Name:   "double-entry-backfill",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "double-entry-backfill-account-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
	})

	var usageLogID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO usage_logs (
			user_id, api_key_id, account_id, request_id, model,
			input_tokens, output_tokens, total_cost, actual_cost, billing_type, created_at
		)
		VALUES ($1, $2, $3, $4, 'gpt-5.4-mini', 12, 4, 1.25, 0.75, $5, NOW())
		RETURNING id
	`, user.ID, apiKey.ID, account.ID, "double-entry-backfill-"+uuid.NewString(), service.BillingTypeBalance).Scan(&usageLogID))

	var billingEntryID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT id FROM billing_usage_entries WHERE usage_log_id = $1
	`, usageLogID).Scan(&billingEntryID))

	_, err := integrationDB.ExecContext(ctx, `
		DELETE FROM ledger_transactions
		WHERE idempotency_key = 'billing_usage_entry:' || $1::bigint::text
	`, billingEntryID)
	require.NoError(t, err)

	migrationSQL, err := fs.ReadFile(migrations.FS, "135_double_entry_billing_ledger.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	var (
		transactionCount int
		lineCount        int
		debitTotal       float64
		creditTotal      float64
	)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT
			COUNT(DISTINCT lt.id),
			COUNT(ll.id),
			COALESCE(SUM(ll.amount_usd) FILTER (WHERE ll.side = 'debit'), 0)::double precision,
			COALESCE(SUM(ll.amount_usd) FILTER (WHERE ll.side = 'credit'), 0)::double precision
		FROM ledger_transactions lt
		LEFT JOIN ledger_lines ll ON ll.transaction_id = lt.id
		WHERE lt.idempotency_key = 'billing_usage_entry:' || $1::bigint::text
	`, billingEntryID).Scan(&transactionCount, &lineCount, &debitTotal, &creditTotal))
	require.Equal(t, 1, transactionCount)
	require.Equal(t, 2, lineCount)
	require.InDelta(t, 0.75, debitTotal, 0.000001)
	require.InDelta(t, 0.75, creditTotal, 0.000001)
}

func TestDoubleEntryLedgerMigration_PostsOpeningBalanceAdjustment(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("double-entry-opening-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      9.99,
	})

	migrationSQL, err := fs.ReadFile(migrations.FS, "135_double_entry_billing_ledger.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	var (
		transactionCount int
		walletBalance    float64
		difference       float64
		unbalancedCount  int
	)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM ledger_transactions
		WHERE idempotency_key = 'opening_wallet_balance:user:' || $1::bigint::text || ':135'
	`, user.ID).Scan(&transactionCount))
	require.Equal(t, 1, transactionCount)

	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT ledger_wallet_balance_usd::double precision, difference_usd::double precision
		FROM ledger_wallet_reconciliation
		WHERE user_id = $1
	`, user.ID).Scan(&walletBalance, &difference))
	require.InDelta(t, 9.99, walletBalance, 0.000001)
	require.InDelta(t, 0, difference, 0.000001)

	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM ledger_unbalanced_transactions
	`).Scan(&unbalancedCount))
	require.Equal(t, 0, unbalancedCount)
}

func assertBalancedLedgerTransaction(t *testing.T, ctx context.Context, transactionID, userID int64, expectedAmount float64, debitAccountType, creditAccountCode string) {
	t.Helper()

	var (
		lineCount        int
		debitTotal       float64
		creditTotal      float64
		hasDebitAccount  bool
		hasCreditAccount bool
		unbalancedCount  int
	)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(ll.amount_usd) FILTER (WHERE ll.side = 'debit'), 0)::double precision,
			COALESCE(SUM(ll.amount_usd) FILTER (WHERE ll.side = 'credit'), 0)::double precision,
			COALESCE(BOOL_OR(ll.side = 'debit' AND (
				($2 = 'user_wallet' AND la.owner_type = 'user' AND la.owner_id = $3)
				OR la.account_type = $2
				OR la.account_code = $2
			)), FALSE),
			COALESCE(BOOL_OR(ll.side = 'credit' AND (
				($4 = 'user_wallet' AND la.owner_type = 'user' AND la.owner_id = $3)
				OR la.account_type = $4
				OR la.account_code = $4
			)), FALSE)
		FROM ledger_lines ll
		JOIN ledger_accounts la ON la.id = ll.account_id
		WHERE ll.transaction_id = $1
	`, transactionID, debitAccountType, userID, creditAccountCode).Scan(
		&lineCount,
		&debitTotal,
		&creditTotal,
		&hasDebitAccount,
		&hasCreditAccount,
	))
	require.Equal(t, 2, lineCount)
	require.InDelta(t, expectedAmount, debitTotal, 0.000001)
	require.InDelta(t, expectedAmount, creditTotal, 0.000001)
	require.True(t, hasDebitAccount)
	require.True(t, hasCreditAccount)

	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM ledger_unbalanced_transactions WHERE transaction_id = $1
	`, transactionID).Scan(&unbalancedCount))
	require.Equal(t, 0, unbalancedCount)
}
