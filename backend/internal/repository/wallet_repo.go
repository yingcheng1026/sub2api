package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

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

func (r *walletRepository) withWalletMutation(ctx context.Context, fn func(sqlQueryExecutor) (service.WalletLedgerEntry, error)) (service.WalletLedgerEntry, error) {
	if existingTx := dbent.TxFromContext(ctx); existingTx != nil {
		exec := sqlExecutorFromEntClient(existingTx.Client())
		if exec == nil {
			return service.WalletLedgerEntry{}, fmt.Errorf("wallet transaction executor is unavailable")
		}
		return fn(exec)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return service.WalletLedgerEntry{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	entry, err := fn(tx)
	if err != nil {
		return service.WalletLedgerEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.WalletLedgerEntry{}, fmt.Errorf("commit: %w", err)
	}
	return entry, nil
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

	if cmd.UsageLogID != nil {
		existing, found, err := findUsageLedger(ctx, tx, *cmd.UsageLogID)
		if err != nil {
			return service.WalletLedgerEntry{}, fmt.Errorf("query usage ledger: %w", err)
		}
		if found {
			if existing.SubscriptionID != cmd.SubscriptionID {
				return service.WalletLedgerEntry{}, fmt.Errorf("usage log is already settled for another subscription")
			}
			if err := tx.Commit(); err != nil {
				return service.WalletLedgerEntry{}, fmt.Errorf("commit replay lookup: %w", err)
			}
			tx = nil
			return existing, nil
		}
	}

	if balance.Float64 < cmd.CostUSD && !cmd.PostpaidSettlement {
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
		if cmd.UsageLogID != nil && isUniqueConstraintViolation(err) {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				return service.WalletLedgerEntry{}, fmt.Errorf("rollback duplicate usage settlement: %w", rollbackErr)
			}
			tx = nil
			existing, found, lookupErr := findUsageLedger(ctx, r.db, *cmd.UsageLogID)
			if lookupErr != nil {
				return service.WalletLedgerEntry{}, fmt.Errorf("query winning usage ledger: %w", lookupErr)
			}
			if found {
				if existing.SubscriptionID != cmd.SubscriptionID {
					return service.WalletLedgerEntry{}, fmt.Errorf("usage log is already settled for another subscription")
				}
				return existing, nil
			}
		}
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
	// 管理员负向扣减不能把正常钱包扣成欠费；但已经因 post-response
	// settlement 进入负数的钱包，必须允许退款/补偿等正向金额逐步还债。
	if cmd.DeltaUSD < 0 && newBalance < 0 {
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

func (r *walletRepository) RecordActivation(ctx context.Context, cmd service.WalletActivationCommand) (service.WalletLedgerEntry, error) {
	if cmd.SubscriptionID <= 0 {
		return service.WalletLedgerEntry{}, service.ErrWalletNotFound
	}
	if cmd.InitialUSD <= 0 {
		return service.WalletLedgerEntry{}, service.ErrWalletNegativeDelta
	}

	return r.withWalletMutation(ctx, func(exec sqlQueryExecutor) (service.WalletLedgerEntry, error) {
		var balance, initial sql.NullFloat64
		err := scanSingleRow(ctx, exec, `
			SELECT wallet_balance_usd, wallet_initial_usd
			FROM user_subscriptions
			WHERE id = $1 AND deleted_at IS NULL
			FOR UPDATE
		`, []any{cmd.SubscriptionID}, &balance, &initial)
		if errors.Is(err, sql.ErrNoRows) {
			return service.WalletLedgerEntry{}, service.ErrWalletNotFound
		}
		if err != nil {
			return service.WalletLedgerEntry{}, fmt.Errorf("lock subscription row: %w", err)
		}
		if !balance.Valid || !initial.Valid {
			return service.WalletLedgerEntry{}, service.ErrWalletNotFound
		}
		if math.Abs(initial.Float64-cmd.InitialUSD) > 0.0000001 || math.Abs(balance.Float64-cmd.InitialUSD) > 0.01 {
			return service.WalletLedgerEntry{}, fmt.Errorf("wallet activation amount does not match opening balance")
		}

		var existing service.WalletLedgerEntry
		var usageLogID, operatorID sql.NullInt64
		var notes sql.NullString
		err = scanSingleRow(ctx, exec, `
			SELECT id, subscription_id, delta_usd, balance_after, reason,
			       usage_log_id, operator_id, notes
			FROM subscription_wallet_ledger
			WHERE subscription_id = $1 AND reason = 'activation'
			ORDER BY id
			LIMIT 1
		`, []any{cmd.SubscriptionID}, &existing.ID, &existing.SubscriptionID, &existing.DeltaUSD,
			&existing.BalanceAfter, &existing.Reason, &usageLogID, &operatorID, &notes)
		if err == nil {
			if math.Abs(existing.DeltaUSD-cmd.InitialUSD) > 0.0000001 {
				return service.WalletLedgerEntry{}, fmt.Errorf("wallet activation ledger conflicts with opening balance")
			}
			if operatorID.Valid {
				v := operatorID.Int64
				existing.OperatorID = &v
			}
			if notes.Valid {
				existing.Notes = notes.String
			}
			return existing, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return service.WalletLedgerEntry{}, fmt.Errorf("query activation ledger: %w", err)
		}

		return insertLedger(ctx, exec, ledgerInsert{
			subscriptionID: cmd.SubscriptionID,
			deltaUSD:       cmd.InitialUSD,
			balanceAfter:   balance.Float64,
			reason:         service.WalletLedgerReasonActivation,
			paymentOrderID: cmd.PaymentOrderID,
			operatorID:     cmd.OperatorID,
			notes:          cmd.Notes,
		})
	})
}

// Topup 额度卡叠加 (B2.4)：同时 +balance 和 +initial，写 reason='topup' 流水。
// DeltaUSD 必须 > 0；非钱包模式订阅（wallet_balance_usd IS NULL）返 ErrWalletNotFound。
func (r *walletRepository) Topup(ctx context.Context, cmd service.WalletTopupCommand) (service.WalletLedgerEntry, error) {
	if cmd.SubscriptionID <= 0 {
		return service.WalletLedgerEntry{}, service.ErrWalletNotFound
	}
	if cmd.DeltaUSD <= 0 {
		return service.WalletLedgerEntry{}, service.ErrWalletNegativeDelta
	}

	return r.withWalletMutation(ctx, func(exec sqlQueryExecutor) (service.WalletLedgerEntry, error) {
		var balance, initial sql.NullFloat64
		err := scanSingleRow(ctx, exec, `
			SELECT wallet_balance_usd, wallet_initial_usd
			FROM user_subscriptions
			WHERE id = $1 AND deleted_at IS NULL
			FOR UPDATE
		`, []any{cmd.SubscriptionID}, &balance, &initial)
		if errors.Is(err, sql.ErrNoRows) {
			return service.WalletLedgerEntry{}, service.ErrWalletNotFound
		}
		if err != nil {
			return service.WalletLedgerEntry{}, fmt.Errorf("lock subscription row: %w", err)
		}
		if !balance.Valid || !initial.Valid {
			return service.WalletLedgerEntry{}, service.ErrWalletNotFound
		}

		newBalance := balance.Float64 + cmd.DeltaUSD
		newInitial := initial.Float64 + cmd.DeltaUSD
		if _, err := exec.ExecContext(ctx, `
			UPDATE user_subscriptions
			SET wallet_balance_usd = $1, wallet_initial_usd = $2, updated_at = NOW()
			WHERE id = $3
		`, newBalance, newInitial, cmd.SubscriptionID); err != nil {
			return service.WalletLedgerEntry{}, fmt.Errorf("update balance+initial: %w", err)
		}

		return insertLedger(ctx, exec, ledgerInsert{
			subscriptionID: cmd.SubscriptionID,
			deltaUSD:       cmd.DeltaUSD,
			balanceAfter:   newBalance,
			reason:         service.WalletLedgerReasonTopup,
			paymentOrderID: cmd.PaymentOrderID,
			operatorID:     cmd.OperatorID,
			notes:          cmd.Notes,
		})
	})
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
	defer func() { _ = rows.Close() }()

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
		       payment_order_id, usage_log_id, operator_id, COALESCE(notes, '')
		FROM subscription_wallet_ledger
		WHERE subscription_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2
	`, subscriptionID, limit)
	if err != nil {
		return nil, fmt.Errorf("query ledger: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]service.WalletLedgerEntry, 0, limit)
	for rows.Next() {
		var e service.WalletLedgerEntry
		var paymentOrderID, usageLogID, operatorID sql.NullInt64
		if err := rows.Scan(&e.ID, &e.SubscriptionID, &e.DeltaUSD, &e.BalanceAfter, &e.Reason,
			&paymentOrderID, &usageLogID, &operatorID, &e.Notes); err != nil {
			return nil, fmt.Errorf("scan ledger: %w", err)
		}
		if paymentOrderID.Valid {
			v := paymentOrderID.Int64
			e.PaymentOrderID = &v
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
	paymentOrderID *int64
	usageLogID     *int64
	operatorID     *int64
	notes          string
}

func findUsageLedger(ctx context.Context, exec sqlQueryExecutor, usageLogID int64) (service.WalletLedgerEntry, bool, error) {
	var entry service.WalletLedgerEntry
	var paymentOrderID, storedUsageLogID, operatorID sql.NullInt64
	err := scanSingleRow(ctx, exec, `
		SELECT id, subscription_id, delta_usd, balance_after, reason,
		       payment_order_id, usage_log_id, operator_id, COALESCE(notes, '')
		FROM subscription_wallet_ledger
		WHERE usage_log_id = $1 AND reason = 'usage'
		ORDER BY id
		LIMIT 1
	`, []any{usageLogID}, &entry.ID, &entry.SubscriptionID, &entry.DeltaUSD, &entry.BalanceAfter,
		&entry.Reason, &paymentOrderID, &storedUsageLogID, &operatorID, &entry.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return service.WalletLedgerEntry{}, false, nil
	}
	if err != nil {
		return service.WalletLedgerEntry{}, false, err
	}
	if paymentOrderID.Valid {
		value := paymentOrderID.Int64
		entry.PaymentOrderID = &value
	}
	if storedUsageLogID.Valid {
		value := storedUsageLogID.Int64
		entry.UsageLogID = &value
	}
	if operatorID.Valid {
		value := operatorID.Int64
		entry.OperatorID = &value
	}
	return entry, true, nil
}

func insertLedger(ctx context.Context, exec sqlQueryExecutor, in ledgerInsert) (service.WalletLedgerEntry, error) {
	var paymentOrder, usageLog, operator sql.NullInt64
	if in.paymentOrderID != nil {
		paymentOrder = sql.NullInt64{Int64: *in.paymentOrderID, Valid: true}
	}
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
	err := scanSingleRow(ctx, exec, `
			INSERT INTO subscription_wallet_ledger
				(subscription_id, delta_usd, balance_after, reason, payment_order_id, usage_log_id, operator_id, notes)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING id
		`, []any{in.subscriptionID, in.deltaUSD, in.balanceAfter, in.reason, paymentOrder, usageLog, operator, notes}, &id)
	if err != nil {
		return service.WalletLedgerEntry{}, fmt.Errorf("insert ledger: %w", err)
	}

	return service.WalletLedgerEntry{
		ID:             id,
		SubscriptionID: in.subscriptionID,
		DeltaUSD:       in.deltaUSD,
		BalanceAfter:   in.balanceAfter,
		Reason:         in.reason,
		PaymentOrderID: in.paymentOrderID,
		UsageLogID:     in.usageLogID,
		OperatorID:     in.operatorID,
		Notes:          in.notes,
	}, nil
}
