package service

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplan"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// TestValidatePlanType 验证 B2.5 plan_type 兜底：空串 → subscription；
// 取值非法 → BadRequest PLAN_TYPE_INVALID。
func TestValidatePlanType(t *testing.T) {
	t.Run("空串默认 subscription", func(t *testing.T) {
		got, err := validatePlanType("")
		require.NoError(t, err)
		require.Equal(t, PlanTypeSubscription, got)
	})
	t.Run("空白也算空串", func(t *testing.T) {
		got, err := validatePlanType("   ")
		require.NoError(t, err)
		require.Equal(t, PlanTypeSubscription, got)
	})
	t.Run("subscription 合法", func(t *testing.T) {
		got, err := validatePlanType("subscription")
		require.NoError(t, err)
		require.Equal(t, PlanTypeSubscription, got)
	})
	t.Run("credits 合法", func(t *testing.T) {
		got, err := validatePlanType("credits")
		require.NoError(t, err)
		require.Equal(t, PlanTypeCredits, got)
	})
	t.Run("其他取值被拒", func(t *testing.T) {
		_, err := validatePlanType("trial")
		require.Error(t, err)
		require.Equal(t, "PLAN_TYPE_INVALID", infraerrors.Reason(err))
	})
}

// TestValidatePlanPatchPlanType 验证 patch 路径下 plan_type 也走相同校验。
func TestValidatePlanPatchPlanType(t *testing.T) {
	bad := "garbage"
	err := validatePlanPatch(UpdatePlanRequest{PlanType: &bad})
	require.Error(t, err)
	require.Equal(t, "PLAN_TYPE_INVALID", infraerrors.Reason(err))

	ok := "credits"
	require.NoError(t, validatePlanPatch(UpdatePlanRequest{PlanType: &ok}))
}

func TestPaymentConfigServiceCreateWalletPlanWithPlanGroupIDs(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}

	g1 := createPlanCoverageGroup(t, ctx, client, "cc-default")
	g3 := createPlanCoverageGroup(t, ctx, client, "openai-default")
	walletQuota := 400.0
	originalPrice := 199.0

	plan, err := svc.CreatePlan(ctx, CreatePlanRequest{
		Name:           "paid-lite-v3-30d",
		Description:    "30 days $400 wallet quota",
		Price:          99,
		OriginalPrice:  &originalPrice,
		ValidityDays:   30,
		ValidityUnit:   "days",
		Features:       "轻量 Claude Code / GPT 日常使用",
		ProductName:    "轻量正式版",
		ForSale:        true,
		SortOrder:      15,
		PlanType:       PlanTypeCredits,
		WalletQuotaUSD: &walletQuota,
		PlanGroupIDs:   []int64{g3.ID, g1.ID, g1.ID},
	})
	require.NoError(t, err)
	require.Nil(t, plan.GroupID)
	require.NotNil(t, plan.WalletQuotaUsd)
	require.InDelta(t, walletQuota, *plan.WalletQuotaUsd, 0.000001)
	require.Equal(t, []int64{g1.ID, g3.ID}, NewSubscriptionPlanResponse(plan).PlanGroupIDs)
}

func TestPaymentConfigServiceUpdatePlanGroupIDs(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}

	g1 := createPlanCoverageGroup(t, ctx, client, "cc-default")
	g3 := createPlanCoverageGroup(t, ctx, client, "openai-default")
	g24 := createPlanCoverageGroup(t, ctx, client, "Claude-Max pool public")
	walletQuota := 400.0
	plan, err := svc.CreatePlan(ctx, CreatePlanRequest{
		Name:           "paid-lite-v3-30d",
		Price:          99,
		ValidityDays:   30,
		ValidityUnit:   "days",
		ForSale:        true,
		PlanType:       PlanTypeCredits,
		WalletQuotaUSD: &walletQuota,
		PlanGroupIDs:   []int64{g1.ID, g3.ID},
	})
	require.NoError(t, err)

	nextIDs := []int64{g24.ID, g1.ID}
	updated, err := svc.UpdatePlan(ctx, plan.ID, UpdatePlanRequest{PlanGroupIDs: &nextIDs})
	require.NoError(t, err)
	require.Equal(t, []int64{g1.ID, g24.ID}, NewSubscriptionPlanResponse(updated).PlanGroupIDs)
}

func TestPaymentConfigServiceRejectsRestrictedPlanGroupIDs(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}

	createGroup := func(name, status, subscriptionType string, exclusive bool) int64 {
		group, err := client.Group.Create().
			SetName(name).
			SetStatus(status).
			SetSubscriptionType(subscriptionType).
			SetIsExclusive(exclusive).
			Save(ctx)
		require.NoError(t, err)
		return group.ID
	}

	inactiveID := createGroup("inactive-plan-coverage", StatusDisabled, SubscriptionTypeStandard, false)
	exclusiveID := createGroup("exclusive-plan-coverage", StatusActive, SubscriptionTypeStandard, true)
	subscriptionID := createGroup("subscription-plan-anchor", StatusActive, SubscriptionTypeSubscription, false)
	vipID := createGroup(WalletDefaultVIPGroupName, StatusActive, SubscriptionTypeStandard, true)
	deletedID := createGroup("deleted-plan-coverage", StatusActive, SubscriptionTypeStandard, false)
	_, err := client.Group.UpdateOneID(deletedID).SetDeletedAt(time.Now()).Save(ctx)
	require.NoError(t, err)

	tests := []struct {
		name    string
		groupID int64
	}{
		{name: "missing", groupID: 999999},
		{name: "inactive", groupID: inactiveID},
		{name: "exclusive", groupID: exclusiveID},
		{name: "subscription anchor", groupID: subscriptionID},
		{name: "reserved vip", groupID: vipID},
		{name: "soft deleted", groupID: deletedID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			walletQuota := 100.0
			_, err := svc.CreatePlan(ctx, CreatePlanRequest{
				Name:           "invalid coverage " + tt.name,
				Price:          10,
				ValidityDays:   30,
				ValidityUnit:   "days",
				ForSale:        true,
				PlanType:       PlanTypeCredits,
				WalletQuotaUSD: &walletQuota,
				PlanGroupIDs:   []int64{tt.groupID},
			})
			require.Error(t, err)
			require.Equal(t, "PLAN_COVERAGE_GROUP_UNAVAILABLE", infraerrors.Reason(err))
		})
	}
}

func TestPaymentConfigServiceUpdateRejectsRestrictedPlanGroupWithoutMutation(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}

	allowed := createPlanCoverageGroup(t, ctx, client, "allowed-plan-coverage")
	restricted, err := client.Group.Create().
		SetName("restricted-exclusive-coverage").
		SetStatus(StatusActive).
		SetSubscriptionType(SubscriptionTypeStandard).
		SetIsExclusive(true).
		Save(ctx)
	require.NoError(t, err)
	walletQuota := 100.0
	plan, err := svc.CreatePlan(ctx, CreatePlanRequest{
		Name:           "immutable coverage",
		Price:          10,
		ValidityDays:   30,
		ValidityUnit:   "days",
		ForSale:        true,
		PlanType:       PlanTypeCredits,
		WalletQuotaUSD: &walletQuota,
		PlanGroupIDs:   []int64{allowed.ID},
	})
	require.NoError(t, err)

	restrictedIDs := []int64{restricted.ID}
	_, err = svc.UpdatePlan(ctx, plan.ID, UpdatePlanRequest{PlanGroupIDs: &restrictedIDs})
	require.Error(t, err)
	require.Equal(t, "PLAN_COVERAGE_GROUP_UNAVAILABLE", infraerrors.Reason(err))

	persisted, err := client.SubscriptionPlan.Query().
		Where(subscriptionplan.IDEQ(plan.ID)).
		WithPlanGroups().
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{allowed.ID}, NewSubscriptionPlanResponse(persisted).PlanGroupIDs)
}

func TestValidateCapturedPlanCoverageGroupsRejectsLegacyRestrictedCoverage(t *testing.T) {
	err := validateCapturedPlanCoverageGroups(PlanTypeCredits, []*dbent.Group{{
		ID:               22,
		Name:             "legacy-restricted-coverage",
		Status:           StatusActive,
		SubscriptionType: SubscriptionTypeStandard,
		IsExclusive:      true,
	}})
	require.Error(t, err)
	require.Equal(t, "PLAN_CHANGED_RETRY", infraerrors.Reason(err))
}

func TestPaymentConfigServiceRejectsNewMonthlyPlans(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}

	validGroup := createMonthlyPlanGroup(t, ctx, client, "monthly-active", StatusActive)
	_, err := svc.CreatePlan(ctx, CreatePlanRequest{
		Name:         "retired monthly",
		GroupID:      &validGroup.ID,
		Price:        20,
		ValidityDays: 30,
		ValidityUnit: "days",
		ForSale:      true,
		PlanType:     PlanTypeSubscription,
	})
	require.Error(t, err)
	require.Equal(t, "MONTHLY_PLANS_RETIRED", infraerrors.Reason(err))
}

func TestPaymentConfigServiceListPlansForSaleReturnsCreditsOnly(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}
	standardGroup := createPlanCoverageGroup(t, ctx, client, "invalid-sale-group")

	legacy, err := client.SubscriptionPlan.Create().
		SetName("invalid monthly sale plan").
		SetGroupID(standardGroup.ID).
		SetPlanType(PlanTypeSubscription).
		SetPrice(20).
		SetValidityDays(30).
		SetForSale(true).
		Save(ctx)
	require.NoError(t, err)

	walletQuota := 100.0
	credits, err := client.SubscriptionPlan.Create().
		SetName("credits sale plan").
		SetPlanType(PlanTypeCredits).
		SetWalletQuotaUsd(walletQuota).
		SetPrice(100).
		SetValidityDays(1).
		SetForSale(true).
		Save(ctx)
	require.NoError(t, err)

	plans, err := svc.ListPlansForSale(ctx)
	require.NoError(t, err)
	require.Len(t, plans, 1)
	require.Equal(t, []int64{credits.ID}, []int64{plans[0].ID})
	require.NotEqual(t, legacy.ID, plans[0].ID)

	paymentSvc := &PaymentService{configService: svc}
	_, err = paymentSvc.validateSubOrder(ctx, CreateOrderRequest{PlanID: legacy.ID})
	require.Error(t, err)
	require.Equal(t, "MONTHLY_PLANS_RETIRED", infraerrors.Reason(err))
}

func createPlanCoverageGroup(t *testing.T, ctx context.Context, client *dbent.Client, name string) *Group {
	t.Helper()
	g, err := client.Group.Create().
		SetName(name).
		SetSubscriptionType(SubscriptionTypeStandard).
		SetStatus(StatusActive).
		Save(ctx)
	require.NoError(t, err)
	return &Group{ID: g.ID, Name: g.Name}
}

func createMonthlyPlanGroup(t *testing.T, ctx context.Context, client *dbent.Client, name, status string) *Group {
	t.Helper()
	g, err := client.Group.Create().
		SetName(name).
		SetSubscriptionType(SubscriptionTypeSubscription).
		SetStatus(status).
		Save(ctx)
	require.NoError(t, err)
	return &Group{ID: g.ID, Name: g.Name}
}
