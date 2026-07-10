//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRateLimitService_HandleUpstreamErrorForModel_OpenAIGPT56UsesModelIsolationOnly(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		mapping   map[string]any
		wantScope string
		repoErr   error
	}{
		{
			name:  "exact tier",
			model: "gpt-5.6-sol",
			mapping: map[string]any{
				"gpt-5.6-sol": "openai/gpt-5.6-sol-high",
			},
			wantScope: "openai/gpt-5.6-sol-high",
		},
		{
			name:  "repository failure never falls back to account disable",
			model: "gpt-5.6-terra",
			mapping: map[string]any{
				"gpt-5.6-terra": "gpt-5.6-terra",
			},
			wantScope: "gpt-5.6-terra",
			repoErr:   errors.New("database unavailable"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{modelRateLimitErr: tt.repoErr}
			counter := &openAI403CounterCacheStub{counts: []int64{3}}
			service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			service.SetOpenAI403CounterCache(counter)
			account := &Account{
				ID: 401, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"model_mapping": tt.mapping},
			}

			shouldFailover := service.HandleUpstreamErrorForModel(
				context.Background(), account, tt.model, http.StatusForbidden, http.Header{},
				[]byte(`{"error":{"message":"model access denied"}}`),
			)

			require.True(t, shouldFailover)
			require.Equal(t, 1, repo.modelRateLimitCalls)
			require.Equal(t, tt.wantScope, repo.lastModelRateLimitKey)
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
			require.Zero(t, counter.increments)
			if tt.repoErr == nil {
				require.WithinDuration(t, time.Now().Add(180*time.Minute), repo.lastModelRateLimitAt, 5*time.Second)
				account.Extra = map[string]any{
					modelRateLimitsKey: map[string]any{
						tt.wantScope: map[string]any{"rate_limit_reset_at": repo.lastModelRateLimitAt.Format(time.RFC3339)},
					},
				}
				require.False(t, account.IsSchedulableForModel(tt.model))
				require.True(t, account.IsSchedulableForModel("gpt-5.4"))
			}
		})
	}
}

func TestRateLimitService_HandleUpstreamErrorForModel_UnentitledOrInvalidGPT56FallsBackToExistingAccountPolicy(t *testing.T) {
	for _, tt := range []struct {
		name  string
		model string
	}{
		{name: "exact tier without entitlement", model: "gpt-5.6-sol"},
		{name: "invalid tier", model: "gpt-5.6-unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			counter := &openAI403CounterCacheStub{counts: []int64{1}}
			service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			service.SetOpenAI403CounterCache(counter)
			account := &Account{ID: 402, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

			shouldDisable := service.HandleUpstreamErrorForModel(
				context.Background(), account, tt.model, http.StatusForbidden, http.Header{},
				[]byte(`{"error":{"message":"workspace forbidden"},"secret":"must-not-be-logged-raw"}`),
			)

			require.True(t, shouldDisable)
			require.Zero(t, repo.modelRateLimitCalls)
			require.Equal(t, 1, repo.tempCalls)
			require.Equal(t, 1, counter.increments)
		})
	}
}

func TestRateLimitService_HandleUpstreamError_OpenAI403FirstHitTempUnschedulable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)
	account := &Account{
		ID:       301,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"temporary edge rejection"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Contains(t, repo.lastTempReason, "temporary edge rejection")
	require.Contains(t, repo.lastTempReason, "(1/3)")
}

func TestRateLimitService_HandleUpstreamError_OpenAI403ThresholdDisables(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{3}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)
	account := &Account{
		ID:       302,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"workspace forbidden by policy"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "workspace forbidden by policy")
	require.Contains(t, repo.lastErrorMsg, "consecutive_403=3/3")
}
