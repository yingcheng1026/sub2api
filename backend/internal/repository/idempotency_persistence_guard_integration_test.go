//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPersistentFinancialIdempotencyDatabaseGuard(t *testing.T) {
	ctx := context.Background()
	scope := "admin.users.balance.update"
	fingerprint := uuid.NewString()
	keyHash := uuid.NewString()
	expiresAt := time.Now().Add(time.Hour)

	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO idempotency_records (
			scope, idempotency_key_hash, request_fingerprint, status, expires_at, is_reclaimable
		) VALUES ($1, $2, $3, 'processing', $4, TRUE)
	`, scope, uuid.NewString(), fingerprint, expiresAt)
	require.Error(t, err, "financial claims must fail closed when marked reclaimable")

	var recordID int64
	err = integrationDB.QueryRowContext(ctx, `
		INSERT INTO idempotency_records (
			scope, idempotency_key_hash, request_fingerprint, status, expires_at, is_reclaimable
		) VALUES ($1, $2, $3, 'processing', $4, FALSE)
		RETURNING id
	`, scope, keyHash, fingerprint, expiresAt).Scan(&recordID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM idempotency_records WHERE id = $1", recordID)
	})

	_, err = integrationDB.ExecContext(ctx,
		"UPDATE idempotency_records SET is_reclaimable = TRUE WHERE id = $1",
		recordID,
	)
	require.Error(t, err, "persistent claims must never be downgraded")

	_, err = integrationDB.ExecContext(ctx,
		"UPDATE idempotency_records SET idempotency_key_hash = $2 WHERE id = $1",
		recordID,
		uuid.NewString(),
	)
	require.Error(t, err, "executed operation identity must be immutable")
}
