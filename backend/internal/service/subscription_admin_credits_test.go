package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAssignAdminCreditsRejectsGroupAndMonthlyPlan(t *testing.T) {
	ctx := context.Background()
	svc := &SubscriptionService{}

	_, err := svc.AssignAdminCredits(ctx, &AssignSubscriptionInput{UserID: 1, GroupID: 22, ValidityDays: 30})
	require.Error(t, err)
	require.Equal(t, "MONTHLY_PLANS_RETIRED", infraerrors.Reason(err))

	client := newPaymentConfigServiceTestClient(t)
	group := createPlanCoverageGroup(t, ctx, client, "retired-admin-monthly")
	plan, err := client.SubscriptionPlan.Create().
		SetName("retired admin monthly").
		SetGroupID(group.ID).
		SetPlanType(PlanTypeSubscription).
		SetPrice(20).
		SetValidityDays(30).
		SetForSale(false).
		Save(ctx)
	require.NoError(t, err)

	svc = &SubscriptionService{entClient: client}
	_, err = svc.AssignAdminCredits(ctx, &AssignSubscriptionInput{UserID: 1, PlanID: &plan.ID})
	require.Error(t, err)
	require.Equal(t, "MONTHLY_PLANS_RETIRED", infraerrors.Reason(err))
}
