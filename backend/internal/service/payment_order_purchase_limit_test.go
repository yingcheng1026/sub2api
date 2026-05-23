//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestCheckPlanPurchaseLimitBlocksPaidTrialRepeat(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user := createPaymentLimitUser(t, ctx, client, "repeat")
	plan := createPaymentLimitPlan(t, ctx, client, paidTrialOncePlanName)
	createPaymentLimitOrder(t, ctx, client, user.ID, plan.ID, OrderStatusCompleted)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	svc := &PaymentService{}
	err = svc.checkPlanPurchaseLimit(ctx, tx, user.ID, plan)
	require.Error(t, err)
	require.True(t, infraerrors.IsConflict(err))
	require.Equal(t, "PLAN_PURCHASE_LIMIT_REACHED", infraerrors.Reason(err))
	require.Equal(t, paidTrialOncePlanName, infraerrors.FromError(err).Metadata["plan_name"])
}

func TestCheckPlanPurchaseLimitAllowsExpiredOrFailedPaidTrialRetry(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user := createPaymentLimitUser(t, ctx, client, "retry")
	plan := createPaymentLimitPlan(t, ctx, client, paidTrialOncePlanName)
	createPaymentLimitOrder(t, ctx, client, user.ID, plan.ID, OrderStatusExpired)
	createPaymentLimitOrder(t, ctx, client, user.ID, plan.ID, OrderStatusFailed)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	svc := &PaymentService{}
	require.NoError(t, svc.checkPlanPurchaseLimit(ctx, tx, user.ID, plan))
}

func TestCheckPlanPurchaseLimitIgnoresOtherPlans(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user := createPaymentLimitUser(t, ctx, client, "standard")
	trialPlan := createPaymentLimitPlan(t, ctx, client, paidTrialOncePlanName)
	standardPlan := createPaymentLimitPlan(t, ctx, client, "paid-standard-v3-30d")
	createPaymentLimitOrder(t, ctx, client, user.ID, trialPlan.ID, OrderStatusCompleted)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	svc := &PaymentService{}
	require.NoError(t, svc.checkPlanPurchaseLimit(ctx, tx, user.ID, standardPlan))
}

func createPaymentLimitUser(t *testing.T, ctx context.Context, client *dbent.Client, suffix string) *dbent.User {
	t.Helper()
	user, err := client.User.Create().
		SetEmail("payment-limit-" + suffix + "@example.com").
		SetPasswordHash("hash").
		SetUsername("payment-limit-" + suffix).
		Save(ctx)
	require.NoError(t, err)
	return user
}

func createPaymentLimitPlan(t *testing.T, ctx context.Context, client *dbent.Client, name string) *dbent.SubscriptionPlan {
	t.Helper()
	group := createPaymentLimitGroup(t, ctx, client, "payment-limit-group-"+name)
	plan, err := client.SubscriptionPlan.Create().
		SetGroupID(group.ID).
		SetName(name).
		SetProductName("Payment Limit " + name).
		SetPrice(29.9).
		SetValidityDays(30).
		SetValidityUnit("day").
		SetForSale(true).
		Save(ctx)
	require.NoError(t, err)
	return plan
}

func createPaymentLimitGroup(t *testing.T, ctx context.Context, client *dbent.Client, name string) *dbent.Group {
	t.Helper()
	group, err := client.Group.Create().
		SetName(name).
		SetSubscriptionType(SubscriptionTypeSubscription).
		SetMonthlyLimitUsd(100).
		Save(ctx)
	require.NoError(t, err)
	return group
}

func createPaymentLimitOrder(t *testing.T, ctx context.Context, client *dbent.Client, userID, planID int64, status string) *dbent.PaymentOrder {
	t.Helper()
	order, err := client.PaymentOrder.Create().
		SetUserID(userID).
		SetUserEmail("payment-limit-order@example.com").
		SetUserName("payment-limit-order").
		SetAmount(29.9).
		SetPayAmount(29.9).
		SetFeeRate(0).
		SetRechargeCode("PAYMENT-LIMIT").
		SetOutTradeNo(fmt.Sprintf("sub2_payment_limit_%d_%s_%d", userID, status, time.Now().UnixNano())).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-payment-limit-" + status).
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(planID).
		SetSubscriptionDays(30).
		SetStatus(status).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	return order
}
