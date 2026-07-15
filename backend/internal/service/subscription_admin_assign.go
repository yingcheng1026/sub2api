package service

import (
	"context"
	"math"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const MaxAdminWalletInitialUSD = 10_000_000

// NormalizeAdminSubscriptionAssignInput validates the public admin assignment
// contract and returns an immutable canonical copy. Plan, group and manual
// wallet modes are strict alternatives; internal payment/redeem callers keep
// using AssignSubscription because they may legitimately carry plan metadata
// together with an already-resolved wallet delta.
func NormalizeAdminSubscriptionAssignInput(input *AssignSubscriptionInput) (*AssignSubscriptionInput, error) {
	if input == nil {
		return nil, ErrSubscriptionNilInput
	}
	if input.UserID <= 0 {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_USER_INVALID", "user_id must be > 0")
	}
	if input.GroupID < 0 {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_GROUP_INVALID", "group_id must be > 0 when provided")
	}
	if input.ValidityDays < 0 || input.ValidityDays > MaxValidityDays {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_VALIDITY_INVALID", "validity_days is outside the allowed range")
	}

	hasPlan := input.PlanID != nil
	if hasPlan && *input.PlanID <= 0 {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_PLAN_INVALID", "plan_id must be > 0")
	}
	hasWallet := input.WalletInitialUSD != nil
	if hasWallet && !isValidAdminWalletDelta(*input.WalletInitialUSD) {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_WALLET_INVALID", "wallet_initial_usd is outside the allowed range")
	}
	hasGroup := input.GroupID > 0

	modeCount := 0
	for _, selected := range []bool{hasPlan, hasGroup, hasWallet} {
		if selected {
			modeCount++
		}
	}
	if modeCount != 1 {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_MODE_INVALID", "exactly one of plan_id, group_id, or wallet_initial_usd is required")
	}

	if (hasPlan || hasWallet) && input.ValidityDays != 0 {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_VALIDITY_INVALID", "validity_days is only allowed for group assignments")
	}
	if hasPlan && input.PlanType != "" {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_PLAN_TYPE_INVALID", "plan type is resolved from plan_id")
	}
	if hasGroup && input.PlanType != "" {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_PLAN_TYPE_INVALID", "plan type is not allowed for group assignments")
	}
	if hasWallet && input.PlanType != "" && input.PlanType != PlanTypeCredits {
		return nil, infraerrors.BadRequest("ADMIN_ASSIGN_PLAN_TYPE_INVALID", "manual wallet assignments must use credits plan type")
	}

	normalized := &AssignSubscriptionInput{
		UserID:       input.UserID,
		GroupID:      input.GroupID,
		ValidityDays: input.ValidityDays,
		AssignedBy:   input.AssignedBy,
		Notes:        input.Notes,
	}
	if hasPlan {
		planID := *input.PlanID
		normalized.PlanID = &planID
	}
	if hasWallet {
		walletDelta := *input.WalletInitialUSD
		normalized.WalletInitialUSD = &walletDelta
		normalized.PlanType = PlanTypeCredits
	}
	return normalized, nil
}

func isValidAdminWalletDelta(value float64) bool {
	return value > 0 && value <= MaxAdminWalletInitialUSD && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// AssignAdminSubscription is the double-validation boundary for the public
// admin endpoint. Callers must not use AssignSubscription directly for raw
// admin request data.
func (s *SubscriptionService) AssignAdminSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, error) {
	normalized, err := NormalizeAdminSubscriptionAssignInput(input)
	if err != nil {
		return nil, err
	}
	normalized, err = s.normalizePlanWalletAssignInput(ctx, normalized)
	if err != nil {
		return nil, err
	}
	if normalized.PlanType == PlanTypeSubscription {
		return nil, infraerrors.BadRequest("MONTHLY_PLANS_RETIRED", "monthly plans can no longer be assigned; add wallet credits instead")
	}
	return s.AssignSubscription(ctx, normalized)
}
