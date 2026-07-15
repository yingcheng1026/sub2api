//go:build integration

package repository

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestRefreshTokenCacheConsumeExactlyOnce(t *testing.T) {
	ctx := context.Background()
	cache := NewRefreshTokenCache(testRedis(t))
	data := &service.RefreshTokenData{
		UserID:       91,
		TokenVersion: 3,
		FamilyID:     "family-exactly-once",
		CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
		ExpiresAt:    time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond),
	}
	require.NoError(t, cache.StoreRefreshToken(ctx, "parent-hash", data, time.Hour))

	const callers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	type consumeResult struct {
		data *service.RefreshTokenData
		err  error
	}
	results := make(chan consumeResult, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			consumed, err := cache.ConsumeRefreshToken(ctx, "parent-hash")
			results <- consumeResult{data: consumed, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var successes atomic.Int32
	var misses atomic.Int32
	for result := range results {
		switch {
		case result.err == nil:
			require.Equal(t, data, result.data)
			successes.Add(1)
		case errors.Is(result.err, service.ErrRefreshTokenNotFound):
			misses.Add(1)
		default:
			require.NoError(t, result.err)
		}
	}
	require.Equal(t, int32(1), successes.Load())
	require.Equal(t, int32(callers-1), misses.Load())
}
