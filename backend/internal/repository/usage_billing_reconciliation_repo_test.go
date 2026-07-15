package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingReconciliationRepositoryListsActionableCases(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT[\\s\\S]+FROM usage_billing_admissions AS admission").
		WithArgs(25).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "api_key_id", "state", "user_id", "subscription_id",
			"wallet_subscription_id", "wallet_reserved_usd", "outbox_id", "outbox_status",
			"last_error_code", "prepared_attempts", "dispatched_attempts", "failed_attempts",
			"finalized_attempts", "ever_dispatched_attempts", "prepared_at", "dispatched_at", "orphaned_at", "reconcile_started_at",
		}).AddRow(
			"req-list", int64(4), service.UsageBillingAdmissionStateReconcile, int64(7), int64(11),
			int64(11), 1.25, int64(31), service.UsageBillingOutboxStatusDeadLetter,
			"billing_failed", 0, 1, 2, 0, 1, now, now, nil, now,
		))

	items, err := NewUsageBillingOutboxRepository(db).ListUsageBillingReconciliationCases(context.Background(), 25)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "req-list", items[0].RequestID)
	require.Equal(t, service.UsageBillingOutboxStatusDeadLetter, items[0].OutboxStatus)
	require.NotNil(t, items[0].WalletSubscriptionID)
	require.Equal(t, int64(11), *items[0].WalletSubscriptionID)
	require.Equal(t, []string{service.UsageBillingReconciliationActionRetryDeadLetter}, items[0].AllowedActions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingReconciliationAllowedActionsExcludeUncloseableNonWalletCase(t *testing.T) {
	require.Empty(t, usageBillingReconciliationAllowedActions(service.UsageBillingReconciliationCase{
		State: service.UsageBillingAdmissionStateOrphaned, DispatchedAttempts: 1,
	}))

	walletID := int64(71)
	require.Equal(t, []string{
		service.UsageBillingReconciliationActionSettleDelivered,
	}, usageBillingReconciliationAllowedActions(service.UsageBillingReconciliationCase{
		State:                service.UsageBillingAdmissionStateOrphaned,
		WalletSubscriptionID: &walletID, DispatchedAttempts: 1, EverDispatchedAttempts: 1,
	}))
	require.Equal(t, []string{
		service.UsageBillingReconciliationActionReleaseUndelivered,
	}, usageBillingReconciliationAllowedActions(service.UsageBillingReconciliationCase{
		State:                service.UsageBillingAdmissionStateOrphaned,
		WalletSubscriptionID: &walletID, PreparedAttempts: 1,
	}))
	dispatchedAt := time.Now().UTC()
	require.Empty(t, usageBillingReconciliationAllowedActions(service.UsageBillingReconciliationCase{
		State:                service.UsageBillingAdmissionStateOrphaned,
		WalletSubscriptionID: &walletID, FailedAttempts: 1, DispatchedAt: &dispatchedAt,
	}), "admission-level dispatch evidence must fail closed even when attempt history is incomplete")
}

func TestUsageBillingReconciliationRepositoryRetriesDeadLetterAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	input := service.UsageBillingReconciliationResolveInput{
		RequestID: "req-retry", APIKeyID: 4, OperatorID: 9,
		Action:      service.UsageBillingReconciliationActionRetryDeadLetter,
		EvidenceRef: "incident:HFC-2026-0712",
	}
	mock.ExpectBegin()
	expectReconciliationWalletLookup(mock, input, nil)
	expectReconciliationAdmissionLock(mock, input, service.UsageBillingAdmissionStateReconcile, 7, nil, 0)
	mock.ExpectQuery("SELECT id, status,[\\s\\S]+last_error_code[\\s\\S]+FROM usage_billing_outbox").
		WithArgs(input.RequestID, input.APIKeyID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status", "last_error_code"}).
			AddRow(int64(31), service.UsageBillingOutboxStatusDeadLetter, "billing_failed"))
	mock.ExpectExec("UPDATE usage_billing_outbox[\\s\\S]+status = 'retry'").
		WithArgs(int64(31)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE usage_billing_admissions[\\s\\S]+state = 'outbox_pending'").
		WithArgs(input.RequestID, input.APIKeyID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectReconciliationAudit(mock, input, service.UsageBillingAdmissionStateReconcile,
		service.UsageBillingAdmissionStateOutboxPending, nil, nil, "billing_failed")
	mock.ExpectCommit()

	err = NewUsageBillingOutboxRepository(db).ResolveUsageBillingReconciliation(context.Background(), input)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingReconciliationRepositoryReleasesOnlyNeverDispatchedHold(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	walletID := int64(71)
	input := service.UsageBillingReconciliationResolveInput{
		RequestID: "req-release", APIKeyID: 4, OperatorID: 9,
		Action:      service.UsageBillingReconciliationActionReleaseUndelivered,
		EvidenceRef: "incident:HFC-2026-0712",
	}
	mock.ExpectBegin()
	expectReconciliationWalletLookup(mock, input, &walletID)
	expectReconciliationWalletLock(mock, walletID, 7, 10)
	expectReconciliationAdmissionLock(mock, input, service.UsageBillingAdmissionStateOrphaned, 7, &walletID, 1.25)
	expectNoReconciliationOutbox(mock, input)
	expectReconciliationAttempts(mock, input,
		[]string{"attempt_id", "state", "ever_dispatched"},
		[]driver.Value{"0123456789abcdef0123456789abcdef", service.UsageBillingAdmissionStatePrepared, false},
	)
	mock.ExpectExec("UPDATE usage_billing_admission_attempts[\\s\\S]+state = 'failed'").
		WithArgs(input.RequestID, input.APIKeyID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE usage_billing_admissions[\\s\\S]+state = 'reconcile'").
		WithArgs(input.RequestID, input.APIKeyID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE usage_billing_admissions[\\s\\S]+state = 'abandoned'").
		WithArgs(input.RequestID, input.APIKeyID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectReconciliationAudit(mock, input, service.UsageBillingAdmissionStateOrphaned,
		service.UsageBillingAdmissionStateAbandoned, nil, nil, "")
	mock.ExpectCommit()

	err = NewUsageBillingOutboxRepository(db).ResolveUsageBillingReconciliation(context.Background(), input)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingReconciliationRepositoryRejectsReleaseAfterAnyDispatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	walletID := int64(71)
	input := service.UsageBillingReconciliationResolveInput{
		RequestID: "req-unknown-delivery", APIKeyID: 4, OperatorID: 9,
		Action:      service.UsageBillingReconciliationActionReleaseUndelivered,
		EvidenceRef: "incident:HFC-2026-0712",
	}
	mock.ExpectBegin()
	expectReconciliationWalletLookup(mock, input, &walletID)
	expectReconciliationWalletLock(mock, walletID, 7, 10)
	expectReconciliationAdmissionLock(mock, input, service.UsageBillingAdmissionStateOrphaned, 7, &walletID, 1.25)
	expectNoReconciliationOutbox(mock, input)
	expectReconciliationAttempts(mock, input,
		[]string{"attempt_id", "state", "ever_dispatched"},
		[]driver.Value{"0123456789abcdef0123456789abcdef", service.UsageBillingAttemptStateFailed, true},
	)
	mock.ExpectRollback()

	err = NewUsageBillingOutboxRepository(db).ResolveUsageBillingReconciliation(context.Background(), input)
	require.ErrorIs(t, err, service.ErrUsageBillingReconciliationConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingReconciliationRepositoryRejectsReleaseWhenOnlyAdmissionShowsDispatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	walletID := int64(71)
	input := service.UsageBillingReconciliationResolveInput{
		RequestID: "req-incomplete-history", APIKeyID: 4, OperatorID: 9,
		Action:      service.UsageBillingReconciliationActionReleaseUndelivered,
		EvidenceRef: "incident:HFC-2026-0712",
	}
	mock.ExpectBegin()
	expectReconciliationWalletLookup(mock, input, &walletID)
	expectReconciliationWalletLock(mock, walletID, 7, 10)
	expectReconciliationAdmissionLock(mock, input, service.UsageBillingAdmissionStateOrphaned, 7, &walletID, 1.25, true)
	expectNoReconciliationOutbox(mock, input)
	expectReconciliationAttempts(mock, input,
		[]string{"attempt_id", "state", "ever_dispatched"},
		[]driver.Value{"0123456789abcdef0123456789abcdef", service.UsageBillingAttemptStateFailed, false},
	)
	mock.ExpectRollback()

	err = NewUsageBillingOutboxRepository(db).ResolveUsageBillingReconciliation(context.Background(), input)
	require.ErrorIs(t, err, service.ErrUsageBillingReconciliationConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingReconciliationRepositorySettlesDeliveredRequestAgainstFrozenReservation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	walletID := int64(71)
	envelope := reconciliationRepositoryWalletEnvelope(t, "req-settle", 4,
		"0123456789abcdef0123456789abcdef", 1)
	input := service.UsageBillingReconciliationResolveInput{
		RequestID: "req-settle", APIKeyID: 4, OperatorID: 9,
		Action:    service.UsageBillingReconciliationActionSettleDelivered,
		AttemptID: "0123456789abcdef0123456789abcdef", Envelope: envelope,
		EvidenceRef: "incident:HFC-2026-0712",
	}
	mock.ExpectBegin()
	expectReconciliationWalletLookup(mock, input, &walletID)
	expectReconciliationWalletLock(mock, walletID, 7, 0.25)
	expectReconciliationAdmissionLock(mock, input, service.UsageBillingAdmissionStateReconcile, 7, &walletID, 1.25)
	expectNoReconciliationOutbox(mock, input)
	expectReconciliationAttempts(mock, input,
		[]string{"attempt_id", "state", "ever_dispatched"},
		[]driver.Value{input.AttemptID, service.UsageBillingAdmissionStateDispatched, true},
		[]driver.Value{"fedcba9876543210fedcba9876543210", service.UsageBillingAdmissionStatePrepared, false},
	)
	expectReconciliationEnvelopeValidation(mock, envelope, service.UsageBillingAdmissionStateReconcile, 1.25)
	mock.ExpectExec("UPDATE usage_billing_admission_attempts[\\s\\S]+state = 'failed'").
		WithArgs(input.RequestID, input.APIKeyID, input.AttemptID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE usage_billing_admission_attempts[\\s\\S]+state = 'finalized'").
		WithArgs(input.RequestID, input.APIKeyID, input.AttemptID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_billing_outbox").
		WithArgs(input.RequestID, input.APIKeyID, envelope.RequestFingerprint(), envelope.Version(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE usage_billing_admissions[\\s\\S]+state = 'outbox_pending'").
		WithArgs(input.RequestID, input.APIKeyID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	cost := envelope.WalletCost()
	expectReconciliationAudit(mock, input, service.UsageBillingAdmissionStateReconcile,
		service.UsageBillingAdmissionStateOutboxPending, &input.AttemptID, &cost, "")
	mock.ExpectCommit()

	err = NewUsageBillingOutboxRepository(db).ResolveUsageBillingReconciliation(context.Background(), input)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingReconciliationRepositoryRejectsSettlementAboveReservation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	walletID := int64(71)
	envelope := reconciliationRepositoryWalletEnvelope(t, "req-over", 4,
		"0123456789abcdef0123456789abcdef", 1.01)
	input := service.UsageBillingReconciliationResolveInput{
		RequestID: "req-over", APIKeyID: 4, OperatorID: 9,
		Action:    service.UsageBillingReconciliationActionSettleDelivered,
		AttemptID: "0123456789abcdef0123456789abcdef", Envelope: envelope,
		EvidenceRef: "incident:HFC-2026-0712",
	}
	mock.ExpectBegin()
	expectReconciliationWalletLookup(mock, input, &walletID)
	expectReconciliationWalletLock(mock, walletID, 7, 10)
	expectReconciliationAdmissionLock(mock, input, service.UsageBillingAdmissionStateReconcile, 7, &walletID, 1)
	mock.ExpectRollback()

	err = NewUsageBillingOutboxRepository(db).ResolveUsageBillingReconciliation(context.Background(), input)
	require.ErrorIs(t, err, service.ErrUsageBillingReconciliationConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingReconciliationRepositoryRejectsZeroCostSettlement(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	walletID := int64(71)
	envelope := reconciliationRepositoryWalletEnvelope(t, "req-zero", 4,
		"0123456789abcdef0123456789abcdef", 0)
	input := service.UsageBillingReconciliationResolveInput{
		RequestID: "req-zero", APIKeyID: 4, OperatorID: 9,
		Action:    service.UsageBillingReconciliationActionSettleDelivered,
		AttemptID: "0123456789abcdef0123456789abcdef", Envelope: envelope,
		EvidenceRef: "incident:HFC-2026-0712",
	}
	mock.ExpectBegin()
	expectReconciliationWalletLookup(mock, input, &walletID)
	expectReconciliationWalletLock(mock, walletID, 7, 10)
	expectReconciliationAdmissionLock(mock, input, service.UsageBillingAdmissionStateReconcile, 7, &walletID, 1)
	mock.ExpectRollback()

	err = NewUsageBillingOutboxRepository(db).ResolveUsageBillingReconciliation(context.Background(), input)
	require.ErrorIs(t, err, service.ErrUsageBillingReconciliationConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingReconciliationRepositoryRejectsInflatedSecondarySettlementCost(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	walletID := int64(71)
	envelope := reconciliationRepositoryWalletEnvelopeWithCosts(t, "req-secondary", 4,
		"0123456789abcdef0123456789abcdef", 1, 99, 1, 0.88)
	input := service.UsageBillingReconciliationResolveInput{
		RequestID: "req-secondary", APIKeyID: 4, OperatorID: 9,
		Action:    service.UsageBillingReconciliationActionSettleDelivered,
		AttemptID: "0123456789abcdef0123456789abcdef", Envelope: envelope,
		EvidenceRef: "incident:HFC-2026-0712",
	}
	mock.ExpectBegin()
	expectReconciliationWalletLookup(mock, input, &walletID)
	expectReconciliationWalletLock(mock, walletID, 7, 10)
	expectReconciliationAdmissionLock(mock, input, service.UsageBillingAdmissionStateReconcile, 7, &walletID, 1)
	mock.ExpectRollback()

	err = NewUsageBillingOutboxRepository(db).ResolveUsageBillingReconciliation(context.Background(), input)
	require.ErrorIs(t, err, service.ErrUsageBillingReconciliationConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func expectReconciliationEnvelopeValidation(
	mock sqlmock.Sqlmock,
	envelope service.UsageBillingEnvelope,
	state string,
	worstCaseCost float64,
) {
	ownerToken := "11111111111111111111111111111111"
	admission, err := service.NewUsageBillingAdmission(service.UsageBillingAdmissionInput{
		RequestID: envelope.RequestID(), APIKeyID: envelope.APIKeyID(),
		AuthCacheLocator: envelope.AuthCacheLocator(), UserID: envelope.UserID(),
		AccountID: envelope.AccountID(), SubscriptionID: envelope.SubscriptionID(),
		GroupID: envelope.GroupID(), EffectiveBillingGroupID: envelope.EffectiveBillingGroupID(),
		AccountType: envelope.AccountType(), BillingType: envelope.BillingType(),
		OwnerToken: ownerToken, AttemptID: envelope.AdmissionAttemptID(),
		BillingModel: envelope.BillingModel(), RequestPayloadHash: envelope.RequestPayloadHash(),
		PricingSource: envelope.PricingSource(), PricingRevision: envelope.PricingRevision(),
		PricingHash: envelope.PricingHash(), RateMultiplier: envelope.RateMultiplier(),
		WorstCaseCostUSD: worstCaseCost,
	})
	if err != nil {
		panic(err)
	}
	walletID := *envelope.SubscriptionID()
	groupID := *envelope.GroupID()
	effectiveGroupID := *envelope.EffectiveBillingGroupID()
	mock.ExpectQuery("SELECT a.binding_fingerprint[\\s\\S]+FROM usage_billing_admissions a").
		WithArgs(envelope.RequestID(), envelope.APIKeyID(), envelope.AccountID(), envelope.AccountType(), envelope.AdmissionAttemptID()).
		WillReturnRows(sqlmock.NewRows([]string{
			"binding_fingerprint", "owner_token", "request_payload_hash", "auth_cache_locator",
			"user_id", "subscription_id", "group_id", "effective_billing_group_id", "billing_type",
			"billing_model", "pricing_source", "pricing_revision", "pricing_hash", "rate_multiplier",
			"worst_case_cost_usd", "alternate_billing_model", "alternate_pricing_source",
			"alternate_pricing_revision", "alternate_pricing_hash", "alternate_rate_multiplier",
			"wallet_subscription_id", "state", "attempt_id", "account_id", "account_type", "attempt_state",
		}).AddRow(
			admission.Fingerprint(), ownerToken, envelope.RequestPayloadHash(), envelope.AuthCacheLocator(),
			envelope.UserID(), walletID, groupID, effectiveGroupID, envelope.BillingType(),
			envelope.BillingModel(), envelope.PricingSource(), envelope.PricingRevision(), envelope.PricingHash(),
			envelope.RateMultiplier(), worstCaseCost, "", "", "", "", 0,
			walletID, state, envelope.AdmissionAttemptID(), envelope.AccountID(), envelope.AccountType(),
			service.UsageBillingAttemptStateDispatched,
		))
}

func reconciliationRepositoryWalletEnvelope(
	t *testing.T,
	requestID string,
	apiKeyID int64,
	attemptID string,
	cost float64,
) service.UsageBillingEnvelope {
	return reconciliationRepositoryWalletEnvelopeWithCosts(
		t, requestID, apiKeyID, attemptID, cost, cost, cost, (cost/1.25)*1.1,
	)
}

func reconciliationRepositoryWalletEnvelopeWithCosts(
	t *testing.T,
	requestID string,
	apiKeyID int64,
	attemptID string,
	cost float64,
	apiKeyQuotaCost float64,
	apiKeyRateLimitCost float64,
	accountQuotaCost float64,
) service.UsageBillingEnvelope {
	t.Helper()
	walletID := int64(71)
	groupID := int64(44)
	envelope, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID: requestID, RequestPayloadHash: strings.Repeat("d", 64),
		AdmissionAttemptID: attemptID, APIKeyID: apiKeyID,
		AuthCacheLocator: strings.Repeat("c", 64), UserID: 7, AccountID: 33,
		SubscriptionID: &walletID, GroupID: &groupID, EffectiveBillingGroupID: &groupID,
		AccountType: service.AccountTypeAPIKey, BillingModel: "gpt-5.6-sol",
		BillingType: service.BillingTypeSubscription, InputTokens: 100, OutputTokens: 20,
		PricingSource:   service.PricingSourceBuiltinGPT56,
		PricingRevision: service.GPT56PricingRevision, PricingHash: strings.Repeat("a", 64),
		RateMultiplier: 1.25, AccountRateMultiplier: 1.1,
		WalletCost: cost, APIKeyQuotaCost: apiKeyQuotaCost, APIKeyRateLimitCost: apiKeyRateLimitCost,
		AccountQuotaCost: accountQuotaCost, ActualCost: cost, TotalCost: cost / 1.25,
	})
	require.NoError(t, err)
	return envelope
}

func expectReconciliationWalletLookup(
	mock sqlmock.Sqlmock,
	input service.UsageBillingReconciliationResolveInput,
	walletID *int64,
) {
	rows := sqlmock.NewRows([]string{"wallet_subscription_id"})
	if walletID == nil {
		rows.AddRow(nil)
	} else {
		rows.AddRow(*walletID)
	}
	mock.ExpectQuery("SELECT wallet_subscription_id[\\s\\S]+FROM usage_billing_admissions").
		WithArgs(input.RequestID, input.APIKeyID).
		WillReturnRows(rows)
}

func expectReconciliationWalletLock(mock sqlmock.Sqlmock, walletID, userID int64, balance float64) {
	mock.ExpectQuery("SELECT user_id, wallet_balance_usd[\\s\\S]+FROM user_subscriptions").
		WithArgs(walletID).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "wallet_balance_usd"}).AddRow(userID, balance))
}

func expectReconciliationAdmissionLock(
	mock sqlmock.Sqlmock,
	input service.UsageBillingReconciliationResolveInput,
	state string,
	userID int64,
	walletID *int64,
	reservedUSD float64,
	everDispatched ...bool,
) {
	var wallet any
	if walletID != nil {
		wallet = *walletID
	}
	wasDispatched := false
	if len(everDispatched) > 0 {
		wasDispatched = everDispatched[0]
	}
	mock.ExpectQuery("SELECT state, user_id, wallet_subscription_id, wallet_reserved_usd[\\s\\S]+dispatched_at IS NOT NULL[\\s\\S]+FROM usage_billing_admissions").
		WithArgs(input.RequestID, input.APIKeyID).
		WillReturnRows(sqlmock.NewRows([]string{"state", "user_id", "wallet_subscription_id", "wallet_reserved_usd", "ever_dispatched"}).
			AddRow(state, userID, wallet, reservedUSD, wasDispatched))
}

func expectNoReconciliationOutbox(mock sqlmock.Sqlmock, input service.UsageBillingReconciliationResolveInput) {
	mock.ExpectQuery("SELECT id, status,[\\s\\S]+last_error_code[\\s\\S]+FROM usage_billing_outbox").
		WithArgs(input.RequestID, input.APIKeyID).
		WillReturnError(sql.ErrNoRows)
}

func expectReconciliationAttempts(
	mock sqlmock.Sqlmock,
	input service.UsageBillingReconciliationResolveInput,
	columns []string,
	values ...[]driver.Value,
) {
	rows := sqlmock.NewRows(columns)
	for _, row := range values {
		rows.AddRow(row...)
	}
	mock.ExpectQuery("SELECT attempt_id, state,[\\s\\S]+dispatched_at IS NOT NULL[\\s\\S]+FROM usage_billing_admission_attempts").
		WithArgs(input.RequestID, input.APIKeyID).
		WillReturnRows(rows)
}

func expectReconciliationAudit(
	mock sqlmock.Sqlmock,
	input service.UsageBillingReconciliationResolveInput,
	fromState string,
	toState string,
	attemptID *string,
	actualCostUSD *float64,
	previousErrorCode string,
) {
	var attempt any
	if attemptID != nil {
		attempt = *attemptID
	}
	var cost any
	if actualCostUSD != nil {
		cost = *actualCostUSD
	}
	var previousError any
	if previousErrorCode != "" {
		previousError = previousErrorCode
	}
	mock.ExpectExec("INSERT INTO usage_billing_reconciliation_audits").
		WithArgs(
			input.RequestID, input.APIKeyID, input.Action, input.OperatorID, input.EvidenceRef,
			attempt, cost, fromState, toState, previousError,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
}
