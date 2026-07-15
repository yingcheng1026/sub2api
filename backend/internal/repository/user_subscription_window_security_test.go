package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAdvanceUsageWindowRejectsStaleResetAfterNewUsage(t *testing.T) {
	ctx, client, repo, subID := newUsageWindowSecurityFixture(t, 11)
	oldStart := time.Now().UTC().Add(-31 * 24 * time.Hour).Truncate(time.Second)
	newStart := time.Now().UTC().Truncate(time.Second)

	for _, window := range []service.SubscriptionUsageWindow{
		service.SubscriptionUsageWindowDaily,
		service.SubscriptionUsageWindowWeekly,
		service.SubscriptionUsageWindowMonthly,
	} {
		setSubscriptionWindowState(t, ctx, client, subID, window, &oldStart, 11)
		advanced, err := repo.AdvanceUsageWindow(ctx, subID, service.SubscriptionUsageWindowAdvance{
			Window:        window,
			ExpectedStart: &oldStart,
			NewStart:      newStart,
			ResetUsage:    true,
		})
		require.NoError(t, err)
		require.True(t, advanced)

		setSubscriptionWindowUsage(t, ctx, client, subID, window, 5)
		advanced, err = repo.AdvanceUsageWindow(ctx, subID, service.SubscriptionUsageWindowAdvance{
			Window:        window,
			ExpectedStart: &oldStart,
			NewStart:      newStart.Add(time.Minute),
			ResetUsage:    true,
		})
		require.NoError(t, err)
		require.False(t, advanced)
		requireSubscriptionWindowState(t, ctx, repo, subID, window, newStart, 5)
	}
}

func TestAdvanceUsageWindowFirstActivationPreservesConcurrentUsage(t *testing.T) {
	ctx, client, repo, subID := newUsageWindowSecurityFixture(t, 7)
	start := time.Now().UTC().Truncate(time.Second)

	advanced, err := repo.AdvanceUsageWindow(ctx, subID, service.SubscriptionUsageWindowAdvance{
		Window:        service.SubscriptionUsageWindowDaily,
		ExpectedStart: nil,
		NewStart:      start,
		ResetUsage:    false,
	})
	require.NoError(t, err)
	require.True(t, advanced)
	requireSubscriptionWindowState(t, ctx, repo, subID, service.SubscriptionUsageWindowDaily, start, 7)

	setSubscriptionWindowUsage(t, ctx, client, subID, service.SubscriptionUsageWindowDaily, 9)
	advanced, err = repo.AdvanceUsageWindow(ctx, subID, service.SubscriptionUsageWindowAdvance{
		Window:        service.SubscriptionUsageWindowDaily,
		ExpectedStart: nil,
		NewStart:      start.Add(time.Minute),
		ResetUsage:    false,
	})
	require.NoError(t, err)
	require.False(t, advanced)
	requireSubscriptionWindowState(t, ctx, repo, subID, service.SubscriptionUsageWindowDaily, start, 9)
}

func newUsageWindowSecurityFixture(t *testing.T, usage float64) (context.Context, *dbent.Client, *userSubscriptionRepository, int64) {
	t.Helper()
	ctx := context.Background()
	client := newSecuritySecretTestClient(t)
	user, err := client.User.Create().
		SetEmail("window-security-" + time.Now().Format("150405.000000000") + "@example.test").
		SetPasswordHash("not-a-real-password-hash").
		Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().SetName("window-security-group-" + time.Now().Format("150405.000000000")).Save(ctx)
	require.NoError(t, err)
	sub, err := client.UserSubscription.Create().
		SetUserID(user.ID).
		SetGroupID(group.ID).
		SetStartsAt(time.Now()).
		SetExpiresAt(time.Now().Add(90 * 24 * time.Hour)).
		SetDailyUsageUsd(usage).
		SetWeeklyUsageUsd(usage).
		SetMonthlyUsageUsd(usage).
		Save(ctx)
	require.NoError(t, err)
	return ctx, client, &userSubscriptionRepository{client: client}, sub.ID
}

func setSubscriptionWindowState(t *testing.T, ctx context.Context, client *dbent.Client, id int64, window service.SubscriptionUsageWindow, start *time.Time, usage float64) {
	t.Helper()
	update := client.UserSubscription.UpdateOneID(id)
	switch window {
	case service.SubscriptionUsageWindowDaily:
		update.SetNillableDailyWindowStart(start).SetDailyUsageUsd(usage)
	case service.SubscriptionUsageWindowWeekly:
		update.SetNillableWeeklyWindowStart(start).SetWeeklyUsageUsd(usage)
	case service.SubscriptionUsageWindowMonthly:
		update.SetNillableMonthlyWindowStart(start).SetMonthlyUsageUsd(usage)
	}
	_, err := update.Save(ctx)
	require.NoError(t, err)
}

func setSubscriptionWindowUsage(t *testing.T, ctx context.Context, client *dbent.Client, id int64, window service.SubscriptionUsageWindow, usage float64) {
	t.Helper()
	setSubscriptionWindowState(t, ctx, client, id, window, subscriptionWindowStart(t, ctx, &userSubscriptionRepository{client: client}, id, window), usage)
}

func subscriptionWindowStart(t *testing.T, ctx context.Context, repo *userSubscriptionRepository, id int64, window service.SubscriptionUsageWindow) *time.Time {
	t.Helper()
	sub, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	switch window {
	case service.SubscriptionUsageWindowDaily:
		return sub.DailyWindowStart
	case service.SubscriptionUsageWindowWeekly:
		return sub.WeeklyWindowStart
	default:
		return sub.MonthlyWindowStart
	}
}

func requireSubscriptionWindowState(t *testing.T, ctx context.Context, repo *userSubscriptionRepository, id int64, window service.SubscriptionUsageWindow, wantStart time.Time, wantUsage float64) {
	t.Helper()
	sub, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	var start *time.Time
	var usage float64
	switch window {
	case service.SubscriptionUsageWindowDaily:
		start, usage = sub.DailyWindowStart, sub.DailyUsageUSD
	case service.SubscriptionUsageWindowWeekly:
		start, usage = sub.WeeklyWindowStart, sub.WeeklyUsageUSD
	case service.SubscriptionUsageWindowMonthly:
		start, usage = sub.MonthlyWindowStart, sub.MonthlyUsageUSD
	}
	require.NotNil(t, start)
	require.WithinDuration(t, wantStart, *start, time.Microsecond)
	require.InDelta(t, wantUsage, usage, 0.000001)
}
