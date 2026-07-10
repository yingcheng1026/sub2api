//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsageBillingOutboxMigration_SchemaAndIndexes(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()

	var table sql.NullString
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT to_regclass('public.usage_billing_outbox')").Scan(&table))
	require.True(t, table.Valid, "expected usage_billing_outbox table to exist")

	requireColumn(t, tx, "usage_billing_outbox", "request_id", "character varying", 255, false)
	requireColumn(t, tx, "usage_billing_outbox", "api_key_id", "bigint", 0, false)
	requireColumn(t, tx, "usage_billing_outbox", "request_fingerprint", "character varying", 64, false)
	requireColumn(t, tx, "usage_billing_outbox", "envelope_version", "smallint", 0, false)
	requireColumn(t, tx, "usage_billing_outbox", "envelope", "jsonb", 0, false)
	requireColumn(t, tx, "usage_billing_outbox", "status", "character varying", 16, false)
	requireColumn(t, tx, "usage_billing_outbox", "attempt_count", "smallint", 0, false)
	requireColumn(t, tx, "usage_billing_outbox", "max_attempts", "smallint", 0, false)
	requireColumn(t, tx, "usage_billing_outbox", "available_at", "timestamp with time zone", 0, false)
	requireColumn(t, tx, "usage_billing_outbox", "locked_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "usage_billing_outbox", "locked_by", "character varying", 128, true)
	requireColumn(t, tx, "usage_billing_outbox", "lease_token", "character varying", 64, true)
	requireColumn(t, tx, "usage_billing_outbox", "completed_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "usage_billing_outbox", "dead_lettered_at", "timestamp with time zone", 0, true)

	requireIndex(t, tx, "usage_billing_outbox", "usage_billing_outbox_request_api_key_key")
	requireIndex(t, tx, "usage_billing_outbox", "idx_usage_billing_outbox_claim")
	requireIndex(t, tx, "usage_billing_outbox", "idx_usage_billing_outbox_dead_letter")

	var businessForeignKeys int
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM information_schema.table_constraints
		WHERE table_schema = 'public'
		  AND table_name = 'usage_billing_outbox'
		  AND constraint_type = 'FOREIGN KEY'
	`).Scan(&businessForeignKeys))
	require.Zero(t, businessForeignKeys, "durable accounting events must not have business foreign keys")

	var constraintCount int
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM pg_constraint
		WHERE conrelid = 'usage_billing_outbox'::regclass
		  AND conname IN (
			'usage_billing_outbox_envelope_size_check',
			'usage_billing_outbox_outer_identity_check'
		  )
	`).Scan(&constraintCount))
	require.Equal(t, 2, constraintCount)

	_, err := tx.ExecContext(ctx, `
		INSERT INTO usage_billing_outbox (
			request_id, api_key_id, request_fingerprint, envelope_version, envelope
		) VALUES (
			'outer-request', 7,
			'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 1,
			'{"request_id":"different-request","api_key_id":7,"request_fingerprint":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","version":1}'::jsonb
		)
	`)
	require.Error(t, err, "outer identity must never diverge from the immutable envelope")
}
