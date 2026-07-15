package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration191RejectsHistoryDuplicatesBeforeAddingUsageSettlementUniqueness(t *testing.T) {
	content, err := FS.ReadFile("191_wallet_usage_replay_idempotency.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "LOCK TABLE subscription_wallet_ledger IN SHARE ROW EXCLUSIVE MODE")
	require.Contains(t, sql, "HAVING COUNT(*) > 1")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_wallet_ledger_one_usage_settlement")
	require.Contains(t, sql, "WHERE usage_log_id IS NOT NULL")
	require.Contains(t, sql, "AND reason = 'usage'")
}
