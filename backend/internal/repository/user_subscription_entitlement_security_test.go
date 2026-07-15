package repository

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestLegacySubscriptionAnchorCoverageUsesPlanIntersection(t *testing.T) {
	tests := []struct {
		name       string
		planCovers []bool
		want       bool
	}{
		{name: "no plan for anchor fails closed", planCovers: nil, want: false},
		{name: "single plan covers target", planCovers: []bool{true}, want: true},
		{name: "single plan does not cover target", planCovers: []bool{false}, want: false},
		{name: "shared anchor inconsistent coverage", planCovers: []bool{false, true}, want: false},
		{name: "shared anchor unanimous coverage", planCovers: []bool{true, true}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, client, anchorID, targetID := newLegacyEntitlementFixture(t)
			for index, covers := range tt.planCovers {
				plan := createLegacyEntitlementPlan(t, ctx, client, anchorID, index)
				if covers {
					bindLegacyEntitlementPlanGroup(t, ctx, client, plan.ID, targetID)
				}
			}

			covered, err := legacySubscriptionAnchorCoversGroupUnambiguously(ctx, client, anchorID, targetID)
			require.NoError(t, err)
			require.Equal(t, tt.want, covered)
		})
	}
}

func TestLegacySubscriptionMNAnchorCoverageUsesPlanIntersection(t *testing.T) {
	ctx, client, anchorID, targetID := newLegacyEntitlementFixture(t)
	for index, covers := range []bool{true, false} {
		plan, err := client.SubscriptionPlan.Create().
			SetName("legacy-mn-plan-" + string(rune('a'+index))).
			SetPlanType(service.PlanTypeSubscription).
			SetPrice(10).
			Save(ctx)
		require.NoError(t, err)
		bindLegacyEntitlementPlanGroup(t, ctx, client, plan.ID, anchorID)
		if covers {
			bindLegacyEntitlementPlanGroup(t, ctx, client, plan.ID, targetID)
		}
	}

	covered, err := legacySubscriptionAnchorCoversGroupUnambiguously(ctx, client, anchorID, targetID)
	require.NoError(t, err)
	require.False(t, covered)
}

func newLegacyEntitlementFixture(t *testing.T) (context.Context, *dbent.Client, int64, int64) {
	t.Helper()
	ctx := context.Background()
	client := newSecuritySecretTestClient(t)
	anchor, err := client.Group.Create().
		SetName("legacy-entitlement-anchor").
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeSubscription).
		Save(ctx)
	require.NoError(t, err)
	target, err := client.Group.Create().
		SetName("legacy-entitlement-target").
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeStandard).
		Save(ctx)
	require.NoError(t, err)
	return ctx, client, anchor.ID, target.ID
}

func createLegacyEntitlementPlan(t *testing.T, ctx context.Context, client *dbent.Client, anchorID int64, index int) *dbent.SubscriptionPlan {
	t.Helper()
	plan, err := client.SubscriptionPlan.Create().
		SetGroupID(anchorID).
		SetName("legacy-entitlement-plan-" + string(rune('a'+index))).
		SetPlanType(service.PlanTypeSubscription).
		SetPrice(10).
		Save(ctx)
	require.NoError(t, err)
	return plan
}

func bindLegacyEntitlementPlanGroup(t *testing.T, ctx context.Context, client *dbent.Client, planID, groupID int64) {
	t.Helper()
	_, err := client.SubscriptionPlanGroup.Create().
		SetPlanID(planID).
		SetGroupID(groupID).
		Save(ctx)
	require.NoError(t, err)
}
