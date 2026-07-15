//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionwalletledger"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestPrepareWalletRefundUsesFulfillmentCreditDeltaNotCumulativeInitial(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user := createWalletRefundTestUser(t, ctx, client, "exact-delta")

	// The plan has since changed and the wallet contains earlier credits. Neither
	// value is authoritative for this order's refund. Fulfillment recorded the
	// exact $50 delta applied by this purchase.
	plan, err := client.SubscriptionPlan.Create().
		SetName("mutated credits plan").
		SetPrice(30).
		SetWalletQuotaUsd(999).
		SetValidityDays(36500).
		SetValidityUnit("day").
		SetPlanType(PlanTypeCredits).
		Save(ctx)
	require.NoError(t, err)
	wallet := createWalletRefundTestWallet(t, ctx, client, user.ID, 150, 150)
	order := createWalletRefundTestOrder(t, ctx, client, user, plan.ID, 30)
	createWalletFulfillmentAudit(t, ctx, client, order.ID, wallet.ID, 50)

	svc := &PaymentService{entClient: client}
	refund := &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: order.Amount}
	early, err := svc.prepDeduct(ctx, order, refund, false)

	require.NoError(t, err)
	require.Nil(t, early)
	require.Equal(t, "wallet", refund.DeductionType)
	require.Equal(t, wallet.ID, refund.SubscriptionID)
	require.InDelta(t, 50, refund.WalletCreditToDeduct, 0.0000001,
		"refund must reverse this order's fulfillment delta, not the mutable plan or cumulative wallet initial")
}

func TestWalletRefundDebitAndCompensationCarryImmutablePaymentOrderSource(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user := createWalletRefundTestUser(t, ctx, client, "refund-ledger-source")
	plan, err := client.SubscriptionPlan.Create().
		SetName("wallet refund source plan").
		SetPrice(30).
		SetWalletQuotaUsd(50).
		SetValidityDays(36500).
		SetValidityUnit("day").
		SetPlanType(PlanTypeCredits).
		Save(ctx)
	require.NoError(t, err)
	wallet := createWalletRefundTestWallet(t, ctx, client, user.ID, 50, 50)
	order := createWalletRefundTestOrder(t, ctx, client, user, plan.ID, 30)
	createWalletFulfillmentAudit(t, ctx, client, order.ID, wallet.ID, 50)

	svc := &PaymentService{entClient: client}
	refundPlan := &RefundPlan{
		OrderID:       order.ID,
		Order:         order,
		RefundAmount:  order.Amount,
		GatewayAmount: order.PayAmount,
		Reason:        "test immutable source",
	}
	early, err := svc.prepDeduct(ctx, order, refundPlan, false)
	require.NoError(t, err)
	require.Nil(t, early)
	require.NoError(t, svc.claimWalletRefund(ctx, refundPlan))

	debits, err := client.SubscriptionWalletLedger.Query().
		Where(subscriptionwalletledger.PaymentOrderIDEQ(order.ID), subscriptionwalletledger.ReasonEQ(WalletLedgerReasonRefund)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, debits, 1)
	require.NotNil(t, debits[0].PaymentOrderID)
	require.Equal(t, order.ID, *debits[0].PaymentOrderID)
	require.Less(t, debits[0].DeltaUsd, 0.0)

	require.NoError(t, svc.compensateWalletRefundBeforeGateway(ctx, refundPlan, errors.New("definite pre-gateway rejection")))
	rows, err := client.SubscriptionWalletLedger.Query().
		Where(subscriptionwalletledger.PaymentOrderIDEQ(order.ID), subscriptionwalletledger.ReasonEQ(WalletLedgerReasonRefund)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.NotNil(t, row.PaymentOrderID)
		require.Equal(t, order.ID, *row.PaymentOrderID)
	}
}

func TestWalletRefundCanReverseSoftDeletedWalletFromImmutableEvidence(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user := createWalletRefundTestUser(t, ctx, client, "soft-deleted-wallet")
	plan, err := client.SubscriptionPlan.Create().
		SetName("soft deleted wallet refund plan").
		SetPrice(30).
		SetWalletQuotaUsd(50).
		SetValidityDays(36500).
		SetValidityUnit("day").
		SetPlanType(PlanTypeCredits).
		Save(ctx)
	require.NoError(t, err)
	wallet := createWalletRefundTestWallet(t, ctx, client, user.ID, 50, 50)
	order := createWalletRefundTestOrder(t, ctx, client, user, plan.ID, 30)
	createWalletFulfillmentAudit(t, ctx, client, order.ID, wallet.ID, 50)
	require.NoError(t, client.UserSubscription.DeleteOneID(wallet.ID).Exec(ctx))

	svc := &PaymentService{entClient: client}
	refundPlan := &RefundPlan{
		OrderID:       order.ID,
		Order:         order,
		RefundAmount:  order.Amount,
		GatewayAmount: order.PayAmount,
		Reason:        "reverse historical wallet credit",
	}
	early, err := svc.prepDeduct(ctx, order, refundPlan, false)
	require.NoError(t, err)
	require.Nil(t, early)
	require.NoError(t, svc.claimWalletRefund(ctx, refundPlan))

	got, err := client.UserSubscription.Get(mixins.SkipSoftDelete(ctx), wallet.ID)
	require.NoError(t, err)
	require.NotNil(t, got.DeletedAt, "refund must preserve the soft-delete lifecycle state")
	require.InDelta(t, 0, *got.WalletInitialUsd, 0.0000001)
	require.InDelta(t, 0, *got.WalletBalanceUsd, 0.0000001)
}

func createWalletRefundTestUser(t *testing.T, ctx context.Context, client *dbent.Client, label string) *dbent.User {
	t.Helper()
	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("wallet-refund-%s-%d@example.com", label, time.Now().UnixNano())).
		SetPasswordHash("hash").
		SetUsername("wallet-refund-" + label).
		Save(ctx)
	require.NoError(t, err)
	return user
}

func createWalletRefundTestWallet(t *testing.T, ctx context.Context, client *dbent.Client, userID int64, initial, balance float64) *dbent.UserSubscription {
	t.Helper()
	wallet, err := client.UserSubscription.Create().
		SetUserID(userID).
		SetStartsAt(time.Now().Add(-time.Hour)).
		SetExpiresAt(MaxExpiresAt).
		SetStatus(SubscriptionStatusActive).
		SetWalletInitialUsd(initial).
		SetWalletBalanceUsd(balance).
		Save(ctx)
	require.NoError(t, err)
	return wallet
}

func createWalletRefundTestOrder(t *testing.T, ctx context.Context, client *dbent.Client, user *dbent.User, planID int64, amount float64) *dbent.PaymentOrder {
	t.Helper()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	updatedAt := time.Now().UTC().Truncate(time.Microsecond)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(amount).
		SetPayAmount(amount).
		SetFeeRate(0).
		SetRechargeCode("WR-" + suffix).
		SetOutTradeNo("wallet-refund-" + suffix).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-wallet-refund-" + suffix).
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(planID).
		SetSubscriptionDays(36500).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now().Add(-time.Minute)).
		SetCompletedAt(time.Now()).
		SetUpdatedAt(updatedAt).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	return order
}

func createWalletFulfillmentAudit(t *testing.T, ctx context.Context, client *dbent.Client, orderID, subscriptionID int64, creditedAmount float64) *dbent.PaymentAuditLog {
	t.Helper()
	_, err := client.SubscriptionWalletLedger.Create().
		SetSubscriptionID(subscriptionID).
		SetDeltaUsd(creditedAmount).
		SetBalanceAfter(creditedAmount).
		SetReason(WalletLedgerReasonActivation).
		SetPaymentOrderID(orderID).
		SetNotes("wallet payment fulfillment source").
		Save(ctx)
	require.NoError(t, err)
	audit, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(orderID, 10)).
		SetAction("SUBSCRIPTION_SUCCESS").
		SetDetail(fmt.Sprintf(`{"subscriptionID":%d,"creditedAmount":%.10f,"planType":"credits"}`, subscriptionID, creditedAmount)).
		SetOperator("system").
		Save(ctx)
	require.NoError(t, err)
	return audit
}
