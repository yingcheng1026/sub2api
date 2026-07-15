package service

import (
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingReconciliationMigration185IsAuditableAndFailClosed(t *testing.T) {
	raw, err := dbmigrations.FS.ReadFile("185_usage_billing_reconciliation_audit.sql")
	require.NoError(t, err)
	sql := string(raw)

	for _, required := range []string{
		"LOCK TABLE idempotency_records IN SHARE ROW EXCLUSIVE MODE",
		"'admin.usage.billing_reconciliation.resolve'",
		"CREATE TABLE IF NOT EXISTS usage_billing_reconciliation_audits",
		"FOREIGN KEY (request_id, api_key_id)",
		"action = 'settle_delivered'",
		"attempt_id IS NOT NULL",
		"to_state = 'outbox_pending'",
		"evidence_ref ~",
		"actual_cost_usd > 0",
		"actual_cost_usd NOT IN ('NaN'::numeric,'Infinity'::numeric,'-Infinity'::numeric)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_billing_reconciliation_terminal_once",
		"BEFORE UPDATE OR DELETE ON usage_billing_reconciliation_audits",
		"RAISE EXCEPTION 'usage billing reconciliation audits are append-only'",
	} {
		require.Contains(t, sql, required)
	}
}
