package service

import (
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingMigration187BlocksUnknownDeliveryWalletRelease(t *testing.T) {
	raw, err := dbmigrations.FS.ReadFile("187_usage_billing_unknown_delivery_release_guard.sql")
	require.NoError(t, err)
	sql := string(raw)

	for _, required := range []string{
		"OLD.wallet_subscription_id IS NOT NULL",
		"OLD.state IN ('orphaned', 'reconcile')",
		"NEW.state = 'abandoned'",
		"OLD.dispatched_at IS NOT NULL",
		"FROM usage_billing_admission_attempts",
		"attempt.dispatched_at IS NOT NULL",
		"attempt.state IN ('dispatched', 'finalized')",
		"CONSTRAINT = 'hfc_wallet_unknown_delivery_release'",
		"BEFORE UPDATE OF state ON usage_billing_admissions",
	} {
		require.Contains(t, sql, required)
	}
}
