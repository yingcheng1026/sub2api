//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPlanShapeUpdateSerializesWithConcurrentOrderCreation(t *testing.T) {
	ctx := context.Background()
	userID, groupID, planID := insertCommittedPlanRaceFixture(t)

	orderTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = orderTx.Rollback() })
	orderID, err := insertMigrationPaymentOrderReturningID(orderTx, userID, planID, &groupID)
	require.NoError(t, err)
	insertPlanSnapshotTestRow(t, orderTx, orderID, userID, planID, groupID)

	planTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = planTx.Rollback() })
	var planBackendPID int
	require.NoError(t, planTx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&planBackendPID))

	updateResult := make(chan error, 1)
	go func() {
		_, updateErr := planTx.ExecContext(ctx, `
			UPDATE subscription_plans
			SET plan_type = 'credits', group_id = NULL, wallet_quota_usd = 50
			WHERE id = $1
		`, planID)
		updateResult <- updateErr
	}()

	requireBackendWaitingOnLock(t, ctx, planBackendPID,
		"plan update must wait for the order transaction's plan lock")
	require.NoError(t, orderTx.Commit())
	requireNoErrorResult(t, updateResult, "plan update remained blocked after snapshotted order committed")
}

func TestConcurrentOrderCreationRevalidatesAfterPlanShapeCommit(t *testing.T) {
	ctx := context.Background()
	userID, groupID, planID := insertCommittedPlanRaceFixture(t)

	planTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = planTx.Rollback() })
	_, err = planTx.ExecContext(ctx, `
		UPDATE subscription_plans
		SET plan_type = 'credits', group_id = NULL, wallet_quota_usd = 50
		WHERE id = $1
	`, planID)
	require.NoError(t, err)

	orderTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = orderTx.Rollback() })
	var orderBackendPID int
	require.NoError(t, orderTx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&orderBackendPID))

	insertResult := make(chan error, 1)
	go func() {
		_, insertErr := insertMigrationPaymentOrder(orderTx, userID, planID, &groupID)
		insertResult <- insertErr
	}()

	requireBackendWaitingOnLock(t, ctx, orderBackendPID,
		"order creation must wait for the in-flight plan shape update")
	require.NoError(t, planTx.Commit())
	requirePostgresErrorResult(t, insertResult, "order creation remained blocked after plan shape committed")
}

func TestPlanDeleteWaitsForConcurrentOrderCreationThenFails(t *testing.T) {
	ctx := context.Background()
	userID, groupID, planID := insertCommittedPlanRaceFixture(t)

	orderTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = orderTx.Rollback() })
	orderID, err := insertMigrationPaymentOrderReturningID(orderTx, userID, planID, &groupID)
	require.NoError(t, err)
	insertPlanSnapshotTestRow(t, orderTx, orderID, userID, planID, groupID)

	deleteTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = deleteTx.Rollback() })
	var deleteBackendPID int
	require.NoError(t, deleteTx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&deleteBackendPID))

	deleteResult := make(chan error, 1)
	go func() {
		_, deleteErr := deleteTx.ExecContext(ctx, "DELETE FROM subscription_plans WHERE id = $1", planID)
		deleteResult <- deleteErr
	}()

	requireBackendWaitingOnLock(t, ctx, deleteBackendPID,
		"plan delete must wait for the concurrent order's plan lock")
	require.NoError(t, orderTx.Commit())
	requireNoErrorResult(t, deleteResult, "plan delete remained blocked after snapshotted order committed")
}

func TestConcurrentOrderCreationRejectsPlanDeletedBeforeCommit(t *testing.T) {
	ctx := context.Background()
	userID, groupID, planID := insertCommittedPlanRaceFixture(t)

	deleteTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = deleteTx.Rollback() })
	_, err = deleteTx.ExecContext(ctx, "DELETE FROM subscription_plans WHERE id = $1", planID)
	require.NoError(t, err)

	orderTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = orderTx.Rollback() })
	var orderBackendPID int
	require.NoError(t, orderTx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&orderBackendPID))

	insertResult := make(chan error, 1)
	go func() {
		_, insertErr := insertMigrationPaymentOrder(orderTx, userID, planID, &groupID)
		insertResult <- insertErr
	}()

	requireBackendWaitingOnLock(t, ctx, orderBackendPID,
		"order creation must wait for the in-flight plan delete")
	require.NoError(t, deleteTx.Commit())
	requirePostgresErrorResult(t, insertResult, "order creation remained blocked after plan delete committed")
}

func TestSubscriptionGroupDisableWaitsForConcurrentPlanCreationThenFails(t *testing.T) {
	ctx := context.Background()
	_, groupID := insertCommittedGroupRaceFixture(t)

	planTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = planTx.Rollback() })
	var planID int64
	require.NoError(t, planTx.QueryRowContext(ctx, `
		INSERT INTO subscription_plans
			(plan_type, group_id, wallet_quota_usd, name, price)
		VALUES ('subscription', $1, NULL, $2, 20)
		RETURNING id
	`, groupID, fmt.Sprintf("wallet-group-race-plan-%d", time.Now().UnixNano())).Scan(&planID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM subscription_plans WHERE id = $1", planID)
	})

	groupTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = groupTx.Rollback() })
	var groupBackendPID int
	require.NoError(t, groupTx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&groupBackendPID))

	updateResult := make(chan error, 1)
	go func() {
		_, updateErr := groupTx.ExecContext(ctx, "UPDATE groups SET status = 'disabled' WHERE id = $1", groupID)
		updateResult <- updateErr
	}()

	requireBackendWaitingOnLock(t, ctx, groupBackendPID,
		"group mutation must wait for the concurrent plan's group lock")
	require.NoError(t, planTx.Commit())
	select {
	case updateErr := <-updateResult:
		require.NoError(t, updateErr, "reverse lifecycle check is deferred until commit")
	case <-time.After(5 * time.Second):
		t.Fatal("group mutation remained blocked after concurrent plan committed")
	}
	requirePostgresCode(t, groupTx.Commit(), "23514")
}

func TestConcurrentPlanCreationRevalidatesAfterGroupDisableCommit(t *testing.T) {
	ctx := context.Background()
	_, groupID := insertCommittedGroupRaceFixture(t)

	groupTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = groupTx.Rollback() })
	_, err = groupTx.ExecContext(ctx, "UPDATE groups SET status = 'disabled' WHERE id = $1", groupID)
	require.NoError(t, err)

	planTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = planTx.Rollback() })
	var planBackendPID int
	require.NoError(t, planTx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&planBackendPID))

	insertResult := make(chan error, 1)
	go func() {
		_, insertErr := planTx.ExecContext(ctx, `
			INSERT INTO subscription_plans
				(plan_type, group_id, wallet_quota_usd, name, price)
			VALUES ('subscription', $1, NULL, $2, 20)
		`, groupID, fmt.Sprintf("wallet-disabled-group-plan-%d", time.Now().UnixNano()))
		insertResult <- insertErr
	}()

	requireBackendWaitingOnLock(t, ctx, planBackendPID,
		"plan creation must wait for the in-flight group disable")
	require.NoError(t, groupTx.Commit())
	requirePostgresErrorResult(t, insertResult, "plan creation remained blocked after group disable committed")
}

func requireBackendWaitingOnLock(t *testing.T, ctx context.Context, backendPID int, message string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		err := integrationDB.QueryRowContext(ctx, `
			SELECT COALESCE(wait_event_type = 'Lock', FALSE)
			FROM pg_stat_activity
			WHERE pid = $1
		`, backendPID).Scan(&waiting)
		return err == nil && waiting
	}, 3*time.Second, 20*time.Millisecond, message)
}

func requirePostgresErrorResult(t *testing.T, result <-chan error, timeoutMessage string) {
	t.Helper()
	select {
	case err := <-result:
		requirePostgresCode(t, err, "23514")
	case <-time.After(5 * time.Second):
		t.Fatal(timeoutMessage)
	}
}

func requireNoErrorResult(t *testing.T, result <-chan error, timeoutMessage string) {
	t.Helper()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal(timeoutMessage)
	}
}

func insertCommittedPlanRaceFixture(t *testing.T) (userID, groupID, planID int64) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO users (email, password_hash)
		VALUES ($1, 'migration-race-test-password-hash')
		RETURNING id
	`, "wallet-migration-race-"+suffix+"@example.test").Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO groups (name, subscription_type, status)
		VALUES ($1, 'subscription', 'active')
		RETURNING id
	`, "wallet-migration-race-group-"+suffix).Scan(&groupID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO subscription_plans
			(plan_type, group_id, wallet_quota_usd, name, price)
		VALUES ('subscription', $1, NULL, $2, 20)
		RETURNING id
	`, groupID, "wallet-migration-race-plan-"+suffix).Scan(&planID))

	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `
			DELETE FROM subscription_plan_fulfillment_snapshots
			WHERE payment_order_id IN (SELECT id FROM payment_orders WHERE plan_id = $1)
		`, planID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM payment_orders WHERE plan_id = $1", planID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM subscription_plan_groups WHERE plan_id = $1", planID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM subscription_plans WHERE id = $1", planID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE id = $1", groupID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})
	return userID, groupID, planID
}

func insertCommittedGroupRaceFixture(t *testing.T) (userID, groupID int64) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO users (email, password_hash)
		VALUES ($1, 'migration-group-race-test-password-hash')
		RETURNING id
	`, "wallet-group-race-"+suffix+"@example.test").Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO groups (name, subscription_type, status)
		VALUES ($1, 'subscription', 'active')
		RETURNING id
	`, "wallet-group-race-"+suffix).Scan(&groupID))

	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE id = $1", groupID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})
	return userID, groupID
}
