//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const stalePaymentOrderAge = 15 * time.Minute

func TestRecoverStalePaidWalletOrderDeliversExactlyOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newWalletPaymentAtomicityFixture(t, ctx, "stale-paid-background")
	apiKeySvc := service.NewAPIKeyService(
		fixture.apiKeyRepo,
		fixture.userRepo,
		fixture.groupRepo,
		fixture.subRepo,
		nil,
		nil,
		&config.Config{},
	)
	paymentSvc := fixture.paymentService(apiKeySvc)
	setPaymentOrderStateAge(t, ctx, fixture.orderID, service.OrderStatusPaid, stalePaymentOrderAge)

	recovered, err := paymentSvc.RecoverStaleFulfillments(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	assertWalletPaymentDeliveredExactlyOnce(t, ctx, fixture)

	recovered, err = paymentSvc.RecoverStaleFulfillments(ctx, 10)
	require.NoError(t, err)
	require.Zero(t, recovered, "completed wallet order must not be selected again")
	assertWalletPaymentDeliveredExactlyOnce(t, ctx, fixture)
}

func TestDuplicateWebhookReclaimsStaleRechargingWalletExactlyOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newWalletPaymentAtomicityFixture(t, ctx, "stale-webhook")
	apiKeySvc := service.NewAPIKeyService(
		fixture.apiKeyRepo,
		fixture.userRepo,
		fixture.groupRepo,
		fixture.subRepo,
		nil,
		nil,
		&config.Config{},
	)
	paymentSvc := fixture.paymentService(apiKeySvc)
	setPaymentOrderStateAge(t, ctx, fixture.orderID, service.OrderStatusRecharging, stalePaymentOrderAge)

	order, err := fixture.client.PaymentOrder.Get(ctx, fixture.orderID)
	require.NoError(t, err)
	notification := &payment.PaymentNotification{
		OrderID: order.OutTradeNo,
		TradeNo: order.PaymentTradeNo,
		Amount:  order.PayAmount,
		Status:  payment.NotificationStatusSuccess,
	}

	errs := runConcurrentPaymentAttempts(2, func() error {
		return paymentSvc.HandlePaymentNotification(ctx, notification, payment.TypeAlipay)
	})
	for _, err := range errs {
		require.NoError(t, err, "duplicate successful webhooks must be acknowledged")
	}
	assertWalletPaymentDeliveredExactlyOnce(t, ctx, fixture)
}

func TestFreshRechargingWalletCannotBeTakenOver(t *testing.T) {
	ctx := context.Background()
	fixture := newWalletPaymentAtomicityFixture(t, ctx, "fresh-lease")
	apiKeySvc := service.NewAPIKeyService(
		fixture.apiKeyRepo,
		fixture.userRepo,
		fixture.groupRepo,
		fixture.subRepo,
		nil,
		nil,
		&config.Config{},
	)
	paymentSvc := fixture.paymentService(apiKeySvc)
	setPaymentOrderStateAge(t, ctx, fixture.orderID, service.OrderStatusRecharging, 0)

	err := paymentSvc.RetryFulfillment(ctx, fixture.orderID)
	require.ErrorContains(t, err, "being processed")
	order, err := fixture.client.PaymentOrder.Get(ctx, fixture.orderID)
	require.NoError(t, err)
	require.NoError(t, paymentSvc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
		OrderID: order.OutTradeNo,
		TradeNo: order.PaymentTradeNo,
		Amount:  order.PayAmount,
		Status:  payment.NotificationStatusSuccess,
	}, payment.TypeAlipay), "duplicate webhook must acknowledge a fresh active lease without taking it over")
	recovered, err := paymentSvc.RecoverStaleFulfillments(ctx, 10)
	require.NoError(t, err)
	require.Zero(t, recovered)
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusRecharging)
	assertWalletPaymentNotDelivered(t, ctx, fixture)
}

func TestInFlightWalletFulfillmentPreventsStaleLeaseTakeover(t *testing.T) {
	ctx := context.Background()
	fixture := newWalletPaymentAtomicityFixture(t, ctx, "lease-fencing")
	apiKeySvc := service.NewAPIKeyService(
		fixture.apiKeyRepo,
		fixture.userRepo,
		fixture.groupRepo,
		fixture.subRepo,
		nil,
		nil,
		&config.Config{},
	)
	blockingKeySvc := newBlockingWalletKeyService(apiKeySvc)
	t.Cleanup(blockingKeySvc.release)
	oldWorker := fixture.paymentService(blockingKeySvc)
	setPaymentOrderStateAge(t, ctx, fixture.orderID, service.OrderStatusPaid, 0)

	oldResult := make(chan error, 1)
	go func() {
		oldResult <- oldWorker.ExecuteSubscriptionFulfillment(ctx, fixture.orderID)
	}()
	select {
	case <-blockingKeySvc.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("old fulfillment worker did not reach the controlled crash window")
	}

	// The immutable payment-source FK and deferred evidence guard deliberately
	// keep the order row locked while wallet effects are uncommitted. A second
	// worker therefore cannot age/reclaim the lease underneath a live atomic
	// fulfillment transaction. A real crashed worker releases this lock when
	// PostgreSQL rolls the transaction back; stale recovery is covered above.
	updateCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, err := integrationDB.ExecContext(updateCtx, `
		UPDATE payment_orders
		SET status = $1, updated_at = $2
		WHERE id = $3
	`, service.OrderStatusRecharging, time.Now().Add(-stalePaymentOrderAge), fixture.orderID)
	require.Error(t, err, "a live wallet transaction must fence a concurrent stale-lease takeover")
	require.ErrorIs(t, updateCtx.Err(), context.DeadlineExceeded)
	blockingKeySvc.release()
	require.NoError(t, <-oldResult)
	assertWalletPaymentDeliveredExactlyOnce(t, ctx, fixture)
	require.Equal(t, 0, paymentAuditCount(t, ctx, fixture.orderID, "FULFILLMENT_LEASE_RECLAIMED"))
}

func TestConcurrentStaleGroupTakeoverCreatesMonthlyEntitlementOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newGroupPaymentAtomicityFixture(t, ctx, "stale-new-monthly")
	paymentSvc := fixture.paymentService()
	setPaymentOrderStateAge(t, ctx, fixture.orderID, service.OrderStatusRecharging, stalePaymentOrderAge)

	errs := runConcurrentPaymentAttempts(8, func() error {
		return paymentSvc.RetryFulfillment(ctx, fixture.orderID)
	})
	requireAtLeastOneSuccessfulAttempt(t, errs)
	require.Equal(t, 1, groupSubscriptionCount(t, ctx, fixture.user.ID, fixture.group.ID))
	require.Equal(t, 1, paymentAuditCount(t, ctx, fixture.orderID, "SUBSCRIPTION_SUCCESS"))
	require.Equal(t, 1, paymentAuditCount(t, ctx, fixture.orderID, "FULFILLMENT_LEASE_RECLAIMED"))
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusCompleted)
}

func TestConcurrentStaleGroupTakeoverExtendsMonthlyEntitlementOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newGroupPaymentAtomicityFixture(t, ctx, "stale-extend-monthly")
	initialExpiry := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Microsecond)
	existing := mustCreateSubscription(t, fixture.client, &service.UserSubscription{
		UserID:    fixture.user.ID,
		GroupID:   &fixture.group.ID,
		StartsAt:  time.Now().UTC().Add(-time.Hour),
		ExpiresAt: initialExpiry,
		Status:    service.SubscriptionStatusActive,
	})
	paymentSvc := fixture.paymentService()
	setPaymentOrderStateAge(t, ctx, fixture.orderID, service.OrderStatusRecharging, stalePaymentOrderAge)

	errs := runConcurrentPaymentAttempts(8, func() error {
		return paymentSvc.RetryFulfillment(ctx, fixture.orderID)
	})
	requireAtLeastOneSuccessfulAttempt(t, errs)
	require.WithinDuration(t, initialExpiry.AddDate(0, 0, fixture.days), groupSubscriptionExpiry(t, ctx, existing.ID), time.Millisecond)
	require.Equal(t, 1, groupSubscriptionCount(t, ctx, fixture.user.ID, fixture.group.ID))
	require.Equal(t, 1, paymentAuditCount(t, ctx, fixture.orderID, "SUBSCRIPTION_SUCCESS"))
	require.Equal(t, 1, paymentAuditCount(t, ctx, fixture.orderID, "FULFILLMENT_LEASE_RECLAIMED"))
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusCompleted)
}

func TestConcurrentStaleBalanceTakeoverCreditsOnce(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("stale-balance-%s@example.com", uuid.NewString()),
		Username:     "stale-balance",
		PasswordHash: "hash",
	})
	orderUUID := uuid.NewString()
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("STALEBAL-" + orderUUID[:20]).
		SetOutTradeNo("stale-bal-" + orderUUID).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("tr-stale-bal-" + orderUUID).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(service.OrderStatusRecharging).
		SetPaidAt(time.Now().Add(-stalePaymentOrderAge)).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	setPaymentOrderStateAge(t, ctx, order.ID, service.OrderStatusRecharging, stalePaymentOrderAge)

	userRepo := NewUserRepository(client, integrationDB)
	redeemRepo := NewRedeemCodeRepository(client)
	redeemSvc := service.NewRedeemService(redeemRepo, userRepo, nil, nil, nil, client, nil, nil)
	paymentSvc := service.NewPaymentService(client, nil, nil, redeemSvc, nil, nil, userRepo, nil, nil)

	errs := runConcurrentPaymentAttempts(8, func() error {
		return paymentSvc.RetryFulfillment(ctx, order.ID)
	})
	requireAtLeastOneSuccessfulAttempt(t, errs)

	reloadedUser, err := userRepo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 88, reloadedUser.Balance, 0.000001, "balance order must credit exactly once")
	var usedCodeCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redeem_codes
		WHERE code = $1 AND status = $2 AND used_by = $3
	`, order.RechargeCode, service.StatusUsed, user.ID).Scan(&usedCodeCount))
	require.Equal(t, 1, usedCodeCount)
	require.Equal(t, 1, paymentAuditCount(t, ctx, order.ID, "RECHARGE_SUCCESS"))
	require.Equal(t, 1, paymentAuditCount(t, ctx, order.ID, "FULFILLMENT_LEASE_RECLAIMED"))
	requirePaymentOrderStatus(t, ctx, order.ID, service.OrderStatusCompleted)
}

func setPaymentOrderStateAge(t *testing.T, ctx context.Context, orderID int64, status string, age time.Duration) {
	t.Helper()
	updatedAt := time.Now().Add(-age)
	_, err := integrationDB.ExecContext(ctx, `
		UPDATE payment_orders
		SET status = $1,
		    paid_at = COALESCE(paid_at, $2),
		    updated_at = $2
		WHERE id = $3
	`, status, updatedAt, orderID)
	require.NoError(t, err)
}

func runConcurrentPaymentAttempts(count int, attempt func() error) []error {
	start := make(chan struct{})
	errs := make([]error, count)
	var wg sync.WaitGroup
	wg.Add(count)
	for i := range count {
		go func(index int) {
			defer wg.Done()
			<-start
			errs[index] = attempt()
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

func requireAtLeastOneSuccessfulAttempt(t *testing.T, errs []error) {
	t.Helper()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	require.GreaterOrEqual(t, successes, 1, "one worker must recover the stale fulfillment; errors=%v", errs)
}

type blockingWalletKeyService struct {
	service.WalletGroupKeyService
	reached     chan struct{}
	releaseCh   chan struct{}
	reachedOnce sync.Once
	releaseOnce sync.Once
}

func newBlockingWalletKeyService(inner service.WalletGroupKeyService) *blockingWalletKeyService {
	return &blockingWalletKeyService{
		WalletGroupKeyService: inner,
		reached:               make(chan struct{}),
		releaseCh:             make(chan struct{}),
	}
}

func (s *blockingWalletKeyService) EnsureWalletUniversalKey(ctx context.Context, userID int64) (*service.APIKey, bool, error) {
	key, created, err := s.WalletGroupKeyService.EnsureWalletUniversalKey(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	s.reachedOnce.Do(func() { close(s.reached) })
	select {
	case <-s.releaseCh:
		return key, created, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

func (s *blockingWalletKeyService) release() {
	s.releaseOnce.Do(func() { close(s.releaseCh) })
}
