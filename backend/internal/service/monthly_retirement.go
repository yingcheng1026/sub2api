package service

import (
	"context"
	"math"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var ErrMonthlyPlansRetired = infraerrors.BadRequest(
	"MONTHLY_PLANS_RETIRED",
	"monthly plans have been retired; use wallet credits instead",
)

func validateNewRedeemCodeType(codeType string) error {
	if codeType == RedeemTypeSubscription {
		return ErrMonthlyPlansRetired
	}
	return nil
}

// AssignAdminCredits is the public admin assignment boundary. New assignments
// may use a credits plan or a direct permanent wallet top-up; group/monthly
// assignments remain readable as history but cannot be created.
func (s *SubscriptionService) AssignAdminCredits(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, error) {
	if input == nil {
		return nil, ErrSubscriptionNilInput
	}
	if input.UserID <= 0 {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_USER_INVALID", "user_id must be > 0")
	}
	if input.GroupID != 0 {
		return nil, ErrMonthlyPlansRetired
	}
	if input.PlanID != nil && *input.PlanID <= 0 {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_PLAN_INVALID", "plan_id must be > 0")
	}
	if input.PlanID != nil && input.WalletInitialUSD != nil {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_MODE_INVALID", "choose either plan_id or wallet_initial_usd")
	}
	if input.PlanID == nil && input.WalletInitialUSD == nil {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_MODE_INVALID", "plan_id or wallet_initial_usd is required")
	}
	if input.WalletInitialUSD != nil {
		amount := *input.WalletInitialUSD
		if amount <= 0 || amount > 10_000_000 || math.IsNaN(amount) || math.IsInf(amount, 0) {
			return nil, infraerrors.BadRequest("ADMIN_ASSIGN_WALLET_INVALID", "wallet_initial_usd is outside the allowed range")
		}
		input = &AssignSubscriptionInput{
			UserID:           input.UserID,
			AssignedBy:       input.AssignedBy,
			Notes:            input.Notes,
			WalletInitialUSD: &amount,
			PlanType:         PlanTypeCredits,
		}
	}
	if input.PlanID != nil {
		if s.entClient == nil {
			return nil, infraerrors.BadRequest("SUBSCRIPTION_PLAN_UNAVAILABLE", "subscription plan lookup is unavailable")
		}
		plan, err := s.entClient.SubscriptionPlan.Get(ctx, *input.PlanID)
		if err != nil {
			return nil, err
		}
		if plan.PlanType != PlanTypeCredits {
			return nil, ErrMonthlyPlansRetired
		}
	}

	normalized, err := s.normalizePlanWalletAssignInput(ctx, input)
	if err != nil {
		return nil, err
	}
	if normalized.PlanType != PlanTypeCredits {
		return nil, ErrMonthlyPlansRetired
	}
	return s.AssignSubscription(ctx, normalized)
}
