package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	entgroup "github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplan"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplangroup"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const planFulfillmentSnapshotSchemaVersion = 1

type planFulfillmentSnapshotGroup struct {
	GroupID        int64   `json:"group_id"`
	Name           string  `json:"name"`
	Platform       string  `json:"platform"`
	RateMultiplier float64 `json:"rate_multiplier"`
}

// planFulfillmentSnapshot contains every mutable product fact needed after an
// order has been created. Source plan rows remain useful administration
// metadata, but fulfillment and subscription authorization never consult them.
type planFulfillmentSnapshot struct {
	SchemaVersion    int                            `json:"schema_version"`
	PlanID           int64                          `json:"plan_id"`
	PlanType         string                         `json:"plan_type"`
	PlanName         string                         `json:"plan_name"`
	ProductName      string                         `json:"product_name"`
	PlanPrice        float64                        `json:"plan_price"`
	GroupID          *int64                         `json:"group_id"`
	SubscriptionDays int                            `json:"subscription_days"`
	WalletQuotaUSD   *float64                       `json:"wallet_quota_usd"`
	CoveredGroupIDs  []int64                        `json:"covered_group_ids"`
	CoveredGroups    []planFulfillmentSnapshotGroup `json:"covered_groups"`
	LockedRates      map[string]float64             `json:"locked_rates"`
	CaptureSource    string                         `json:"capture_source"`
}

func (s *planFulfillmentSnapshot) Validate() error {
	if s == nil {
		return errors.New("plan fulfillment snapshot is nil")
	}
	if s.SchemaVersion != planFulfillmentSnapshotSchemaVersion {
		return fmt.Errorf("unsupported plan snapshot schema_version %d", s.SchemaVersion)
	}
	if s.PlanID <= 0 {
		return errors.New("plan snapshot requires plan_id")
	}
	if s.SubscriptionDays <= 0 || s.SubscriptionDays > MaxValidityDays {
		return errors.New("plan snapshot subscription_days is out of range")
	}
	if !isFinitePositivePlanSnapshotNumber(s.PlanPrice) {
		return errors.New("plan snapshot requires a finite positive plan_price")
	}
	switch s.PlanType {
	case PlanTypeSubscription:
		if s.GroupID == nil || *s.GroupID <= 0 {
			return errors.New("monthly snapshot requires group_id")
		}
		if s.WalletQuotaUSD != nil {
			return errors.New("monthly snapshot cannot contain wallet_quota_usd")
		}
	case PlanTypeCredits:
		if s.GroupID != nil {
			return errors.New("credits snapshot cannot contain group_id")
		}
		if s.WalletQuotaUSD == nil || !isFinitePositivePlanSnapshotNumber(*s.WalletQuotaUSD) {
			return errors.New("credits snapshot requires wallet_quota_usd")
		}
	default:
		return fmt.Errorf("invalid plan snapshot plan_type %q", s.PlanType)
	}

	seen := make(map[int64]struct{}, len(s.CoveredGroupIDs))
	for _, groupID := range s.CoveredGroupIDs {
		if groupID <= 0 {
			return errors.New("plan snapshot covered_group_ids must be positive")
		}
		if _, exists := seen[groupID]; exists {
			return fmt.Errorf("plan snapshot covered_group_ids contains duplicate %d", groupID)
		}
		seen[groupID] = struct{}{}
	}
	if s.PlanType == PlanTypeSubscription {
		if _, ok := seen[*s.GroupID]; !ok {
			return errors.New("monthly snapshot covered_group_ids must contain group_id")
		}
	}
	for groupID, rate := range s.LockedRates {
		parsed, err := strconv.ParseInt(groupID, 10, 64)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("plan snapshot locked_rates has invalid group id %q", groupID)
		}
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
			return fmt.Errorf("plan snapshot locked_rates[%s] must be finite and non-negative", groupID)
		}
	}
	return nil
}

func (s *planFulfillmentSnapshot) ValidateForOrder(order *dbent.PaymentOrder) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if order == nil || order.OrderType != payment.OrderTypeSubscription {
		return errors.New("plan snapshot requires a subscription order")
	}
	if order.PlanID == nil || *order.PlanID != s.PlanID {
		return errors.New("plan snapshot plan_id does not match order")
	}
	if !sameOptionalInt64(order.SubscriptionGroupID, s.GroupID) {
		return errors.New("plan snapshot group_id does not match order")
	}
	if order.SubscriptionDays == nil || *order.SubscriptionDays != s.SubscriptionDays {
		return errors.New("plan snapshot subscription_days does not match order")
	}
	if math.Abs(order.Amount-s.PlanPrice) > 0.000001 {
		return errors.New("plan snapshot plan_price does not match order amount")
	}
	return nil
}

func isFinitePositivePlanSnapshotNumber(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *PaymentService) capturePlanFulfillmentSnapshot(ctx context.Context, client *dbent.Client, userID, planID int64) (*planFulfillmentSnapshot, error) {
	if client == nil || planID <= 0 || userID <= 0 {
		return nil, infraerrors.BadRequest("PLAN_SNAPSHOT_INPUT_INVALID", "invalid plan snapshot input")
	}
	plan, err := client.SubscriptionPlan.Query().
		Where(subscriptionplan.IDEQ(planID)).
		ForShare().
		Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, infraerrors.Conflict("PLAN_CHANGED_RETRY", "subscription plan changed; create the order again")
	}
	if err != nil {
		return nil, fmt.Errorf("lock subscription plan: %w", err)
	}
	if !plan.ForSale {
		return nil, infraerrors.Conflict("PLAN_CHANGED_RETRY", "subscription plan is no longer for sale")
	}

	planType, err := validatePlanType(plan.PlanType)
	if err != nil {
		return nil, err
	}
	groupIDs, err := capturePlanGroupIDs(ctx, client, plan)
	if err != nil {
		return nil, err
	}
	groups, err := capturePlanGroups(ctx, client, groupIDs, planType)
	if err != nil {
		return nil, err
	}

	coveredGroups := make([]planFulfillmentSnapshotGroup, 0, len(groups))
	lockedRates := make(map[string]float64, len(groups))
	for _, group := range groups {
		rate := group.RateMultiplier
		if fixed, ok := monthlyLockedRateForGroup(group.Name, group.Platform); ok && planType == PlanTypeSubscription {
			rate = fixed
		} else if userRate, ok, rateErr := captureUserGroupRate(ctx, client, userID, group.ID); rateErr != nil {
			return nil, rateErr
		} else if ok {
			rate = userRate
		}
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
			return nil, fmt.Errorf("group %d has invalid effective rate multiplier", group.ID)
		}
		lockedRates[strconv.FormatInt(group.ID, 10)] = rate
		coveredGroups = append(coveredGroups, planFulfillmentSnapshotGroup{
			GroupID:        group.ID,
			Name:           group.Name,
			Platform:       group.Platform,
			RateMultiplier: rate,
		})
	}

	snapshot := &planFulfillmentSnapshot{
		SchemaVersion:    planFulfillmentSnapshotSchemaVersion,
		PlanID:           plan.ID,
		PlanType:         planType,
		PlanName:         plan.Name,
		ProductName:      plan.ProductName,
		PlanPrice:        plan.Price,
		GroupID:          cloneInt64Pointer(plan.GroupID),
		SubscriptionDays: psComputeValidityDays(plan.ValidityDays, plan.ValidityUnit),
		WalletQuotaUSD:   cloneFloat64Pointer(plan.WalletQuotaUsd),
		CoveredGroupIDs:  append([]int64(nil), groupIDs...),
		CoveredGroups:    coveredGroups,
		LockedRates:      lockedRates,
		CaptureSource:    "native",
	}
	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("invalid captured plan snapshot: %w", err)
	}
	return snapshot, nil
}

func capturePlanGroupIDs(ctx context.Context, client *dbent.Client, plan *dbent.SubscriptionPlan) ([]int64, error) {
	seen := make(map[int64]struct{})
	groupIDs := make([]int64, 0)
	if plan.GroupID != nil && *plan.GroupID > 0 {
		seen[*plan.GroupID] = struct{}{}
		groupIDs = append(groupIDs, *plan.GroupID)
	}
	rows, err := client.SubscriptionPlanGroup.Query().
		Where(subscriptionplangroup.PlanIDEQ(plan.ID)).
		ForShare().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("lock subscription plan groups: %w", err)
	}
	for _, row := range rows {
		if row.GroupID <= 0 {
			continue
		}
		if _, exists := seen[row.GroupID]; exists {
			continue
		}
		seen[row.GroupID] = struct{}{}
		groupIDs = append(groupIDs, row.GroupID)
	}
	sort.Slice(groupIDs, func(i, j int) bool { return groupIDs[i] < groupIDs[j] })
	return groupIDs, nil
}

func capturePlanGroups(ctx context.Context, client *dbent.Client, groupIDs []int64, planType string) ([]*dbent.Group, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	groups, err := client.Group.Query().Where(entgroup.IDIn(groupIDs...)).ForShare().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("lock plan coverage groups: %w", err)
	}
	if len(groups) != len(groupIDs) {
		return nil, infraerrors.Conflict("PLAN_CHANGED_RETRY", "subscription plan coverage changed; create the order again")
	}
	if err := validateCapturedPlanCoverageGroups(planType, groups); err != nil {
		return nil, err
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	return groups, nil
}

func validateCapturedPlanCoverageGroups(planType string, groups []*dbent.Group) error {
	if planType != PlanTypeCredits {
		return nil
	}
	for _, candidate := range groups {
		if candidate == nil ||
			candidate.Status != StatusActive ||
			candidate.SubscriptionType != SubscriptionTypeStandard ||
			candidate.IsExclusive ||
			candidate.Name == WalletDefaultVIPGroupName ||
			candidate.DeletedAt != nil {
			return infraerrors.Conflict("PLAN_CHANGED_RETRY", "subscription plan coverage is no longer available; create the order again")
		}
	}
	return nil
}

func captureUserGroupRate(ctx context.Context, client *dbent.Client, userID, groupID int64) (float64, bool, error) {
	var rate sql.NullFloat64
	rows, err := client.QueryContext(ctx, `
		SELECT rate_multiplier
		FROM user_group_rate_multipliers
		WHERE user_id = $1 AND group_id = $2
		FOR SHARE
	`, userID, groupID)
	if err != nil {
		return 0, false, fmt.Errorf("read user group rate for snapshot: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, false, nil
	}
	if err := rows.Scan(&rate); err != nil {
		return 0, false, fmt.Errorf("read user group rate for snapshot: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, false, fmt.Errorf("read user group rate for snapshot: %w", err)
	}
	if !rate.Valid {
		return 0, false, nil
	}
	return rate.Float64, true, nil
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloat64Pointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func persistPlanFulfillmentSnapshot(ctx context.Context, client *dbent.Client, orderID, userID int64, snapshot *planFulfillmentSnapshot) error {
	if client == nil || orderID <= 0 || userID <= 0 {
		return errors.New("invalid plan snapshot persistence input")
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal plan fulfillment snapshot: %w", err)
	}
	_, err = client.ExecContext(ctx, `
		INSERT INTO subscription_plan_fulfillment_snapshots (
			payment_order_id, user_id, source_plan_id, snapshot
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT (payment_order_id) DO NOTHING
	`, orderID, userID, snapshot.PlanID, string(payload))
	if err != nil {
		return fmt.Errorf("persist plan fulfillment snapshot: %w", err)
	}

	stored, err := loadPlanFulfillmentSnapshot(ctx, client, orderID)
	if err != nil {
		return err
	}
	storedPayload, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	if string(storedPayload) != string(payload) {
		return infraerrors.Conflict("PLAN_SNAPSHOT_CONFLICT", "payment order already has a different plan snapshot")
	}
	return nil
}

func loadPlanFulfillmentSnapshot(ctx context.Context, client *dbent.Client, orderID int64) (*planFulfillmentSnapshot, error) {
	if client == nil || orderID <= 0 {
		return nil, errors.New("invalid plan snapshot lookup input")
	}
	var raw []byte
	rows, err := client.QueryContext(ctx, `
		SELECT snapshot
		FROM subscription_plan_fulfillment_snapshots
		WHERE payment_order_id = $1
	`, orderID)
	if err != nil {
		return nil, fmt.Errorf("load plan fulfillment snapshot: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, infraerrors.Conflict("FULFILLMENT_SNAPSHOT_MISSING", "payment order plan snapshot is missing")
	}
	if err := rows.Scan(&raw); err != nil {
		return nil, fmt.Errorf("load plan fulfillment snapshot: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load plan fulfillment snapshot: %w", err)
	}
	var snapshot planFulfillmentSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("decode plan fulfillment snapshot: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("invalid persisted plan fulfillment snapshot: %w", err)
	}
	return &snapshot, nil
}

func attachPlanSnapshotGrant(ctx context.Context, client *dbent.Client, orderID, subscriptionID int64, startsAt, expiresAt time.Time) error {
	if client == nil || orderID <= 0 || subscriptionID <= 0 || startsAt.IsZero() || !expiresAt.After(startsAt) {
		return errors.New("invalid subscription grant attachment")
	}
	attachedAt := time.Now().UTC()
	result, err := client.ExecContext(ctx, `
		UPDATE subscription_plan_fulfillment_snapshots
		SET user_subscription_id = $2,
			grant_starts_at = $3,
			grant_expires_at = $4,
			attached_at = $5
		WHERE payment_order_id = $1
		  AND (
			user_subscription_id IS NULL
			OR (
				user_subscription_id = $2
				AND grant_starts_at = $3
				AND grant_expires_at = $4
			)
		  )
	`, orderID, subscriptionID, startsAt, expiresAt, attachedAt)
	if err != nil {
		return fmt.Errorf("attach plan snapshot grant: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return infraerrors.Conflict("SUBSCRIPTION_GRANT_CONFLICT", "payment order is already attached to another subscription grant")
	}
	return nil
}

func planSnapshotGrantWindow(sub *UserSubscription, snapshot *planFulfillmentSnapshot, now time.Time) (time.Time, time.Time) {
	if sub == nil || snapshot == nil {
		return time.Time{}, time.Time{}
	}
	if snapshot.PlanType == PlanTypeCredits {
		return now, sub.ExpiresAt
	}
	startsAt := sub.ExpiresAt.AddDate(0, 0, -snapshot.SubscriptionDays)
	if startsAt.Before(sub.StartsAt) {
		startsAt = sub.StartsAt
	}
	return startsAt, sub.ExpiresAt
}

func planSnapshotRates(snapshot *planFulfillmentSnapshot) map[string]float64 {
	if snapshot == nil || len(snapshot.LockedRates) == 0 {
		return nil
	}
	out := make(map[string]float64, len(snapshot.LockedRates))
	for groupID, rate := range snapshot.LockedRates {
		out[groupID] = rate
	}
	return out
}

func snapshotPlanID(snapshot *planFulfillmentSnapshot) int64 {
	if snapshot == nil {
		return 0
	}
	return snapshot.PlanID
}

func snapshotVersion(snapshot *planFulfillmentSnapshot) int {
	if snapshot == nil {
		return 0
	}
	return snapshot.SchemaVersion
}

func validateCapturedPlanPrice(snapshot *planFulfillmentSnapshot, orderAmount, limitAmount float64) error {
	if snapshot == nil {
		return errors.New("plan snapshot is nil")
	}
	if math.Abs(snapshot.PlanPrice-orderAmount) > 0.000001 || math.Abs(snapshot.PlanPrice-limitAmount) > 0.000001 {
		return infraerrors.Conflict("PLAN_CHANGED_RETRY", "subscription plan price changed; create the order again")
	}
	return nil
}

func isPlanSnapshotMissing(err error) bool {
	return strings.EqualFold(infraerrors.Reason(err), "FULFILLMENT_SNAPSHOT_MISSING")
}

// Keep the generated paymentorder import tied to this file's plan-order
// contract. It also catches accidental renames during Ent regeneration.
var _ = paymentorder.FieldPlanID
