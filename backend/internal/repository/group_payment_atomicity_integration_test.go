//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestGroupFulfillmentCompletionFailureRollsBackNewSubscriptionAndRetryDeliversOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newGroupPaymentAtomicityFixture(t, ctx, "new")
	removeFailure := installPaymentCompletionFailure(t, ctx, fixture.orderID)

	err := fixture.paymentService().ExecuteSubscriptionFulfillment(ctx, fixture.orderID)
	require.ErrorContains(t, err, "injected payment completion failure")
	require.Equal(t, 0, groupSubscriptionCount(t, ctx, fixture.user.ID, fixture.group.ID), "failed order completion must roll back the new monthly entitlement")
	require.Equal(t, 0, paymentAuditCount(t, ctx, fixture.orderID, "SUBSCRIPTION_SUCCESS"))
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusFailed)

	removeFailure()
	require.NoError(t, fixture.paymentService().ExecuteSubscriptionFulfillment(ctx, fixture.orderID))
	require.Equal(t, 1, groupSubscriptionCount(t, ctx, fixture.user.ID, fixture.group.ID))
	require.Equal(t, 1, paymentAuditCount(t, ctx, fixture.orderID, "SUBSCRIPTION_SUCCESS"))
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusCompleted)
}

func TestGroupFulfillmentCompletionFailureRollsBackExtensionAndRetryExtendsOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newGroupPaymentAtomicityFixture(t, ctx, "extend")
	initialExpiry := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Microsecond)
	existing := mustCreateSubscription(t, fixture.client, &service.UserSubscription{
		UserID:    fixture.user.ID,
		GroupID:   &fixture.group.ID,
		StartsAt:  time.Now().UTC().Add(-time.Hour),
		ExpiresAt: initialExpiry,
		Status:    service.SubscriptionStatusActive,
	})
	removeFailure := installPaymentCompletionFailure(t, ctx, fixture.orderID)

	err := fixture.paymentService().ExecuteSubscriptionFulfillment(ctx, fixture.orderID)
	require.ErrorContains(t, err, "injected payment completion failure")
	require.WithinDuration(t, initialExpiry, groupSubscriptionExpiry(t, ctx, existing.ID), time.Millisecond,
		"failed order completion must roll back the monthly extension")
	require.Equal(t, 0, paymentAuditCount(t, ctx, fixture.orderID, "SUBSCRIPTION_SUCCESS"))
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusFailed)

	removeFailure()
	require.NoError(t, fixture.paymentService().ExecuteSubscriptionFulfillment(ctx, fixture.orderID))
	wantExpiry := initialExpiry.AddDate(0, 0, fixture.days)
	require.WithinDuration(t, wantExpiry, groupSubscriptionExpiry(t, ctx, existing.ID), time.Millisecond,
		"retry must extend exactly once")
	require.Equal(t, 1, paymentAuditCount(t, ctx, fixture.orderID, "SUBSCRIPTION_SUCCESS"))
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusCompleted)
}

func TestConcurrentGroupFulfillmentsForSameUserAndGroupAccumulateBothExtensions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	fixture := newGroupPaymentAtomicityFixture(t, ctx, "concurrent-extension")
	secondOrderID := fixture.createPaidOrder(t, ctx)
	initialExpiry := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Microsecond)
	mustCreateSubscription(t, fixture.client, &service.UserSubscription{
		UserID:    fixture.user.ID,
		GroupID:   &fixture.group.ID,
		StartsAt:  time.Now().UTC().Add(-time.Hour),
		ExpiresAt: initialExpiry,
		Status:    service.SubscriptionStatusActive,
	})

	barrier := newSubscriptionReadBarrier(500 * time.Millisecond)
	baseRepo := NewUserSubscriptionRepository(fixture.client)
	paymentService := fixture.paymentServiceWithSubscriptionRepo(&barrierUserSubscriptionRepository{
		UserSubscriptionRepository: baseRepo,
		barrier:                    barrier,
	})

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, orderID := range []int64{fixture.orderID, secondOrderID} {
		orderID := orderID
		go func() {
			<-start
			errs <- paymentService.ExecuteSubscriptionFulfillment(ctx, orderID)
		}()
	}
	close(start)
	for range 2 {
		require.NoError(t, <-errs)
	}

	wantExpiry := initialExpiry.AddDate(0, 0, fixture.days*2)
	require.WithinDuration(t, wantExpiry, groupSubscriptionExpiry(t, ctx, barrier.subscriptionID()), time.Millisecond,
		"two independently paid orders must extend the monthly entitlement twice")
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusCompleted)
	requirePaymentOrderStatus(t, ctx, secondOrderID, service.OrderStatusCompleted)
	require.Equal(t, 1, paymentAuditCount(t, ctx, fixture.orderID, "SUBSCRIPTION_SUCCESS"))
	require.Equal(t, 1, paymentAuditCount(t, ctx, secondOrderID, "SUBSCRIPTION_SUCCESS"))
}

type subscriptionReadBarrier struct {
	mu            sync.Mutex
	reads         int
	seenSubID     int64
	release       chan struct{}
	releaseOnce   sync.Once
	fallbackAfter time.Duration
}

func newSubscriptionReadBarrier(fallbackAfter time.Duration) *subscriptionReadBarrier {
	return &subscriptionReadBarrier{
		release:       make(chan struct{}),
		fallbackAfter: fallbackAfter,
	}
}

func (b *subscriptionReadBarrier) wait(subscriptionID int64) {
	b.mu.Lock()
	b.reads++
	b.seenSubID = subscriptionID
	reads := b.reads
	b.mu.Unlock()
	if reads >= 2 {
		b.releaseOnce.Do(func() { close(b.release) })
	}

	timer := time.NewTimer(b.fallbackAfter)
	defer timer.Stop()
	select {
	case <-b.release:
	case <-timer.C:
		// Once the production row lock exists, the second transaction cannot
		// reach this read until the first commits. Let the first continue after
		// a bounded wait; the second will then observe the committed expiry.
		b.releaseOnce.Do(func() { close(b.release) })
	}
}

func (b *subscriptionReadBarrier) subscriptionID() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seenSubID
}

type barrierUserSubscriptionRepository struct {
	service.UserSubscriptionRepository
	barrier *subscriptionReadBarrier
}

func (r *barrierUserSubscriptionRepository) LockUserForSubscriptionAssignment(ctx context.Context, userID int64) error {
	locker, ok := r.UserSubscriptionRepository.(interface {
		LockUserForSubscriptionAssignment(context.Context, int64) error
	})
	if !ok {
		return nil
	}
	return locker.LockUserForSubscriptionAssignment(ctx, userID)
}

func (r *barrierUserSubscriptionRepository) GetByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	sub, err := r.UserSubscriptionRepository.GetByUserIDAndGroupID(ctx, userID, groupID)
	if err == nil && sub != nil {
		r.barrier.wait(sub.ID)
	}
	return sub, err
}

type groupPaymentAtomicityFixture struct {
	client  *dbent.Client
	user    *service.User
	group   *service.Group
	orderID int64
	days    int
}

func newGroupPaymentAtomicityFixture(t *testing.T, ctx context.Context, label string) *groupPaymentAtomicityFixture {
	t.Helper()
	client := testEntClient(t)
	suffix := uuid.NewString()
	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("group-payment-atomicity-%s-%s@example.com", label, suffix),
		Username:     "group-payment-atomicity-" + label,
		PasswordHash: "hash",
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "group-payment-atomicity-" + label + "-" + suffix,
		Platform:         service.PlatformOpenAI,
		SubscriptionType: service.SubscriptionTypeSubscription,
		Status:           service.StatusActive,
	})
	const days = 30
	orderUUID := uuid.NewString()
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(30).
		SetPayAmount(30).
		SetFeeRate(0).
		SetRechargeCode("GRP-" + orderUUID).
		SetOutTradeNo("grp-" + orderUUID).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("tr-grp-" + orderUUID).
		SetOrderType(payment.OrderTypeSubscription).
		SetSubscriptionGroupID(group.ID).
		SetSubscriptionDays(days).
		SetStatus(service.OrderStatusPaid).
		SetPaidAt(time.Now()).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	return &groupPaymentAtomicityFixture{client: client, user: user, group: group, orderID: order.ID, days: days}
}

func (f *groupPaymentAtomicityFixture) paymentService() *service.PaymentService {
	subRepo := NewUserSubscriptionRepository(f.client)
	return f.paymentServiceWithSubscriptionRepo(subRepo)
}

func (f *groupPaymentAtomicityFixture) paymentServiceWithSubscriptionRepo(subRepo service.UserSubscriptionRepository) *service.PaymentService {
	groupRepo := NewGroupRepository(f.client, integrationDB)
	userRepo := NewUserRepository(f.client, integrationDB)
	subSvc := service.NewSubscriptionService(groupRepo, subRepo, nil, f.client, nil)
	return service.NewPaymentService(f.client, nil, nil, nil, subSvc, nil, userRepo, groupRepo, nil)
}

func (f *groupPaymentAtomicityFixture) createPaidOrder(t *testing.T, ctx context.Context) int64 {
	t.Helper()
	orderUUID := uuid.NewString()
	order, err := f.client.PaymentOrder.Create().
		SetUserID(f.user.ID).
		SetUserEmail(f.user.Email).
		SetUserName(f.user.Username).
		SetAmount(30).
		SetPayAmount(30).
		SetFeeRate(0).
		SetRechargeCode("GRP-" + orderUUID).
		SetOutTradeNo("grp-" + orderUUID).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("tr-grp-" + orderUUID).
		SetOrderType(payment.OrderTypeSubscription).
		SetSubscriptionGroupID(f.group.ID).
		SetSubscriptionDays(f.days).
		SetStatus(service.OrderStatusPaid).
		SetPaidAt(time.Now()).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	return order.ID
}

func installPaymentCompletionFailure(t *testing.T, ctx context.Context, orderID int64) func() {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "test_fail_payment_completed_" + suffix
	triggerName := "trg_fail_payment_completed_" + suffix
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.id = %d AND NEW.status = 'COMPLETED' THEN
				RAISE EXCEPTION 'injected payment completion failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON payment_orders
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, orderID, triggerName, functionName))
	require.NoError(t, err)
	removed := false
	remove := func() {
		if removed {
			return
		}
		removed = true
		_, dropErr := integrationDB.ExecContext(context.Background(), fmt.Sprintf(`
			DROP TRIGGER IF EXISTS %s ON payment_orders;
			DROP FUNCTION IF EXISTS %s();
		`, triggerName, functionName))
		require.NoError(t, dropErr)
	}
	t.Cleanup(remove)
	return remove
}

func groupSubscriptionCount(t *testing.T, ctx context.Context, userID, groupID int64) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM user_subscriptions
		WHERE user_id=$1 AND group_id=$2 AND deleted_at IS NULL
	`, userID, groupID).Scan(&count))
	return count
}

func groupSubscriptionExpiry(t *testing.T, ctx context.Context, subscriptionID int64) time.Time {
	t.Helper()
	var expiresAt time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT expires_at FROM user_subscriptions WHERE id=$1
	`, subscriptionID).Scan(&expiresAt))
	return expiresAt
}
