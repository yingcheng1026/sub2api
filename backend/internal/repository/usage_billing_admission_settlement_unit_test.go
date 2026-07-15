package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSettleUsageBillingWalletAdmissionRejectsCostAboveReservation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	subscriptionID := int64(71)
	cmd := &service.UsageBillingCommand{
		RequestID: "wallet-over-reservation", APIKeyID: 11,
		SubscriptionID: &subscriptionID, WalletCost: 2, BindingsFrozen: true,
	}
	mock.ExpectQuery("SELECT wallet_subscription_id").
		WithArgs(cmd.RequestID, cmd.APIKeyID).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_subscription_id"}).AddRow(subscriptionID))
	mock.ExpectQuery("SELECT wallet_balance_usd").
		WithArgs(subscriptionID, true).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_balance_usd"}).AddRow(10.0))
	mock.ExpectQuery("SELECT state, wallet_subscription_id, wallet_reserved_usd").
		WithArgs(cmd.RequestID, cmd.APIKeyID).
		WillReturnRows(sqlmock.NewRows([]string{
			"state", "wallet_subscription_id", "wallet_reserved_usd", "wallet_consumed_at", "wallet_released_at",
		}).AddRow(service.UsageBillingAdmissionStateOutboxPending, subscriptionID, 1.0, nil, nil))

	handled, _, _, err := settleUsageBillingWalletAdmission(context.Background(), tx, cmd)
	require.False(t, handled)
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSettleUsageBillingNonWalletAdmissionMarksSettled(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	cmd := &service.UsageBillingCommand{
		RequestID: "balance-admission-settlement", APIKeyID: 11, BindingsFrozen: true,
	}
	mock.ExpectQuery("SELECT state, wallet_subscription_id").
		WithArgs(cmd.RequestID, cmd.APIKeyID).
		WillReturnRows(sqlmock.NewRows([]string{"state", "wallet_subscription_id"}).
			AddRow(service.UsageBillingAdmissionStateOutboxPending, nil))
	mock.ExpectExec("UPDATE usage_billing_admissions").
		WithArgs(cmd.RequestID, cmd.APIKeyID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, settleUsageBillingNonWalletAdmission(context.Background(), tx, cmd))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSettleUsageBillingNonWalletAdmissionAllowsLegacyEventWithoutAdmission(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	cmd := &service.UsageBillingCommand{
		RequestID: "legacy-before-admission", APIKeyID: 11, BindingsFrozen: true,
	}
	mock.ExpectQuery("SELECT state, wallet_subscription_id").
		WithArgs(cmd.RequestID, cmd.APIKeyID).
		WillReturnError(sql.ErrNoRows)

	require.NoError(t, settleUsageBillingNonWalletAdmission(context.Background(), tx, cmd))
	require.NoError(t, mock.ExpectationsWereMet())
}
