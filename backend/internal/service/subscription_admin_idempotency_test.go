package service

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func adminAssignIdempotencyOptions(key string, payload any) IdempotencyExecuteOptions {
	return IdempotencyExecuteOptions{
		Scope:          "admin.subscriptions.assign",
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

func TestAdminAssignIdempotencyCoordinatorProtectsWalletTopupService(t *testing.T) {
	ctx := context.Background()
	subRepo := newSubscriptionUserSubRepoStub()
	existingBalance := 100.0
	existingInitial := 100.0
	subRepo.seed(&UserSubscription{
		ID:               7,
		UserID:           173,
		Status:           SubscriptionStatusActive,
		ExpiresAt:        MaxExpiresAt,
		WalletBalanceUSD: &existingBalance,
		WalletInitialUSD: &existingInitial,
	})

	topup := &walletTopupServiceStub{
		returnEntry: WalletLedgerEntry{ID: 555, SubscriptionID: 7, DeltaUSD: 50, BalanceAfter: 150},
	}
	subscriptionService := NewSubscriptionService(groupRepoNoop{}, subRepo, nil, nil, nil)
	subscriptionService.SetWalletTopupService(topup)

	cfg := DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	coordinator := NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), cfg)
	payload := map[string]any{"user_id": int64(173), "wallet_initial_usd": 50.0, "notes": "manual topup"}
	inputAmount := 50.0
	executeCalls := 0
	execute := func(ctx context.Context) (any, error) {
		executeCalls++
		return subscriptionService.AssignSubscription(ctx, &AssignSubscriptionInput{
			UserID:           173,
			AssignedBy:       99,
			Notes:            "manual topup",
			WalletInitialUSD: &inputAmount,
			PlanType:         PlanTypeCredits,
		})
	}

	first, err := coordinator.Execute(ctx, adminAssignIdempotencyOptions("wallet-ack-lost", payload), execute)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	second, err := coordinator.Execute(ctx, adminAssignIdempotencyOptions("wallet-ack-lost", payload), execute)
	require.NoError(t, err)
	require.True(t, second.Replayed)
	require.Equal(t, 1, executeCalls)
	require.Equal(t, 1, topup.calls, "the wallet Topup service must run exactly once")

	_, err = coordinator.Execute(ctx, adminAssignIdempotencyOptions("wallet-ack-lost", map[string]any{
		"user_id":            int64(173),
		"wallet_initial_usd": 75.0,
	}), execute)
	require.Error(t, err)
	require.Equal(t, infraerrors.Code(ErrIdempotencyKeyConflict), infraerrors.Code(err))
	require.Equal(t, 1, executeCalls)
	require.Equal(t, 1, topup.calls)
}

func TestAdminAssignIdempotencyCoordinatorProtectsMonthlyService(t *testing.T) {
	ctx := context.Background()
	groupRepo := &subscriptionGroupRepoStub{
		group: &Group{ID: 22, SubscriptionType: SubscriptionTypeSubscription},
	}
	subRepo := newSubscriptionUserSubRepoStub()
	subscriptionService := NewSubscriptionService(groupRepo, subRepo, nil, nil, nil)

	cfg := DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	coordinator := NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), cfg)
	payload := map[string]any{
		"user_id":       int64(208),
		"group_id":      int64(22),
		"validity_days": 30,
		"notes":         "monthly vip",
	}
	executeCalls := 0
	execute := func(ctx context.Context) (any, error) {
		executeCalls++
		return subscriptionService.AssignSubscription(ctx, &AssignSubscriptionInput{
			UserID:       208,
			GroupID:      22,
			ValidityDays: 30,
			AssignedBy:   99,
			Notes:        "monthly vip",
		})
	}

	first, err := coordinator.Execute(ctx, adminAssignIdempotencyOptions("monthly-ack-lost", payload), execute)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	second, err := coordinator.Execute(ctx, adminAssignIdempotencyOptions("monthly-ack-lost", payload), execute)
	require.NoError(t, err)
	require.True(t, second.Replayed)
	require.Equal(t, 1, executeCalls)
	require.Equal(t, 1, subRepo.createCalls, "monthly assignment must create at most one subscription")
}
