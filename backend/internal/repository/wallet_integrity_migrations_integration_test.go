//go:build integration

package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const permanentCreditsWalletThreshold = "2099-12-30 23:59:59+00"

func TestWalletLedgerIntegrityMigration175BackfillsZeroOpeningBaseline(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	createWalletMigrationTempTables(t, tx)

	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_subscriptions (
			id, user_id, wallet_initial_usd, wallet_balance_usd,
			status, expires_at, created_at
		) VALUES (234, 42, 50, 50, 'active', '2099-12-31 23:59:59+00', $1)
	`, createdAt)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO subscription_wallet_ledger (
			subscription_id, delta_usd, balance_after, reason, created_at
		) VALUES (234, 50, 50, 'topup', $1::timestamptz + INTERVAL '1 day')
	`, createdAt)
	require.NoError(t, err)

	migration := readMigration(t, "175_wallet_ledger_integrity.sql")
	_, err = tx.ExecContext(ctx, migration)
	require.NoError(t, err)

	var (
		delta        float64
		balanceAfter float64
		activationAt time.Time
	)
	err = tx.QueryRowContext(ctx, `
		SELECT delta_usd, balance_after, created_at
		FROM subscription_wallet_ledger
		WHERE subscription_id = 234 AND reason = 'activation'
	`).Scan(&delta, &balanceAfter, &activationAt)
	require.NoError(t, err)
	require.Zero(t, delta, "a wallet first funded entirely by topups needs a zero activation baseline")
	require.Zero(t, balanceAfter)
	require.True(t, activationAt.Equal(createdAt), "historical activation must keep subscription chronology")

	_, err = tx.ExecContext(ctx, migration)
	require.NoError(t, err, "migration 175 must remain idempotent after the zero baseline is recorded")

	var activations int
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM subscription_wallet_ledger
		WHERE subscription_id = 234 AND reason = 'activation'
	`).Scan(&activations))
	require.Equal(t, 1, activations)
}

func TestWalletLedgerIntegrityMigration175BackfillsDeletedWalletHistory(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	createWalletMigrationTempTables(t, tx)

	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_subscriptions (
			id, user_id, wallet_initial_usd, wallet_balance_usd,
			status, deleted_at, expires_at, created_at
		) VALUES (235, 42, 50, 50, 'active', NOW(), '2099-12-31 23:59:59+00', NOW())
	`)
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, readMigration(t, "175_wallet_ledger_integrity.sql"))
	require.NoError(t, err)

	var activations int
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM subscription_wallet_ledger
		WHERE subscription_id = 235 AND reason = 'activation'
	`).Scan(&activations))
	require.Equal(t, 1, activations, "soft deletion must not erase or skip financial opening history")
}

func TestWalletLedgerIntegrityMigration175RejectsUnsafeBaselines(t *testing.T) {
	tests := []struct {
		name          string
		initial       float64
		balance       float64
		topup         float64
		wantErrorText string
	}{
		{
			name:          "negative opening baseline",
			initial:       40,
			balance:       40,
			topup:         50,
			wantErrorText: "activation backfill is not safely explainable",
		},
		{
			name:          "unexplained cached balance drift",
			initial:       50,
			balance:       49,
			topup:         0,
			wantErrorText: "activation backfill is not safely explainable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			ctx := context.Background()
			createWalletMigrationTempTables(t, tx)

			_, err := tx.ExecContext(ctx, `
				INSERT INTO user_subscriptions (
					id, user_id, wallet_initial_usd, wallet_balance_usd,
					status, expires_at, created_at
				) VALUES (1, 1, $1, $2, 'active', '2099-12-31 23:59:59+00', NOW())
			`, tt.initial, tt.balance)
			require.NoError(t, err)
			if tt.topup != 0 {
				_, err = tx.ExecContext(ctx, `
					INSERT INTO subscription_wallet_ledger (
						subscription_id, delta_usd, balance_after, reason
					) VALUES (1, $1, $1, 'topup')
				`, tt.topup)
				require.NoError(t, err)
			}

			_, err = tx.ExecContext(ctx, readMigration(t, "175_wallet_ledger_integrity.sql"))
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErrorText)
		})
	}
}

func TestWalletIntegrityIndexesRejectDuplicateActiveState(t *testing.T) {
	t.Run("one activation per subscription", func(t *testing.T) {
		tx := testTx(t)
		ctx := context.Background()
		userID, _ := insertWalletMigrationUserAndGroups(t, tx)
		subscriptionID := insertWalletSubscription(t, tx, userID, "2099-12-31 23:59:59+00", "active")

		_, err := tx.ExecContext(ctx, `
			INSERT INTO subscription_wallet_ledger
				(subscription_id, delta_usd, balance_after, reason)
			VALUES ($1, 10, 10, 'activation')
		`, subscriptionID)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO subscription_wallet_ledger
				(subscription_id, delta_usd, balance_after, reason)
			VALUES ($1, 0, 10, 'activation')
		`, subscriptionID)
		requirePostgresCode(t, err, "23505")
	})

	t.Run("one active permanent credits wallet per user", func(t *testing.T) {
		tx := testTx(t)
		userID, _ := insertWalletMigrationUserAndGroups(t, tx)
		insertWalletSubscription(t, tx, userID, "2099-12-31 23:59:59+00", "active")

		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO user_subscriptions (
				user_id, starts_at, expires_at, status,
				wallet_balance_usd, wallet_initial_usd
			) VALUES ($1, NOW(), '2099-12-31 23:59:59+00', 'active', 20, 20)
		`, userID)
		requirePostgresCode(t, err, "23505")
	})
}

func TestWalletPermanenceBoundaryMatchesApplicationPolicy(t *testing.T) {
	t.Run("finite wallet late in 2099 remains forbidden", func(t *testing.T) {
		tx := testTx(t)
		userID, _ := insertWalletMigrationUserAndGroups(t, tx)

		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO user_subscriptions (
				user_id, starts_at, expires_at, status,
				wallet_balance_usd, wallet_initial_usd
			) VALUES ($1, NOW(), '2099-12-01 00:00:00+00', 'active', 10, 10)
		`, userID)
		requirePostgresCode(t, err, "23514")
	})

	t.Run("application threshold is accepted as permanent", func(t *testing.T) {
		tx := testTx(t)
		userID, _ := insertWalletMigrationUserAndGroups(t, tx)

		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO user_subscriptions (
				user_id, starts_at, expires_at, status,
				wallet_balance_usd, wallet_initial_usd
			) VALUES ($1, NOW(), $2::timestamptz, 'active', 10, 10)
		`, userID, permanentCreditsWalletThreshold)
		require.NoError(t, err)
	})

	t.Run("group subscription cannot carry hidden wallet initial value", func(t *testing.T) {
		tx := testTx(t)
		userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)

		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO user_subscriptions (
				user_id, group_id, starts_at, expires_at, status,
				wallet_initial_usd
			) VALUES ($1, $2, NOW(), NOW() + INTERVAL '30 days', 'active', 10)
		`, userID, groupIDs[0])
		requirePostgresCode(t, err, "23514")
	})
}

func TestWalletPostpaidSettlementConstraintAllowsDebtButRejectsNonFinite(t *testing.T) {
	t.Run("negative debt balance is persisted", func(t *testing.T) {
		tx := testTx(t)
		userID, _ := insertWalletMigrationUserAndGroups(t, tx)

		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO user_subscriptions (
				user_id, starts_at, expires_at, status,
				wallet_balance_usd, wallet_initial_usd
			) VALUES ($1, NOW(), '2099-12-31 23:59:59+00', 'active', -5, 10)
		`, userID)
		require.NoError(t, err)
	})

	t.Run("NaN balance is rejected", func(t *testing.T) {
		tx := testTx(t)
		userID, _ := insertWalletMigrationUserAndGroups(t, tx)

		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO user_subscriptions (
				user_id, starts_at, expires_at, status,
				wallet_balance_usd, wallet_initial_usd
			) VALUES ($1, NOW(), '2099-12-31 23:59:59+00', 'active', 'NaN'::numeric, 10)
		`, userID)
		requirePostgresCode(t, err, "23514")
	})

	for _, initial := range []string{"-1", "NaN", "Infinity", "-Infinity"} {
		t.Run("invalid initial amount "+initial+" is rejected", func(t *testing.T) {
			tx := testTx(t)
			userID, _ := insertWalletMigrationUserAndGroups(t, tx)

			_, err := tx.ExecContext(context.Background(), `
				INSERT INTO user_subscriptions (
					user_id, starts_at, expires_at, status,
					wallet_balance_usd, wallet_initial_usd
				) VALUES ($1, NOW(), '2099-12-31 23:59:59+00', 'active', 10, $2::numeric)
			`, userID, initial)
			if initial == "Infinity" || initial == "-Infinity" {
				requirePostgresCodeOneOf(t, err, "22003", "23514")
			} else {
				requirePostgresCode(t, err, "23514")
			}
		})
	}
}

func TestMonthlyAndCreditsPlanDatabaseShapesAreExclusive(t *testing.T) {
	t.Run("monthly wallet plan is rejected", func(t *testing.T) {
		tx := testTx(t)
		_, _ = insertWalletMigrationUserAndGroups(t, tx)
		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO subscription_plans
				(plan_type, group_id, wallet_quota_usd, name, price)
			VALUES ('subscription', NULL, 20, 'invalid monthly wallet', 20)
		`)
		requirePostgresCode(t, err, "23514")
	})

	t.Run("credits plan cannot bind a group", func(t *testing.T) {
		tx := testTx(t)
		_, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO subscription_plans
				(plan_type, group_id, wallet_quota_usd, name, price)
			VALUES ('credits', $1, NULL, 'invalid grouped credits', 20)
		`, groupIDs[0])
		requirePostgresCode(t, err, "23514")
	})

	t.Run("valid monthly and credits plans remain accepted", func(t *testing.T) {
		tx := testTx(t)
		_, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO subscription_plans
				(plan_type, group_id, wallet_quota_usd, name, price)
			VALUES
				('subscription', $1, NULL, 'valid monthly', 20),
				('credits', NULL, 20, 'valid credits', 20)
		`, groupIDs[0])
		require.NoError(t, err)
	})

	for _, quota := range []string{"0", "-1", "NaN", "Infinity", "-Infinity"} {
		t.Run("invalid credits quota "+quota+" is rejected", func(t *testing.T) {
			tx := testTx(t)
			_, _ = insertWalletMigrationUserAndGroups(t, tx)
			_, err := tx.ExecContext(context.Background(), `
				INSERT INTO subscription_plans
					(plan_type, group_id, wallet_quota_usd, name, price)
				VALUES ('credits', NULL, $1::numeric, 'invalid credits quota', 20)
			`, quota)
			if quota == "Infinity" || quota == "-Infinity" {
				requirePostgresCodeOneOf(t, err, "22003", "23514")
			} else {
				requirePostgresCode(t, err, "23514")
			}
		})
	}

	t.Run("monthly plan requires active subscription group", func(t *testing.T) {
		tx := testTx(t)
		_, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
		_, err := tx.ExecContext(context.Background(), "UPDATE groups SET status = 'disabled' WHERE id = $1", groupIDs[0])
		require.NoError(t, err)
		_, err = tx.ExecContext(context.Background(), `
			INSERT INTO subscription_plans
				(plan_type, group_id, wallet_quota_usd, name, price)
			VALUES ('subscription', $1, NULL, 'invalid monthly group', 20)
		`, groupIDs[0])
		requirePostgresCode(t, err, "23514")
	})
}

func TestMonthlyPlanMigration176BlocksHistoricalWalletWithoutRewritingIt(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	createMonthlySeparationMigrationTempTables(t, tx)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO subscription_plans (id, plan_type, group_id, wallet_quota_usd)
		VALUES (77, 'subscription', NULL, 50)
	`)
	require.NoError(t, err)

	migration := readMigration(t, "176_enforce_monthly_group_not_wallet.sql")
	require.NotContains(t, strings.ToUpper(migration), "UPDATE SUBSCRIPTION_PLANS",
		"migration 176 must not guess a group or destructively rewrite historical plan quota")
	require.NoError(t, execMigrationTestSQL(tx, "SAVEPOINT before_migration_176"))
	_, err = tx.ExecContext(ctx, migration)
	requirePostgresCode(t, err, "23514")
	require.Contains(t, err.Error(), "plan_ids={77}")
	require.NoError(t, execMigrationTestSQL(tx, "ROLLBACK TO SAVEPOINT before_migration_176"))

	var (
		groupID sql.NullInt64
		quota   float64
	)
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT group_id, wallet_quota_usd
		FROM subscription_plans
		WHERE id = 77
	`).Scan(&groupID, &quota))
	require.False(t, groupID.Valid)
	require.Equal(t, 50.0, quota)
}

func TestMonthlyPlanMigration176BlocksHistoricalMixedWalletShapes(t *testing.T) {
	tests := []struct {
		name    string
		groupID *int64
		balance *float64
		initial *float64
	}{
		{name: "group with both wallet fields", groupID: migrationInt64Ptr(7), balance: migrationFloat64Ptr(10), initial: migrationFloat64Ptr(10)},
		{name: "group with hidden initial only", groupID: migrationInt64Ptr(7), initial: migrationFloat64Ptr(10)},
		{name: "wallet balance without initial", balance: migrationFloat64Ptr(10)},
		{name: "wallet initial without balance", initial: migrationFloat64Ptr(10)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			ctx := context.Background()
			createMonthlySeparationMigrationTempTables(t, tx)
			if tt.groupID != nil {
				_, err := tx.ExecContext(ctx, `
					INSERT INTO groups (id, status, subscription_type)
					VALUES ($1, 'active', 'subscription')
				`, *tt.groupID)
				require.NoError(t, err)
			}
			_, err := tx.ExecContext(ctx, `
				INSERT INTO user_subscriptions (
					id, group_id, wallet_balance_usd, wallet_initial_usd, expires_at
				) VALUES (88, $1, $2, $3, '2099-12-31 23:59:59+00')
			`, tt.groupID, tt.balance, tt.initial)
			require.NoError(t, err)

			require.NoError(t, execMigrationTestSQL(tx, "SAVEPOINT before_mixed_row_migration_176"))
			_, err = tx.ExecContext(ctx, readMigration(t, "176_enforce_monthly_group_not_wallet.sql"))
			requirePostgresCode(t, err, "23514")
			require.Contains(t, err.Error(), "subscription_ids={88}")
			require.NoError(t, execMigrationTestSQL(tx, "ROLLBACK TO SAVEPOINT before_mixed_row_migration_176"))

			var rows int
			require.NoError(t, tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM user_subscriptions WHERE id = 88").Scan(&rows))
			require.Equal(t, 1, rows, "fail-closed preflight must not rewrite the historical row")
		})
	}
}

func TestMonthlyPlanMigration176BlocksInvalidFulfillableOrders(t *testing.T) {
	tests := []struct {
		name        string
		planType    string
		planGroup   *int64
		planQuota   *float64
		orderPlan   *int64
		orderGroup  *int64
		orderStatus string
		paidFailure bool
	}{
		{
			name:       "monthly order uses wrong group",
			planType:   "subscription",
			planGroup:  migrationInt64Ptr(7),
			orderPlan:  migrationInt64Ptr(11),
			orderGroup: migrationInt64Ptr(8),
		},
		{
			name:      "monthly order has no group",
			planType:  "subscription",
			planGroup: migrationInt64Ptr(7),
			orderPlan: migrationInt64Ptr(11),
		},
		{
			name:       "credits order uses group path",
			planType:   "credits",
			planQuota:  migrationFloat64Ptr(50),
			orderPlan:  migrationInt64Ptr(11),
			orderGroup: migrationInt64Ptr(7),
		},
		{
			name:      "order references missing plan",
			orderPlan: migrationInt64Ptr(999),
		},
		{
			name: "subscription order has neither plan nor group",
		},
		{
			name:       "legacy order references unavailable group",
			orderGroup: migrationInt64Ptr(999),
		},
		{
			name:        "cancelled monthly order can still recover",
			planType:    "subscription",
			planGroup:   migrationInt64Ptr(7),
			orderPlan:   migrationInt64Ptr(11),
			orderGroup:  migrationInt64Ptr(8),
			orderStatus: "CANCELLED",
		},
		{
			name:        "recent expired monthly order can still recover",
			planType:    "subscription",
			planGroup:   migrationInt64Ptr(7),
			orderPlan:   migrationInt64Ptr(11),
			orderGroup:  migrationInt64Ptr(8),
			orderStatus: "EXPIRED",
		},
		{
			name:        "paid failed monthly order can still retry",
			planType:    "subscription",
			planGroup:   migrationInt64Ptr(7),
			orderPlan:   migrationInt64Ptr(11),
			orderGroup:  migrationInt64Ptr(8),
			orderStatus: "FAILED",
			paidFailure: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			ctx := context.Background()
			createMonthlySeparationMigrationTempTables(t, tx)
			_, err := tx.ExecContext(ctx, `
				INSERT INTO groups (id, status, subscription_type)
				VALUES
					(7, 'active', 'subscription'),
					(8, 'active', 'subscription')
			`)
			require.NoError(t, err)
			if tt.planType != "" {
				_, err = tx.ExecContext(ctx, `
					INSERT INTO subscription_plans
						(id, plan_type, group_id, wallet_quota_usd)
					VALUES (11, $1::varchar, $2::bigint, $3::numeric)
				`, tt.planType, tt.planGroup, tt.planQuota)
				require.NoError(t, err)
			}
			status := tt.orderStatus
			if status == "" {
				status = "PAID"
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO payment_orders (
					id, order_type, plan_id, subscription_group_id, status,
					paid_at, payment_trade_no
				) VALUES (
					101, 'subscription', $1::bigint, $2::bigint, $3::varchar,
					CASE WHEN $4::boolean THEN NOW() ELSE NULL END,
					CASE WHEN $4::boolean THEN 'trusted-preflight-payment' ELSE NULL END
				)
			`, tt.orderPlan, tt.orderGroup, status, tt.paidFailure)
			require.NoError(t, err)

			_, err = tx.ExecContext(ctx, readMigration(t, "176_enforce_monthly_group_not_wallet.sql"))
			requirePostgresCode(t, err, "23514")
			require.Contains(t, err.Error(), "order_ids={101}")
		})
	}
}

func TestMonthlyPlanMigration176AllowsValidOrTerminalHistoricalOrders(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	createMonthlySeparationMigrationTempTables(t, tx)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO groups (id, status, subscription_type) VALUES (7, 'active', 'subscription');
		INSERT INTO subscription_plans (id, plan_type, group_id, wallet_quota_usd)
		VALUES
			(11, 'subscription', 7, NULL),
			(12, 'credits', NULL, 50);
		INSERT INTO payment_orders (
			id, order_type, plan_id, subscription_group_id, status
		) VALUES
			(101, 'subscription', 11, 7, 'PAID'),
			(102, 'subscription', 12, NULL, 'RECHARGING'),
			(103, 'subscription', 11, 999, 'COMPLETED'),
			(104, 'subscription', NULL, 7, 'PAID'),
			(105, 'subscription', 11, 999, 'EXPIRED'),
			(106, 'subscription', 11, 999, 'FAILED'),
			(107, 'subscription', 11, 999, 'CANCELLED');
		UPDATE payment_orders
		SET updated_at = NOW() - INTERVAL '6 minutes'
		WHERE id IN (105, 107);
	`)
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, readMigration(t, "176_enforce_monthly_group_not_wallet.sql"))
	require.NoError(t, err)
}

func TestPaymentOrderPlanGroupTriggerEnforcesFulfillmentPath(t *testing.T) {
	t.Run("monthly order requires exact plan group", func(t *testing.T) {
		tx := testTx(t)
		userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
		planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)

		_, err := insertMigrationPaymentOrder(tx, userID, planID, &groupIDs[1])
		requirePostgresCode(t, err, "23514")
	})

	t.Run("monthly order accepts exact plan group", func(t *testing.T) {
		tx := testTx(t)
		userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
		planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)

		_, err := insertMigrationPaymentOrder(tx, userID, planID, &groupIDs[0])
		require.NoError(t, err)
	})

	t.Run("credits order must not take a group fulfillment path", func(t *testing.T) {
		tx := testTx(t)
		userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
		quota := 20.0
		planID := insertMigrationPlan(t, tx, "credits", nil, &quota)

		_, err := insertMigrationPaymentOrder(tx, userID, planID, &groupIDs[0])
		requirePostgresCode(t, err, "23514")
	})

	t.Run("credits order accepts wallet fulfillment path", func(t *testing.T) {
		tx := testTx(t)
		userID, _ := insertWalletMigrationUserAndGroups(t, tx)
		quota := 20.0
		planID := insertMigrationPlan(t, tx, "credits", nil, &quota)

		_, err := insertMigrationPaymentOrder(tx, userID, planID, nil)
		require.NoError(t, err)
	})

	t.Run("dangling plan id cannot bypass the guard", func(t *testing.T) {
		tx := testTx(t)
		userID, _ := insertWalletMigrationUserAndGroups(t, tx)

		_, err := insertMigrationPaymentOrder(tx, userID, 9_999_999_999, nil)
		requirePostgresCode(t, err, "23514")
	})
}

func TestPaymentOrderStatusOnlyReopenRevalidatesFulfillmentPath(t *testing.T) {
	tx := testTx(t)
	userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
	planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)
	_, err := insertMigrationPaymentOrder(tx, userID, planID, &groupIDs[0])
	require.NoError(t, err)
	require.NoError(t, setMigrationPaymentOrderStatus(tx, planID, "COMPLETED"))

	_, err = tx.ExecContext(context.Background(), `
		UPDATE subscription_plans
		SET plan_type = 'credits', group_id = NULL, wallet_quota_usd = 50
		WHERE id = $1
	`, planID)
	require.NoError(t, err)

	// Updating only status used to bypass the path trigger. FAILED is retryable,
	// so reopening the stale grouped order must revalidate against the new plan.
	err = setMigrationPaymentOrderStatus(tx, planID, "FAILED")
	requirePostgresCode(t, err, "23514")
}

func TestPlanFulfillmentShapeCannotRaceFulfillableOrders(t *testing.T) {
	for _, status := range []string{"PENDING", "PAID", "RECHARGING", "FAILED", "CANCELLED", "EXPIRED"} {
		t.Run("blocks monthly to credits with "+status+" order", func(t *testing.T) {
			tx := testTx(t)
			userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
			planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)
			_, err := insertMigrationPaymentOrder(tx, userID, planID, &groupIDs[0])
			require.NoError(t, err)
			require.NoError(t, setMigrationPaymentOrderStatus(tx, planID, status))

			_, err = tx.ExecContext(context.Background(), `
				UPDATE subscription_plans
				SET plan_type = 'credits', group_id = NULL, wallet_quota_usd = 50
				WHERE id = $1
			`, planID)
			requirePostgresCode(t, err, "23514")
		})
	}

	t.Run("blocks credits quota change with fulfillable order", func(t *testing.T) {
		tx := testTx(t)
		userID, _ := insertWalletMigrationUserAndGroups(t, tx)
		quota := 20.0
		planID := insertMigrationPlan(t, tx, "credits", nil, &quota)
		_, err := insertMigrationPaymentOrder(tx, userID, planID, nil)
		require.NoError(t, err)

		_, err = tx.ExecContext(context.Background(), `
			UPDATE subscription_plans
			SET wallet_quota_usd = 50
			WHERE id = $1
		`, planID)
		requirePostgresCode(t, err, "23514")
	})
}

func TestUnpaidFailedAndStaleCancelledOrdersDoNotFreezePlanLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		statusSQL string
	}{
		{
			name: "unpaid provider creation failure",
			statusSQL: `
				UPDATE payment_orders
				SET status = 'FAILED', paid_at = NULL, payment_trade_no = ''
				WHERE plan_id = $1
			`,
		},
		{
			name: "cancelled beyond recovery grace",
			statusSQL: `
				UPDATE payment_orders
				SET status = 'CANCELLED', updated_at = NOW() - INTERVAL '6 minutes'
				WHERE plan_id = $1
			`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
			planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)
			_, err := insertMigrationPaymentOrder(tx, userID, planID, &groupIDs[0])
			require.NoError(t, err)
			_, err = tx.ExecContext(context.Background(), tt.statusSQL, planID)
			require.NoError(t, err)

			_, err = tx.ExecContext(context.Background(), `
				UPDATE subscription_plans
				SET plan_type = 'credits', group_id = NULL, wallet_quota_usd = 50
				WHERE id = $1
			`, planID)
			require.NoError(t, err)
		})
	}
}

func TestPlanFulfillmentShapeCanChangeAfterOrdersAreTerminal(t *testing.T) {
	terminalStatuses := []string{
		"COMPLETED",
		"REFUND_REQUESTED",
		"REFUNDING",
		"PARTIALLY_REFUNDED",
		"REFUNDED",
		"REFUND_FAILED",
	}

	t.Run("allows change after expired recovery grace", func(t *testing.T) {
		tx := testTx(t)
		userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
		planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)
		_, err := insertMigrationPaymentOrder(tx, userID, planID, &groupIDs[0])
		require.NoError(t, err)
		_, err = tx.ExecContext(context.Background(), `
			UPDATE payment_orders
			SET status = 'EXPIRED', updated_at = NOW() - INTERVAL '6 minutes'
			WHERE plan_id = $1
		`, planID)
		require.NoError(t, err)

		_, err = tx.ExecContext(context.Background(), `
			UPDATE subscription_plans
			SET plan_type = 'credits', group_id = NULL, wallet_quota_usd = 50
			WHERE id = $1
		`, planID)
		require.NoError(t, err)
	})
	for _, status := range terminalStatuses {
		t.Run("allows change with only "+status+" order", func(t *testing.T) {
			tx := testTx(t)
			userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
			planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)
			_, err := insertMigrationPaymentOrder(tx, userID, planID, &groupIDs[0])
			require.NoError(t, err)
			require.NoError(t, setMigrationPaymentOrderStatus(tx, planID, status))

			_, err = tx.ExecContext(context.Background(), `
				UPDATE subscription_plans
				SET plan_type = 'credits', group_id = NULL, wallet_quota_usd = 50
				WHERE id = $1
			`, planID)
			require.NoError(t, err)
		})
	}

	t.Run("allows change when plan has no orders", func(t *testing.T) {
		tx := testTx(t)
		_, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
		planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)

		_, err := tx.ExecContext(context.Background(), `
			UPDATE subscription_plans
			SET plan_type = 'credits', group_id = NULL, wallet_quota_usd = 50
			WHERE id = $1
		`, planID)
		require.NoError(t, err)
	})
}

func TestPlanDeleteGuardProtectsRecoverableOrders(t *testing.T) {
	for _, status := range []string{"PENDING", "PAID", "RECHARGING", "FAILED", "CANCELLED", "EXPIRED"} {
		t.Run("blocks delete with "+status+" order", func(t *testing.T) {
			tx := testTx(t)
			userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
			planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)
			_, err := insertMigrationPaymentOrder(tx, userID, planID, &groupIDs[0])
			require.NoError(t, err)
			require.NoError(t, setMigrationPaymentOrderStatus(tx, planID, status))

			_, err = tx.ExecContext(context.Background(), "DELETE FROM subscription_plans WHERE id = $1", planID)
			requirePostgresCode(t, err, "23514")
		})
	}

	t.Run("allows delete after expired recovery grace", func(t *testing.T) {
		tx := testTx(t)
		userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
		planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)
		_, err := insertMigrationPaymentOrder(tx, userID, planID, &groupIDs[0])
		require.NoError(t, err)
		_, err = tx.ExecContext(context.Background(), `
			UPDATE payment_orders
			SET status = 'EXPIRED', updated_at = NOW() - INTERVAL '6 minutes'
			WHERE plan_id = $1
		`, planID)
		require.NoError(t, err)

		_, err = tx.ExecContext(context.Background(), "DELETE FROM subscription_plans WHERE id = $1", planID)
		require.NoError(t, err)
	})
}
