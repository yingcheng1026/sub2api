//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type windowMaintenanceRepoStub struct {
	userSubRepoNoop
	mu  sync.Mutex
	sub *UserSubscription
}

func (r *windowMaintenanceRepoStub) GetByID(_ context.Context, id int64) (*UserSubscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sub == nil || r.sub.ID != id {
		return nil, ErrSubscriptionNotFound
	}
	cloned := *r.sub
	return &cloned, nil
}

func (r *windowMaintenanceRepoStub) AdvanceUsageWindow(_ context.Context, id int64, advance SubscriptionUsageWindowAdvance) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sub == nil || r.sub.ID != id {
		return false, ErrSubscriptionNotFound
	}
	current := r.windowStart(advance.Window)
	if !sameOptionalTime(current, advance.ExpectedStart) {
		return false, nil
	}
	r.setWindow(advance)
	return true, nil
}

func (r *windowMaintenanceRepoStub) IncrementUsage(_ context.Context, id int64, cost float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sub == nil || r.sub.ID != id {
		return ErrSubscriptionNotFound
	}
	r.sub.DailyUsageUSD += cost
	r.sub.WeeklyUsageUSD += cost
	r.sub.MonthlyUsageUSD += cost
	return nil
}

func (r *windowMaintenanceRepoStub) windowStart(window SubscriptionUsageWindow) *time.Time {
	switch window {
	case SubscriptionUsageWindowDaily:
		return r.sub.DailyWindowStart
	case SubscriptionUsageWindowWeekly:
		return r.sub.WeeklyWindowStart
	default:
		return r.sub.MonthlyWindowStart
	}
}

func (r *windowMaintenanceRepoStub) setWindow(advance SubscriptionUsageWindowAdvance) {
	start := advance.NewStart
	switch advance.Window {
	case SubscriptionUsageWindowDaily:
		r.sub.DailyWindowStart = &start
		if advance.ResetUsage {
			r.sub.DailyUsageUSD = 0
		}
	case SubscriptionUsageWindowWeekly:
		r.sub.WeeklyWindowStart = &start
		if advance.ResetUsage {
			r.sub.WeeklyUsageUSD = 0
		}
	case SubscriptionUsageWindowMonthly:
		r.sub.MonthlyWindowStart = &start
		if advance.ResetUsage {
			r.sub.MonthlyUsageUSD = 0
		}
	}
}

func sameOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func TestWindowMaintenanceStaleSecondPassCannotEraseNewUsage(t *testing.T) {
	oldDaily := time.Now().Add(-25 * time.Hour)
	recentWeekly := time.Now().Add(-time.Hour)
	recentMonthly := time.Now().Add(-time.Hour)
	repo := &windowMaintenanceRepoStub{sub: &UserSubscription{
		ID: 71, UserID: 8, GroupID: ptrInt64(9), Status: SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(24 * time.Hour), DailyWindowStart: &oldDaily,
		WeeklyWindowStart: &recentWeekly, MonthlyWindowStart: &recentMonthly,
		DailyUsageUSD: 12,
	}}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)

	first, err := svc.DoWindowMaintenance(context.Background(), 71)
	require.NoError(t, err)
	require.Zero(t, first.DailyUsageUSD)
	require.NoError(t, repo.IncrementUsage(context.Background(), 71, 5))

	second, err := svc.DoWindowMaintenance(context.Background(), 71)
	require.NoError(t, err)
	require.InDelta(t, 5, second.DailyUsageUSD, 0.000001)
	require.WithinDuration(t, *first.DailyWindowStart, *second.DailyWindowStart, time.Microsecond)
}

func TestWindowMaintenanceFirstActivationPreservesAlreadyRecordedUsage(t *testing.T) {
	repo := &windowMaintenanceRepoStub{sub: &UserSubscription{
		ID: 72, UserID: 8, GroupID: ptrInt64(9), Status: SubscriptionStatusActive,
		StartsAt: time.Now(), ExpiresAt: time.Now().Add(24 * time.Hour),
		DailyUsageUSD: 3, WeeklyUsageUSD: 3, MonthlyUsageUSD: 3,
	}}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)

	maintained, err := svc.DoWindowMaintenance(context.Background(), 72)
	require.NoError(t, err)
	require.InDelta(t, 3, maintained.DailyUsageUSD, 0.000001)
	require.InDelta(t, 3, maintained.WeeklyUsageUSD, 0.000001)
	require.InDelta(t, 3, maintained.MonthlyUsageUSD, 0.000001)
	require.NotNil(t, maintained.DailyWindowStart)
	require.NotNil(t, maintained.WeeklyWindowStart)
	require.NotNil(t, maintained.MonthlyWindowStart)
}

func TestValidateAndCheckLimitsDoesNotMutateCachedSubscription(t *testing.T) {
	oldDaily := time.Now().Add(-25 * time.Hour)
	limit := 1.0
	sub := &UserSubscription{
		DailyWindowStart: &oldDaily,
		DailyUsageUSD:    99,
		Status:           SubscriptionStatusActive,
		ExpiresAt:        time.Now().Add(time.Hour),
	}
	group := &Group{DailyLimitUSD: &limit}
	svc := &SubscriptionService{}

	needsMaintenance, err := svc.ValidateAndCheckLimits(sub, group)
	require.NoError(t, err)
	require.True(t, needsMaintenance)
	require.InDelta(t, 99, sub.DailyUsageUSD, 0.000001)
}

func TestWindowMaintenanceWorkerPanicFailsClosedWithoutHangingCaller(t *testing.T) {
	cfg := &config.Config{}
	cfg.SubscriptionMaintenance.WorkerCount = 1
	cfg.SubscriptionMaintenance.QueueSize = 1
	svc := NewSubscriptionService(groupRepoNoop{}, userSubRepoNoop{}, nil, nil, cfg)
	t.Cleanup(svc.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := svc.DoWindowMaintenance(ctx, 73)
	require.Error(t, err)
	require.ErrorContains(t, err, "subscription window maintenance panic")
}
