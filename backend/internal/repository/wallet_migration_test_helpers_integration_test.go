//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func createWalletMigrationTempTables(t *testing.T, tx *sql.Tx) {
	t.Helper()
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `
		CREATE TEMP TABLE users (
			id BIGINT PRIMARY KEY
		);
		CREATE TEMP TABLE user_subscriptions (
			id BIGINT PRIMARY KEY,
			user_id BIGINT NOT NULL,
			group_id BIGINT,
			wallet_initial_usd NUMERIC(20,10),
			wallet_balance_usd NUMERIC(20,10),
			status TEXT NOT NULL,
			deleted_at TIMESTAMPTZ,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		);
		CREATE TEMP TABLE subscription_wallet_ledger (
			id BIGSERIAL PRIMARY KEY,
			subscription_id BIGINT NOT NULL,
			delta_usd NUMERIC(20,10) NOT NULL,
			balance_after NUMERIC(20,10) NOT NULL,
			reason VARCHAR(32) NOT NULL,
			notes TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
	require.NoError(t, err)
}

func createMonthlySeparationMigrationTempTables(t *testing.T, tx *sql.Tx) {
	t.Helper()
	_, err := tx.ExecContext(context.Background(), `
		CREATE TEMP TABLE groups (
			id BIGINT PRIMARY KEY,
			status VARCHAR(20) NOT NULL,
			subscription_type VARCHAR(20) NOT NULL,
			deleted_at TIMESTAMPTZ
		);
		CREATE TEMP TABLE subscription_plans (
			id BIGINT PRIMARY KEY,
			plan_type VARCHAR(16) NOT NULL,
			group_id BIGINT,
			wallet_quota_usd NUMERIC(20,8),
			for_sale BOOLEAN NOT NULL DEFAULT TRUE
		);
		CREATE TEMP TABLE subscription_plan_groups (
			id BIGINT PRIMARY KEY,
			plan_id BIGINT NOT NULL,
			group_id BIGINT NOT NULL
		);
		CREATE TEMP TABLE payment_orders (
			id BIGINT PRIMARY KEY,
			order_type VARCHAR(20) NOT NULL,
			plan_id BIGINT,
			subscription_group_id BIGINT,
			status VARCHAR(30) NOT NULL DEFAULT 'PENDING',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			paid_at TIMESTAMPTZ,
			payment_trade_no VARCHAR(200)
		);
		CREATE TEMP TABLE user_subscriptions (
			id BIGINT PRIMARY KEY,
			group_id BIGINT,
			wallet_balance_usd NUMERIC(20,10),
			wallet_initial_usd NUMERIC(20,10),
			expires_at TIMESTAMPTZ NOT NULL,
			status VARCHAR(20) NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ
		);
	`)
	require.NoError(t, err)
}

func execMigrationTestSQL(tx *sql.Tx, statement string) error {
	_, err := tx.ExecContext(context.Background(), statement)
	return err
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	content, err := dbmigrations.FS.ReadFile(name)
	require.NoError(t, err)
	return string(content)
}

func insertWalletMigrationUserAndGroups(t *testing.T, tx *sql.Tx) (int64, [2]int64) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var userID int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO users (email, password_hash)
		VALUES ($1, 'migration-test-password-hash')
		RETURNING id
	`, "wallet-migration-"+suffix+"@example.test").Scan(&userID)
	require.NoError(t, err)

	var groupIDs [2]int64
	for i := range groupIDs {
		err = tx.QueryRowContext(ctx, `
			INSERT INTO groups (name, subscription_type, status)
			VALUES ($1, 'subscription', 'active')
			RETURNING id
		`, fmt.Sprintf("wallet-migration-group-%s-%d", suffix, i)).Scan(&groupIDs[i])
		require.NoError(t, err)
	}
	return userID, groupIDs
}

func insertWalletSubscription(t *testing.T, tx *sql.Tx, userID int64, expiresAt, status string) int64 {
	t.Helper()
	var id int64
	err := tx.QueryRowContext(context.Background(), `
		INSERT INTO user_subscriptions (
			user_id, starts_at, expires_at, status,
			wallet_balance_usd, wallet_initial_usd
		) VALUES ($1, NOW(), $2::timestamptz, $3, 10, 10)
		RETURNING id
	`, userID, expiresAt, status).Scan(&id)
	require.NoError(t, err)
	return id
}

func insertMigrationPlan(t *testing.T, tx *sql.Tx, planType string, groupID *int64, walletQuota *float64) int64 {
	t.Helper()
	var id int64
	err := tx.QueryRowContext(context.Background(), `
		INSERT INTO subscription_plans
			(plan_type, group_id, wallet_quota_usd, name, price)
		VALUES ($1::varchar, $2::bigint, $3::numeric, $4::varchar, 20)
		RETURNING id
	`, planType, groupID, walletQuota, fmt.Sprintf("migration-plan-%d", time.Now().UnixNano())).Scan(&id)
	require.NoError(t, err)
	return id
}

func insertMigrationPaymentOrder(tx *sql.Tx, userID, planID int64, groupID *int64) (sql.Result, error) {
	return tx.ExecContext(context.Background(), `
		INSERT INTO payment_orders (
			user_id, user_email, user_name, amount, pay_amount,
			order_type, plan_id, subscription_group_id, subscription_days,
			expires_at
		) VALUES ($1::bigint, 'migration@example.test', 'migration test', 20, 20,
			'subscription', $2::bigint, $3::bigint, 30, NOW() + INTERVAL '15 minutes')
	`, userID, planID, groupID)
}

func insertMigrationPaymentOrderReturningID(tx *sql.Tx, userID, planID int64, groupID *int64) (int64, error) {
	var orderID int64
	err := tx.QueryRowContext(context.Background(), `
		INSERT INTO payment_orders (
			user_id, user_email, user_name, amount, pay_amount,
			order_type, plan_id, subscription_group_id, subscription_days,
			expires_at
		) VALUES ($1::bigint, 'migration@example.test', 'migration test', 20, 20,
			'subscription', $2::bigint, $3::bigint, 30, NOW() + INTERVAL '15 minutes')
		RETURNING id
	`, userID, planID, groupID).Scan(&orderID)
	return orderID, err
}

func setMigrationPaymentOrderStatus(tx *sql.Tx, planID int64, status string) error {
	_, err := tx.ExecContext(context.Background(), `
		UPDATE payment_orders
		SET
			status = $1::varchar,
			paid_at = CASE WHEN $1::text = 'FAILED' THEN NOW() ELSE paid_at END,
			payment_trade_no = CASE WHEN $1::text = 'FAILED' THEN 'trusted-test-payment' ELSE payment_trade_no END
		WHERE plan_id = $2
	`, status, planID)
	return err
}

func requirePostgresCode(t *testing.T, err error, code pq.ErrorCode) {
	t.Helper()
	require.Error(t, err)
	var pqErr *pq.Error
	require.True(t, errors.As(err, &pqErr), "expected PostgreSQL error, got %T: %v", err, err)
	require.Equal(t, code, pqErr.Code, "unexpected PostgreSQL error: %s", pqErr.Message)
}

func requirePostgresCodeOneOf(t *testing.T, err error, codes ...pq.ErrorCode) {
	t.Helper()
	require.Error(t, err)
	var pqErr *pq.Error
	require.True(t, errors.As(err, &pqErr), "expected PostgreSQL error, got %T: %v", err, err)
	for _, code := range codes {
		if pqErr.Code == code {
			return
		}
	}
	require.Failf(t, "unexpected PostgreSQL error", "code=%s message=%s expected one of=%v", pqErr.Code, pqErr.Message, codes)
}

func migrationInt64Ptr(value int64) *int64 {
	return &value
}

func migrationFloat64Ptr(value float64) *float64 {
	return &value
}
