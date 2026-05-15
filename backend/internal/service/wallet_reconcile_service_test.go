//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type stubReconcileRepo struct {
	fakeWalletRepo
	drifts []WalletReconcileDrift
	err    error
	calls  int
}

func (s *stubReconcileRepo) ReconcileBalances(_ context.Context, _ float64) ([]WalletReconcileDrift, error) {
	s.calls++
	return s.drifts, s.err
}

// TestWalletReconcile_DriftIncrementsMetric 漂移记录必须 +1
// walletLedgerDriftTotal，并以漂移条数累加。
func TestWalletReconcile_DriftIncrementsMetric(t *testing.T) {
	resetWalletMetricsForTest()

	repo := &stubReconcileRepo{
		drifts: []WalletReconcileDrift{
			{SubscriptionID: 1, Cached: 100, LedgerSum: 99, Drift: 1},
			{SubscriptionID: 2, Cached: 50, LedgerSum: 47, Drift: 3},
		},
	}
	svc := NewWalletReconcileService(repo, 0, 0.01) // interval=0 → 不起 goroutine
	svc.runOnce()

	snap := SnapshotWalletMetrics()
	require.Equal(t, int64(2), snap.LedgerDriftTotal)
	require.Equal(t, 1, repo.calls)
}

// TestWalletReconcile_NoDriftLeavesMetricUntouched 全部对账通过时不应递增。
func TestWalletReconcile_NoDriftLeavesMetricUntouched(t *testing.T) {
	resetWalletMetricsForTest()

	repo := &stubReconcileRepo{drifts: nil}
	svc := NewWalletReconcileService(repo, 0, 0.01)
	svc.runOnce()

	snap := SnapshotWalletMetrics()
	require.Equal(t, int64(0), snap.LedgerDriftTotal)
}

// TestWalletReconcile_RepoErrorIsLoggedNotPanic repo 报错时静默继续，不影响
// 后续 tick；也不增加 drift 计数（错误 ≠ 漂移）。
func TestWalletReconcile_RepoErrorIsLoggedNotPanic(t *testing.T) {
	resetWalletMetricsForTest()

	repo := &stubReconcileRepo{err: errors.New("db down")}
	svc := NewWalletReconcileService(repo, 0, 0.01)
	require.NotPanics(t, svc.runOnce)
	require.Equal(t, int64(0), SnapshotWalletMetrics().LedgerDriftTotal)
}

// TestSnapshotWalletMetrics_HandlerInsufficientCounter 公开的
// IncWalletInsufficientTotal 必须直接反映在 snapshot.InsufficientTotal。
func TestSnapshotWalletMetrics_HandlerInsufficientCounter(t *testing.T) {
	resetWalletMetricsForTest()

	IncWalletInsufficientTotal()
	IncWalletInsufficientTotal()

	require.Equal(t, int64(2), SnapshotWalletMetrics().InsufficientTotal)
}

// TestCheckWalletEligibility_LowBalanceIncrementsMetric 余额 > 0 但 ≤ 阈值
// 应通过预检（不返错误）但 +1 walletBalanceLowTotal，让前端弹「即将耗尽」横幅。
func TestCheckWalletEligibility_LowBalanceIncrementsMetric(t *testing.T) {
	resetWalletMetricsForTest()

	low := walletBalanceLowThresholdUSD - 0.01
	high := walletBalanceLowThresholdUSD + 100.0

	s := &BillingCacheService{}
	require.NoError(t, s.checkWalletEligibility(&UserSubscription{ID: 1, WalletBalanceUSD: &high}))
	require.Equal(t, int64(0), SnapshotWalletMetrics().BalanceLowTotal,
		"high balance must not trigger low-balance counter")

	require.NoError(t, s.checkWalletEligibility(&UserSubscription{ID: 1, WalletBalanceUSD: &low}))
	require.Equal(t, int64(1), SnapshotWalletMetrics().BalanceLowTotal,
		"low balance must trigger low-balance counter")
}
