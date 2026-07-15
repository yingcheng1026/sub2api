//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type failFirstMarkSucceededIdempotencyRepo struct {
	service.IdempotencyRepository
	failed atomic.Bool
}

func (r *failFirstMarkSucceededIdempotencyRepo) MarkSucceeded(
	ctx context.Context,
	id int64,
	responseStatus int,
	responseBody string,
	expiresAt time.Time,
) error {
	if r.failed.CompareAndSwap(false, true) {
		return errors.New("injected crash before idempotency success marker")
	}
	return r.IdempotencyRepository.MarkSucceeded(ctx, id, responseStatus, responseBody, expiresAt)
}

func adminSubscriptionAssignPGOptions(scope, key string, payload any) service.IdempotencyExecuteOptions {
	return service.IdempotencyExecuteOptions{
		Scope:          scope,
		ActorScope:     "admin:99",
		Method:         "POST",
		Route:          "/api/v1/admin/subscriptions/assign",
		IdempotencyKey: key,
		Payload:        payload,
		TTL:            time.Hour,
		RequireKey:     true,
		Persistent:     true,
	}
}

func executeAdminSubscriptionAssignPGAtomic(
	ctx context.Context,
	subscriptionService *service.SubscriptionService,
	coordinator *service.IdempotencyCoordinator,
	options service.IdempotencyExecuteOptions,
	execute func(context.Context) (any, error),
) (*service.IdempotencyExecuteResult, error) {
	var result *service.IdempotencyExecuteResult
	err := subscriptionService.RunAssignmentTransaction(ctx, func(txCtx context.Context) error {
		var executeErr error
		result, executeErr = coordinator.Execute(txCtx, options, execute)
		return executeErr
	})
	return result, err
}

func TestAdminSubscriptionAssignIdempotencyPG_WalletTopupAndAffiliateOnce(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := uuid.NewString()

	inviter := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("admin-idem-inviter-%s@example.com", suffix),
		Username: "admin-idem-inviter",
	})
	invitee := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("admin-idem-invitee-%s@example.com", suffix),
		Username: "admin-idem-invitee",
	})
	operator := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("admin-idem-operator-%s@example.com", suffix),
		Username: "admin-idem-operator",
		Role:     service.RoleAdmin,
	})

	affiliateRepo := NewAffiliateRepository(client, integrationDB)
	_, err := affiliateRepo.EnsureUserAffiliate(ctx, inviter.ID)
	require.NoError(t, err)
	_, err = affiliateRepo.EnsureUserAffiliate(ctx, invitee.ID)
	require.NoError(t, err)
	bound, err := affiliateRepo.BindInviter(ctx, invitee.ID, inviter.ID)
	require.NoError(t, err)
	require.True(t, bound)

	groupRepo := NewGroupRepository(client, integrationDB)
	subRepo := NewUserSubscriptionRepository(client)
	walletRepo := NewWalletRepository(client, integrationDB)
	walletService := service.NewWalletService(walletRepo)
	subscriptionService := service.NewSubscriptionService(groupRepo, subRepo, nil, client, nil)
	subscriptionService.SetWalletTopupService(walletService)

	initial := 100.0
	seed, err := subscriptionService.AssignSubscription(ctx, &service.AssignSubscriptionInput{
		UserID:           invitee.ID,
		AssignedBy:       operator.ID,
		Notes:            "seed credits wallet",
		WalletInitialUSD: &initial,
		PlanType:         service.PlanTypeCredits,
	})
	require.NoError(t, err)

	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	idempotencyRepo := NewIdempotencyRepository(client, integrationDB)
	failingCoordinator := service.NewIdempotencyCoordinator(&failFirstMarkSucceededIdempotencyRepo{
		IdempotencyRepository: idempotencyRepo,
	}, cfg)
	coordinator := service.NewIdempotencyCoordinator(idempotencyRepo, cfg)
	scope := "admin.subscriptions.assign." + suffix
	key := "wallet-topup-" + suffix
	payload := map[string]any{
		"user_id":            invitee.ID,
		"wallet_initial_usd": 50.0,
		"notes":              "manual topup",
	}
	delta := 50.0
	executeCalls := 0
	failAffiliate := true
	execute := func(ctx context.Context) (any, error) {
		executeCalls++
		sub, assignErr := subscriptionService.AssignSubscription(ctx, &service.AssignSubscriptionInput{
			UserID:           invitee.ID,
			AssignedBy:       operator.ID,
			Notes:            "manual topup",
			WalletInitialUSD: &delta,
			PlanType:         service.PlanTypeCredits,
		})
		if assignErr != nil {
			return nil, assignErr
		}
		if failAffiliate {
			return nil, errors.New("injected affiliate accrual failure")
		}
		if sub.WalletCreditDeltaUSD == nil || *sub.WalletCreditDeltaUSD <= 0 {
			return nil, errors.New("wallet assignment did not expose the current credit delta")
		}
		rebateAmount := *sub.WalletCreditDeltaUSD * (service.AffiliateRebateCreditsCardRate / 100)
		applied, rebateErr := affiliateRepo.AccrueQuota(ctx, inviter.ID, invitee.ID, rebateAmount, 0, nil)
		if rebateErr != nil {
			return nil, rebateErr
		}
		if !applied {
			return nil, fmt.Errorf("affiliate rebate was not applied")
		}
		return map[string]any{"subscription_id": sub.ID}, nil
	}

	options := adminSubscriptionAssignPGOptions(scope, key, payload)
	_, err = executeAdminSubscriptionAssignPGAtomic(ctx, subscriptionService, coordinator, options, execute)
	require.ErrorContains(t, err, "injected affiliate accrual failure")
	failAffiliate = false

	var walletBalanceAfterAffiliateFailure float64
	var topupCountAfterAffiliateFailure int
	var idempotencyCountAfterAffiliateFailure int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT wallet_balance_usd::double precision FROM user_subscriptions WHERE id = $1",
		seed.ID,
	).Scan(&walletBalanceAfterAffiliateFailure))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM subscription_wallet_ledger
		WHERE subscription_id = $1 AND reason = 'topup'
	`, seed.ID).Scan(&topupCountAfterAffiliateFailure))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM idempotency_records
		WHERE scope = $1 AND idempotency_key_hash = $2
	`, scope, service.HashIdempotencyKey(key)).Scan(&idempotencyCountAfterAffiliateFailure))
	require.InDelta(t, 100.0, walletBalanceAfterAffiliateFailure, 0.000001)
	require.Zero(t, topupCountAfterAffiliateFailure, "affiliate failure must roll back the wallet topup")
	require.Zero(t, idempotencyCountAfterAffiliateFailure, "affiliate failure must roll back the idempotency claim")

	_, err = executeAdminSubscriptionAssignPGAtomic(ctx, subscriptionService, failingCoordinator, options, execute)
	require.ErrorContains(t, err, "injected crash before idempotency success marker")

	var walletBalanceAfterFailure float64
	var topupCountAfterFailure int
	var affiliateQuotaAfterFailure float64
	var idempotencyCountAfterFailure int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT wallet_balance_usd::double precision FROM user_subscriptions WHERE id = $1",
		seed.ID,
	).Scan(&walletBalanceAfterFailure))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM subscription_wallet_ledger
		WHERE subscription_id = $1 AND reason = 'topup'
	`, seed.ID).Scan(&topupCountAfterFailure))
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1",
		inviter.ID,
	).Scan(&affiliateQuotaAfterFailure))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM idempotency_records
		WHERE scope = $1 AND idempotency_key_hash = $2
	`, scope, service.HashIdempotencyKey(key)).Scan(&idempotencyCountAfterFailure))
	require.InDelta(t, 100.0, walletBalanceAfterFailure, 0.000001, "failed success-marker write must roll back topup")
	require.Zero(t, topupCountAfterFailure)
	require.InDelta(t, 0.0, affiliateQuotaAfterFailure, 0.000001, "affiliate ledger must share the rollback")
	require.Zero(t, idempotencyCountAfterFailure, "failed atomic attempt must not strand a processing claim")

	first, err := executeAdminSubscriptionAssignPGAtomic(ctx, subscriptionService, coordinator, options, execute)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	second, err := executeAdminSubscriptionAssignPGAtomic(ctx, subscriptionService, coordinator, options, execute)
	require.NoError(t, err)
	require.True(t, second.Replayed)
	require.Equal(t, 3, executeCalls, "rolled-back attempts may rerun, but durable effects must commit once")

	var walletBalance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT wallet_balance_usd::double precision FROM user_subscriptions WHERE id = $1",
		seed.ID,
	).Scan(&walletBalance))
	require.InDelta(t, 150.0, walletBalance, 0.000001)

	var topupCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM subscription_wallet_ledger
		WHERE subscription_id = $1 AND reason = 'topup'
	`, seed.ID).Scan(&topupCount))
	require.Equal(t, 1, topupCount)

	var affiliateQuota float64
	var affiliateLedgerCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1",
		inviter.ID,
	).Scan(&affiliateQuota))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM user_affiliate_ledger
		WHERE user_id = $1 AND source_user_id = $2 AND action = 'accrue'
	`, inviter.ID, invitee.ID).Scan(&affiliateLedgerCount))
	require.InDelta(t, 5.0, affiliateQuota, 0.000001)
	require.Equal(t, 1, affiliateLedgerCount)

	_, err = integrationDB.ExecContext(ctx, `
		UPDATE idempotency_records
		SET expires_at = NOW() - INTERVAL '1 hour'
		WHERE scope = $1 AND idempotency_key_hash = $2
	`, scope, service.HashIdempotencyKey(key))
	require.NoError(t, err)
	expiredReplay, err := executeAdminSubscriptionAssignPGAtomic(ctx, subscriptionService, coordinator, options, execute)
	require.NoError(t, err)
	require.True(t, expiredReplay.Replayed, "persistent financial keys must replay after clock expiry")
	require.Equal(t, 3, executeCalls)

	_, err = idempotencyRepo.DeleteExpired(ctx, time.Now(), 500)
	require.NoError(t, err)
	var persistentRecordCount int
	var isReclaimable bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(BOOL_AND(is_reclaimable), FALSE)
		FROM idempotency_records
		WHERE scope = $1 AND idempotency_key_hash = $2
	`, scope, service.HashIdempotencyKey(key)).Scan(&persistentRecordCount, &isReclaimable))
	require.Equal(t, 1, persistentRecordCount, "cleanup must retain persistent financial idempotency rows")
	require.False(t, isReclaimable)

	_, err = executeAdminSubscriptionAssignPGAtomic(ctx, subscriptionService, coordinator, adminSubscriptionAssignPGOptions(scope, key, map[string]any{
		"user_id":            invitee.ID,
		"wallet_initial_usd": 75.0,
	}), execute)
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(service.ErrIdempotencyKeyConflict), infraerrors.Code(err))
	require.Equal(t, 3, executeCalls)
}

func TestAdminSubscriptionAssignIdempotencyPG_MonthlyAssignmentOnce(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := uuid.NewString()
	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("admin-idem-monthly-%s@example.com", suffix),
		Username: "admin-idem-monthly",
	})
	operator := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("admin-idem-monthly-operator-%s@example.com", suffix),
		Username: "admin-idem-monthly-operator",
		Role:     service.RoleAdmin,
	})
	monthlyLimit := 99.0
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "admin-idem-monthly-" + suffix,
		Platform:         service.PlatformAnthropic,
		SubscriptionType: service.SubscriptionTypeSubscription,
		MonthlyLimitUSD:  &monthlyLimit,
	})

	groupRepo := NewGroupRepository(client, integrationDB)
	subRepo := NewUserSubscriptionRepository(client)
	subscriptionService := service.NewSubscriptionService(groupRepo, subRepo, nil, client, nil)
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	coordinator := service.NewIdempotencyCoordinator(NewIdempotencyRepository(client, integrationDB), cfg)
	scope := "admin.subscriptions.assign.monthly." + suffix
	key := "monthly-assign-" + suffix
	payload := map[string]any{
		"user_id":       user.ID,
		"group_id":      group.ID,
		"validity_days": 30,
		"notes":         "manual monthly",
	}
	executeCalls := 0
	execute := func(ctx context.Context) (any, error) {
		executeCalls++
		sub, assignErr := subscriptionService.AssignSubscription(ctx, &service.AssignSubscriptionInput{
			UserID:       user.ID,
			GroupID:      group.ID,
			ValidityDays: 30,
			AssignedBy:   operator.ID,
			Notes:        "manual monthly",
		})
		if assignErr != nil {
			return nil, assignErr
		}
		return map[string]any{"subscription_id": sub.ID}, nil
	}

	options := adminSubscriptionAssignPGOptions(scope, key, payload)
	first, err := executeAdminSubscriptionAssignPGAtomic(ctx, subscriptionService, coordinator, options, execute)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	second, err := executeAdminSubscriptionAssignPGAtomic(ctx, subscriptionService, coordinator, options, execute)
	require.NoError(t, err)
	require.True(t, second.Replayed)
	require.Equal(t, 1, executeCalls)

	var subscriptionCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM user_subscriptions
		WHERE user_id = $1 AND group_id = $2 AND deleted_at IS NULL
	`, user.ID, group.ID).Scan(&subscriptionCount))
	require.Equal(t, 1, subscriptionCount)
}
