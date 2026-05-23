//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestCheckPaidTrialRedeemLimitBlocksExistingUsedTrialCode(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	group := createPaidTrialRedeemLimitGroup(t, ctx, client)
	user := createPaymentLimitUser(t, ctx, client, "redeem-used")
	createPaidTrialRedeemLimitUsedCode(t, ctx, client, group.ID, user.ID, "trial-used-1")
	code := createPaidTrialRedeemLimitCode(t, ctx, client, group.ID, "trial-current-1", 30)

	err := checkPaidTrialRedeemLimitForTest(t, ctx, client, user.ID, code)

	require.Error(t, err)
	require.True(t, infraerrors.IsConflict(err))
	require.Equal(t, "PLAN_PURCHASE_LIMIT_REACHED", infraerrors.Reason(err))
	require.Equal(t, paidTrialOncePlanName, infraerrors.FromError(err).Metadata["plan_name"])
	require.Equal(t, "redeem_code", infraerrors.FromError(err).Metadata["source"])
}

func TestCheckPaidTrialRedeemLimitBlocksExistingTrialSubscription(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	group := createPaidTrialRedeemLimitGroup(t, ctx, client)
	user := createPaymentLimitUser(t, ctx, client, "redeem-sub")
	createPaidTrialRedeemLimitSubscription(t, ctx, client, group.ID, user.ID)
	code := createPaidTrialRedeemLimitCode(t, ctx, client, group.ID, "trial-current-2", 30)

	err := checkPaidTrialRedeemLimitForTest(t, ctx, client, user.ID, code)

	require.Error(t, err)
	require.True(t, infraerrors.IsConflict(err))
	require.Equal(t, "subscription", infraerrors.FromError(err).Metadata["source"])
}

func TestCheckPaidTrialRedeemLimitBlocksExistingTrialPaymentOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	group := createPaidTrialRedeemLimitGroup(t, ctx, client)
	user := createPaymentLimitUser(t, ctx, client, "redeem-order")
	plan := createPaidTrialRedeemLimitPlan(t, ctx, client)
	createPaymentLimitOrder(t, ctx, client, user.ID, plan.ID, OrderStatusCompleted)
	code := createPaidTrialRedeemLimitCode(t, ctx, client, group.ID, "trial-current-3", 30)

	err := checkPaidTrialRedeemLimitForTest(t, ctx, client, user.ID, code)

	require.Error(t, err)
	require.True(t, infraerrors.IsConflict(err))
	require.Equal(t, "payment_order", infraerrors.FromError(err).Metadata["source"])
}

func TestCheckPaidTrialRedeemLimitIgnoresOtherGroupsAndRefundAdjustments(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	group := createPaidTrialRedeemLimitGroup(t, ctx, client)
	otherGroup := createRedeemLimitGroup(t, ctx, client, "redeem-other-group")
	user := createPaymentLimitUser(t, ctx, client, "redeem-other")
	createPaidTrialRedeemLimitUsedCode(t, ctx, client, group.ID, user.ID, "trial-used-2")
	otherGroupCode := createPaidTrialRedeemLimitCode(t, ctx, client, otherGroup.ID, "trial-current-4", 30)
	refundAdjustmentCode := createPaidTrialRedeemLimitCode(t, ctx, client, group.ID, "trial-current-5", -30)

	require.NoError(t, checkPaidTrialRedeemLimitForTest(t, ctx, client, user.ID, otherGroupCode))
	require.NoError(t, checkPaidTrialRedeemLimitForTest(t, ctx, client, user.ID, refundAdjustmentCode))
}

func checkPaidTrialRedeemLimitForTest(t *testing.T, ctx context.Context, client *dbent.Client, userID int64, code *RedeemCode) error {
	t.Helper()
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	return (&RedeemService{}).checkPaidTrialRedeemLimit(ctx, tx, userID, code)
}

func createPaidTrialRedeemLimitGroup(t *testing.T, ctx context.Context, client *dbent.Client) *dbent.Group {
	t.Helper()
	var group *dbent.Group
	for i := int64(1); i <= paidTrialRedeemGroupID; i++ {
		group = createRedeemLimitGroup(t, ctx, client, fmt.Sprintf("paid-trial-redeem-limit-%d", i))
	}
	require.Equal(t, paidTrialRedeemGroupID, group.ID)
	return group
}

func createRedeemLimitGroup(t *testing.T, ctx context.Context, client *dbent.Client, name string) *dbent.Group {
	t.Helper()
	group, err := client.Group.Create().
		SetName(name).
		SetSubscriptionType(SubscriptionTypeSubscription).
		SetMonthlyLimitUsd(100).
		Save(ctx)
	require.NoError(t, err)
	return group
}

func createPaidTrialRedeemLimitPlan(t *testing.T, ctx context.Context, client *dbent.Client) *dbent.SubscriptionPlan {
	t.Helper()
	var plan *dbent.SubscriptionPlan
	for i := int64(1); i <= paidTrialOncePlanID; i++ {
		name := fmt.Sprintf("paid-trial-redeem-limit-plan-%d", i)
		if i == paidTrialOncePlanID {
			name = paidTrialOncePlanName
		}
		plan = createPaymentLimitPlan(t, ctx, client, name)
	}
	require.Equal(t, paidTrialOncePlanID, plan.ID)
	return plan
}

func createPaidTrialRedeemLimitCode(t *testing.T, ctx context.Context, client *dbent.Client, groupID int64, code string, validityDays int) *RedeemCode {
	t.Helper()
	redeemCode, err := client.RedeemCode.Create().
		SetCode(code).
		SetType(RedeemTypeSubscription).
		SetStatus(StatusUnused).
		SetGroupID(groupID).
		SetValidityDays(validityDays).
		Save(ctx)
	require.NoError(t, err)
	return &RedeemCode{
		ID:           redeemCode.ID,
		Code:         redeemCode.Code,
		Type:         redeemCode.Type,
		Status:       redeemCode.Status,
		GroupID:      redeemCode.GroupID,
		ValidityDays: redeemCode.ValidityDays,
	}
}

func createPaidTrialRedeemLimitUsedCode(t *testing.T, ctx context.Context, client *dbent.Client, groupID, userID int64, code string) {
	t.Helper()
	_, err := client.RedeemCode.Create().
		SetCode(code).
		SetType(RedeemTypeSubscription).
		SetStatus(StatusUsed).
		SetGroupID(groupID).
		SetUsedBy(userID).
		SetUsedAt(time.Now()).
		SetValidityDays(30).
		Save(ctx)
	require.NoError(t, err)
}

func createPaidTrialRedeemLimitSubscription(t *testing.T, ctx context.Context, client *dbent.Client, groupID, userID int64) {
	t.Helper()
	now := time.Now()
	_, err := client.UserSubscription.Create().
		SetUserID(userID).
		SetGroupID(groupID).
		SetStartsAt(now.Add(-time.Hour)).
		SetExpiresAt(now.Add(30 * 24 * time.Hour)).
		SetStatus(SubscriptionStatusActive).
		Save(ctx)
	require.NoError(t, err)
}
