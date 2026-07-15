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
		Notes:            "manual topup",
		WalletInitialUSD: &amount,
	}, 1)

	require.Equal(t, int64(173), input.UserID)
	require.Equal(t, int64(1), input.AssignedBy)
	require.Equal(t, service.PlanTypeCredits, input.PlanType)
	require.Same(t, &amount, input.WalletInitialUSD)
	require.Zero(t, input.ValidityDays)
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

func TestAdminAssignPayloadFromInputUsesCanonicalModeFields(t *testing.T) {
	planID := int64(987654)
	walletDelta := 50.0

	tests := []struct {
		name  string
		input *service.AssignSubscriptionInput
		want  adminAssignIdempotencyPayload
	}{
		{
			name:  "plan",
			input: &service.AssignSubscriptionInput{UserID: 1, PlanID: &planID, Notes: "plan"},
			want:  adminAssignIdempotencyPayload{Mode: "plan", UserID: 1, PlanID: planID, Notes: "plan"},
		},
		{
			name:  "wallet",
			input: &service.AssignSubscriptionInput{UserID: 2, WalletInitialUSD: &walletDelta, PlanType: service.PlanTypeCredits, Notes: "wallet"},
			want:  adminAssignIdempotencyPayload{Mode: "wallet", UserID: 2, WalletInitialUSD: walletDelta, Notes: "wallet"},
		},
		{
			name:  "group",
			input: &service.AssignSubscriptionInput{UserID: 3, GroupID: 22, ValidityDays: 30, Notes: "group"},
			want:  adminAssignIdempotencyPayload{Mode: "group", UserID: 3, GroupID: 22, ValidityDays: 30, Notes: "group"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, adminAssignPayloadFromInput(tt.input))
		})
	}
}
