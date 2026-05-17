package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserSubscriptionLockedRateForGroup(t *testing.T) {
	sub := &UserSubscription{LockedRates: map[string]float64{
		"1": 1.0,
		"5": 3.5,
	}}

	rate, ok := sub.LockedRateForGroup(5)
	require.True(t, ok)
	require.Equal(t, 3.5, rate)

	_, ok = sub.LockedRateForGroup(2)
	require.False(t, ok)
}

func TestResolveEffectiveRateMultiplier_LockedRatesOverrideUserGroupRate(t *testing.T) {
	userRate := 0.3
	resolver := newUserGroupRateResolver(
		&openAIUserGroupRateRepoStub{rate: &userRate},
		nil,
		0,
		nil,
		"service.locked_rates.test",
	)
	sub := &UserSubscription{LockedRates: map[string]float64{"5": 3.5}}
	groupID := int64(5)

	got := resolveEffectiveRateMultiplier(
		context.Background(),
		resolver,
		21,
		&groupID,
		&Group{ID: groupID, RateMultiplier: 0.7},
		sub,
		1.0,
	)

	require.Equal(t, 3.5, got.Multiplier)
	require.Equal(t, RateMultiplierSourceLockedRates, got.Source)
}

func TestMonthlyLockedRateForGroup(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		want     float64
		ok       bool
	}{
		{name: "cc-default", platform: "anthropic", want: 1.0, ok: true},
		{name: "openai-default", platform: "openai", want: 1.0, ok: true},
		{name: "cc-antigravity", platform: "anthropic", want: 3.5, ok: true},
		{name: "claude-Max pool", platform: "anthropic", want: 8.5, ok: true},
		{name: "gemini-default", platform: "gemini", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := monthlyLockedRateForGroup(tt.name, tt.platform)
			require.Equal(t, tt.ok, ok)
			if tt.ok {
				require.Equal(t, tt.want, got)
			}
		})
	}
}
