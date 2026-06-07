package service

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
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
		PlanType:       PlanTypeSubscription,
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
		PlanType:       PlanTypeSubscription,
		WalletQuotaUSD: &walletQuota,
		PlanGroupIDs:   []int64{g1.ID, g3.ID},
	})
	require.NoError(t, err)

	nextIDs := []int64{g24.ID, g1.ID}
	updated, err := svc.UpdatePlan(ctx, plan.ID, UpdatePlanRequest{PlanGroupIDs: &nextIDs})
	require.NoError(t, err)
	require.Equal(t, []int64{g1.ID, g24.ID}, NewSubscriptionPlanResponse(updated).PlanGroupIDs)
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
