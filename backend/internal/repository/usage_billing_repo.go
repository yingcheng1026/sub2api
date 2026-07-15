package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type usageBillingRepository struct {
	db *sql.DB
}

func NewUsageBillingRepository(_ *dbent.Client, sqlDB *sql.DB) service.UsageBillingRepository {
	return &usageBillingRepository{db: sqlDB}
}

func (r *usageBillingRepository) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (_ *service.UsageBillingApplyResult, err error) {
	if cmd == nil {
		return &service.UsageBillingApplyResult{}, nil
	}
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}

	cmd.Normalize()
	if cmd.RequestID == "" {
		return nil, service.ErrUsageBillingRequestIDRequired
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	applied, err := r.claimUsageBillingKey(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	if !applied {
		return &service.UsageBillingApplyResult{Applied: false}, nil
	}

	result := &service.UsageBillingApplyResult{Applied: true}
	if err := r.applyUsageBillingEffects(ctx, tx, cmd, result); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return result, nil
}

func (r *usageBillingRepository) claimUsageBillingKey(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) (bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO usage_billing_dedup (request_id, api_key_id, request_fingerprint)
		VALUES ($1, $2, $3)
		ON CONFLICT (request_id, api_key_id) DO NOTHING
		RETURNING id
	`, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var existingFingerprint string
		if err := tx.QueryRowContext(ctx, `
			SELECT request_fingerprint
			FROM usage_billing_dedup
			WHERE request_id = $1 AND api_key_id = $2
		`, cmd.RequestID, cmd.APIKeyID).Scan(&existingFingerprint); err != nil {
			return false, err
		}
		if strings.TrimSpace(existingFingerprint) != strings.TrimSpace(cmd.RequestFingerprint) {
			return false, service.ErrUsageBillingRequestConflict
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var archivedFingerprint string
	err = tx.QueryRowContext(ctx, `
		SELECT request_fingerprint
		FROM usage_billing_dedup_archive
		WHERE request_id = $1 AND api_key_id = $2
	`, cmd.RequestID, cmd.APIKeyID).Scan(&archivedFingerprint)
	if err == nil {
		if strings.TrimSpace(archivedFingerprint) != strings.TrimSpace(cmd.RequestFingerprint) {
			return false, service.ErrUsageBillingRequestConflict
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return true, nil
}

func (r *usageBillingRepository) applyUsageBillingEffects(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, result *service.UsageBillingApplyResult) error {
	walletAdmissionHandled, walletBalance, walletInsufficient, err := settleUsageBillingWalletAdmission(ctx, tx, cmd)
	if err != nil {
		return err
	}
	if walletAdmissionHandled {
		result.NewWalletBalance = &walletBalance
		result.WalletInsufficient = walletInsufficient
	}

	if cmd.SubscriptionCost > 0 && cmd.SubscriptionID != nil {
		if err := incrementUsageBillingSubscription(ctx, tx, *cmd.SubscriptionID, cmd.SubscriptionCost, cmd.BindingsFrozen); err != nil {
			return err
		}
	}

	// 钱包模式 (v4) 扣款：FOR UPDATE 锁住 user_subscriptions 行扣减 wallet_balance_usd
	// 并落 ledger 流水。SubscriptionCost 与 WalletCost 由 buildUsageBillingCommand 保证互斥。
	if !walletAdmissionHandled && cmd.WalletCost > 0 && cmd.SubscriptionID != nil {
		newBalance, insufficient, err := deductUsageBillingWallet(ctx, tx, *cmd.SubscriptionID, cmd.WalletCost, cmd.BindingsFrozen)
		if err != nil {
			return err
		}
		if insufficient {
			// 余额不足发生在上游响应之后，仍然完整结算为负数债务；否则保留
			// 正余额会让相同请求继续通过预检并无限免费调用。下一次预检看到
			// balance <= 0 会直接 402，flag 同时供告警/运营对账使用。
			result.WalletInsufficient = true
		}
		result.NewWalletBalance = &newBalance
	}

	if cmd.BalanceCost > 0 {
		newBalance, err := deductUsageBillingBalance(ctx, tx, cmd.UserID, cmd.BalanceCost, cmd.BindingsFrozen)
		if err != nil {
			return err
		}
		result.NewBalance = &newBalance
	}

	if cmd.APIKeyQuotaCost > 0 {
		exhausted, err := incrementUsageBillingAPIKeyQuota(ctx, tx, cmd.APIKeyID, cmd.APIKeyQuotaCost, cmd.BindingsFrozen)
		if err != nil {
			return err
		}
		result.APIKeyQuotaExhausted = exhausted
	}

	if cmd.APIKeyRateLimitCost > 0 {
		if err := incrementUsageBillingAPIKeyRateLimit(ctx, tx, cmd.APIKeyID, cmd.APIKeyRateLimitCost, cmd.BindingsFrozen); err != nil {
			return err
		}
	}

	if cmd.AccountQuotaCost > 0 && (strings.EqualFold(cmd.AccountType, service.AccountTypeAPIKey) || strings.EqualFold(cmd.AccountType, service.AccountTypeBedrock)) {
		quotaState, err := incrementUsageBillingAccountQuota(ctx, tx, cmd.AccountID, cmd.AccountQuotaCost, cmd.BindingsFrozen)
		if err != nil {
			return err
		}
		result.QuotaState = quotaState
	}

	return settleUsageBillingNonWalletAdmission(ctx, tx, cmd)
}

// settleUsageBillingWalletAdmission consumes the pre-upstream hold in the same
// transaction as the authoritative wallet debit. Missing admissions are
// treated as legacy events created before admission enforcement was enabled;
// production Enqueue requires an admission before it can create a new outbox
// event. A present wallet admission is always fail-closed.
func settleUsageBillingWalletAdmission(
	ctx context.Context,
	tx *sql.Tx,
	cmd *service.UsageBillingCommand,
) (handled bool, newBalance float64, insufficient bool, err error) {
	if cmd == nil || !cmd.BindingsFrozen {
		return false, 0, false, nil
	}

	var walletSubscriptionID sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT wallet_subscription_id
		FROM usage_billing_admissions
		WHERE request_id = $1 AND api_key_id = $2
	`, cmd.RequestID, cmd.APIKeyID).Scan(&walletSubscriptionID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, false, nil
	}
	if err != nil {
		return false, 0, false, err
	}
	if !walletSubscriptionID.Valid {
		if cmd.WalletCost > 0 {
			return false, 0, false, service.ErrUsageBillingRequestConflict
		}
		return false, 0, false, nil
	}
	if cmd.SubscriptionID == nil || *cmd.SubscriptionID != walletSubscriptionID.Int64 {
		return false, 0, false, service.ErrUsageBillingRequestConflict
	}

	var balance sql.NullFloat64
	err = tx.QueryRowContext(ctx, `
		SELECT wallet_balance_usd
		FROM user_subscriptions
		WHERE id = $1 AND ($2 OR deleted_at IS NULL)
		FOR UPDATE
	`, walletSubscriptionID.Int64, cmd.BindingsFrozen).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, false, service.ErrSubscriptionNotFound
	}
	if err != nil {
		return false, 0, false, err
	}
	if !balance.Valid {
		return false, 0, false, service.ErrSubscriptionNotFound
	}

	var (
		state        string
		reservedUSD  float64
		consumedAt   sql.NullTime
		releasedAt   sql.NullTime
		lockedWallet sql.NullInt64
	)
	err = tx.QueryRowContext(ctx, `
		SELECT state, wallet_subscription_id, wallet_reserved_usd,
			wallet_consumed_at, wallet_released_at
		FROM usage_billing_admissions
		WHERE request_id = $1 AND api_key_id = $2
		FOR UPDATE
	`, cmd.RequestID, cmd.APIKeyID).Scan(
		&state, &lockedWallet, &reservedUSD, &consumedAt, &releasedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, false, service.ErrUsageBillingAdmissionMissing
	}
	if err != nil {
		return false, 0, false, err
	}
	if state != service.UsageBillingAdmissionStateOutboxPending ||
		!lockedWallet.Valid || lockedWallet.Int64 != walletSubscriptionID.Int64 ||
		reservedUSD <= 0 || consumedAt.Valid || releasedAt.Valid {
		return false, 0, false, service.ErrUsageBillingAdmissionLeaseLost
	}
	if cmd.WalletCost > reservedUSD+1e-12 {
		return false, 0, false, service.ErrUsageBillingRequestConflict
	}

	newBalance = balance.Float64
	if cmd.WalletCost > 0 {
		insufficient = balance.Float64 < cmd.WalletCost
		newBalance = balance.Float64 - cmd.WalletCost
		if _, err = tx.ExecContext(ctx, `
			UPDATE user_subscriptions
			SET wallet_balance_usd = $1, updated_at = NOW()
			WHERE id = $2
		`, newBalance, walletSubscriptionID.Int64); err != nil {
			return false, 0, false, err
		}
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO subscription_wallet_ledger
				(subscription_id, delta_usd, balance_after, reason, usage_log_id, operator_id, notes)
			VALUES ($1, $2, $3, 'usage', NULL, NULL, NULL)
		`, walletSubscriptionID.Int64, -cmd.WalletCost, newBalance); err != nil {
			return false, 0, false, err
		}
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE usage_billing_admissions
		SET state = 'settled',
			settled_at = COALESCE(settled_at, NOW()),
			wallet_consumed_at = NOW(),
			updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2
		  AND state = 'outbox_pending'
		  AND wallet_consumed_at IS NULL
		  AND wallet_released_at IS NULL
	`, cmd.RequestID, cmd.APIKeyID)
	if err != nil {
		return false, 0, false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, 0, false, err
	}
	if updated != 1 {
		return false, 0, false, service.ErrUsageBillingAdmissionLeaseLost
	}
	return true, newBalance, insufficient, nil
}

// settleUsageBillingNonWalletAdmission closes the admission in the same
// transaction as all balance, quota and usage effects. Events written before
// admission enforcement intentionally remain replayable without a base row.
func settleUsageBillingNonWalletAdmission(
	ctx context.Context,
	tx *sql.Tx,
	cmd *service.UsageBillingCommand,
) error {
	if cmd == nil || !cmd.BindingsFrozen {
		return nil
	}
	var (
		state                string
		walletSubscriptionID sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `
		SELECT state, wallet_subscription_id
		FROM usage_billing_admissions
		WHERE request_id = $1 AND api_key_id = $2
		FOR UPDATE
	`, cmd.RequestID, cmd.APIKeyID).Scan(&state, &walletSubscriptionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if walletSubscriptionID.Valid {
		if state == service.UsageBillingAdmissionStateSettled {
			return nil
		}
		return service.ErrUsageBillingAdmissionLeaseLost
	}
	if state != service.UsageBillingAdmissionStateOutboxPending {
		return service.ErrUsageBillingAdmissionLeaseLost
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE usage_billing_admissions
		SET state = 'settled', settled_at = COALESCE(settled_at, NOW()), updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2
		  AND state = 'outbox_pending'
		  AND wallet_subscription_id IS NULL
	`, cmd.RequestID, cmd.APIKeyID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return service.ErrUsageBillingAdmissionLeaseLost
	}
	return nil
}

func incrementUsageBillingSubscription(ctx context.Context, tx *sql.Tx, subscriptionID int64, costUSD float64, bindingsFrozen bool) error {
	const updateSQL = `
		UPDATE user_subscriptions us
		SET
			daily_usage_usd = us.daily_usage_usd + $1,
			weekly_usage_usd = us.weekly_usage_usd + $1,
			monthly_usage_usd = us.monthly_usage_usd + $1,
			updated_at = NOW()
		FROM groups g
		WHERE us.id = $2
			AND ($3 OR us.deleted_at IS NULL)
			AND us.group_id = g.id
			AND ($3 OR g.deleted_at IS NULL)
	`
	res, err := tx.ExecContext(ctx, updateSQL, costUSD, subscriptionID, bindingsFrozen)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	return service.ErrSubscriptionNotFound
}

// deductUsageBillingWallet 钱包模式 (v4) 在共享 billing 事务内扣款 + 落 ledger。
//
// 返回值：
//   - newBalance: 完整结算后的余额；可能为负，表示已交付请求产生的欠费。
//   - insufficient: 扣款前 balance < cost 时为 true，供调用方告警。
//
// 不会返回 service.ErrWalletInsufficient — 这一层不拒事务，把决策权交给调用方
// （usage_logs 已经写入，钱包扣款失败应当作可观察异常而非阻断 billing 提交）。
func deductUsageBillingWallet(ctx context.Context, tx *sql.Tx, subscriptionID int64, costUSD float64, bindingsFrozen bool) (float64, bool, error) {
	var balance sql.NullFloat64
	err := tx.QueryRowContext(ctx, `
		SELECT wallet_balance_usd
		FROM user_subscriptions
		WHERE id = $1 AND ($2 OR deleted_at IS NULL)
		FOR UPDATE
	`, subscriptionID, bindingsFrozen).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, service.ErrSubscriptionNotFound
	}
	if err != nil {
		return 0, false, err
	}
	if !balance.Valid {
		// wallet_balance_usd IS NULL → 老的 group 订阅，不该走到这条路径。
		// 上游 buildUsageBillingCommand 已经按 IsWalletMode 分流；这里兜底返错。
		return 0, false, service.ErrSubscriptionNotFound
	}

	insufficient := balance.Float64 < costUSD
	newBalance := balance.Float64 - costUSD
	if _, err := tx.ExecContext(ctx, `
		UPDATE user_subscriptions
		SET wallet_balance_usd = $1, updated_at = NOW()
		WHERE id = $2
	`, newBalance, subscriptionID); err != nil {
		return 0, false, err
	}

	var notes sql.NullString
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO subscription_wallet_ledger
			(subscription_id, delta_usd, balance_after, reason, usage_log_id, operator_id, notes)
		VALUES ($1, $2, $3, 'usage', NULL, NULL, $4)
	`, subscriptionID, -costUSD, newBalance, notes); err != nil {
		return 0, false, err
	}
	return newBalance, insufficient, nil
}

func deductUsageBillingBalance(ctx context.Context, tx *sql.Tx, userID int64, amount float64, bindingsFrozen bool) (float64, error) {
	var newBalance float64
	err := tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = balance - $1,
			updated_at = NOW()
		WHERE id = $2 AND ($3 OR deleted_at IS NULL)
		RETURNING balance
	`, amount, userID, bindingsFrozen).Scan(&newBalance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, service.ErrUserNotFound
	}
	if err != nil {
		return 0, err
	}
	return newBalance, nil
}

func incrementUsageBillingAPIKeyQuota(ctx context.Context, tx *sql.Tx, apiKeyID int64, amount float64, bindingsFrozen bool) (bool, error) {
	var exhausted bool
	err := tx.QueryRowContext(ctx, `
		UPDATE api_keys
		SET quota_used = quota_used + $1,
			status = CASE
				WHEN quota > 0
					AND status = $3
					AND quota_used < quota
					AND quota_used + $1 >= quota
				THEN $4
				ELSE status
			END,
			updated_at = NOW()
		WHERE id = $2 AND ($5 OR deleted_at IS NULL)
		RETURNING quota > 0 AND quota_used >= quota AND quota_used - $1 < quota
	`, amount, apiKeyID, service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted, bindingsFrozen).Scan(&exhausted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, service.ErrAPIKeyNotFound
	}
	if err != nil {
		return false, err
	}
	return exhausted, nil
}

func incrementUsageBillingAPIKeyRateLimit(ctx context.Context, tx *sql.Tx, apiKeyID int64, cost float64, bindingsFrozen bool) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE api_keys SET
			usage_5h = CASE WHEN window_5h_start IS NOT NULL AND window_5h_start + INTERVAL '5 hours' <= NOW() THEN $1 ELSE usage_5h + $1 END,
			usage_1d = CASE WHEN window_1d_start IS NOT NULL AND window_1d_start + INTERVAL '24 hours' <= NOW() THEN $1 ELSE usage_1d + $1 END,
			usage_7d = CASE WHEN window_7d_start IS NOT NULL AND window_7d_start + INTERVAL '7 days' <= NOW() THEN $1 ELSE usage_7d + $1 END,
			window_5h_start = CASE WHEN window_5h_start IS NULL OR window_5h_start + INTERVAL '5 hours' <= NOW() THEN NOW() ELSE window_5h_start END,
			window_1d_start = CASE WHEN window_1d_start IS NULL OR window_1d_start + INTERVAL '24 hours' <= NOW() THEN date_trunc('day', NOW()) ELSE window_1d_start END,
			window_7d_start = CASE WHEN window_7d_start IS NULL OR window_7d_start + INTERVAL '7 days' <= NOW() THEN date_trunc('day', NOW()) ELSE window_7d_start END,
			updated_at = NOW()
		WHERE id = $2 AND ($3 OR deleted_at IS NULL)
	`, cost, apiKeyID, bindingsFrozen)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAPIKeyNotFound
	}
	return nil
}

func incrementUsageBillingAccountQuota(ctx context.Context, tx *sql.Tx, accountID int64, amount float64, bindingsFrozen bool) (*service.AccountQuotaState, error) {
	rows, err := tx.QueryContext(ctx,
		`UPDATE accounts SET extra = (
			COALESCE(extra, '{}'::jsonb)
			|| jsonb_build_object('quota_used', COALESCE((extra->>'quota_used')::numeric, 0) + $1)
			|| CASE WHEN COALESCE((extra->>'quota_daily_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_daily_used',
					CASE WHEN `+dailyExpiredExpr+`
					THEN $1
					ELSE COALESCE((extra->>'quota_daily_used')::numeric, 0) + $1 END,
					'quota_daily_start',
					CASE WHEN `+dailyExpiredExpr+`
					THEN `+nowUTC+`
					ELSE COALESCE(extra->>'quota_daily_start', `+nowUTC+`) END
				)
				|| CASE WHEN `+dailyExpiredExpr+` AND `+nextDailyResetAtExpr+` IS NOT NULL
				   THEN jsonb_build_object('quota_daily_reset_at', `+nextDailyResetAtExpr+`)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
			|| CASE WHEN COALESCE((extra->>'quota_weekly_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_weekly_used',
					CASE WHEN `+weeklyExpiredExpr+`
					THEN $1
					ELSE COALESCE((extra->>'quota_weekly_used')::numeric, 0) + $1 END,
					'quota_weekly_start',
					CASE WHEN `+weeklyExpiredExpr+`
					THEN `+nowUTC+`
					ELSE COALESCE(extra->>'quota_weekly_start', `+nowUTC+`) END
				)
				|| CASE WHEN `+weeklyExpiredExpr+` AND `+nextWeeklyResetAtExpr+` IS NOT NULL
				   THEN jsonb_build_object('quota_weekly_reset_at', `+nextWeeklyResetAtExpr+`)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
		), updated_at = NOW()
		WHERE id = $2 AND ($3 OR deleted_at IS NULL)
		RETURNING
			COALESCE((extra->>'quota_used')::numeric, 0),
			COALESCE((extra->>'quota_limit')::numeric, 0),
			COALESCE((extra->>'quota_daily_used')::numeric, 0),
			COALESCE((extra->>'quota_daily_limit')::numeric, 0),
			COALESCE((extra->>'quota_weekly_used')::numeric, 0),
			COALESCE((extra->>'quota_weekly_limit')::numeric, 0)`,
		amount, accountID, bindingsFrozen)
	if err != nil {
		return nil, err
	}

	var state service.AccountQuotaState
	if rows.Next() {
		if err := rows.Scan(
			&state.TotalUsed, &state.TotalLimit,
			&state.DailyUsed, &state.DailyLimit,
			&state.WeeklyUsed, &state.WeeklyLimit,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
	} else {
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
		return nil, service.ErrAccountNotFound
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	// 必须在执行下一条 SQL 前显式关闭 rows：pq 驱动在同一连接上
	// 不允许前一条查询的结果集未耗尽时启动新查询，否则会返回
	// "unexpected Parse response" 错误。
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// 任意维度额度在本次递增中从"未超"跨越到"已超"时，必须刷新调度快照，
	// 否则 Redis 中缓存的 Account 仍显示旧的 used 值，后续请求会继续选中本账号，
	// 最终观察到 daily_used / weekly_used 大幅超过配置的 limit。
	// 对于日/周额度，即使本次触发了周期重置（pre=0、post=amount），
	// 判定式 (post-amount) < limit 同样成立，逻辑与总额度保持一致。
	crossedTotal := state.TotalLimit > 0 && state.TotalUsed >= state.TotalLimit && (state.TotalUsed-amount) < state.TotalLimit
	crossedDaily := state.DailyLimit > 0 && state.DailyUsed >= state.DailyLimit && (state.DailyUsed-amount) < state.DailyLimit
	crossedWeekly := state.WeeklyLimit > 0 && state.WeeklyUsed >= state.WeeklyLimit && (state.WeeklyUsed-amount) < state.WeeklyLimit
	if crossedTotal || crossedDaily || crossedWeekly {
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
			logger.LegacyPrintf("repository.usage_billing", "[SchedulerOutbox] enqueue quota exceeded failed: account=%d err=%v", accountID, err)
			return nil, err
		}
	}
	return &state, nil
}
