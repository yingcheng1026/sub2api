package service

import (
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingAdmissionMigration184HardensExistingInstallations(t *testing.T) {
	raw, err := dbmigrations.FS.ReadFile("184_usage_billing_admission_lifecycle_hardening.sql")
	require.NoError(t, err)
	sql := string(raw)

	for _, required := range []string{
		"CREATE OR REPLACE FUNCTION hfc_guard_usage_billing_admission_immutable()",
		"OLD.state = 'prepared' AND NEW.state IN ('dispatched','orphaned','abandoned')",
		"settled wallet admission requires wallet_consumed_at",
		"abandoned wallet admission requires wallet_released_at",
		"OLD.state = 'prepared' AND NEW.state IN ('dispatched','failed')",
		"finalized attempt timestamp mismatch",
		"idx_usage_billing_admissions_prepared_reconcile",
	} {
		require.Contains(t, sql, required)
	}
	require.NotContains(t, sql, "OLD.state = 'prepared' AND NEW.state IN ('dispatched','outbox_pending'")
	require.NotContains(t, sql, "OLD.state = 'prepared' AND NEW.state IN ('dispatched','failed','finalized')")
}
