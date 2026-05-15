package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func newWalletRepoWithMock(t *testing.T) (*walletRepository, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	return &walletRepository{db: db}, mock, db
}

func TestWalletRepo_Deduct_HappyPath(t *testing.T) {
	repo, mock, db := newWalletRepoWithMock(t)
	defer func() { _ = db.Close() }()

	usageLogID := int64(7777)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT wallet_balance_usd FROM user_subscriptions").
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_balance_usd"}).AddRow(100.0))
	mock.ExpectExec("UPDATE user_subscriptions").
		WithArgs(75.5, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO subscription_wallet_ledger").
		WithArgs(int64(42), -24.5, 75.5, "usage", sql.NullInt64{Int64: usageLogID, Valid: true}, sql.NullInt64{}, sql.NullString{}).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(99)))
	mock.ExpectCommit()

	entry, err := repo.Deduct(context.Background(), service.WalletDeductCommand{
		SubscriptionID: 42,
		CostUSD:        24.5,
		UsageLogID:     &usageLogID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(99), entry.ID)
	require.Equal(t, 75.5, entry.BalanceAfter)
	require.Equal(t, -24.5, entry.DeltaUSD)
	require.Equal(t, "usage", entry.Reason)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWalletRepo_Deduct_InsufficientBalance(t *testing.T) {
	repo, mock, db := newWalletRepoWithMock(t)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT wallet_balance_usd FROM user_subscriptions").
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_balance_usd"}).AddRow(10.0))
	mock.ExpectRollback()

	_, err := repo.Deduct(context.Background(), service.WalletDeductCommand{
		SubscriptionID: 42,
		CostUSD:        50.0,
	})
	require.ErrorIs(t, err, service.ErrWalletInsufficient)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWalletRepo_Deduct_SubscriptionNotFound(t *testing.T) {
	repo, mock, db := newWalletRepoWithMock(t)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT wallet_balance_usd FROM user_subscriptions").
		WithArgs(int64(42)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err := repo.Deduct(context.Background(), service.WalletDeductCommand{
		SubscriptionID: 42,
		CostUSD:        1.0,
	})
	require.ErrorIs(t, err, service.ErrWalletNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWalletRepo_Deduct_NotWalletMode(t *testing.T) {
	// 钱包列为 NULL = 不是钱包模式订阅 → ErrWalletNotFound
	repo, mock, db := newWalletRepoWithMock(t)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT wallet_balance_usd FROM user_subscriptions").
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_balance_usd"}).AddRow(nil))
	mock.ExpectRollback()

	_, err := repo.Deduct(context.Background(), service.WalletDeductCommand{
		SubscriptionID: 42,
		CostUSD:        1.0,
	})
	require.ErrorIs(t, err, service.ErrWalletNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWalletRepo_Deduct_RejectsZeroCost(t *testing.T) {
	repo, _, db := newWalletRepoWithMock(t)
	defer func() { _ = db.Close() }()

	_, err := repo.Deduct(context.Background(), service.WalletDeductCommand{
		SubscriptionID: 42,
		CostUSD:        0,
	})
	require.ErrorIs(t, err, service.ErrWalletNegativeDelta)
}

func TestWalletRepo_Adjust_RefundIncreasesBalance(t *testing.T) {
	repo, mock, db := newWalletRepoWithMock(t)
	defer func() { _ = db.Close() }()

	operatorID := int64(5)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT wallet_balance_usd FROM user_subscriptions").
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_balance_usd"}).AddRow(50.0))
	mock.ExpectExec("UPDATE user_subscriptions").
		WithArgs(70.0, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO subscription_wallet_ledger").
		WithArgs(int64(42), 20.0, 70.0, "refund", sql.NullInt64{}, sql.NullInt64{Int64: operatorID, Valid: true}, sql.NullString{String: "credited back", Valid: true}).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(101)))
	mock.ExpectCommit()

	entry, err := repo.Adjust(context.Background(), service.WalletAdjustCommand{
		SubscriptionID: 42,
		DeltaUSD:       20.0,
		Reason:         service.WalletLedgerReasonRefund,
		OperatorID:     &operatorID,
		Notes:          "credited back",
	})
	require.NoError(t, err)
	require.Equal(t, 70.0, entry.BalanceAfter)
	require.Equal(t, 20.0, entry.DeltaUSD)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWalletRepo_Adjust_NegativeDeltaCannotGoBelowZero(t *testing.T) {
	repo, mock, db := newWalletRepoWithMock(t)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT wallet_balance_usd FROM user_subscriptions").
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"wallet_balance_usd"}).AddRow(5.0))
	mock.ExpectRollback()

	_, err := repo.Adjust(context.Background(), service.WalletAdjustCommand{
		SubscriptionID: 42,
		DeltaUSD:       -10.0,
		Reason:         service.WalletLedgerReasonAdjustment,
	})
	require.ErrorIs(t, err, service.ErrWalletInsufficient)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWalletRepo_Adjust_BeginTxError(t *testing.T) {
	// 触发 BeginTx 失败的回退路径：模拟 connection 错误。
	repo, mock, db := newWalletRepoWithMock(t)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin().WillReturnError(errors.New("conn dead"))
	_, err := repo.Adjust(context.Background(), service.WalletAdjustCommand{
		SubscriptionID: 42,
		DeltaUSD:       1.0,
		Reason:         service.WalletLedgerReasonAdjustment,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "begin tx")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestWalletRepo_ReconcileBalances_DriftDetection 验证 cached vs ledger SUM
// 漂移检测：相等 / 容忍内 / 漂移超容忍三种情况都要正确分类。
func TestWalletRepo_ReconcileBalances_DriftDetection(t *testing.T) {
	repo, mock, db := newWalletRepoWithMock(t)
	defer func() { _ = db.Close() }()

	// 三条订阅：
	//   1) cached=100, sum=100         → 0 漂移，不上报
	//   2) cached=50.005, sum=50       → 0.005 < tolerance 0.01，不上报
	//   3) cached=42, sum=40           → 2.0 > tolerance，上报
	mock.ExpectQuery("SELECT us.id, us.wallet_balance_usd").
		WillReturnRows(sqlmock.NewRows([]string{"id", "wallet_balance_usd", "ledger_sum"}).
			AddRow(int64(1), 100.0, 100.0).
			AddRow(int64(2), 50.005, 50.0).
			AddRow(int64(3), 42.0, 40.0))

	drifts, err := repo.ReconcileBalances(context.Background(), 0.01)
	require.NoError(t, err)
	require.Len(t, drifts, 1, "only sub#3 should be flagged")
	require.Equal(t, int64(3), drifts[0].SubscriptionID)
	require.InDelta(t, 2.0, drifts[0].Drift, 0.0001)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestWalletRepo_ReconcileBalances_DefaultTolerance tolerance ≤ 0 时应回退
// 默认值 0.01；负 tolerance 不应误把所有订阅都标为漂移。
func TestWalletRepo_ReconcileBalances_DefaultTolerance(t *testing.T) {
	repo, mock, db := newWalletRepoWithMock(t)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT us.id, us.wallet_balance_usd").
		WillReturnRows(sqlmock.NewRows([]string{"id", "wallet_balance_usd", "ledger_sum"}).
			AddRow(int64(1), 100.0, 100.0))

	drifts, err := repo.ReconcileBalances(context.Background(), -1)
	require.NoError(t, err)
	require.Empty(t, drifts)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestWalletRepo_ReconcileBalances_QueryError SQL 错误时返回错误，不静默丢弃。
func TestWalletRepo_ReconcileBalances_QueryError(t *testing.T) {
	repo, mock, db := newWalletRepoWithMock(t)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT us.id, us.wallet_balance_usd").
		WillReturnError(errors.New("db down"))

	_, err := repo.ReconcileBalances(context.Background(), 0.01)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWalletRepo_ListLedger_OrderedAndScanned(t *testing.T) {
	repo, mock, db := newWalletRepoWithMock(t)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT id, subscription_id, delta_usd").
		WithArgs(int64(42), 50).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "subscription_id", "delta_usd", "balance_after", "reason",
			"usage_log_id", "operator_id", "notes",
		}).
			AddRow(int64(2), int64(42), -1.5, 8.5, "usage", int64(7), nil, "").
			AddRow(int64(1), int64(42), 10.0, 10.0, "activation", nil, nil, ""))

	out, err := repo.ListLedger(context.Background(), 42, 50)
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.Equal(t, "usage", out[0].Reason)
	require.NotNil(t, out[0].UsageLogID)
	require.Equal(t, int64(7), *out[0].UsageLogID)
	require.Equal(t, "activation", out[1].Reason)
	require.Nil(t, out[1].UsageLogID)
	require.NoError(t, mock.ExpectationsWereMet())
}
