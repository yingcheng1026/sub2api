package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// walletRepository 实现 service.WalletRepository。
//
// 设计要点：
//   - 直接走 *sql.DB，避开 ent 的 transaction 抽象，原因是：
//     1) ent 不直接支持 FOR UPDATE row-level lock 的 fluent API
//     2) 钱包扣款是热路径，少一层抽象 = 少一份分配开销
//   - DECIMAL(20,10) 精度匹配 user_subscriptions.wallet_balance_usd 列定义；
//     用 float64 即可承载（业务最大值远小于 2^53）。
//   - 调用方应保证 wallet_balance_usd 列已是 NOT NULL（CHECK 约束保证：
//     只有钱包模式订阅会进 wallet_repo，wallet_balance_usd 必非 NULL）。
type walletRepository struct {
	db *sql.DB
}

func NewWalletRepository(_ *dbent.Client, sqlDB *sql.DB) service.WalletRepository {
	return &walletRepository{db: sqlDB}
}

func (r *walletRepository) Deduct(ctx context.Context, cmd service.WalletDeductCommand) (service.WalletLedgerEntry, error) {
	if cmd.SubscriptionID <= 0 {
		return service.WalletLedgerEntry{}, service.ErrWalletNotFound
	}
	if cmd.CostUSD <= 0 {
		return service.WalletLedgerEntry{}, service.ErrWalletNegativeDelta
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return service.WalletLedgerEntry{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	// FOR UPDATE 锁住订阅行，串行化同 user 多并发请求的扣款。
	// 同时 NOWAIT 不能用：v3 老订阅的 daily_usage_usd UPDATE 也走同一行，
	// 简单的 FOR UPDATE 排队即可。
	var balance sql.NullFloat64
	err = tx.QueryRowContext(ctx, `
		SELECT wallet_balance_usd
		FROM user_subscriptions
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE
	`, cmd.SubscriptionID).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return service.WalletLedgerEntry{}, service.ErrWalletNotFound
	}
	if err != nil {
		return service.WalletLedgerEntry{}, fmt.Errorf("lock subscription row: %w", err)
	}
	if !balance.Valid {
		// 不是钱包模式订阅
		return service.WalletLedgerEntry{}, service.ErrWalletNotFound
	}

	if balance.Float64 < cmd.CostUSD {
		return service.WalletLedgerEntry{}, service.ErrWalletInsufficient
	}

	newBalance := balance.Float64 - cmd.CostUSD
	if _, err := tx.ExecContext(ctx, `
		UPDATE user_subscriptions
		SET wallet_balance_usd = $1, updated_at = NOW()
		WHERE id = $2
	`, newBalance, cmd.SubscriptionID); err != nil {
		return service.WalletLedgerEntry{}, fmt.Errorf("update balance: %w", err)
	}

	entry, err := insertLedger(ctx, tx, ledgerInsert{
		subscriptionID: cmd.SubscriptionID,
		deltaUSD:       -cmd.CostUSD,
		balanceAfter:   newBalance,
		reason:         service.WalletLedgerReasonUsage,
		usageLogID:     cmd.UsageLogID,
	})
	if err != nil {
		return service.WalletLedgerEntry{}, err
	}

	if err := tx.Commit(); err != nil {
		return service.WalletLedgerEntry{}, fmt.Errorf("commit: %w", err)
	}
	tx = nil
	return entry, nil
}

func (r *walletRepository) Adjust(ctx context.Context, cmd service.WalletAdjustCommand) (service.WalletLedgerEntry, error) {
	if cmd.SubscriptionID <= 0 {
		return service.WalletLedgerEntry{}, service.ErrWalletNotFound
	}
	if cmd.DeltaUSD == 0 {
		return service.WalletLedgerEntry{}, service.ErrWalletNegativeDelta
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return service.WalletLedgerEntry{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	var balance sql.NullFloat64
	err = tx.QueryRowContext(ctx, `
		SELECT wallet_balance_usd
		FROM user_subscriptions
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE
	`, cmd.SubscriptionID).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return service.WalletLedgerEntry{}, service.ErrWalletNotFound
	}
	if err != nil {
		return service.WalletLedgerEntry{}, fmt.Errorf("lock subscription row: %w", err)
	}
	if !balance.Valid {
		return service.WalletLedgerEntry{}, service.ErrWalletNotFound
	}

	newBalance := balance.Float64 + cmd.DeltaUSD
	// 约束：调整后余额不可负 (DB CHECK 兜底允许 -0.01 浮点抖动；应用层提前拦)
	if newBalance < 0 {
		return service.WalletLedgerEntry{}, service.ErrWalletInsufficient
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE user_subscriptions
		SET wallet_balance_usd = $1, updated_at = NOW()
		WHERE id = $2
	`, newBalance, cmd.SubscriptionID); err != nil {
		return service.WalletLedgerEntry{}, fmt.Errorf("update balance: %w", err)
	}

	entry, err := insertLedger(ctx, tx, ledgerInsert{
		subscriptionID: cmd.SubscriptionID,
		deltaUSD:       cmd.DeltaUSD,
		balanceAfter:   newBalance,
		reason:         cmd.Reason,
		operatorID:     cmd.OperatorID,
		notes:          cmd.Notes,
	})
	if err != nil {
		return service.WalletLedgerEntry{}, err
	}

	if err := tx.Commit(); err != nil {
		return service.WalletLedgerEntry{}, fmt.Errorf("commit: %w", err)
	}
	tx = nil
	return entry, nil
}

// ReconcileBalances 把所有钱包模式订阅的 cached wallet_balance_usd 与
// subscription_wallet_ledger.SUM(delta_usd) 对比，返回漂移超过 tolerance 的
// 条目。漂移 0 / -0 视为相等。
//
// 设计：
//   - 所有 ledger 行的 delta 总和 = 当前 wallet_balance_usd（activation 加 +bal，
//     usage 减 -cost，refund 再加 +bal …）；任何单调插入都不会改变这个不变量。
//   - 用 LEFT JOIN COALESCE(SUM, 0) 兼容刚开通还没出账的订阅。
//   - DECIMAL(20,10) 在 sql 端聚合时自动转 float64，精度足够。
func (r *walletRepository) ReconcileBalances(ctx context.Context, tolerance float64) ([]service.WalletReconcileDrift, error) {
	if tolerance < 0 {
		tolerance = 0.01
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT us.id, us.wallet_balance_usd, COALESCE(SUM(l.delta_usd), 0)
		FROM user_subscriptions us
		LEFT JOIN subscription_wallet_ledger l ON l.subscription_id = us.id
		WHERE us.wallet_balance_usd IS NOT NULL
		  AND us.deleted_at IS NULL
		GROUP BY us.id, us.wallet_balance_usd
	`)
	if err != nil {
		return nil, fmt.Errorf("reconcile query: %w", err)
	}
	defer rows.Close()

	var out []service.WalletReconcileDrift
	for rows.Next() {
		var (
			id        int64
			cached    float64
			ledgerSum float64
		)
		if err := rows.Scan(&id, &cached, &ledgerSum); err != nil {
			return nil, fmt.Errorf("reconcile scan: %w", err)
		}
		drift := cached - ledgerSum
		if drift < 0 {
			drift = -drift
		}
		if drift > tolerance {
			out = append(out, service.WalletReconcileDrift{
				SubscriptionID: id,
				Cached:         cached,
				LedgerSum:      ledgerSum,
				Drift:          drift,
			})
		}
	}
	return out, rows.Err()
}

func (r *walletRepository) ListLedger(ctx context.Context, subscriptionID int64, limit int) ([]service.WalletLedgerEntry, error) {
	if subscriptionID <= 0 {
		return nil, service.ErrWalletNotFound
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, subscription_id, delta_usd, balance_after, reason,
		       usage_log_id, operator_id, COALESCE(notes, '')
		FROM subscription_wallet_ledger
		WHERE subscription_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2
	`, subscriptionID, limit)
	if err != nil {
		return nil, fmt.Errorf("query ledger: %w", err)
	}
	defer rows.Close()

	out := make([]service.WalletLedgerEntry, 0, limit)
	for rows.Next() {
		var e service.WalletLedgerEntry
		var usageLogID, operatorID sql.NullInt64
		if err := rows.Scan(&e.ID, &e.SubscriptionID, &e.DeltaUSD, &e.BalanceAfter, &e.Reason,
			&usageLogID, &operatorID, &e.Notes); err != nil {
			return nil, fmt.Errorf("scan ledger: %w", err)
		}
		if usageLogID.Valid {
			v := usageLogID.Int64
			e.UsageLogID = &v
		}
		if operatorID.Valid {
			v := operatorID.Int64
			e.OperatorID = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type ledgerInsert struct {
	subscriptionID int64
	deltaUSD       float64
	balanceAfter   float64
	reason         string
	usageLogID     *int64
	operatorID     *int64
	notes          string
}

func insertLedger(ctx context.Context, tx *sql.Tx, in ledgerInsert) (service.WalletLedgerEntry, error) {
	var usageLog, operator sql.NullInt64
	if in.usageLogID != nil {
		usageLog = sql.NullInt64{Int64: *in.usageLogID, Valid: true}
	}
	if in.operatorID != nil {
		operator = sql.NullInt64{Int64: *in.operatorID, Valid: true}
	}
	var notes sql.NullString
	if in.notes != "" {
		notes = sql.NullString{String: in.notes, Valid: true}
	}

	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO subscription_wallet_ledger
			(subscription_id, delta_usd, balance_after, reason, usage_log_id, operator_id, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, in.subscriptionID, in.deltaUSD, in.balanceAfter, in.reason, usageLog, operator, notes).Scan(&id)
	if err != nil {
		return service.WalletLedgerEntry{}, fmt.Errorf("insert ledger: %w", err)
	}

	return service.WalletLedgerEntry{
		ID:             id,
		SubscriptionID: in.subscriptionID,
		DeltaUSD:       in.deltaUSD,
		BalanceAfter:   in.balanceAfter,
		Reason:         in.reason,
		UsageLogID:     in.usageLogID,
		OperatorID:     in.operatorID,
		Notes:          in.notes,
	}, nil
}
