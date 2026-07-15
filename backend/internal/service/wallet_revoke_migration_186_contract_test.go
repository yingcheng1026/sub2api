package service

import (
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestWalletRevokeMigration186BlocksOpenUsageBillingHold(t *testing.T) {
	raw, err := dbmigrations.FS.ReadFile("186_wallet_open_admission_revoke_guard.sql")
	require.NoError(t, err)
	sql := string(raw)

	for _, required := range []string{
		"BEFORE UPDATE OF deleted_at ON user_subscriptions",
		"OLD.wallet_balance_usd IS NOT NULL",
		"NEW.deleted_at IS NOT NULL",
		"FROM usage_billing_admissions",
		"wallet_subscription_id = OLD.id",
		"wallet_consumed_at IS NULL",
		"wallet_released_at IS NULL",
		"CONSTRAINT = 'hfc_wallet_open_admission_revoke'",
	} {
		require.Contains(t, sql, required)
	}
}
