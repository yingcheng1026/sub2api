//go:build unit

package repository

import (
	"context"
	"database/sql"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSettleUsageBillingWalletAdmission_ConsumesHoldWithActualCost(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT wallet_subscription_id FROM usage_billing_admissions").
		WithArgs("wallet-request", int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_subscription_id"}).AddRow(int64(42)))
	mock.ExpectQuery("SELECT wallet_balance_usd FROM user_subscriptions").
		WithArgs(int64(42), true).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_balance_usd"}).AddRow(0.5))
	mock.ExpectQuery("SELECT state, wallet_subscription_id, wallet_reserved_usd").
		WithArgs("wallet-request", int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{
			"state", "wallet_subscription_id", "wallet_reserved_usd", "wallet_consumed_at", "wallet_released_at",
		}).AddRow(service.UsageBillingAdmissionStateOutboxPending, int64(42), 1.0, nil, nil))
	mock.ExpectExec("UPDATE user_subscriptions").
		WithArgs(-0.5, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO subscription_wallet_ledger").
		WithArgs(int64(42), -1.0, -0.5).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE usage_billing_admissions").
		WithArgs("wallet-request", int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	subscriptionID := int64(42)
	handled, balance, insufficient, err := settleUsageBillingWalletAdmission(context.Background(), tx, &service.UsageBillingCommand{
		RequestID:      "wallet-request",
		APIKeyID:       7,
		SubscriptionID: &subscriptionID,
		WalletCost:     1,
		BindingsFrozen: true,
	})
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, insufficient)
	require.InDelta(t, -0.5, balance, 0.000001)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSettleUsageBillingWalletAdmission_ZeroCostStillReleasesSlot(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT wallet_subscription_id FROM usage_billing_admissions").
		WithArgs("free-wallet-request", int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_subscription_id"}).AddRow(int64(43)))
	mock.ExpectQuery("SELECT wallet_balance_usd FROM user_subscriptions").
		WithArgs(int64(43), true).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_balance_usd"}).AddRow(9.0))
	mock.ExpectQuery("SELECT state, wallet_subscription_id, wallet_reserved_usd").
		WithArgs("free-wallet-request", int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{
			"state", "wallet_subscription_id", "wallet_reserved_usd", "wallet_consumed_at", "wallet_released_at",
		}).AddRow(service.UsageBillingAdmissionStateOutboxPending, int64(43), 9.0, nil, nil))
	mock.ExpectExec("UPDATE usage_billing_admissions").
		WithArgs("free-wallet-request", int64(8)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	subscriptionID := int64(43)
	handled, balance, insufficient, err := settleUsageBillingWalletAdmission(context.Background(), tx, &service.UsageBillingCommand{
		RequestID:      "free-wallet-request",
		APIKeyID:       8,
		SubscriptionID: &subscriptionID,
		BindingsFrozen: true,
	})
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, insufficient)
	require.InDelta(t, 9, balance, 0.000001)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSettleUsageBillingWalletAdmission_MissingAdmissionUsesLegacyPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT wallet_subscription_id FROM usage_billing_admissions").
		WithArgs("legacy-wallet-request", int64(9)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	handled, _, _, err := settleUsageBillingWalletAdmission(context.Background(), tx, &service.UsageBillingCommand{
		RequestID:      "legacy-wallet-request",
		APIKeyID:       9,
		BindingsFrozen: true,
	})
	require.NoError(t, err)
	require.False(t, handled)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}
