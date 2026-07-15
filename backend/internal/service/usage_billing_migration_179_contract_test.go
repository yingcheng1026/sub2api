package service

import (
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingAdmissionMigrationRequiresImmutableQuoteAndAttemptOwnership(t *testing.T) {
	content, err := dbmigrations.FS.ReadFile("179_usage_billing_admissions.sql")
	require.NoError(t, err)
	sql := string(content)

	for _, required := range []string{
		"request_payload_hash VARCHAR(64) NOT NULL",
		"request_payload_hash ~ '^[0-9a-f]{64}$'",
		"billing_model IS NOT NULL",
		"pricing_source IS NOT NULL",
		"pricing_revision IS NOT NULL",
		"pricing_hash IS NOT NULL",
		"rate_multiplier > 0",
		"worst_case_cost_usd > 0",
		"alternate_billing_model VARCHAR(128)",
		"usage_billing_admissions_alternate_quote_check",
		"alternate_rate_multiplier IS NOT NULL AND alternate_rate_multiplier > 0",
		"alternate_rate_multiplier NOT IN ('NaN'::numeric,'Infinity'::numeric,'-Infinity'::numeric)));",
		"subscription_id IS NOT NULL",
		"REFERENCES usage_billing_admissions (request_id, api_key_id)",
		"hfc_guard_usage_billing_admission_immutable",
		"TG_OP = 'DELETE'",
		"OLD.binding_fingerprint",
		"OLD.request_payload_hash",
		"OLD.worst_case_cost_usd",
		"OLD.alternate_pricing_hash",
		"OLD.state = 'prepared' AND NEW.state IN ('dispatched','orphaned','abandoned')",
		"OLD.state = 'dispatched' AND NEW.state IN ('outbox_pending','orphaned','reconcile','abandoned')",
		"hfc_guard_usage_billing_admission_attempt_immutable",
		"usage billing admission attempts are append-only",
		"OLD.attempt_fingerprint",
		"idx_usage_billing_admissions_open_wallet",
		"idx_usage_billing_admissions_prepared_reconcile",
	} {
		require.Contains(t, sql, required)
	}
	require.NotContains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_billing_admissions_one_open_wallet")
	require.NotContains(t, sql, "OLD.state = 'prepared' AND NEW.state IN ('dispatched','outbox_pending'")
	require.NotContains(t, sql, "OLD.state = 'prepared' AND NEW.state IN ('dispatched','failed','finalized')")
}
