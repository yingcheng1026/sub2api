package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type usageBillingReplayLogRepoStub struct {
	UsageLogRepository
	logs []*UsageLog
}

func (s *usageBillingReplayLogRepoStub) Create(_ context.Context, log *UsageLog) (bool, error) {
	s.logs = append(s.logs, log)
	return len(s.logs) == 1, nil
}

type usageBillingReplayCacheStub struct {
	balances      []int64
	subscriptions [][2]int64
	rateLimits    []int64
}

func (s *usageBillingReplayCacheStub) InvalidateUserBalance(_ context.Context, userID int64) error {
	s.balances = append(s.balances, userID)
	return nil
}

func (s *usageBillingReplayCacheStub) InvalidateSubscription(_ context.Context, userID, groupID int64) error {
	s.subscriptions = append(s.subscriptions, [2]int64{userID, groupID})
	return nil
}

func (s *usageBillingReplayCacheStub) InvalidateAPIKeyRateLimit(_ context.Context, apiKeyID int64) error {
	s.rateLimits = append(s.rateLimits, apiKeyID)
	return nil
}

type usageBillingReplayAuthStub struct {
	users    []int64
	locators []string
	err      error
}

func (*usageBillingReplayAuthStub) InvalidateAuthCacheByKey(context.Context, string) {}
func (s *usageBillingReplayAuthStub) InvalidateAuthCacheByUserID(_ context.Context, userID int64) {
	s.users = append(s.users, userID)
}
func (*usageBillingReplayAuthStub) InvalidateAuthCacheByGroupID(context.Context, int64) {}
func (s *usageBillingReplayAuthStub) InvalidateAuthCacheByUserIDReliable(_ context.Context, userID int64) error {
	s.users = append(s.users, userID)
	return s.err
}
func (s *usageBillingReplayAuthStub) InvalidateAuthCacheByLocatorReliable(_ context.Context, locator string) error {
	s.locators = append(s.locators, locator)
	return s.err
}

type usageBillingReplayTouchStub struct {
	accounts []int64
}

func (s *usageBillingReplayTouchStub) ScheduleLastUsedUpdate(accountID int64) {
	s.accounts = append(s.accounts, accountID)
}

func TestUsageBillingReplayWriter_WritesFrozenSnapshot(t *testing.T) {
	envelope, err := NewUsageBillingEnvelope(validUsageBillingEnvelopeInput())
	require.NoError(t, err)
	repo := &usageBillingReplayLogRepoStub{}
	writer := NewUsageBillingReplayWriter(repo)

	require.NoError(t, writer.WriteUsageBillingReplay(context.Background(), envelope))
	require.Len(t, repo.logs, 1)
	require.Equal(t, "gpt-5.6-sol", repo.logs[0].Model)
	require.Equal(t, "gpt-5.6-sol", *repo.logs[0].BillingModel)
	require.Equal(t, envelope.RequestID(), repo.logs[0].RequestID)
}

func TestUsageBillingReplayFinalizer_UsesOnlyRepeatableInvalidations(t *testing.T) {
	cache := &usageBillingReplayCacheStub{}
	auth := &usageBillingReplayAuthStub{}
	touch := &usageBillingReplayTouchStub{}
	finalizer := NewUsageBillingReplayFinalizer(cache, auth, touch)

	balanceEnvelope, err := NewUsageBillingEnvelope(validUsageBillingEnvelopeInput())
	require.NoError(t, err)
	monthlyInput := validUsageBillingEnvelopeInput()
	monthlyInput.RequestID = "replay-monthly"
	monthlyInput.BillingType = BillingTypeSubscription
	monthlyInput.BalanceCost = 0
	monthlyInput.APIKeyQuotaCost = 0
	monthlyInput.APIKeyRateLimitCost = 0
	monthlyInput.SubscriptionCost = 2.5
	subscriptionID := int64(55)
	effectiveBillingGroupID := int64(77)
	monthlyInput.SubscriptionID = &subscriptionID
	monthlyInput.EffectiveBillingGroupID = &effectiveBillingGroupID
	monthlyEnvelope, err := NewUsageBillingEnvelope(monthlyInput)
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		require.NoError(t, finalizer.FinalizeUsageBillingReplay(context.Background(), balanceEnvelope, &UsageBillingApplyResult{Applied: i == 0}))
		require.NoError(t, finalizer.FinalizeUsageBillingReplay(context.Background(), monthlyEnvelope, &UsageBillingApplyResult{Applied: i == 0}))
	}
	require.Equal(t, []int64{22, 22}, cache.balances)
	require.Equal(t, [][2]int64{{22, 77}, {22, 77}}, cache.subscriptions)
	require.Equal(t, []int64{11, 11}, cache.rateLimits)
	require.Empty(t, auth.users)
	require.Equal(t, []string{strings.Repeat("c", 64), strings.Repeat("c", 64)}, auth.locators)
	require.Equal(t, []int64{33, 33, 33, 33}, touch.accounts)
}

func TestUsageBillingReplayFinalizer_RetriesWhenReliableAuthInvalidationFails(t *testing.T) {
	input := validUsageBillingEnvelopeInput()
	input.APIKeyQuotaCost = 1
	envelope, err := NewUsageBillingEnvelope(input)
	require.NoError(t, err)

	sentinel := errors.New("redis publish unavailable")
	finalizer := NewUsageBillingReplayFinalizer(
		&usageBillingReplayCacheStub{},
		&usageBillingReplayAuthStub{err: sentinel},
		&usageBillingReplayTouchStub{},
	)

	err = finalizer.FinalizeUsageBillingReplay(context.Background(), envelope, &UsageBillingApplyResult{Applied: true})
	require.ErrorIs(t, err, sentinel)
}
