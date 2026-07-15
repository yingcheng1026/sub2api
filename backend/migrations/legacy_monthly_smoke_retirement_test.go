package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration175aRetiresOnlyTheLegacyMonthlyBillingSmokeGrant(t *testing.T) {
	content, err := FS.ReadFile("175a_retire_legacy_monthly_billing_smoke.sql")
	require.NoError(t, err)

	sql := string(content)
	upper := strings.ToUpper(sql)
	require.Contains(t, sql, "monthly-billing-smoke-own-group")
	require.Contains(t, sql, "monthly-billing-smoke-vip-group22")
	require.Contains(t, sql, "notes ILIKE '%smoke%'")
	require.Contains(t, sql, "notes ILIKE '%canary%'")
	require.Contains(t, sql, "notes ILIKE '%monthly%'")
	require.Contains(t, upper, "RAISE EXCEPTION")
	require.Contains(t, upper, "UPDATE USER_SUBSCRIPTIONS")
	require.Contains(t, upper, "STATUS = 'EXPIRED'")
	require.Contains(t, upper, "DELETED_AT = NOW()")
	require.Contains(t, upper, "UPDATE API_KEYS")
	require.Contains(t, upper, "STATUS = 'REVOKED'")
	require.Contains(t, sql, "key = '__deleted__hfc_monthly_smoke_' || id::text")
	require.NotContains(t, sql, "UPDATE user_subscriptions\nSET deleted_at = NOW()\nWHERE status = 'active'")
}
