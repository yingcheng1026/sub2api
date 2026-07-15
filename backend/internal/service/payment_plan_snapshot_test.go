//go:build unit

package service

import (
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestPlanFulfillmentSnapshotValidate(t *testing.T) {
	t.Parallel()

	groupID := int64(22)
	quota := 50.0
	tests := []struct {
		name     string
		snapshot *planFulfillmentSnapshot
		wantErr  string
	}{
		{
			name: "monthly snapshot",
			snapshot: &planFulfillmentSnapshot{
				SchemaVersion:    planFulfillmentSnapshotSchemaVersion,
				PlanID:           11,
				PlanType:         PlanTypeSubscription,
				PlanPrice:        20,
				GroupID:          &groupID,
				SubscriptionDays: 30,
				CoveredGroupIDs:  []int64{3, 22},
				LockedRates:      map[string]float64{"3": 1, "22": 8.5},
			},
		},
		{
			name: "credits snapshot",
			snapshot: &planFulfillmentSnapshot{
				SchemaVersion:    planFulfillmentSnapshotSchemaVersion,
				PlanID:           12,
				PlanType:         PlanTypeCredits,
				PlanPrice:        20,
				SubscriptionDays: 36500,
				WalletQuotaUSD:   &quota,
				CoveredGroupIDs:  []int64{3},
				LockedRates:      map[string]float64{"3": 1},
			},
		},
		{
			name: "monthly missing group",
			snapshot: &planFulfillmentSnapshot{
				SchemaVersion:    planFulfillmentSnapshotSchemaVersion,
				PlanID:           13,
				PlanType:         PlanTypeSubscription,
				PlanPrice:        20,
				SubscriptionDays: 30,
			},
			wantErr: "monthly snapshot requires group_id",
		},
		{
			name: "credits missing quota",
			snapshot: &planFulfillmentSnapshot{
				SchemaVersion:    planFulfillmentSnapshotSchemaVersion,
				PlanID:           14,
				PlanType:         PlanTypeCredits,
				PlanPrice:        20,
				SubscriptionDays: 36500,
			},
			wantErr: "credits snapshot requires wallet_quota_usd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.snapshot.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestPlanFulfillmentSnapshotMatchesOrder(t *testing.T) {
	t.Parallel()

	groupID := int64(22)
	days := 30
	snapshot := &planFulfillmentSnapshot{
		SchemaVersion:    planFulfillmentSnapshotSchemaVersion,
		PlanID:           11,
		PlanType:         PlanTypeSubscription,
		PlanPrice:        20,
		GroupID:          &groupID,
		SubscriptionDays: days,
		CoveredGroupIDs:  []int64{22},
		LockedRates:      map[string]float64{"22": 8.5},
	}
	order := &dbent.PaymentOrder{
		ID:                  91,
		OrderType:           payment.OrderTypeSubscription,
		Amount:              20,
		PlanID:              paymentPlanInt64Ptr(11),
		SubscriptionGroupID: &groupID,
		SubscriptionDays:    &days,
	}

	require.NoError(t, snapshot.ValidateForOrder(order))
	wrongDays := 7
	order.SubscriptionDays = &wrongDays
	require.ErrorContains(t, snapshot.ValidateForOrder(order), "subscription_days")
}

func paymentPlanInt64Ptr(value int64) *int64 { return &value }
