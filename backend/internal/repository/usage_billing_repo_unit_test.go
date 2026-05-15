//go:build unit

package repository

import (
	"context"
	"database/sql"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TestDeductUsageBillingWallet_HappyPath unified billing 事务内的钱包扣款 +
// ledger 落库。
func TestDeductUsageBillingWallet_HappyPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT wallet_balance_usd FROM user_subscriptions").
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_balance_usd"}).AddRow(100.0))
	mock.ExpectExec("UPDATE user_subscriptions").
		WithArgs(98.5, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO subscription_wallet_ledger").
		WithArgs(int64(42), -1.5, 98.5, sql.NullString{}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)

	newBalance, insufficient, err := deductUsageBillingWallet(context.Background(), tx, 42, 1.5)
	require.NoError(t, err)
	require.False(t, insufficient)
	require.InDelta(t, 98.5, newBalance, 0.0001)

	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDeductUsageBillingWallet_Insufficient 余额不足时不扣款也不落 ledger，
// 仅返回 insufficient=true 由调用方决定后续动作。
func TestDeductUsageBillingWallet_Insufficient(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT wallet_balance_usd FROM user_subscriptions").
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_balance_usd"}).AddRow(0.5))
	mock.ExpectCommit()

	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)

	balance, insufficient, err := deductUsageBillingWallet(context.Background(), tx, 42, 1.0)
	require.NoError(t, err)
	require.True(t, insufficient)
	require.InDelta(t, 0.5, balance, 0.0001)

	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDeductUsageBillingWallet_NotWalletMode wallet_balance_usd 为 NULL 视为
// 老 group 订阅，不该走到这条路径，返回 ErrSubscriptionNotFound。
func TestDeductUsageBillingWallet_NotWalletMode(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT wallet_balance_usd FROM user_subscriptions").
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_balance_usd"}).AddRow(nil))
	mock.ExpectRollback()

	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)

	_, _, err = deductUsageBillingWallet(context.Background(), tx, 42, 1.0)
	require.ErrorIs(t, err, service.ErrSubscriptionNotFound)

	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}
