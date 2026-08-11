//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type imageConcurrencyLeaseCacheStub struct {
	*stubConcurrencyCacheForTest
	mu         sync.Mutex
	leases     map[string]struct{}
	acquireErr error
	refreshErr error
	releaseErr error
}

func newImageConcurrencyLeaseCacheStub() *imageConcurrencyLeaseCacheStub {
	return &imageConcurrencyLeaseCacheStub{
		stubConcurrencyCacheForTest: &stubConcurrencyCacheForTest{},
		leases:                      make(map[string]struct{}),
	}
}

func (c *imageConcurrencyLeaseCacheStub) AcquireImageConcurrencyLease(_ context.Context, maxConcurrent, _ int, leaseID string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.acquireErr != nil {
		return false, c.acquireErr
	}
	if _, ok := c.leases[leaseID]; ok {
		return true, nil
	}
	if len(c.leases) >= maxConcurrent {
		return false, nil
	}
	c.leases[leaseID] = struct{}{}
	return true, nil
}

func (c *imageConcurrencyLeaseCacheStub) RefreshImageConcurrencyLease(_ context.Context, _ int, leaseID string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshErr != nil {
		return false, c.refreshErr
	}
	_, ok := c.leases[leaseID]
	return ok, nil
}

func (c *imageConcurrencyLeaseCacheStub) ReleaseImageConcurrencyLease(_ context.Context, leaseID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.releaseErr != nil {
		return c.releaseErr
	}
	delete(c.leases, leaseID)
	return nil
}

func TestAcquireImageConcurrencyLeaseFailsClosedWithoutDistributedCache(t *testing.T) {
	svc := NewConcurrencyService(&stubConcurrencyCacheForTest{})

	lease, acquired, err := svc.AcquireImageConcurrencyLease(context.Background(), 2, time.Minute)

	require.ErrorContains(t, err, "image concurrency lease cache")
	require.False(t, acquired)
	require.Nil(t, lease)
}

func TestAcquireImageConcurrencyLeaseSharesGlobalCapacity(t *testing.T) {
	cache := newImageConcurrencyLeaseCacheStub()
	firstInstance := NewConcurrencyService(cache)
	secondInstance := NewConcurrencyService(cache)

	first, acquired, err := firstInstance.AcquireImageConcurrencyLease(context.Background(), 1, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, first)

	blocked, acquired, err := secondInstance.AcquireImageConcurrencyLease(context.Background(), 1, time.Minute)
	require.NoError(t, err)
	require.False(t, acquired)
	require.Nil(t, blocked)

	first.Release()
	second, acquired, err := secondInstance.AcquireImageConcurrencyLease(context.Background(), 1, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, second)
	second.Release()
}

func TestAcquireImageConcurrencyLeaseFailsClosedOnCacheError(t *testing.T) {
	cache := newImageConcurrencyLeaseCacheStub()
	cache.acquireErr = errors.New("redis unavailable")
	svc := NewConcurrencyService(cache)

	lease, acquired, err := svc.AcquireImageConcurrencyLease(context.Background(), 1, time.Minute)

	require.ErrorContains(t, err, "redis unavailable")
	require.False(t, acquired)
	require.Nil(t, lease)
}

func TestImageConcurrencyLeaseSignalsLossWhenRefreshCannotConfirmOwnership(t *testing.T) {
	cache := newImageConcurrencyLeaseCacheStub()
	svc := NewConcurrencyService(cache)

	lease, acquired, err := svc.AcquireImageConcurrencyLease(context.Background(), 1, 3*time.Second)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, lease)

	cache.mu.Lock()
	delete(cache.leases, lease.leaseID)
	cache.mu.Unlock()

	select {
	case <-lease.Lost():
		require.ErrorIs(t, lease.Err(), ErrImageConcurrencyLeaseLost)
		require.ErrorIs(t, context.Cause(lease.Context()), ErrImageConcurrencyLeaseLost)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for image concurrency lease loss")
	}
	lease.Release()
}

func TestImageConcurrencyLeaseReleaseCancelsContextWithoutReportingLoss(t *testing.T) {
	cache := newImageConcurrencyLeaseCacheStub()
	svc := NewConcurrencyService(cache)
	lease, acquired, err := svc.AcquireImageConcurrencyLease(context.Background(), 1, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)

	lease.Release()

	require.ErrorIs(t, context.Cause(lease.Context()), context.Canceled)
	require.NoError(t, lease.Err())
}

func TestImageConcurrencyLeaseTimingBounds(t *testing.T) {
	require.Equal(t, time.Second, imageConcurrencyLeaseRefreshInterval(3*time.Second))
	require.Equal(t, 5*time.Second, imageConcurrencyLeaseRefreshInterval(30*time.Second))
	require.Equal(t, 5*time.Second, imageConcurrencyLeaseRefreshInterval(900*time.Second))
	require.Equal(t, 3*time.Second, imageConcurrencyLeaseLossGrace(3*time.Second))
	require.Equal(t, 15*time.Second, imageConcurrencyLeaseLossGrace(30*time.Second))
	require.Equal(t, 15*time.Second, imageConcurrencyLeaseLossGrace(900*time.Second))
}

func TestImageConcurrencyLeaseSignalsLossAfterRefreshErrorsExceedTTL(t *testing.T) {
	cache := newImageConcurrencyLeaseCacheStub()
	cache.refreshErr = errors.New("redis unavailable")
	lease := &ImageConcurrencyLease{
		cache:       cache,
		leaseID:     "owner",
		ttl:         3 * time.Second,
		stopCh:      make(chan struct{}),
		refreshDone: make(chan struct{}),
		lostCh:      make(chan struct{}),
	}

	lastConfirmedAt, lost := lease.refresh(time.Now().Add(-4 * time.Second))

	require.True(t, lost)
	require.False(t, lastConfirmedAt.IsZero())
	require.ErrorIs(t, lease.Err(), ErrImageConcurrencyLeaseLost)
}
