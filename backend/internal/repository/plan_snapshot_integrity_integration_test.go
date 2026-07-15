//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanSnapshotSurvivesPlanMutationAndDeletion(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
	planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)
	orderID := insertPlanSnapshotTestOrder(t, tx, userID, planID, groupIDs[0])

	insertPlanSnapshotTestRow(t, tx, orderID, userID, planID, groupIDs[0])
	require.NoError(t, execMigrationTestSQL(tx, "SET CONSTRAINTS ALL IMMEDIATE"))
	require.NoError(t, execMigrationTestSQL(tx, "SET CONSTRAINTS ALL DEFERRED"))

	_, err := tx.ExecContext(ctx, `
		UPDATE subscription_plans
		SET plan_type = 'credits', group_id = NULL, wallet_quota_usd = 999,
			validity_days = 36500, validity_unit = 'day'
		WHERE id = $1
	`, planID)
	require.NoError(t, err, "a durable order snapshot must decouple fulfillment from later plan edits")
	_, err = tx.ExecContext(ctx, "DELETE FROM subscription_plans WHERE id = $1", planID)
	require.NoError(t, err, "a durable order snapshot must survive source plan deletion")

	var planType string
	var snapshotGroupID int64
	var snapshotDays int
	var snapshotQuota sql.NullFloat64
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT
			snapshot->>'plan_type',
			(snapshot->>'group_id')::bigint,
			(snapshot->>'subscription_days')::integer,
			(snapshot->>'wallet_quota_usd')::numeric
		FROM subscription_plan_fulfillment_snapshots
		WHERE payment_order_id = $1
	`, orderID).Scan(&planType, &snapshotGroupID, &snapshotDays, &snapshotQuota))
	require.Equal(t, "subscription", planType)
	require.Equal(t, groupIDs[0], snapshotGroupID)
	require.Equal(t, 30, snapshotDays)
	require.False(t, snapshotQuota.Valid)

	_, err = tx.ExecContext(ctx, `
		UPDATE payment_orders
		SET status = 'PAID', paid_at = NOW(), payment_trade_no = 'snapshot-trade'
		WHERE id = $1
	`, orderID)
	require.NoError(t, err)
	require.NoError(t, execMigrationTestSQL(tx, "SET CONSTRAINTS ALL IMMEDIATE"),
		"paid transition must validate against the immutable snapshot, not the deleted plan")
}

func TestPlanSnapshotIsRequiredAndImmutable(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
	planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)

	require.NoError(t, execMigrationTestSQL(tx, "SAVEPOINT before_missing_snapshot"))
	_ = insertPlanSnapshotTestOrder(t, tx, userID, planID, groupIDs[0])
	_, err := tx.ExecContext(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	requirePostgresCode(t, err, "23514")
	require.NoError(t, execMigrationTestSQL(tx, "ROLLBACK TO SAVEPOINT before_missing_snapshot"))
	require.NoError(t, execMigrationTestSQL(tx, "SET CONSTRAINTS ALL DEFERRED"))

	require.NoError(t, execMigrationTestSQL(tx, "SAVEPOINT before_malformed_snapshot"))
	malformedOrderID := insertPlanSnapshotTestOrder(t, tx, userID, planID, groupIDs[0])
	_, err = tx.ExecContext(ctx, `
		INSERT INTO subscription_plan_fulfillment_snapshots (
			payment_order_id, user_id, source_plan_id, snapshot
		) VALUES ($1::bigint, $2::bigint, $3::bigint, jsonb_build_object(
			'schema_version', 1,
			'plan_id', $3::bigint,
			'plan_type', 'subscription',
			'plan_price', 20,
			'group_id', $4::bigint,
			'subscription_days', 30,
			'wallet_quota_usd', NULL::numeric,
			'covered_group_ids', jsonb_build_array($4::bigint)
		))
	`, malformedOrderID, userID, planID, groupIDs[0])
	requirePostgresCode(t, err, "23514")
	require.NoError(t, execMigrationTestSQL(tx, "ROLLBACK TO SAVEPOINT before_malformed_snapshot"))

	orderID := insertPlanSnapshotTestOrder(t, tx, userID, planID, groupIDs[0])
	insertPlanSnapshotTestRow(t, tx, orderID, userID, planID, groupIDs[0])

	require.NoError(t, execMigrationTestSQL(tx, "SAVEPOINT before_snapshot_mutation"))
	_, err = tx.ExecContext(ctx, `
		UPDATE subscription_plan_fulfillment_snapshots
		SET snapshot = jsonb_set(snapshot, '{subscription_days}', '7'::jsonb)
		WHERE payment_order_id = $1
	`, orderID)
	requirePostgresCode(t, err, "23514")
	require.NoError(t, execMigrationTestSQL(tx, "ROLLBACK TO SAVEPOINT before_snapshot_mutation"))

	var subscriptionID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
		INSERT INTO user_subscriptions (
			user_id, group_id, starts_at, expires_at, status
		) VALUES ($1, $2, NOW(), NOW() + INTERVAL '30 days', 'active')
		RETURNING id
	`, userID, groupIDs[0]).Scan(&subscriptionID))
	_, err = tx.ExecContext(ctx, `
		UPDATE subscription_plan_fulfillment_snapshots
		SET user_subscription_id = $2,
			grant_starts_at = NOW(),
			grant_expires_at = NOW() + INTERVAL '30 days',
			attached_at = NOW()
		WHERE payment_order_id = $1
	`, orderID, subscriptionID)
	require.NoError(t, err)
	require.NoError(t, execMigrationTestSQL(tx, "SET CONSTRAINTS ALL IMMEDIATE"))
	require.NoError(t, execMigrationTestSQL(tx, "SET CONSTRAINTS ALL DEFERRED"))

	require.NoError(t, execMigrationTestSQL(tx, "SAVEPOINT before_grant_mutation"))
	_, err = tx.ExecContext(ctx, `
		UPDATE subscription_plan_fulfillment_snapshots
		SET grant_expires_at = grant_expires_at + INTERVAL '1 day'
		WHERE payment_order_id = $1
	`, orderID)
	requirePostgresCode(t, err, "23514")
	require.NoError(t, execMigrationTestSQL(tx, "ROLLBACK TO SAVEPOINT before_grant_mutation"))

	require.NoError(t, execMigrationTestSQL(tx, "SAVEPOINT before_snapshot_delete"))
	_, err = tx.ExecContext(ctx, `
		DELETE FROM subscription_plan_fulfillment_snapshots
		WHERE payment_order_id = $1
	`, orderID)
	requirePostgresCode(t, err, "23514")
	require.NoError(t, execMigrationTestSQL(tx, "ROLLBACK TO SAVEPOINT before_snapshot_delete"))
}

func insertPlanSnapshotTestOrder(t *testing.T, tx *sql.Tx, userID, planID, groupID int64) int64 {
	t.Helper()
	var orderID int64
	err := tx.QueryRowContext(context.Background(), `
		INSERT INTO payment_orders (
			user_id, user_email, user_name, amount, pay_amount,
			order_type, plan_id, subscription_group_id, subscription_days,
			status, expires_at
		) VALUES ($1, 'snapshot@example.test', 'snapshot test', 20, 20,
			'subscription', $2, $3, 30, 'PENDING', NOW() + INTERVAL '15 minutes')
		RETURNING id
	`, userID, planID, groupID).Scan(&orderID)
	require.NoError(t, err)
	return orderID
}

func insertPlanSnapshotTestRow(t *testing.T, tx *sql.Tx, orderID, userID, planID, groupID int64) {
	t.Helper()
	_, err := tx.ExecContext(context.Background(), `
		INSERT INTO subscription_plan_fulfillment_snapshots (
			payment_order_id, user_id, source_plan_id, snapshot
		) VALUES ($1::bigint, $2::bigint, $3::bigint, jsonb_build_object(
			'schema_version', 1,
			'plan_id', $3::bigint,
			'plan_type', 'subscription',
			'plan_price', 20,
			'group_id', $4::bigint,
			'subscription_days', 30,
			'wallet_quota_usd', NULL::numeric,
			'covered_group_ids', jsonb_build_array($4::bigint),
			'locked_rates', jsonb_build_object($4::text, 8.5)
		))
	`, orderID, userID, planID, groupID)
	require.NoError(t, err)
}
