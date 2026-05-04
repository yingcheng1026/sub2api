//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIQuotaGuardDecisionFromExtra(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(3 * time.Hour)

	tests := []struct {
		name       string
		extra      map[string]any
		wantGuard  bool
		wantReason string
	}{
		{
			name: "below threshold",
			extra: map[string]any{
				"codex_7d_used_percent": 94.9,
				"codex_7d_reset_at":     resetAt.Format(time.RFC3339),
			},
			wantGuard: false,
		},
		{
			name: "threshold with future reset",
			extra: map[string]any{
				"codex_7d_used_percent": 95.0,
				"codex_7d_reset_at":     resetAt.Format(time.RFC3339),
			},
			wantGuard:  true,
			wantReason: OpenAIQuotaGuardReasonCodex7d,
		},
		{
			name: "above threshold with expired reset",
			extra: map[string]any{
				"codex_7d_used_percent": 99.0,
				"codex_7d_reset_at":     now.Add(-1 * time.Minute).Format(time.RFC3339),
			},
			wantGuard: false,
		},
		{
			name: "above threshold without reset uses short guard",
			extra: map[string]any{
				"codex_7d_used_percent": 95.0,
				"codex_usage_updated_at": now.Add(-1 * time.Hour).
					Format(time.RFC3339),
			},
			wantGuard:  true,
			wantReason: OpenAIQuotaGuardReasonNoReset,
		},
		{
			name: "stale no-reset guard expires",
			extra: map[string]any{
				"codex_7d_used_percent": 95.0,
				"codex_usage_updated_at": now.Add(-7 * time.Hour).
					Format(time.RFC3339),
			},
			wantGuard: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openAIQuotaGuardDecisionFromExtra(tt.extra, now)
			if !tt.wantGuard {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tt.wantReason, got.Reason)
			require.True(t, got.Until.After(now))
		})
	}
}

func TestAccountIsSchedulable_OpenAIQuotaGuard(t *testing.T) {
	resetAt := time.Now().Add(2 * time.Hour).UTC()

	guarded := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			"codex_7d_used_percent": 95.0,
			"codex_7d_reset_at":     resetAt.Format(time.RFC3339),
		},
	}
	require.False(t, guarded.IsSchedulable())

	apiKey := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			"codex_7d_used_percent": 95.0,
			"codex_7d_reset_at":     resetAt.Format(time.RFC3339),
		},
	}
	require.True(t, apiKey.IsSchedulable())
}

type quotaGuardRepoStub struct {
	AccountRepository
	calls  int
	until  time.Time
	reason string
}

func (r *quotaGuardRepoStub) SetTempUnschedulable(_ context.Context, _ int64, until time.Time, reason string) error {
	r.calls++
	r.until = until
	r.reason = reason
	return nil
}

func TestApplyOpenAIQuotaGuardFromUpdates(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(4 * time.Hour)
	repo := &quotaGuardRepoStub{}

	applyOpenAIQuotaGuardFromUpdates(context.Background(), repo, 123, map[string]any{
		"codex_7d_used_percent": 95.0,
		"codex_7d_reset_at":     resetAt.Format(time.RFC3339),
	}, now)

	require.Equal(t, 1, repo.calls)
	require.Equal(t, OpenAIQuotaGuardReasonCodex7d, repo.reason)
	require.WithinDuration(t, resetAt, repo.until, time.Second)
}

func TestApplyOpenAIQuotaGuardFromUpdatesBelowThreshold(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	repo := &quotaGuardRepoStub{}

	applyOpenAIQuotaGuardFromUpdates(context.Background(), repo, 123, map[string]any{
		"codex_7d_used_percent": 94.9,
		"codex_7d_reset_at":     now.Add(4 * time.Hour).Format(time.RFC3339),
	}, now)

	require.Zero(t, repo.calls)
}
