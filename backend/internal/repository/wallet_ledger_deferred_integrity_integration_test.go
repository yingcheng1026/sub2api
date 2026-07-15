//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWalletLedgerMigration175DeferredIntegrityGuard(t *testing.T) {
	t.Run("wallet and activation may be created in the same transaction", func(t *testing.T) {
		tx := newWalletLedgerGuardTestTransaction(t)
		insertTempWalletAndLedger(t, tx, 1, 10, 10, []tempLedgerAmount{
			{delta: "10", balanceAfter: "10", reason: "activation"},
		})

		_, err := tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
		require.NoError(t, err)
	})

	t.Run("wallet without activation fails at the transaction boundary", func(t *testing.T) {
		tx := newWalletLedgerGuardTestTransaction(t)
		insertTempWalletAndLedger(t, tx, 1, 10, 10, nil)

		_, err := tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
		requirePostgresCode(t, err, "23514")
		require.Contains(t, err.Error(), "exactly one activation")
	})

	t.Run("cached balance must exactly equal the ledger sum", func(t *testing.T) {
		tx := newWalletLedgerGuardTestTransaction(t)
		insertTempWalletAndLedger(t, tx, 1, 10, 10, []tempLedgerAmount{
			{delta: "9.99", balanceAfter: "9.99", reason: "activation"},
		})

		_, err := tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
		requirePostgresCode(t, err, "23514")
		require.Contains(t, err.Error(), "cached balance must equal ledger sum")
	})

	t.Run("negative post-response debt remains valid when fully ledgered", func(t *testing.T) {
		tx := newWalletLedgerGuardTestTransaction(t)
		insertTempWalletAndLedger(t, tx, 1, 10, -2, []tempLedgerAmount{
			{delta: "10", balanceAfter: "10", reason: "activation"},
			{delta: "-12", balanceAfter: "-2", reason: "usage"},
		})

		_, err := tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
		require.NoError(t, err)
	})

	t.Run("a second active permanent wallet is rejected before index 178", func(t *testing.T) {
		tx := newWalletLedgerGuardTestTransaction(t)
		insertTempWalletAndLedger(t, tx, 1, 10, 10, []tempLedgerAmount{
			{delta: "10", balanceAfter: "10", reason: "activation"},
		})
		insertTempWalletAndLedger(t, tx, 2, 20, 20, []tempLedgerAmount{
			{delta: "20", balanceAfter: "20", reason: "activation"},
		})

		_, err := tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
		requirePostgresCode(t, err, "23514")
		require.Contains(t, err.Error(), "at most one active permanent credits wallet")
	})

	t.Run("ledger rows cannot be rewritten", func(t *testing.T) {
		tx := newWalletLedgerGuardTestTransaction(t)
		insertTempWalletAndLedger(t, tx, 1, 10, 10, []tempLedgerAmount{
			{delta: "10", balanceAfter: "10", reason: "activation"},
		})
		_, err := tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
		require.NoError(t, err)

		_, err = tx.ExecContext(context.Background(), `
			UPDATE subscription_wallet_ledger
			SET notes = 'rewritten audit'
			WHERE subscription_id = 1
		`)
		requirePostgresCode(t, err, "23514")
		require.Contains(t, err.Error(), "append-only")
	})

	t.Run("ledger rows cannot be deleted", func(t *testing.T) {
		tx := newWalletLedgerGuardTestTransaction(t)
		insertTempWalletAndLedger(t, tx, 1, 10, 10, []tempLedgerAmount{
			{delta: "10", balanceAfter: "10", reason: "activation"},
		})
		_, err := tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
		require.NoError(t, err)

		_, err = tx.ExecContext(context.Background(), `
			DELETE FROM subscription_wallet_ledger
			WHERE subscription_id = 1
		`)
		requirePostgresCode(t, err, "23514")
		require.Contains(t, err.Error(), "append-only")
	})

	t.Run("wallet parent cannot cascade-delete its ledger", func(t *testing.T) {
		tx := newWalletLedgerGuardTestTransaction(t)
		insertTempWalletAndLedger(t, tx, 1, 10, 10, []tempLedgerAmount{
			{delta: "10", balanceAfter: "10", reason: "activation"},
		})
		_, err := tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
		require.NoError(t, err)

		_, err = tx.ExecContext(context.Background(), "DELETE FROM user_subscriptions WHERE id = 1")
		requirePostgresCodeOneOf(t, err, "23001", "23503")
	})

	t.Run("soft-deleted wallet remains protected from cached balance drift", func(t *testing.T) {
		tx := newWalletLedgerGuardTestTransaction(t)
		insertTempWalletAndLedger(t, tx, 1, 10, 10, []tempLedgerAmount{
			{delta: "10", balanceAfter: "10", reason: "activation"},
		})
		_, err := tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
		require.NoError(t, err)
		_, err = tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL DEFERRED")
		require.NoError(t, err)
		_, err = tx.ExecContext(context.Background(), `
			UPDATE user_subscriptions
			SET deleted_at = NOW(), wallet_balance_usd = 9
			WHERE id = 1
		`)
		require.NoError(t, err)

		_, err = tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
		requirePostgresCode(t, err, "23514")
		require.Contains(t, err.Error(), "cached balance must equal ledger sum")
	})

	t.Run("frozen settlement may append debt to a soft-deleted wallet", func(t *testing.T) {
		tx := newWalletLedgerGuardTestTransaction(t)
		insertTempWalletAndLedger(t, tx, 1, 10, 10, []tempLedgerAmount{
			{delta: "10", balanceAfter: "10", reason: "activation"},
		})
		_, err := tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
		require.NoError(t, err)
		_, err = tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL DEFERRED")
		require.NoError(t, err)
		_, err = tx.ExecContext(context.Background(), `
			UPDATE user_subscriptions
			SET deleted_at = NOW(), wallet_balance_usd = 8
			WHERE id = 1
		`)
		require.NoError(t, err)
		_, err = tx.ExecContext(context.Background(), `
			INSERT INTO subscription_wallet_ledger (
				subscription_id, delta_usd, balance_after, reason
			) VALUES (1, -2, 8, 'usage')
		`)
		require.NoError(t, err)

		_, err = tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
		require.NoError(t, err)
	})

	t.Run("ledger reason cannot disguise the direction of money", func(t *testing.T) {
		tx := newWalletLedgerGuardTestTransaction(t)
		insertTempWalletAndLedger(t, tx, 1, 10, 10, []tempLedgerAmount{
			{delta: "10", balanceAfter: "10", reason: "activation"},
		})
		_, err := tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL IMMEDIATE")
		require.NoError(t, err)
		_, err = tx.ExecContext(context.Background(), "SET CONSTRAINTS ALL DEFERRED")
		require.NoError(t, err)
		_, err = tx.ExecContext(context.Background(), "UPDATE user_subscriptions SET wallet_balance_usd = 11 WHERE id = 1")
		require.NoError(t, err)
		_, err = tx.ExecContext(context.Background(), `
			INSERT INTO subscription_wallet_ledger (
				subscription_id, delta_usd, balance_after, reason
			) VALUES (1, 1, 11, 'usage')
		`)
		requirePostgresCode(t, err, "23514")
	})
}

type tempLedgerAmount struct {
	delta        string
	balanceAfter string
	reason       string
}

func newWalletLedgerGuardTestTransaction(t *testing.T) *sql.Tx {
	t.Helper()
	tx := testTx(t)
	createWalletMigrationTempTables(t, tx)
	_, err := tx.ExecContext(context.Background(), "INSERT INTO users (id) VALUES (42)")
	require.NoError(t, err)
	_, err = tx.ExecContext(context.Background(), readMigration(t, "175_wallet_ledger_integrity.sql"))
	require.NoError(t, err)
	return tx
}

func insertTempWalletAndLedger(
	t *testing.T,
	tx *sql.Tx,
	subscriptionID int64,
	initial float64,
	balance float64,
	entries []tempLedgerAmount,
) {
	t.Helper()
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_subscriptions (
			id, user_id, wallet_initial_usd, wallet_balance_usd,
			status, expires_at, created_at
		) VALUES ($1, 42, $2, $3, 'active', '2099-12-31 23:59:59+00', NOW())
	`, subscriptionID, initial, balance)
	require.NoError(t, err)

	for _, entry := range entries {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO subscription_wallet_ledger (
				subscription_id, delta_usd, balance_after, reason
			) VALUES ($1, $2::numeric, $3::numeric, $4)
		`, subscriptionID, entry.delta, entry.balanceAfter, entry.reason)
		require.NoError(t, err)
	}
}
