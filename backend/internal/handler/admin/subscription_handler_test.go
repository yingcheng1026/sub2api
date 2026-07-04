package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAssignSubscriptionInputFromRequestMapsManualWalletToCredits(t *testing.T) {
	amount := 50.0
	input := assignSubscriptionInputFromRequest(AssignSubscriptionRequest{
		UserID:           173,
		ValidityDays:     30,
		Notes:            "manual topup",
		WalletInitialUSD: &amount,
	}, 1)

	require.Equal(t, int64(173), input.UserID)
	require.Equal(t, int64(1), input.AssignedBy)
	require.Equal(t, service.PlanTypeCredits, input.PlanType)
	require.Same(t, &amount, input.WalletInitialUSD)
	require.Equal(t, 30, input.ValidityDays)
}

func TestAssignSubscriptionInputFromRequestKeepsPlanModeForPlanIDOnly(t *testing.T) {
	planID := int64(8)
	input := assignSubscriptionInputFromRequest(AssignSubscriptionRequest{
		UserID: 88,
		PlanID: &planID,
	}, 1)

	require.Equal(t, int64(88), input.UserID)
	require.Same(t, &planID, input.PlanID)
	require.Empty(t, input.PlanType)
	require.Nil(t, input.WalletInitialUSD)
}
