package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestPersistentFinancialIdempotencyMigrationBackfillsAndGuardsKnownScopes(t *testing.T) {
	content, err := dbmigrations.FS.ReadFile("182_persistent_financial_idempotency.sql")
	require.NoError(t, err)
	forwardContent, err := dbmigrations.FS.ReadFile("185_usage_billing_reconciliation_audit.sql")
	require.NoError(t, err)
	sql := string(content) + "\n" + string(forwardContent)

	for _, scope := range PersistentFinancialIdempotencyScopes() {
		require.Contains(t, sql, "'"+scope+"'", "migration must guard %s", scope)
	}

	for _, required := range []string{
		"LOCK TABLE idempotency_records IN SHARE ROW EXCLUSIVE MODE",
		"SET is_reclaimable = FALSE",
		"hfc_idempotency_scope_is_persistent",
		"hfc_guard_persistent_financial_idempotency",
		"OLD.scope",
		"OLD.idempotency_key_hash",
		"OLD.request_fingerprint",
		"NEW.is_reclaimable IS DISTINCT FROM FALSE",
	} {
		require.Contains(t, sql, required)
	}
}

func TestKnownFinancialScopeForcesKeyAndPersistenceEvenInObserveOnlyMode(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.ObserveOnly = true
	coordinator := NewIdempotencyCoordinator(repo, cfg)

	executeCalls := 0
	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:      "admin.users.balance.update",
		Method:     "POST",
		Route:      "/api/v1/admin/users/173/balance",
		ActorScope: "admin:99",
		Payload:    map[string]any{"user_id": 173, "balance": 50, "operation": "add"},
	}, func(context.Context) (any, error) {
		executeCalls++
		return nil, nil
	})
	require.Equal(t, infraerrors.Code(ErrIdempotencyKeyRequired), infraerrors.Code(err))
	require.Zero(t, executeCalls)

	result, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "admin.users.balance.update",
		Method:         "POST",
		Route:          "/api/v1/admin/users/173/balance",
		ActorScope:     "admin:99",
		IdempotencyKey: "balance-topup-173-20260712",
		Payload:        map[string]any{"user_id": 173, "balance": 50, "operation": "add"},
	}, func(context.Context) (any, error) {
		executeCalls++
		return map[string]any{"ok": true}, nil
	})
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Equal(t, 1, executeCalls)

	record := repo.data[repo.key("admin.users.balance.update", HashIdempotencyKey("balance-topup-173-20260712"))]
	require.NotNil(t, record)
	require.True(t, record.Persistent)
}
