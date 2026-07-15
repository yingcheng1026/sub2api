//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

const walletUniversalKeyMigrationName = "钱包通用 key（自动路由）"

func TestAPIKeyPurposeMigration181BackfillsOnlyUnambiguousWalletKey(t *testing.T) {
	tx := testTx(t)
	createAPIKeyPurposeMigrationTempTables(t, tx)
	ctx := context.Background()

	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_subscriptions
			(id, user_id, group_id, wallet_balance_usd, status, expires_at)
		VALUES (1, 42, NULL, 25, 'active', '2099-12-31 23:59:59+00');
		INSERT INTO api_keys (id, user_id, name, group_id, deleted_at)
		VALUES
			(10, 42, '钱包通用 key（自动路由）', NULL, NULL),
			(11, 42, 'legacy arbitrary null key', NULL, NULL);
	`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `CREATE INDEX apikey_user_id_purpose ON api_keys (user_id)`)
	require.NoError(t, err, "fixture must start with a non-canonical same-name index")

	migration := readMigration(t, "181_api_key_purpose.sql")
	_, err = tx.ExecContext(ctx, migration)
	require.NoError(t, err)

	var systemPurpose, legacyPurpose string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT purpose FROM api_keys WHERE id = 10`).Scan(&systemPurpose))
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT purpose FROM api_keys WHERE id = 11`).Scan(&legacyPurpose))
	require.Equal(t, "wallet_universal", systemPurpose)
	require.Equal(t, "standard", legacyPurpose)

	var indexDefinition string
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT pg_get_indexdef('apikey_user_id_purpose'::regclass)
	`).Scan(&indexDefinition))
	require.Contains(t, indexDefinition, "CREATE UNIQUE INDEX")
	require.Contains(t, indexDefinition, "(user_id, purpose)")
	require.Contains(t, indexDefinition, "deleted_at IS NULL")
	require.Contains(t, indexDefinition, "'wallet_universal'",
		"PostgreSQL may render explicit text casts in partial-index predicates")

	_, err = tx.ExecContext(ctx, migration)
	require.NoError(t, err, "migration 181 must be idempotent")
}

func TestAPIKeyPurposeMigration181BackfillsHistoricalFiniteWalletKey(t *testing.T) {
	tx := testTx(t)
	createAPIKeyPurposeMigrationTempTables(t, tx)
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_subscriptions
			(id, user_id, group_id, wallet_balance_usd, status, expires_at)
		VALUES (1, 42, NULL, 25, 'active', NOW() + INTERVAL '30 days');
		INSERT INTO api_keys (id, user_id, name, group_id, deleted_at)
		VALUES (10, 42, '钱包通用 key（自动路由）', NULL, NULL);
	`)
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, readMigration(t, "181_api_key_purpose.sql"))
	require.NoError(t, err)
	var purpose string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT purpose FROM api_keys WHERE id = 10`).Scan(&purpose))
	require.Equal(t, "wallet_universal", purpose)
}

func TestAPIKeyPurposeMigration181RejectsAmbiguousReservedName(t *testing.T) {
	tests := []struct {
		name      string
		setupSQL  string
		wantError string
	}{
		{
			name: "reserved name without wallet evidence",
			setupSQL: `
				INSERT INTO api_keys (id, user_id, name, group_id, deleted_at)
				VALUES (10, 42, '钱包通用 key（自动路由）', NULL, NULL)`,
			wantError: "reserved wallet key has no historical credits wallet evidence",
		},
		{
			name: "duplicate candidates for one user",
			setupSQL: `
				INSERT INTO user_subscriptions
					(id, user_id, group_id, wallet_balance_usd, status, expires_at)
				VALUES (1, 42, NULL, 25, 'active', '2099-12-31 23:59:59+00');
				INSERT INTO api_keys (id, user_id, name, group_id, deleted_at)
				VALUES
					(10, 42, '钱包通用 key（自动路由）', NULL, NULL),
					(11, 42, '钱包通用 key（自动路由）', NULL, NULL)`,
			wantError: "multiple live wallet universal key candidates",
		},
		{
			name: "reserved name bound to a group",
			setupSQL: `
				INSERT INTO user_subscriptions
					(id, user_id, group_id, wallet_balance_usd, status, expires_at)
				VALUES (1, 42, NULL, 25, 'active', '2099-12-31 23:59:59+00');
				INSERT INTO api_keys (id, user_id, name, group_id, deleted_at)
				VALUES (10, 42, '钱包通用 key（自动路由）', 3, NULL)`,
			wantError: "reserved wallet key has a non-null group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			createAPIKeyPurposeMigrationTempTables(t, tx)
			_, err := tx.ExecContext(context.Background(), tt.setupSQL)
			require.NoError(t, err)

			_, err = tx.ExecContext(context.Background(), readMigration(t, "181_api_key_purpose.sql"))
			require.ErrorContains(t, err, tt.wantError)
		})
	}
}

func TestAPIKeyPurposeMigration181EnforcesIdentityAtDatabaseBoundary(t *testing.T) {
	t.Run("purpose is immutable", func(t *testing.T) {
		tx := migratedAPIKeyPurposeTestTx(t)
		_, err := tx.ExecContext(context.Background(), `
			UPDATE api_keys SET purpose = 'wallet_universal' WHERE id = 20
		`)
		requirePostgresCode(t, err, pq.ErrorCode("23514"))
	})

	t.Run("reserved live name requires wallet purpose", func(t *testing.T) {
		tx := migratedAPIKeyPurposeTestTx(t)
		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO api_keys (id, user_id, name, purpose, group_id)
			VALUES (21, 77, $1, 'standard', NULL)
		`, walletUniversalKeyMigrationName)
		requirePostgresCode(t, err, pq.ErrorCode("23514"))
	})

	t.Run("one live wallet key per user", func(t *testing.T) {
		tx := migratedAPIKeyPurposeTestTx(t)
		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO api_keys (id, user_id, name, purpose, group_id)
			VALUES (21, 42, $1, 'wallet_universal', NULL)
		`, walletUniversalKeyMigrationName)
		requirePostgresCode(t, err, pq.ErrorCode("23505"))
	})
}

func migratedAPIKeyPurposeTestTx(t *testing.T) *sql.Tx {
	t.Helper()
	tx := testTx(t)
	createAPIKeyPurposeMigrationTempTables(t, tx)
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_subscriptions
			(id, user_id, group_id, wallet_balance_usd, status, expires_at)
		VALUES (1, 42, NULL, 25, 'active', '2099-12-31 23:59:59+00');
		INSERT INTO api_keys (id, user_id, name, group_id)
		VALUES
			(10, 42, '钱包通用 key（自动路由）', NULL),
			(20, 77, 'ordinary key', 3);
	`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, readMigration(t, "181_api_key_purpose.sql"))
	require.NoError(t, err)
	return tx
}

func createAPIKeyPurposeMigrationTempTables(t *testing.T, tx *sql.Tx) {
	t.Helper()
	_, err := tx.ExecContext(context.Background(), `
		CREATE TEMP TABLE user_subscriptions (
			id BIGINT PRIMARY KEY,
			user_id BIGINT NOT NULL,
			group_id BIGINT,
			wallet_balance_usd NUMERIC(20,10),
			status TEXT NOT NULL,
			deleted_at TIMESTAMPTZ,
			expires_at TIMESTAMPTZ NOT NULL
		);
		CREATE TEMP TABLE api_keys (
			id BIGINT PRIMARY KEY,
			user_id BIGINT NOT NULL,
			name VARCHAR(100) NOT NULL,
			group_id BIGINT,
			status VARCHAR(20) NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ
		);
	`)
	require.NoError(t, err)
}
