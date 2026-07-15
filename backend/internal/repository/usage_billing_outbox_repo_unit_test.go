package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingOutboxRepository_EnqueueClassifiesStorageFailureForRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	sentinel := errors.New("database temporarily unavailable")
	mock.ExpectBegin().WillReturnError(sentinel)
	repo := NewUsageBillingOutboxRepository(db)
	groupID := int64(4)
	envelope, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID: "outbox-admission-retry", RequestPayloadHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		APIKeyID: 1, UserID: 2, AccountID: 3,
		GroupID: &groupID, AccountType: service.AccountTypeAPIKey,
		BillingModel: "gpt-5.6-sol", ExecutionModel: "gpt-5.6-sol",
		BillingType: service.BillingTypeBalance, InputTokens: 1,
		OccurredAtUnixMs: time.Now().UnixMilli(),
		PricingSource:    service.PricingSourceBuiltinGPT56, PricingRevision: service.GPT56PricingRevision,
		PricingHash:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RateMultiplier: 1, AccountRateMultiplier: 1,
	})
	require.NoError(t, err)

	_, _, err = repo.Enqueue(context.Background(), envelope)
	require.ErrorIs(t, err, service.ErrUsageBillingOutboxAdmissionRetryable)
	require.ErrorIs(t, err, sentinel)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingOutboxRepository_EnqueueKeepsInvalidEnvelopePermanent(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, _, err = NewUsageBillingOutboxRepository(db).Enqueue(context.Background(), service.UsageBillingEnvelope{})
	require.ErrorIs(t, err, service.ErrUsageBillingEnvelopeVersion)
	require.NotErrorIs(t, err, service.ErrUsageBillingOutboxAdmissionRetryable)
}

func TestUsageBillingOutboxRepository_EnqueueRequiresAdmissionForWalletCharge(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	envelope := usageBillingWalletPolicyEnvelope(t, 31, "")
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT[\\s\\S]+FROM usage_billing_outbox").
		WithArgs(envelope.RequestID(), envelope.APIKeyID()).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT a.binding_fingerprint").
		WithArgs(envelope.RequestID(), envelope.APIKeyID(), envelope.AccountID(), envelope.AccountType(), envelope.AdmissionAttemptID()).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, _, err = NewUsageBillingOutboxRepository(db).Enqueue(context.Background(), envelope)
	require.ErrorIs(t, err, service.ErrUsageBillingAdmissionMissing)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingOutboxRepository_ReconcileStaleAdmissions(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	preparedGrace := 5 * time.Minute
	dispatchedGrace := 24 * time.Hour
	mock.ExpectBegin()
	mock.ExpectExec("WITH candidates AS").
		WithArgs(preparedGrace.Milliseconds(), 5).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("WITH candidates AS").
		WithArgs(preparedGrace.Milliseconds(), 3).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("WITH candidates AS").
		WithArgs(dispatchedGrace.Milliseconds(), 2).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("WITH candidates AS").
		WithArgs(1).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := NewUsageBillingOutboxRepository(db).ReconcileStaleAdmissions(
		context.Background(),
		preparedGrace,
		dispatchedGrace,
		5,
	)

	require.NoError(t, err)
	require.Equal(t, service.UsageBillingAdmissionReconcileResult{
		AbandonedPrepared:         2,
		AbandonedFailedDispatched: 1,
		OrphanedDispatched:        1,
		FlaggedDeadLetter:         1,
	}, result)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingOutboxRepository_ReconcileStaleAdmissionsStopsAtSharedLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	preparedGrace := 5 * time.Minute
	mock.ExpectBegin()
	mock.ExpectExec("WITH candidates AS").
		WithArgs(preparedGrace.Milliseconds(), 2).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	result, err := NewUsageBillingOutboxRepository(db).ReconcileStaleAdmissions(
		context.Background(),
		preparedGrace,
		24*time.Hour,
		2,
	)

	require.NoError(t, err)
	require.Equal(t, service.UsageBillingAdmissionReconcileResult{AbandonedPrepared: 2}, result)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingOutboxRepository_ReconcileStaleAdmissionsReturnsZeroWhenTransactionRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	preparedGrace := 5 * time.Minute
	dispatchedGrace := 24 * time.Hour
	sentinel := errors.New("orphan reconciliation failed")
	mock.ExpectBegin()
	mock.ExpectExec("WITH candidates AS").
		WithArgs(preparedGrace.Milliseconds(), 3).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("WITH candidates AS").
		WithArgs(preparedGrace.Milliseconds(), 2).
		WillReturnError(sentinel)
	mock.ExpectRollback()

	result, err := NewUsageBillingOutboxRepository(db).ReconcileStaleAdmissions(
		context.Background(),
		preparedGrace,
		dispatchedGrace,
		3,
	)

	require.ErrorIs(t, err, sentinel)
	require.Zero(t, result.Total())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingOutboxRepository_ReconcileStaleAdmissionsZeroLimitIsNoOp(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	result, err := NewUsageBillingOutboxRepository(db).ReconcileStaleAdmissions(
		context.Background(),
		5*time.Minute,
		24*time.Hour,
		0,
	)

	require.NoError(t, err)
	require.Zero(t, result.Total())
	require.NoError(t, mock.ExpectationsWereMet())
}
