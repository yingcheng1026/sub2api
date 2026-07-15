//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestUnpaidFailedWalletOrderCannotFulfillOrRetry(t *testing.T) {
	ctx := context.Background()
	fixture, paymentSvc := newWalletPaidEvidenceFixture(t, ctx, "unpaid-failed")
	setPaymentOrderFailedEvidence(t, ctx, fixture.orderID, false)

	err := paymentSvc.ExecuteSubscriptionFulfillment(ctx, fixture.orderID)
	require.ErrorContains(t, err, "payment is not confirmed")
	err = paymentSvc.RetryFulfillment(ctx, fixture.orderID)
	require.ErrorContains(t, err, "payment is not confirmed")
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusFailed)
	assertWalletPaymentNotDelivered(t, ctx, fixture)
}

func TestLateSuccessfulWebhookPaysUnpaidFailedWalletOrderBeforeSingleFulfillment(t *testing.T) {
	ctx := context.Background()
	fixture, paymentSvc := newWalletPaidEvidenceFixture(t, ctx, "late-webhook")
	setPaymentOrderFailedEvidence(t, ctx, fixture.orderID, false)
	order, err := fixture.client.PaymentOrder.Get(ctx, fixture.orderID)
	require.NoError(t, err)
	notification := &payment.PaymentNotification{
		OrderID: order.OutTradeNo,
		TradeNo: "late-provider-trade-1",
		Amount:  order.PayAmount,
		Status:  payment.NotificationStatusSuccess,
	}

	require.NoError(t, paymentSvc.HandlePaymentNotification(ctx, notification, payment.TypeAlipay))
	assertWalletPaymentDeliveredExactlyOnce(t, ctx, fixture)
	reloaded, err := fixture.client.PaymentOrder.Get(ctx, fixture.orderID)
	require.NoError(t, err)
	require.NotNil(t, reloaded.PaidAt)
	require.Equal(t, notification.TradeNo, reloaded.PaymentTradeNo)

	require.NoError(t, paymentSvc.HandlePaymentNotification(ctx, notification, payment.TypeAlipay))
	assertWalletPaymentDeliveredExactlyOnce(t, ctx, fixture)
}

func TestPaidFailedWalletOrderCanRetryFulfillmentExactlyOnce(t *testing.T) {
	ctx := context.Background()
	fixture, paymentSvc := newWalletPaidEvidenceFixture(t, ctx, "paid-failed")
	setPaymentOrderFailedEvidence(t, ctx, fixture.orderID, true)

	require.NoError(t, paymentSvc.RetryFulfillment(ctx, fixture.orderID))
	assertWalletPaymentDeliveredExactlyOnce(t, ctx, fixture)
	require.ErrorContains(t, paymentSvc.RetryFulfillment(ctx, fixture.orderID), "already completed")
	assertWalletPaymentDeliveredExactlyOnce(t, ctx, fixture)
}

func newWalletPaidEvidenceFixture(t *testing.T, ctx context.Context, label string) (*walletPaymentAtomicityFixture, *service.PaymentService) {
	t.Helper()
	fixture := newWalletPaymentAtomicityFixture(t, ctx, label)
	apiKeySvc := service.NewAPIKeyService(
		fixture.apiKeyRepo,
		fixture.userRepo,
		fixture.groupRepo,
		fixture.subRepo,
		nil,
		nil,
		&config.Config{},
	)
	return fixture, fixture.paymentService(apiKeySvc)
}

func setPaymentOrderFailedEvidence(t *testing.T, ctx context.Context, orderID int64, paid bool) {
	t.Helper()
	if paid {
		_, err := integrationDB.ExecContext(ctx, `
			UPDATE payment_orders
			SET status = $1,
			    paid_at = $2,
			    payment_trade_no = 'confirmed-provider-trade',
			    failed_at = $2,
			    failed_reason = 'fulfillment_failed',
			    updated_at = $2
			WHERE id = $3
		`, service.OrderStatusFailed, time.Now().UTC(), orderID)
		require.NoError(t, err)
		return
	}
	_, err := integrationDB.ExecContext(ctx, `
		UPDATE payment_orders
		SET status = $1,
		    paid_at = NULL,
		    payment_trade_no = '',
		    failed_at = NOW(),
		    failed_reason = 'payment_create_failed',
		    updated_at = NOW()
		WHERE id = $2
	`, service.OrderStatusFailed, orderID)
	require.NoError(t, err)
}
