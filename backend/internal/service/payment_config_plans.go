package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplan"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplangroup"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// validatePlanRequired checks that all required fields for a plan are provided.
func validatePlanRequired(name string, groupID *int64, walletQuotaUSD *float64, planType string, price float64, validityDays int, validityUnit string, originalPrice *float64) error {
	if strings.TrimSpace(name) == "" {
		return infraerrors.BadRequest("PLAN_NAME_REQUIRED", "plan name is required")
	}
	if err := validatePlanFulfillmentShape(groupID, walletQuotaUSD, planType); err != nil {
		return err
	}
	if price <= 0 {
		return infraerrors.BadRequest("PLAN_PRICE_INVALID", "price must be > 0")
	}
	if validityDays <= 0 {
		return infraerrors.BadRequest("PLAN_VALIDITY_REQUIRED", "validity days must be > 0")
	}
	if strings.TrimSpace(validityUnit) == "" {
		return infraerrors.BadRequest("PLAN_VALIDITY_UNIT_REQUIRED", "validity unit is required")
	}
	if originalPrice != nil && *originalPrice < 0 {
		return infraerrors.BadRequest("PLAN_ORIGINAL_PRICE_INVALID", "original price must be >= 0")
	}
	return nil
}

func validatePlanFulfillmentShape(groupID *int64, walletQuotaUSD *float64, planType string) error {
	switch planType {
	case PlanTypeSubscription:
		if groupID == nil || *groupID <= 0 {
			return infraerrors.BadRequest("PLAN_GROUP_REQUIRED", "monthly plans require a subscription group")
		}
		if walletQuotaUSD != nil {
			return infraerrors.BadRequest("PLAN_MODE_INVALID", "monthly plans cannot use wallet quota")
		}
	case PlanTypeCredits:
		if groupID != nil {
			return infraerrors.BadRequest("PLAN_MODE_INVALID", "credits wallet plans must not set group_id")
		}
		if walletQuotaUSD == nil || *walletQuotaUSD <= 0 || math.IsNaN(*walletQuotaUSD) || math.IsInf(*walletQuotaUSD, 0) {
			return infraerrors.BadRequest("PLAN_WALLET_QUOTA_INVALID", "credits plans require a finite wallet_quota_usd > 0")
		}
	default:
		return infraerrors.BadRequest("PLAN_TYPE_INVALID", "plan_type must be 'subscription' or 'credits'")
	}
	return nil
}

// validatePlanType 兜底 plan_type 取值（空串视为 subscription，向后兼容）。
func validatePlanType(planType string) (string, error) {
	pt := strings.TrimSpace(planType)
	if pt == "" {
		return PlanTypeSubscription, nil
	}
	if pt != PlanTypeSubscription && pt != PlanTypeCredits {
		return "", infraerrors.BadRequest("PLAN_TYPE_INVALID", "plan_type must be 'subscription' or 'credits'")
	}
	return pt, nil
}

// validatePlanPatch validates only the non-nil fields in a patch update.
func validatePlanPatch(req UpdatePlanRequest) error {
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		return infraerrors.BadRequest("PLAN_NAME_REQUIRED", "plan name is required")
	}
	if req.GroupID != nil && *req.GroupID <= 0 {
		return infraerrors.BadRequest("PLAN_GROUP_REQUIRED", "group is required")
	}
	if req.WalletQuotaUSD != nil && (*req.WalletQuotaUSD <= 0 || math.IsNaN(*req.WalletQuotaUSD) || math.IsInf(*req.WalletQuotaUSD, 0)) {
		return infraerrors.BadRequest("PLAN_WALLET_QUOTA_INVALID", "wallet_quota_usd must be > 0")
	}
	if req.Price != nil && *req.Price <= 0 {
		return infraerrors.BadRequest("PLAN_PRICE_INVALID", "price must be > 0")
	}
	if req.ValidityDays != nil && *req.ValidityDays <= 0 {
		return infraerrors.BadRequest("PLAN_VALIDITY_REQUIRED", "validity days must be > 0")
	}
	if req.ValidityUnit != nil && strings.TrimSpace(*req.ValidityUnit) == "" {
		return infraerrors.BadRequest("PLAN_VALIDITY_UNIT_REQUIRED", "validity unit is required")
	}
	if req.OriginalPrice != nil && *req.OriginalPrice < 0 {
		return infraerrors.BadRequest("PLAN_ORIGINAL_PRICE_INVALID", "original price must be >= 0")
	}
	if req.PlanType != nil {
		if _, err := validatePlanType(*req.PlanType); err != nil {
			return err
		}
	}
	return nil
}

// --- Plan CRUD ---

// SubscriptionPlanResponse is the admin-facing plan payload with flattened plan-group IDs.
type SubscriptionPlanResponse struct {
	*dbent.SubscriptionPlan
	PlanGroupIDs []int64 `json:"plan_group_ids"`
}

func NewSubscriptionPlanResponse(plan *dbent.SubscriptionPlan) SubscriptionPlanResponse {
	return SubscriptionPlanResponse{
		SubscriptionPlan: plan,
		PlanGroupIDs:     planGroupIDsFromEdges(plan),
	}
}

func NewSubscriptionPlanResponses(plans []*dbent.SubscriptionPlan) []SubscriptionPlanResponse {
	out := make([]SubscriptionPlanResponse, 0, len(plans))
	for _, plan := range plans {
		out = append(out, NewSubscriptionPlanResponse(plan))
	}
	return out
}

func planGroupIDsFromEdges(plan *dbent.SubscriptionPlan) []int64 {
	if plan == nil || len(plan.Edges.PlanGroups) == 0 {
		return []int64{}
	}
	ids := make([]int64, 0, len(plan.Edges.PlanGroups))
	for _, pg := range plan.Edges.PlanGroups {
		ids = append(ids, pg.GroupID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// PlanGroupInfo holds the group details needed for subscription plan display.
type PlanGroupInfo struct {
	Platform        string   `json:"platform"`
	Name            string   `json:"name"`
	RateMultiplier  float64  `json:"rate_multiplier"`
	DailyLimitUSD   *float64 `json:"daily_limit_usd"`
	WeeklyLimitUSD  *float64 `json:"weekly_limit_usd"`
	MonthlyLimitUSD *float64 `json:"monthly_limit_usd"`
	ModelScopes     []string `json:"supported_model_scopes"`
}

// GetGroupPlatformMap returns a map of group_id → platform for the given plans.
func (s *PaymentConfigService) GetGroupPlatformMap(ctx context.Context, plans []*dbent.SubscriptionPlan) map[int64]string {
	info := s.GetGroupInfoMap(ctx, plans)
	m := make(map[int64]string, len(info))
	for id, gi := range info {
		m[id] = gi.Platform
	}
	return m
}

// GetGroupInfoMap returns a map of group_id → PlanGroupInfo for the given plans.
func (s *PaymentConfigService) GetGroupInfoMap(ctx context.Context, plans []*dbent.SubscriptionPlan) map[int64]PlanGroupInfo {
	ids := make([]int64, 0, len(plans))
	seen := make(map[int64]bool)
	for _, p := range plans {
		// 钱包模式 plan (v4) GroupID 为 nil, 不参与单 group 信息聚合
		if p.GroupID == nil {
			continue
		}
		if !seen[*p.GroupID] {
			seen[*p.GroupID] = true
			ids = append(ids, *p.GroupID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	groups, err := s.entClient.Group.Query().Where(group.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil
	}
	m := make(map[int64]PlanGroupInfo, len(groups))
	for _, g := range groups {
		m[int64(g.ID)] = PlanGroupInfo{
			Platform:        g.Platform,
			Name:            g.Name,
			RateMultiplier:  g.RateMultiplier,
			DailyLimitUSD:   g.DailyLimitUsd,
			WeeklyLimitUSD:  g.WeeklyLimitUsd,
			MonthlyLimitUSD: g.MonthlyLimitUsd,
			ModelScopes:     g.SupportedModelScopes,
		}
	}
	return m
}

func (s *PaymentConfigService) ListPlans(ctx context.Context) ([]*dbent.SubscriptionPlan, error) {
	return s.entClient.SubscriptionPlan.Query().
		WithPlanGroups().
		Order(subscriptionplan.BySortOrder()).
		All(ctx)
}

func (s *PaymentConfigService) ListPlansForSale(ctx context.Context) ([]*dbent.SubscriptionPlan, error) {
	plans, err := s.entClient.SubscriptionPlan.Query().Where(
		subscriptionplan.ForSaleEQ(true),
		subscriptionplan.PlanTypeEQ(PlanTypeCredits),
	).Order(subscriptionplan.BySortOrder()).All(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.validatePlanGroupAvailability(ctx, s.entClient, plans); err != nil {
		return nil, err
	}
	return plans, nil
}

func (s *PaymentConfigService) CreatePlan(ctx context.Context, req CreatePlanRequest) (*dbent.SubscriptionPlan, error) {
	planType, err := validatePlanType(req.PlanType)
	if err != nil {
		return nil, err
	}
	if planType != PlanTypeCredits {
		return nil, infraerrors.BadRequest("MONTHLY_PLANS_RETIRED", "monthly plans have been retired; create a credits plan instead")
	}
	if err := validatePlanRequired(req.Name, req.GroupID, req.WalletQuotaUSD, planType, req.Price, req.ValidityDays, req.ValidityUnit, req.OriginalPrice); err != nil {
		return nil, err
	}
	planGroupIDs, err := normalizePlanGroupIDs(req.PlanGroupIDs)
	if err != nil {
		return nil, err
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.requireAvailableMonthlyPlanGroup(ctx, tx.Client(), planType, req.GroupID); err != nil {
		return nil, err
	}
	if err := validatePlanCoverageGroupIDs(ctx, tx.Client(), planGroupIDs); err != nil {
		return nil, err
	}

	b := tx.SubscriptionPlan.Create().
		SetName(req.Name).SetDescription(req.Description).
		SetPrice(req.Price).SetValidityDays(req.ValidityDays).SetValidityUnit(req.ValidityUnit).
		SetFeatures(req.Features).SetProductName(req.ProductName).
		SetForSale(req.ForSale).SetSortOrder(req.SortOrder).
		SetPlanType(planType)
	if req.GroupID != nil {
		b.SetGroupID(*req.GroupID)
	}
	if req.WalletQuotaUSD != nil {
		b.SetWalletQuotaUsd(*req.WalletQuotaUSD)
	}
	if req.OriginalPrice != nil {
		b.SetOriginalPrice(*req.OriginalPrice)
	}
	plan, err := b.Save(ctx)
	if err != nil {
		return nil, err
	}
	if err := replacePlanGroupIDs(ctx, tx, plan.ID, planGroupIDs); err != nil {
		return nil, err
	}
	plan, err = tx.SubscriptionPlan.Query().
		Where(subscriptionplan.IDEQ(plan.ID)).
		WithPlanGroups().
		Only(ctx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return plan, nil
}

// UpdatePlan updates a subscription plan by ID (patch semantics).
// NOTE: This function exceeds 30 lines due to per-field nil-check patch update boilerplate
// plus a validation guard for non-nil fields.
func (s *PaymentConfigService) UpdatePlan(ctx context.Context, id int64, req UpdatePlanRequest) (*dbent.SubscriptionPlan, error) {
	if err := validatePlanPatch(req); err != nil {
		return nil, err
	}
	var planGroupIDs []int64
	if req.PlanGroupIDs != nil {
		var err error
		planGroupIDs, err = normalizePlanGroupIDs(*req.PlanGroupIDs)
		if err != nil {
			return nil, err
		}
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := tx.SubscriptionPlan.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	nextPlanType, err := validatePlanType(existing.PlanType)
	if err != nil {
		return nil, err
	}
	nextGroupID := existing.GroupID
	nextWalletQuota := existing.WalletQuotaUsd
	if req.GroupID != nil {
		nextGroupID = req.GroupID
		nextWalletQuota = nil
	}
	if req.WalletQuotaUSD != nil {
		nextWalletQuota = req.WalletQuotaUSD
		nextGroupID = nil
	}
	if req.PlanType != nil {
		nextPlanType, err = validatePlanType(*req.PlanType)
		if err != nil {
			return nil, err
		}
	}
	if nextPlanType != PlanTypeCredits && req.ForSale != nil && *req.ForSale {
		return nil, infraerrors.BadRequest("MONTHLY_PLANS_RETIRED", "monthly plans cannot be put back on sale")
	}
	if err := validatePlanFulfillmentShape(nextGroupID, nextWalletQuota, nextPlanType); err != nil {
		return nil, err
	}
	if err := s.requireAvailableMonthlyPlanGroup(ctx, tx.Client(), nextPlanType, nextGroupID); err != nil {
		return nil, err
	}
	if req.PlanGroupIDs != nil {
		if err := validatePlanCoverageGroupIDs(ctx, tx.Client(), planGroupIDs); err != nil {
			return nil, err
		}
	}

	u := tx.SubscriptionPlan.UpdateOneID(id)
	if req.GroupID != nil {
		u.SetGroupID(*req.GroupID)
		u.ClearWalletQuotaUsd()
	}
	if req.WalletQuotaUSD != nil {
		u.SetWalletQuotaUsd(*req.WalletQuotaUSD)
		u.ClearGroupID()
	}
	if req.Name != nil {
		u.SetName(*req.Name)
	}
	if req.Description != nil {
		u.SetDescription(*req.Description)
	}
	if req.Price != nil {
		u.SetPrice(*req.Price)
	}
	if req.OriginalPrice != nil {
		u.SetOriginalPrice(*req.OriginalPrice)
	}
	if req.ValidityDays != nil {
		u.SetValidityDays(*req.ValidityDays)
	}
	if req.ValidityUnit != nil {
		u.SetValidityUnit(*req.ValidityUnit)
	}
	if req.Features != nil {
		u.SetFeatures(*req.Features)
	}
	if req.ProductName != nil {
		u.SetProductName(*req.ProductName)
	}
	if req.ForSale != nil {
		u.SetForSale(*req.ForSale)
	}
	if req.SortOrder != nil {
		u.SetSortOrder(*req.SortOrder)
	}
	if req.PlanType != nil {
		pt, err := validatePlanType(*req.PlanType)
		if err != nil {
			return nil, err
		}
		u.SetPlanType(pt)
	}
	if _, err := u.Save(ctx); err != nil {
		return nil, err
	}
	if req.PlanGroupIDs != nil {
		if err := replacePlanGroupIDs(ctx, tx, id, planGroupIDs); err != nil {
			return nil, err
		}
	}
	plan, err := tx.SubscriptionPlan.Query().
		Where(subscriptionplan.IDEQ(id)).
		WithPlanGroups().
		Only(ctx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return plan, nil
}

func normalizePlanGroupIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return []int64{}, nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, infraerrors.BadRequest("PLAN_GROUP_REQUIRED", "plan_group_ids must contain positive group IDs")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func validatePlanCoverageGroupIDs(ctx context.Context, client *dbent.Client, groupIDs []int64) error {
	if len(groupIDs) == 0 {
		return nil
	}
	if client == nil {
		return infraerrors.BadRequest("PLAN_COVERAGE_GROUP_UNAVAILABLE", "plan coverage group is unavailable")
	}
	groups, err := client.Group.Query().
		Where(group.IDIn(groupIDs...), group.DeletedAtIsNil()).
		All(ctx)
	if err != nil {
		return fmt.Errorf("validate plan coverage groups: %w", err)
	}
	if len(groups) != len(groupIDs) {
		return infraerrors.BadRequest("PLAN_COVERAGE_GROUP_UNAVAILABLE", "plan coverage groups must exist and be active public standard groups")
	}
	for _, candidate := range groups {
		if candidate.Status != StatusActive ||
			candidate.SubscriptionType != SubscriptionTypeStandard ||
			candidate.IsExclusive ||
			candidate.Name == WalletDefaultVIPGroupName ||
			candidate.DeletedAt != nil {
			return infraerrors.BadRequest("PLAN_COVERAGE_GROUP_UNAVAILABLE", "plan coverage groups must exist and be active public standard groups")
		}
	}
	return nil
}

func (s *PaymentConfigService) validatePlanGroupAvailability(ctx context.Context, client *dbent.Client, plans []*dbent.SubscriptionPlan) error {
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		planType, err := validatePlanType(plan.PlanType)
		if err != nil {
			return err
		}
		if err := validatePlanFulfillmentShape(plan.GroupID, plan.WalletQuotaUsd, planType); err != nil {
			return err
		}
		if err := s.requireAvailableMonthlyPlanGroup(ctx, client, planType, plan.GroupID); err != nil {
			return err
		}
	}
	return nil
}

func (s *PaymentConfigService) requireAvailableMonthlyPlanGroup(ctx context.Context, client *dbent.Client, planType string, groupID *int64) error {
	if planType != PlanTypeSubscription {
		return nil
	}
	if client == nil || groupID == nil || *groupID <= 0 {
		return infraerrors.BadRequest("PLAN_GROUP_UNAVAILABLE", "monthly plan group is unavailable")
	}
	exists, err := client.Group.Query().Where(
		group.IDEQ(*groupID),
		group.StatusEQ(StatusActive),
		group.SubscriptionTypeEQ(SubscriptionTypeSubscription),
		group.DeletedAtIsNil(),
	).Exist(ctx)
	if err != nil {
		return fmt.Errorf("validate monthly plan group: %w", err)
	}
	if !exists {
		return infraerrors.BadRequest("PLAN_GROUP_UNAVAILABLE", "monthly plan group must be active and subscription type")
	}
	return nil
}

func replacePlanGroupIDs(ctx context.Context, tx *dbent.Tx, planID int64, groupIDs []int64) error {
	if _, err := tx.SubscriptionPlanGroup.Delete().
		Where(subscriptionplangroup.PlanIDEQ(planID)).
		Exec(ctx); err != nil {
		return fmt.Errorf("delete plan groups: %w", err)
	}
	for _, groupID := range groupIDs {
		if _, err := tx.SubscriptionPlanGroup.Create().
			SetPlanID(planID).
			SetGroupID(groupID).
			Save(ctx); err != nil {
			return fmt.Errorf("create plan group %d: %w", groupID, err)
		}
	}
	return nil
}

func (s *PaymentConfigService) DeletePlan(ctx context.Context, id int64) error {
	count, err := s.countPendingOrdersByPlan(ctx, id)
	if err != nil {
		return fmt.Errorf("check pending orders: %w", err)
	}
	if count > 0 {
		return infraerrors.Conflict("PENDING_ORDERS",
			fmt.Sprintf("this plan has %d in-progress orders and cannot be deleted — wait for orders to complete first", count))
	}
	return s.entClient.SubscriptionPlan.DeleteOneID(id).Exec(ctx)
}

// GetPlan returns a subscription plan by ID.
func (s *PaymentConfigService) GetPlan(ctx context.Context, id int64) (*dbent.SubscriptionPlan, error) {
	plan, err := s.entClient.SubscriptionPlan.Get(ctx, id)
	if err != nil {
		return nil, infraerrors.NotFound("PLAN_NOT_FOUND", "subscription plan not found")
	}
	return plan, nil
}
