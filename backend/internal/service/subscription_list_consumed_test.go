//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

// listConsumedUserSubRepoStub returns canned subscriptions from List() and panics on others.
type listConsumedUserSubRepoStub struct {
	userSubRepoNoop
	rows []UserSubscription
}

func (s *listConsumedUserSubRepoStub) List(_ context.Context, params pagination.PaginationParams, _, _ *int64, _, _, _, _ string) ([]UserSubscription, *pagination.PaginationResult, error) {
	rows := make([]UserSubscription, len(s.rows))
	copy(rows, s.rows)
	return rows, &pagination.PaginationResult{
		Total:    int64(len(rows)),
		Page:     params.Page,
		PageSize: params.PageSize,
		Pages:    1,
	}, nil
}

// fakeSubscriptionUsageReader stubs SUM(actual_cost) lookups by (user_id, group_id).
type fakeSubscriptionUsageReader struct {
	totals map[string]float64
	calls  []usagestats.UsageLogFilters
}

func (f *fakeSubscriptionUsageReader) GetStatsWithFilters(_ context.Context, filters usagestats.UsageLogFilters) (*usagestats.UsageStats, error) {
	f.calls = append(f.calls, filters)
	key := strconvFormatInt(filters.UserID) + ":" + strconvFormatInt(filters.GroupID)
	return &usagestats.UsageStats{TotalActualCost: f.totals[key]}, nil
}

// Bug fix: standard-mode groups never increment monthly_usage_usd, so the
// admin SubscriptionsView used to display 0 even when the user actually spent
// money via balance deduction. The List path must aggregate from usage_logs.
func TestSubscriptionService_List_StandardGroupConsumedFromUsageLogs(t *testing.T) {
	standardGroup := &Group{ID: 10, Name: "paid-trial-bonus", SubscriptionType: SubscriptionTypeStandard}
	sub := UserSubscription{
		ID:              1,
		UserID:          100,
		GroupID:         10,
		Status:          SubscriptionStatusActive,
		StartsAt:        time.Now().Add(-24 * time.Hour),
		ExpiresAt:       time.Now().Add(72 * time.Hour),
		MonthlyUsageUSD: 0, // standard groups never bump this
		Group:           standardGroup,
	}
	repo := &listConsumedUserSubRepoStub{rows: []UserSubscription{sub}}
	reader := &fakeSubscriptionUsageReader{totals: map[string]float64{"100:10": 7.5}}

	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	svc.SetUsageReader(reader)

	out, _, err := svc.List(context.Background(), 1, 20, nil, nil, "", "", "created_at", "desc")
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.InDelta(t, 7.5, out[0].ConsumedUSD, 0.0001,
		"standard-type group consumed should come from SUM(usage_logs.actual_cost) by user+group")
	require.Len(t, reader.calls, 1, "should query usage_logs once for the standard subscription")
	require.Equal(t, int64(100), reader.calls[0].UserID)
	require.Equal(t, int64(10), reader.calls[0].GroupID)
}

// Subscription-mode groups already track consumption in monthly_usage_usd.
// The List path should keep that source of truth and avoid extra usage_logs queries.
func TestSubscriptionService_List_SubscriptionGroupConsumedKeepsMonthlyUsage(t *testing.T) {
	subscriptionGroup := &Group{ID: 20, Name: "monthly-pro", SubscriptionType: SubscriptionTypeSubscription}
	monthlyStart := time.Now().Add(-1 * time.Hour)
	sub := UserSubscription{
		ID:                 2,
		UserID:             200,
		GroupID:            20,
		Status:             SubscriptionStatusActive,
		StartsAt:           time.Now().Add(-24 * time.Hour),
		ExpiresAt:          time.Now().Add(72 * time.Hour),
		MonthlyWindowStart: &monthlyStart,
		MonthlyUsageUSD:    42.0,
		Group:              subscriptionGroup,
	}
	repo := &listConsumedUserSubRepoStub{rows: []UserSubscription{sub}}
	reader := &fakeSubscriptionUsageReader{totals: map[string]float64{"200:20": 99.99}}

	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	svc.SetUsageReader(reader)

	out, _, err := svc.List(context.Background(), 1, 20, nil, nil, "", "", "created_at", "desc")
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.InDelta(t, 42.0, out[0].ConsumedUSD, 0.0001,
		"subscription-type group consumed should equal MonthlyUsageUSD (current period)")
	require.Empty(t, reader.calls, "should NOT query usage_logs for subscription-type groups")
}

// When usageReader is not configured, List should still succeed and leave
// ConsumedUSD at zero rather than panic. Wire DI may not always inject a reader.
func TestSubscriptionService_List_NoUsageReaderLeavesConsumedZero(t *testing.T) {
	standardGroup := &Group{ID: 30, SubscriptionType: SubscriptionTypeStandard}
	sub := UserSubscription{
		ID:        3,
		UserID:    300,
		GroupID:   30,
		Status:    SubscriptionStatusActive,
		StartsAt:  time.Now().Add(-24 * time.Hour),
		ExpiresAt: time.Now().Add(72 * time.Hour),
		Group:     standardGroup,
	}
	repo := &listConsumedUserSubRepoStub{rows: []UserSubscription{sub}}

	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	// Deliberately no SetUsageReader call.

	out, _, err := svc.List(context.Background(), 1, 20, nil, nil, "", "", "created_at", "desc")
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, 0.0, out[0].ConsumedUSD)
}
