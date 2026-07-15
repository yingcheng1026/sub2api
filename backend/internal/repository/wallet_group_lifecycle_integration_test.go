//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubscriptionGroupLifecycleGuardProtectsMonthlyPlans(t *testing.T) {
	tests := []struct {
		name      string
		mutation  string
		arguments func(groupID int64) []any
	}{
		{
			name:      "disable",
			mutation:  "UPDATE groups SET status = 'disabled' WHERE id = $1",
			arguments: func(groupID int64) []any { return []any{groupID} },
		},
		{
			name:      "subscription type drift",
			mutation:  "UPDATE groups SET subscription_type = 'standard' WHERE id = $1",
			arguments: func(groupID int64) []any { return []any{groupID} },
		},
		{
			name:      "soft delete",
			mutation:  "UPDATE groups SET deleted_at = NOW() WHERE id = $1",
			arguments: func(groupID int64) []any { return []any{groupID} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			_, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
			_ = insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)

			_, err := tx.ExecContext(context.Background(), tt.mutation, tt.arguments(groupIDs[0])...)
			require.NoError(t, err, "lifecycle check is deferred to the transaction boundary")
			_, err = tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
			requirePostgresCode(t, err, "23514")
			require.Contains(t, err.Error(), "referenced by a monthly plan")
		})
	}
}

func TestSubscriptionGroupHardDeleteIsRestrictedByPlanForeignKey(t *testing.T) {
	tx := testTx(t)
	_, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
	_ = insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)

	_, err := tx.ExecContext(context.Background(), "DELETE FROM groups WHERE id = $1", groupIDs[0])
	requirePostgresCodeOneOf(t, err, "23001", "23503")
}

func TestSubscriptionGroupLifecycleGuardProtectsRecoverableLegacyOrder(t *testing.T) {
	tx := testTx(t)
	userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
	insertLegacyMigrationPaymentOrder(t, tx, userID, groupIDs[0], "PAID")

	_, err := tx.ExecContext(context.Background(), `
		UPDATE groups
		SET status = 'disabled'
		WHERE id = $1
	`, groupIDs[0])
	require.NoError(t, err)
	_, err = tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
	requirePostgresCode(t, err, "23514")
	require.Contains(t, err.Error(), "legacy order remains fulfillable")
}

func TestSubscriptionGroupLifecycleGuardIgnoresUnpaidFailedAndStaleCancelledOrders(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		makeStale bool
	}{
		{name: "unpaid failed", status: "FAILED"},
		{name: "stale cancelled", status: "CANCELLED", makeStale: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
			insertLegacyMigrationPaymentOrder(t, tx, userID, groupIDs[0], tt.status)
			if tt.makeStale {
				_, err := tx.ExecContext(context.Background(), `
					UPDATE payment_orders
					SET updated_at = NOW() - INTERVAL '6 minutes'
					WHERE plan_id IS NULL AND subscription_group_id = $1
				`, groupIDs[0])
				require.NoError(t, err)
			}

			_, err := tx.ExecContext(context.Background(), `
				UPDATE groups
				SET status = 'disabled'
				WHERE id = $1
			`, groupIDs[0])
			require.NoError(t, err)
			_, err = tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
			require.NoError(t, err)
		})
	}
}

func TestSubscriptionGroupLifecycleGuardAllowsUnreferencedGroupMutation(t *testing.T) {
	tx := testTx(t)
	_, groupIDs := insertWalletMigrationUserAndGroups(t, tx)

	_, err := tx.ExecContext(context.Background(), `
		UPDATE groups
		SET status = 'disabled'
		WHERE id = $1
	`, groupIDs[0])
	require.NoError(t, err)
	_, err = tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
	require.NoError(t, err)
}

func TestSubscriptionGroupLifecycleGuardDoesNotFreezeArchivedUnusedPlan(t *testing.T) {
	tx := testTx(t)
	_, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
	planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)
	_, err := tx.ExecContext(context.Background(), `
		UPDATE subscription_plans
		SET for_sale = FALSE
		WHERE id = $1
	`, planID)
	require.NoError(t, err)

	_, err = tx.ExecContext(context.Background(), `
		UPDATE groups
		SET status = 'disabled'
		WHERE id = $1
	`, groupIDs[0])
	require.NoError(t, err)
	_, err = tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
	require.NoError(t, err)
}

func TestSubscriptionGroupLifecycleGuardProtectsActiveSubscription(t *testing.T) {
	tx := testTx(t)
	userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
	_ = insertActiveGroupSubscription(t, tx, userID, groupIDs[0])

	_, err := tx.ExecContext(context.Background(), `
		UPDATE groups
		SET status = 'disabled'
		WHERE id = $1
	`, groupIDs[0])
	require.NoError(t, err)
	_, err = tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
	requirePostgresCode(t, err, "23514")
	require.Contains(t, err.Error(), "active or resumable subscriptions")
}

func TestCompletedOrderCannotLeadToPaidSubscriptionCascadeDeletion(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	userID, groupIDs := insertWalletMigrationUserAndGroups(t, tx)
	planID := insertMigrationPlan(t, tx, "subscription", &groupIDs[0], nil)
	_, err := insertMigrationPaymentOrder(tx, userID, planID, &groupIDs[0])
	require.NoError(t, err)
	require.NoError(t, setMigrationPaymentOrderStatus(tx, planID, "COMPLETED"))
	subscriptionID := insertActiveGroupSubscription(t, tx, userID, groupIDs[0])

	_, err = tx.ExecContext(ctx, "DELETE FROM subscription_plans WHERE id = $1", planID)
	require.NoError(t, err, "terminal order no longer needs the mutable product row for fulfillment")
	require.NoError(t, execMigrationTestSQL(tx, "SAVEPOINT before_protected_group_delete"))
	_, err = tx.ExecContext(ctx, "DELETE FROM groups WHERE id = $1", groupIDs[0])
	requirePostgresCodeOneOf(t, err, "23001", "23503")
	require.NoError(t, execMigrationTestSQL(tx, "ROLLBACK TO SAVEPOINT before_protected_group_delete"))

	var subscriptions int
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM user_subscriptions
		WHERE id = $1
	`, subscriptionID).Scan(&subscriptions))
	require.Equal(t, 1, subscriptions, "hard group delete must never cascade away paid subscription history")
}

func insertLegacyMigrationPaymentOrder(t *testing.T, tx *sql.Tx, userID, groupID int64, status string) {
	t.Helper()
	_, err := tx.ExecContext(context.Background(), `
		INSERT INTO payment_orders (
			user_id, user_email, user_name, amount, pay_amount,
			order_type, subscription_group_id, subscription_days,
			status, expires_at
		) VALUES ($1, 'migration@example.test', 'migration test', 20, 20,
			'subscription', $2, 30, $3, NOW() + INTERVAL '15 minutes')
	`, userID, groupID, status)
	require.NoError(t, err)
}

func insertActiveGroupSubscription(t *testing.T, tx *sql.Tx, userID, groupID int64) int64 {
	t.Helper()
	var subscriptionID int64
	err := tx.QueryRowContext(context.Background(), `
		INSERT INTO user_subscriptions (
			user_id, group_id, starts_at, expires_at, status
		) VALUES ($1, $2, NOW(), NOW() + INTERVAL '30 days', 'active')
		RETURNING id
	`, userID, groupID).Scan(&subscriptionID)
	require.NoError(t, err)
	return subscriptionID
}
