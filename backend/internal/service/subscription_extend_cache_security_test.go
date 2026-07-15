package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

type extendCacheUserSubRepoStub struct {
	userSubRepoNoop
	sub         UserSubscription
	extendCalls atomic.Int32
}

func (s *extendCacheUserSubRepoStub) GetByID(context.Context, int64) (*UserSubscription, error) {
	copy := s.sub
	return &copy, nil
}

func (s *extendCacheUserSubRepoStub) ExtendExpiry(_ context.Context, _ int64, expiresAt time.Time) error {
	s.sub.ExpiresAt = expiresAt
	s.extendCalls.Add(1)
	return nil
}

func TestExtendSubscriptionDefersCacheInvalidationUntilOuterCommit(t *testing.T) {
	groupID := int64(22)
	repo := &extendCacheUserSubRepoStub{sub: UserSubscription{
		ID:        91,
		UserID:    173,
		GroupID:   &groupID,
		Status:    SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}}
	cache := &billingCacheWorkerStub{}
	svc := &SubscriptionService{
		userSubRepo:         repo,
		billingCacheService: &BillingCacheService{cache: cache},
	}
	txCtx := dbent.NewTxContext(context.Background(), &dbent.Tx{})

	_, err := svc.ExtendSubscription(txCtx, repo.sub.ID, 1)
	require.NoError(t, err)
	require.Equal(t, int32(1), repo.extendCalls.Load())
	require.Zero(t, atomic.LoadInt64(&cache.subscriptionInvalidations), "an uncommitted write must not evict the shared cache")

	require.NoError(t, svc.InvalidateSubscriptionCachesAfterCommit(context.Background(), repo.sub.ID))
	require.Equal(t, int64(1), atomic.LoadInt64(&cache.subscriptionInvalidations))
}
