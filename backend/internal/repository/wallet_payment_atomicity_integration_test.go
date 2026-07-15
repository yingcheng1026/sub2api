//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestWalletFulfillmentKeyCreateFailureRollsBackAndRetryCreditsOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newWalletPaymentAtomicityFixture(t, ctx, "key-create")

	failingAPIKeyRepo := &failOnceAPIKeyRepository{
		APIKeyRepository: fixture.apiKeyRepo,
		err:              errors.New("injected wallet key create failure"),
	}
	apiKeySvc := service.NewAPIKeyService(
		failingAPIKeyRepo,
		fixture.userRepo,
		fixture.groupRepo,
		fixture.subRepo,
		nil,
		nil,
		&config.Config{},
	)
	paymentSvc := fixture.paymentService(apiKeySvc)

	err := paymentSvc.ExecuteSubscriptionFulfillment(ctx, fixture.orderID)
	require.ErrorContains(t, err, "injected wallet key create failure")
	assertWalletPaymentNotDelivered(t, ctx, fixture)
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusFailed)

	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, fixture.orderID))
	assertWalletPaymentDeliveredExactlyOnce(t, ctx, fixture)
	require.Equal(t, 2, failingAPIKeyRepo.createAttempts(), "retry should make one fresh key-create attempt")
}

func TestWalletFulfillmentOrderCompletionFailureRollsBackAndRetryCreditsOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newWalletPaymentAtomicityFixture(t, ctx, "order-complete")

	apiKeySvc := service.NewAPIKeyService(
		fixture.apiKeyRepo,
		fixture.userRepo,
		fixture.groupRepo,
		fixture.subRepo,
		nil,
		nil,
		&config.Config{},
	)
	removeFailure := installPaymentCompletionFailure(t, ctx, fixture.orderID)
	paymentSvc := fixture.paymentService(apiKeySvc)

	err := paymentSvc.ExecuteSubscriptionFulfillment(ctx, fixture.orderID)
	require.ErrorContains(t, err, "injected payment completion failure")
	assertWalletPaymentNotDelivered(t, ctx, fixture)
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusFailed)

	removeFailure()
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, fixture.orderID))
	assertWalletPaymentDeliveredExactlyOnce(t, ctx, fixture)
}

func TestWalletTopupKeyEnsureFailureRollsBackAndRetryCreditsOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newWalletPaymentAtomicityFixture(t, ctx, "topup-key-ensure")
	apiKeySvc := fixture.seedWallet(t, ctx)

	fixture.orderID = createWalletPaymentAtomicityOrder(t, ctx, fixture.client, fixture.user, fixture.planID, "topup-key-ensure-retry")
	failingKeySvc := &failOnceWalletKeyService{
		WalletGroupKeyService: apiKeySvc,
		err:                   errors.New("injected wallet key ensure failure"),
	}
	paymentSvc := fixture.paymentService(failingKeySvc)

	err := paymentSvc.ExecuteSubscriptionFulfillment(ctx, fixture.orderID)
	require.ErrorContains(t, err, "injected wallet key ensure failure")
	assertWalletPaymentState(t, ctx, fixture, fixture.quotaUSD, 1, 0)
	require.Equal(t, 0, paymentAuditCount(t, ctx, fixture.orderID, "SUBSCRIPTION_SUCCESS"))
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusFailed)

	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, fixture.orderID))
	assertWalletPaymentState(t, ctx, fixture, fixture.quotaUSD*2, 2, 1)
	require.Equal(t, 1, userAPIKeyCount(t, ctx, fixture.userID), "topup retry must reuse the universal key")
	require.Equal(t, 1, paymentAuditCount(t, ctx, fixture.orderID, "SUBSCRIPTION_SUCCESS"))
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusCompleted)
}

func TestWalletTopupOrderCompletionFailureRollsBackAndRetryCreditsOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newWalletPaymentAtomicityFixture(t, ctx, "topup-order-complete")
	apiKeySvc := fixture.seedWallet(t, ctx)

	fixture.orderID = createWalletPaymentAtomicityOrder(t, ctx, fixture.client, fixture.user, fixture.planID, "topup-order-complete-retry")
	removeFailure := installPaymentCompletionFailure(t, ctx, fixture.orderID)
	paymentSvc := fixture.paymentService(apiKeySvc)

	err := paymentSvc.ExecuteSubscriptionFulfillment(ctx, fixture.orderID)
	require.ErrorContains(t, err, "injected payment completion failure")
	assertWalletPaymentState(t, ctx, fixture, fixture.quotaUSD, 1, 0)
	require.Equal(t, 0, paymentAuditCount(t, ctx, fixture.orderID, "SUBSCRIPTION_SUCCESS"))
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusFailed)

	removeFailure()
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, fixture.orderID))
	assertWalletPaymentState(t, ctx, fixture, fixture.quotaUSD*2, 2, 1)
	require.Equal(t, 1, userAPIKeyCount(t, ctx, fixture.userID), "topup retry must reuse the universal key")
	require.Equal(t, 1, paymentAuditCount(t, ctx, fixture.orderID, "SUBSCRIPTION_SUCCESS"))
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusCompleted)
}

type walletPaymentAtomicityFixture struct {
	client     *dbent.Client
	user       *service.User
	userID     int64
	planID     int64
	orderID    int64
	quotaUSD   float64
	userRepo   service.UserRepository
	groupRepo  service.GroupRepository
	subRepo    service.UserSubscriptionRepository
	apiKeyRepo service.APIKeyRepository
	walletSvc  *service.WalletService
}

func newWalletPaymentAtomicityFixture(t *testing.T, ctx context.Context, label string) *walletPaymentAtomicityFixture {
	t.Helper()
	client := testEntClient(t)
	suffix := uuid.NewString()
	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("wallet-atomicity-%s-%s@example.com", label, suffix),
		Username:     "wallet-atomicity-" + label,
		PasswordHash: "hash",
	})

	const quotaUSD = 100.0
	plan, err := client.SubscriptionPlan.Create().
		SetName("Wallet atomicity " + label + " " + suffix).
		SetPrice(30).
		SetWalletQuotaUsd(quotaUSD).
		SetValidityDays(36500).
		SetValidityUnit("day").
		SetPlanType(service.PlanTypeCredits).
		Save(ctx)
	require.NoError(t, err)

	orderID := createWalletPaymentAtomicityOrder(t, ctx, client, user, plan.ID, label)

	userRepo := NewUserRepository(client, integrationDB)
	groupRepo := NewGroupRepository(client, integrationDB)
	subRepo := NewUserSubscriptionRepository(client)
	apiKeyRepo := NewAPIKeyRepository(client, integrationDB, strictAPIKeyTestProtector{})
	walletRepo := NewWalletRepository(client, integrationDB)

	return &walletPaymentAtomicityFixture{
		client:     client,
		user:       user,
		userID:     user.ID,
		planID:     plan.ID,
		orderID:    orderID,
		quotaUSD:   quotaUSD,
		userRepo:   userRepo,
		groupRepo:  groupRepo,
		subRepo:    subRepo,
		apiKeyRepo: apiKeyRepo,
		walletSvc:  service.NewWalletService(walletRepo),
	}
}

func createWalletPaymentAtomicityOrder(t *testing.T, ctx context.Context, client *dbent.Client, user *service.User, planID int64, label string) int64 {
	t.Helper()
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	txClient := tx.Client()
	orderUUID := uuid.NewString()
	order, err := txClient.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(30).
		SetPayAmount(30).
		SetFeeRate(0).
		SetRechargeCode("WAT-" + orderUUID).
		SetOutTradeNo("wat-" + orderUUID).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("tr-wat-" + orderUUID).
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(planID).
		SetSubscriptionDays(36500).
		SetStatus(service.OrderStatusPaid).
		SetPaidAt(time.Now()).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	insertCreditsPlanFulfillmentSnapshot(t, ctx, txClient, order.ID, user.ID, planID, 30, 36500, 100)
	require.NoError(t, tx.Commit())
	return order.ID
}

func (f *walletPaymentAtomicityFixture) paymentService(keySvc service.WalletGroupKeyService) *service.PaymentService {
	subSvc := service.NewSubscriptionService(f.groupRepo, f.subRepo, nil, f.client, nil)
	subSvc.SetWalletGroupKeyService(keySvc)
	subSvc.SetWalletTopupService(f.walletSvc)
	return service.NewPaymentService(f.client, nil, nil, nil, subSvc, nil, f.userRepo, f.groupRepo, nil)
}

func (f *walletPaymentAtomicityFixture) seedWallet(t *testing.T, ctx context.Context) *service.APIKeyService {
	t.Helper()
	apiKeySvc := service.NewAPIKeyService(
		f.apiKeyRepo,
		f.userRepo,
		f.groupRepo,
		f.subRepo,
		nil,
		nil,
		&config.Config{},
	)
	require.NoError(t, f.paymentService(apiKeySvc).ExecuteSubscriptionFulfillment(ctx, f.orderID))
	assertWalletPaymentDeliveredExactlyOnce(t, ctx, f)
	return apiKeySvc
}

type failOnceAPIKeyRepository struct {
	service.APIKeyRepository

	mu       sync.Mutex
	attempts int
	err      error
}

func (r *failOnceAPIKeyRepository) GetByUserIDAndPurpose(ctx context.Context, userID int64, purpose string) (*service.APIKey, error) {
	repo, ok := r.APIKeyRepository.(service.APIKeyPurposeRepository)
	if !ok {
		return nil, fmt.Errorf("wrapped api key repository does not support purpose lookup")
	}
	return repo.GetByUserIDAndPurpose(ctx, userID, purpose)
}

func (r *failOnceAPIKeyRepository) Create(ctx context.Context, key *service.APIKey) error {
	r.mu.Lock()
	r.attempts++
	attempt := r.attempts
	r.mu.Unlock()
	if attempt == 1 {
		return r.err
	}
	return r.APIKeyRepository.Create(ctx, key)
}

func (r *failOnceAPIKeyRepository) createAttempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

type failOnceWalletKeyService struct {
	service.WalletGroupKeyService

	mu       sync.Mutex
	attempts int
	err      error
}

func (s *failOnceWalletKeyService) EnsureWalletUniversalKey(ctx context.Context, userID int64) (*service.APIKey, bool, error) {
	s.mu.Lock()
	s.attempts++
	attempt := s.attempts
	s.mu.Unlock()
	if attempt == 1 {
		return nil, false, s.err
	}
	return s.WalletGroupKeyService.EnsureWalletUniversalKey(ctx, userID)
}

func assertWalletPaymentNotDelivered(t *testing.T, ctx context.Context, fixture *walletPaymentAtomicityFixture) {
	t.Helper()
	require.Equal(t, 0, walletSubscriptionCount(t, ctx, fixture.userID))
	require.Equal(t, 0, walletLedgerCount(t, ctx, fixture.userID))
	require.Equal(t, 0, userAPIKeyCount(t, ctx, fixture.userID))
	require.Equal(t, 0, paymentAuditCount(t, ctx, fixture.orderID, "SUBSCRIPTION_SUCCESS"))
}

func assertWalletPaymentDeliveredExactlyOnce(t *testing.T, ctx context.Context, fixture *walletPaymentAtomicityFixture) {
	t.Helper()
	requirePaymentOrderStatus(t, ctx, fixture.orderID, service.OrderStatusCompleted)
	require.Equal(t, 1, walletSubscriptionCount(t, ctx, fixture.userID))
	require.Equal(t, 1, userAPIKeyCount(t, ctx, fixture.userID))
	require.Equal(t, 1, paymentAuditCount(t, ctx, fixture.orderID, "SUBSCRIPTION_SUCCESS"))

	var subscriptionID int64
	var initialUSD, balanceUSD float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT id, wallet_initial_usd, wallet_balance_usd
		FROM user_subscriptions
		WHERE user_id = $1 AND wallet_balance_usd IS NOT NULL AND deleted_at IS NULL
	`, fixture.userID).Scan(&subscriptionID, &initialUSD, &balanceUSD))
	require.InDelta(t, fixture.quotaUSD, initialUSD, 0.000001)
	require.InDelta(t, fixture.quotaUSD, balanceUSD, 0.000001)

	var activationCount int
	var activationSum float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(delta_usd), 0)
		FROM subscription_wallet_ledger
		WHERE subscription_id = $1 AND reason = 'activation'
	`, subscriptionID).Scan(&activationCount, &activationSum))
	require.Equal(t, 1, activationCount)
	require.InDelta(t, fixture.quotaUSD, activationSum, 0.000001)
	require.Equal(t, 1, walletLedgerCount(t, ctx, fixture.userID), "retry must not leave duplicate wallet ledger rows")
}

func assertWalletPaymentState(t *testing.T, ctx context.Context, fixture *walletPaymentAtomicityFixture, wantBalance float64, wantLedgerCount, wantTopupCount int) {
	t.Helper()
	require.Equal(t, 1, walletSubscriptionCount(t, ctx, fixture.userID))

	var subscriptionID int64
	var initialUSD, balanceUSD float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT id, wallet_initial_usd, wallet_balance_usd
		FROM user_subscriptions
		WHERE user_id = $1 AND wallet_balance_usd IS NOT NULL AND deleted_at IS NULL
	`, fixture.userID).Scan(&subscriptionID, &initialUSD, &balanceUSD))
	require.InDelta(t, wantBalance, initialUSD, 0.000001)
	require.InDelta(t, wantBalance, balanceUSD, 0.000001)

	var ledgerCount, topupCount int
	var ledgerSum float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE reason = 'topup'),
		       COALESCE(SUM(delta_usd), 0)
		FROM subscription_wallet_ledger
		WHERE subscription_id = $1
	`, subscriptionID).Scan(&ledgerCount, &topupCount, &ledgerSum))
	require.Equal(t, wantLedgerCount, ledgerCount)
	require.Equal(t, wantTopupCount, topupCount)
	require.InDelta(t, wantBalance, ledgerSum, 0.000001)
}

func walletSubscriptionCount(t *testing.T, ctx context.Context, userID int64) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM user_subscriptions
		WHERE user_id = $1 AND wallet_balance_usd IS NOT NULL AND deleted_at IS NULL
	`, userID).Scan(&count))
	return count
}

func walletLedgerCount(t *testing.T, ctx context.Context, userID int64) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM subscription_wallet_ledger l
		JOIN user_subscriptions us ON us.id = l.subscription_id
		WHERE us.user_id = $1
	`, userID).Scan(&count))
	return count
}

func userAPIKeyCount(t *testing.T, ctx context.Context, userID int64) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM api_keys WHERE user_id = $1 AND deleted_at IS NULL
	`, userID).Scan(&count))
	return count
}

func paymentAuditCount(t *testing.T, ctx context.Context, orderID int64, action string) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM payment_audit_logs WHERE order_id = $1 AND action = $2
	`, fmt.Sprintf("%d", orderID), action).Scan(&count))
	return count
}

func requirePaymentOrderStatus(t *testing.T, ctx context.Context, orderID int64, want string) {
	t.Helper()
	var got string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT status FROM payment_orders WHERE id = $1
	`, orderID).Scan(&got))
	require.Equal(t, want, got)
}
