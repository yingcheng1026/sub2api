//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

type integrationPlanFulfillmentSnapshot struct {
	SchemaVersion    int                `json:"schema_version"`
	PlanID           int64              `json:"plan_id"`
	PlanType         string             `json:"plan_type"`
	PlanName         string             `json:"plan_name"`
	ProductName      string             `json:"product_name"`
	PlanPrice        float64            `json:"plan_price"`
	GroupID          *int64             `json:"group_id"`
	SubscriptionDays int                `json:"subscription_days"`
	WalletQuotaUSD   *float64           `json:"wallet_quota_usd"`
	CoveredGroupIDs  []int64            `json:"covered_group_ids"`
	LockedRates      map[string]float64 `json:"locked_rates"`
	CaptureSource    string             `json:"capture_source"`
}

func insertCreditsPlanFulfillmentSnapshot(
	t *testing.T,
	ctx context.Context,
	client *dbent.Client,
	orderID, userID, planID int64,
	price float64,
	days int,
	quotaUSD float64,
) {
	t.Helper()
	insertIntegrationPlanFulfillmentSnapshot(t, ctx, client, orderID, userID, integrationPlanFulfillmentSnapshot{
		SchemaVersion:    1,
		PlanID:           planID,
		PlanType:         "credits",
		PlanName:         "integration credits plan",
		PlanPrice:        price,
		SubscriptionDays: days,
		WalletQuotaUSD:   &quotaUSD,
		CoveredGroupIDs:  []int64{},
		LockedRates:      map[string]float64{},
		CaptureSource:    "integration-test",
	})
}

func insertMonthlyPlanFulfillmentSnapshot(
	t *testing.T,
	ctx context.Context,
	client *dbent.Client,
	orderID, userID, planID, groupID int64,
	price float64,
	days int,
	rate float64,
) {
	t.Helper()
	insertIntegrationPlanFulfillmentSnapshot(t, ctx, client, orderID, userID, integrationPlanFulfillmentSnapshot{
		SchemaVersion:    1,
		PlanID:           planID,
		PlanType:         "subscription",
		PlanName:         "integration monthly plan",
		PlanPrice:        price,
		GroupID:          &groupID,
		SubscriptionDays: days,
		CoveredGroupIDs:  []int64{groupID},
		LockedRates:      map[string]float64{strconv.FormatInt(groupID, 10): rate},
		CaptureSource:    "integration-test",
	})
}

func insertIntegrationPlanFulfillmentSnapshot(
	t *testing.T,
	ctx context.Context,
	client *dbent.Client,
	orderID, userID int64,
	snapshot integrationPlanFulfillmentSnapshot,
) {
	t.Helper()
	payload, err := json.Marshal(snapshot)
	require.NoError(t, err)
	_, err = client.ExecContext(ctx, `
		INSERT INTO subscription_plan_fulfillment_snapshots (
			payment_order_id, user_id, source_plan_id, snapshot
		) VALUES ($1, $2, $3, $4::jsonb)
	`, orderID, userID, snapshot.PlanID, string(payload))
	require.NoError(t, err)
}
