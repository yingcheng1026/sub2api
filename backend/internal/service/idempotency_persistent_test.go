package service

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestIdempotencyCoordinatorPersistentSuccessReplaysAfterClockExpiry(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())
	opts := IdempotencyExecuteOptions{
		Scope:          "admin.subscriptions.assign",
		Method:         "POST",
		Route:          "/api/v1/admin/subscriptions/assign",
		ActorScope:     "admin:99",
		IdempotencyKey: "persistent-wallet-topup",
		Payload:        map[string]any{"user_id": 173, "wallet_initial_usd": 50},
		RequireKey:     true,
		Persistent:     true,
	}
	executeCalls := 0
	execute := func(context.Context) (any, error) {
		executeCalls++
		return map[string]any{"subscription_id": 701}, nil
	}

	first, err := coordinator.Execute(context.Background(), opts, execute)
	require.NoError(t, err)
	require.False(t, first.Replayed)

	keyHash := HashIdempotencyKey(opts.IdempotencyKey)
	repo.mu.Lock()
	record := repo.data[repo.key(opts.Scope, keyHash)]
	require.NotNil(t, record)
	require.True(t, record.Persistent)
	record.ExpiresAt = time.Now().Add(-time.Hour)
	repo.mu.Unlock()

	replayed, err := coordinator.Execute(context.Background(), opts, execute)
	require.NoError(t, err)
	require.True(t, replayed.Replayed, "persistent financial keys must replay after the ordinary TTL")
	require.Equal(t, 1, executeCalls, "clock expiry must never execute the financial side effect again")
}

func TestIdempotencyCoordinatorPersistentIncompleteRecordRequiresManualRecovery(t *testing.T) {
	for _, status := range []string{IdempotencyStatusProcessing, IdempotencyStatusFailedRetryable} {
		t.Run(status, func(t *testing.T) {
			repo := newInMemoryIdempotencyRepo()
			coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())
			opts := IdempotencyExecuteOptions{
				Scope:          "admin.subscriptions.assign." + status,
				Method:         "POST",
				Route:          "/api/v1/admin/subscriptions/assign",
				ActorScope:     "admin:99",
				IdempotencyKey: "persistent-incomplete",
				Payload:        map[string]any{"user_id": 173},
				RequireKey:     true,
				Persistent:     true,
			}
			fingerprint, err := BuildIdempotencyFingerprint(opts.Method, opts.Route, opts.ActorScope, opts.Payload)
			require.NoError(t, err)
			past := time.Now().Add(-time.Hour)
			keyHash := HashIdempotencyKey(opts.IdempotencyKey)
			repo.data[repo.key(opts.Scope, keyHash)] = &IdempotencyRecord{
				ID:                 1,
				Scope:              opts.Scope,
				IdempotencyKeyHash: keyHash,
				RequestFingerprint: fingerprint,
				Status:             status,
				LockedUntil:        &past,
				ExpiresAt:          past,
				Persistent:         true,
			}

			executeCalls := 0
			_, err = coordinator.Execute(context.Background(), opts, func(context.Context) (any, error) {
				executeCalls++
				return map[string]any{"unexpected": true}, nil
			})
			require.Error(t, err)
			require.Equal(t, infraerrors.Reason(ErrIdempotencyManualRecovery), infraerrors.Reason(err))
			require.Zero(t, executeCalls)
		})
	}
}
