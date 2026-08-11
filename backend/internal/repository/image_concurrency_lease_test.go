package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestImageConcurrencyLeaseCacheIsAtomicAcrossInstances(t *testing.T) {
	redisServer := miniredis.RunT(t)
	clientA := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	clientB := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = clientB.Close()
	})

	cacheA := NewConcurrencyCache(clientA, 1, 60).(service.ImageConcurrencyLeaseCache)
	cacheB := NewConcurrencyCache(clientB, 1, 60).(service.ImageConcurrencyLeaseCache)
	ctx := context.Background()

	acquired, err := cacheA.AcquireImageConcurrencyLease(ctx, 1, 30, "instance-a")
	require.NoError(t, err)
	require.True(t, acquired)

	acquired, err = cacheB.AcquireImageConcurrencyLease(ctx, 1, 30, "instance-b")
	require.NoError(t, err)
	require.False(t, acquired)

	require.NoError(t, cacheA.ReleaseImageConcurrencyLease(ctx, "instance-a"))
	acquired, err = cacheB.AcquireImageConcurrencyLease(ctx, 1, 30, "instance-b")
	require.NoError(t, err)
	require.True(t, acquired)
}

func TestImageConcurrencyLeaseCacheRefreshAndExpiry(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 1, 60).(service.ImageConcurrencyLeaseCache)
	ctx := context.Background()

	acquired, err := cache.AcquireImageConcurrencyLease(ctx, 1, 30, "owner")
	require.NoError(t, err)
	require.True(t, acquired)

	refreshed, err := cache.RefreshImageConcurrencyLease(ctx, 30, "other")
	require.NoError(t, err)
	require.False(t, refreshed)

	redisServer.FastForward(20 * time.Second)
	refreshed, err = cache.RefreshImageConcurrencyLease(ctx, 30, "owner")
	require.NoError(t, err)
	require.True(t, refreshed)

	redisServer.FastForward(20 * time.Second)
	acquired, err = cache.AcquireImageConcurrencyLease(ctx, 1, 30, "competitor")
	require.NoError(t, err)
	require.False(t, acquired)

	redisServer.FastForward(31 * time.Second)
	acquired, err = cache.AcquireImageConcurrencyLease(ctx, 1, 30, "competitor")
	require.NoError(t, err)
	require.True(t, acquired)
}

func TestImageConcurrencyLeaseReleaseDoesNotRemoveAnotherOwner(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 1, 60).(service.ImageConcurrencyLeaseCache)
	ctx := context.Background()

	acquired, err := cache.AcquireImageConcurrencyLease(ctx, 2, 30, "owner-a")
	require.NoError(t, err)
	require.True(t, acquired)
	acquired, err = cache.AcquireImageConcurrencyLease(ctx, 2, 30, "owner-b")
	require.NoError(t, err)
	require.True(t, acquired)

	require.NoError(t, cache.ReleaseImageConcurrencyLease(ctx, "owner-a"))
	refreshed, err := cache.RefreshImageConcurrencyLease(ctx, 30, "owner-b")
	require.NoError(t, err)
	require.True(t, refreshed)
}

func TestImageConcurrencyLeaseConcurrentAcquireHonorsGlobalLimit(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 1, 60).(service.ImageConcurrencyLeaseCache)

	const (
		workers = 32
		limit   = 5
	)
	start := make(chan struct{})
	results := make(chan bool, workers)
	errorsCh := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			acquired, err := cache.AcquireImageConcurrencyLease(context.Background(), limit, 30, fmt.Sprintf("lease-%d", index))
			if err != nil {
				errorsCh <- err
				return
			}
			results <- acquired
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errorsCh)

	for err := range errorsCh {
		require.NoError(t, err)
	}
	acquiredCount := 0
	for acquired := range results {
		if acquired {
			acquiredCount++
		}
	}
	require.Equal(t, limit, acquiredCount)
}
