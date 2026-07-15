package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration180aRemediatesLegacyReservedWalletKeyShapes(t *testing.T) {
	content, err := FS.ReadFile("180a_remediate_legacy_reserved_wallet_keys.sql")
	require.NoError(t, err)

	sql := string(content)
	upper := strings.ToUpper(sql)
	require.Contains(t, sql, "name = '钱包通用 key（自动路由）'")
	require.Contains(t, sql, "group_id IS NOT NULL")
	require.Contains(t, sql, "'固定分组 Key（历史钱包迁移 #' || id::text || '）'")
	require.Contains(t, sql, "group_id IS NULL")
	require.Contains(t, sql, "wallet_balance_usd IS NOT NULL")
	require.Contains(t, sql, "key = '__deleted__hfc_orphan_wallet_key_' || ak.id::text")
	require.Contains(t, upper, "STATUS = 'REVOKED'")
	require.Contains(t, upper, "DELETED_AT = NOW()")
	require.Contains(t, upper, "HAVING COUNT(*) > 1")
	require.Contains(t, upper, "RAISE EXCEPTION")
	require.NotContains(t, sql, "UPDATE api_keys\nSET status = 'revoked'\nWHERE group_id IS NULL;")
}
