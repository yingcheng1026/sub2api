package service

import (
	"strings"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestPlanSnapshotMigrationHasAtomicCutoverAndFailClosedImmutability(t *testing.T) {
	content, err := dbmigrations.FS.ReadFile("183_subscription_plan_fulfillment_snapshots.sql")
	require.NoError(t, err)
	sql := string(content)

	for _, required := range []string{
		"LOCK TABLE payment_orders IN SHARE ROW EXCLUSIVE MODE",
		"subscription_plan_snapshot_cutovers",
		"max_legacy_payment_order_id",
		"hfc_plan_fulfillment_snapshot_shape_valid",
		"TG_OP = 'DELETE'",
		"source_snapshot.source_plan_id IS DISTINCT FROM source_order.plan_id",
		"NEW.user_subscription_id IS DISTINCT FROM OLD.user_subscription_id",
		"legacy unsnapshotted orders are still fulfillable",
		"snapshot.payment_order_id = po.id",
		"snapshot.payment_order_id = NEW.id",
		"Do not consult a mutable (or subsequently deleted) plan",
	} {
		require.Contains(t, sql, required)
	}

	require.NotContains(t, sql,
		"AND (snapshot->>'schema_version')::integer = 1",
		"raw JSON casts inside a CHECK are not a fail-closed shape validator",
	)
	require.GreaterOrEqual(t, strings.Count(sql, "IS DISTINCT FROM"), 10,
		"nullable immutable facts must not be compared with SQL three-valued equality")
}
