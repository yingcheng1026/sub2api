package service

import (
	"context"
	"math"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAdminSubscriptionAssignInputCanonicalModes(t *testing.T) {
	planID := int64(987654)
	walletDelta := 50.0

	tests := []struct {
		name  string
		input *AssignSubscriptionInput
		check func(*testing.T, *AssignSubscriptionInput)
	}{
		{
			name:  "plan",
			input: &AssignSubscriptionInput{UserID: 1, PlanID: &planID, AssignedBy: 9, Notes: "plan"},
			check: func(t *testing.T, got *AssignSubscriptionInput) {
				require.NotNil(t, got.PlanID)
				require.Equal(t, planID, *got.PlanID)
				require.NotSame(t, &planID, got.PlanID)
				require.Nil(t, got.WalletInitialUSD)
				require.Zero(t, got.GroupID)
				require.Empty(t, got.PlanType)
			},
		},
		{
			name:  "manual wallet",
			input: &AssignSubscriptionInput{UserID: 1, WalletInitialUSD: &walletDelta, AssignedBy: 9, Notes: "wallet"},
			check: func(t *testing.T, got *AssignSubscriptionInput) {
				require.NotNil(t, got.WalletInitialUSD)
				require.Equal(t, walletDelta, *got.WalletInitialUSD)
				require.NotSame(t, &walletDelta, got.WalletInitialUSD)
				require.Nil(t, got.PlanID)
				require.Zero(t, got.GroupID)
				require.Equal(t, PlanTypeCredits, got.PlanType)
			},
		},
		{
			name:  "group",
			input: &AssignSubscriptionInput{UserID: 1, GroupID: 22, ValidityDays: 30, AssignedBy: 9, Notes: "group"},
			check: func(t *testing.T, got *AssignSubscriptionInput) {
				require.Equal(t, int64(22), got.GroupID)
				require.Equal(t, 30, got.ValidityDays)
				require.Nil(t, got.PlanID)
				require.Nil(t, got.WalletInitialUSD)
				require.Empty(t, got.PlanType)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeAdminSubscriptionAssignInput(tt.input)
			require.NoError(t, err)
			require.NotSame(t, tt.input, got)
			require.Equal(t, tt.input.UserID, got.UserID)
			require.Equal(t, tt.input.AssignedBy, got.AssignedBy)
			require.Equal(t, tt.input.Notes, got.Notes)
			tt.check(t, got)
		})
	}
}

func TestNormalizeAdminSubscriptionAssignInputRejectsInvalidOrAmbiguousInput(t *testing.T) {
	planID := int64(11)
	zeroPlanID := int64(0)
	walletDelta := 50.0
	zeroWallet := 0.0
	overWallet := float64(MaxAdminWalletInitialUSD + 1)
	nanWallet := math.NaN()

	tests := []struct {
		name       string
		input      *AssignSubscriptionInput
		wantReason string
	}{
		{name: "nil", input: nil, wantReason: infraerrors.Reason(ErrSubscriptionNilInput)},
		{name: "invalid user", input: &AssignSubscriptionInput{UserID: 0, PlanID: &planID}, wantReason: "ADMIN_ASSIGN_USER_INVALID"},
		{name: "negative group", input: &AssignSubscriptionInput{UserID: 1, GroupID: -1}, wantReason: "ADMIN_ASSIGN_GROUP_INVALID"},
		{name: "zero plan id", input: &AssignSubscriptionInput{UserID: 1, PlanID: &zeroPlanID}, wantReason: "ADMIN_ASSIGN_PLAN_INVALID"},
		{name: "negative validity", input: &AssignSubscriptionInput{UserID: 1, GroupID: 22, ValidityDays: -1}, wantReason: "ADMIN_ASSIGN_VALIDITY_INVALID"},
		{name: "excess validity", input: &AssignSubscriptionInput{UserID: 1, GroupID: 22, ValidityDays: MaxValidityDays + 1}, wantReason: "ADMIN_ASSIGN_VALIDITY_INVALID"},
		{name: "no mode", input: &AssignSubscriptionInput{UserID: 1}, wantReason: "ADMIN_ASSIGN_MODE_INVALID"},
		{name: "plan and wallet", input: &AssignSubscriptionInput{UserID: 1, PlanID: &planID, WalletInitialUSD: &walletDelta}, wantReason: "ADMIN_ASSIGN_MODE_INVALID"},
		{name: "plan and group", input: &AssignSubscriptionInput{UserID: 1, PlanID: &planID, GroupID: 22}, wantReason: "ADMIN_ASSIGN_MODE_INVALID"},
		{name: "wallet and group", input: &AssignSubscriptionInput{UserID: 1, GroupID: 22, WalletInitialUSD: &walletDelta}, wantReason: "ADMIN_ASSIGN_MODE_INVALID"},
		{name: "plan validity", input: &AssignSubscriptionInput{UserID: 1, PlanID: &planID, ValidityDays: 30}, wantReason: "ADMIN_ASSIGN_VALIDITY_INVALID"},
		{name: "wallet validity", input: &AssignSubscriptionInput{UserID: 1, WalletInitialUSD: &walletDelta, ValidityDays: 30}, wantReason: "ADMIN_ASSIGN_VALIDITY_INVALID"},
		{name: "zero wallet", input: &AssignSubscriptionInput{UserID: 1, WalletInitialUSD: &zeroWallet}, wantReason: "ADMIN_ASSIGN_WALLET_INVALID"},
		{name: "excess wallet", input: &AssignSubscriptionInput{UserID: 1, WalletInitialUSD: &overWallet}, wantReason: "ADMIN_ASSIGN_WALLET_INVALID"},
		{name: "nan wallet", input: &AssignSubscriptionInput{UserID: 1, WalletInitialUSD: &nanWallet}, wantReason: "ADMIN_ASSIGN_WALLET_INVALID"},
		{name: "wallet wrong type", input: &AssignSubscriptionInput{UserID: 1, WalletInitialUSD: &walletDelta, PlanType: PlanTypeSubscription}, wantReason: "ADMIN_ASSIGN_PLAN_TYPE_INVALID"},
		{name: "group with plan type", input: &AssignSubscriptionInput{UserID: 1, GroupID: 22, PlanType: PlanTypeCredits}, wantReason: "ADMIN_ASSIGN_PLAN_TYPE_INVALID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeAdminSubscriptionAssignInput(tt.input)
			require.Error(t, err)
			require.Equal(t, tt.wantReason, infraerrors.Reason(err))
		})
	}
}

func TestAssignAdminSubscriptionValidatesBeforeRepositoryAccess(t *testing.T) {
	planID := int64(11)
	svc := &SubscriptionService{}
	_, err := svc.AssignAdminSubscription(context.Background(), &AssignSubscriptionInput{
		UserID:       1,
		PlanID:       &planID,
		ValidityDays: 30,
	})
	require.Error(t, err)
	require.Equal(t, "ADMIN_ASSIGN_VALIDITY_INVALID", infraerrors.Reason(err))
}

func TestAssignAdminSubscriptionRejectsRetiredMonthlyPlan(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	group := createMonthlyPlanGroup(t, ctx, client, "retired-admin-monthly", StatusActive)
	plan, err := client.SubscriptionPlan.Create().
		SetName("retired admin monthly").
		SetGroupID(group.ID).
		SetPlanType(PlanTypeSubscription).
		SetPrice(20).
		SetValidityDays(30).
		SetForSale(false).
		Save(ctx)
	require.NoError(t, err)

	svc := &SubscriptionService{entClient: client}
	_, err = svc.AssignAdminSubscription(ctx, &AssignSubscriptionInput{
		UserID:     1,
		PlanID:     &plan.ID,
		AssignedBy: 9,
		Notes:      "must not revive monthly",
	})
	require.Error(t, err)
	require.Equal(t, "MONTHLY_PLANS_RETIRED", infraerrors.Reason(err))
}

func TestAffiliateRebateOverrideForAdminAssignUsesResolvedPlanTypeNotDatabaseID(t *testing.T) {
	dynamicPlanID := int64(987654)
	formerlyCreditsID := int64(11)

	tests := []struct {
		name     string
		planID   *int64
		planType string
		want     float64
	}{
		{name: "dynamic credits plan", planID: &dynamicPlanID, planType: PlanTypeCredits, want: AffiliateRebateCreditsCardRate},
		{name: "reused legacy id is monthly", planID: &formerlyCreditsID, planType: PlanTypeSubscription, want: AffiliateRebateSubscriptionRate},
		{name: "manual wallet remains zero", planID: nil, planType: PlanTypeCredits, want: AffiliateRebateSubscriptionRate},
		{name: "missing resolved type fails closed", planID: &formerlyCreditsID, planType: "", want: AffiliateRebateSubscriptionRate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rate := AffiliateRebateOverrideForAdminAssign(tt.planID, tt.planType)
			require.NotNil(t, rate)
			require.Equal(t, tt.want, *rate)
		})
	}
}
