package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpsRequestLifecycleCountsLogicalAndAttemptStages(t *testing.T) {
	start := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	tracker := newOpsRequestLifecycleTracker(start)
	handle := tracker.begin(start.Add(2 * time.Second))

	handle.acceptedAt(start.Add(3 * time.Second))
	handle.acceptedAt(start.Add(4 * time.Second))
	handle.completeAt(start.Add(5*time.Second), 200, nil)
	handle.completeAt(start.Add(6*time.Second), 500, errors.New("duplicate"))

	snapshot := tracker.snapshot(start, start.Add(time.Minute))
	require.Equal(t, uint64(1), snapshot.OfferedCount)
	require.Equal(t, uint64(1), snapshot.AcceptedCount)
	require.Equal(t, uint64(2), snapshot.AcceptedAttemptCount)
	require.Equal(t, uint64(1), snapshot.CompletedCount)
	require.Equal(t, uint64(1), snapshot.SucceededCount)
	require.Zero(t, snapshot.ActiveCount)
	require.InDelta(t, 1.0, snapshot.OfferedRPM, 0.0001)
}

func TestOpsRequestLifecycleFailoverKeepsOneLogicalOffer(t *testing.T) {
	start := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	tracker := newOpsRequestLifecycleTracker(start)
	handle := tracker.begin(start.Add(time.Second))

	// A retry on a second account is a second accepted attempt, not a second
	// logical request. The handler keeps this handle open until the final retry
	// outcome is known.
	handle.acceptedAt(start.Add(2 * time.Second))
	handle.acceptedAt(start.Add(3 * time.Second))
	handle.completeAt(start.Add(4*time.Second), 200, nil)

	snapshot := tracker.snapshot(start, start.Add(time.Minute))
	require.Equal(t, uint64(1), snapshot.OfferedCount)
	require.Equal(t, uint64(2), snapshot.AcceptedAttemptCount)
	require.Equal(t, uint64(1), snapshot.AcceptedCount)
	require.Equal(t, uint64(1), snapshot.CompletedCount)
	require.Equal(t, uint64(1), snapshot.SucceededCount)
}

func TestOpsRequestLifecycleClassifiesRejectedFailedAndCanceled(t *testing.T) {
	start := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	tracker := newOpsRequestLifecycleTracker(start)

	rejected := tracker.begin(start.Add(time.Second))
	rejected.completeAt(start.Add(2*time.Second), 429, nil)

	failed := tracker.begin(start.Add(3 * time.Second))
	failed.acceptedAt(start.Add(4 * time.Second))
	failed.completeAt(start.Add(5*time.Second), 502, nil)

	canceled := tracker.begin(start.Add(6 * time.Second))
	canceled.completeAt(start.Add(7*time.Second), 200, context.Canceled)

	snapshot := tracker.snapshot(start, start.Add(time.Minute))
	require.Equal(t, uint64(3), snapshot.CompletedCount)
	require.Equal(t, uint64(1), snapshot.RejectedCount)
	require.Equal(t, uint64(1), snapshot.FailedCount)
	require.Equal(t, uint64(1), snapshot.CanceledCount)
	require.Zero(t, snapshot.SucceededCount)
	require.Zero(t, snapshot.ActiveCount)
}

func TestOpsRequestLifecycleClassifiesAcceptedErrorAsFailed(t *testing.T) {
	start := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	tracker := newOpsRequestLifecycleTracker(start)
	handle := tracker.begin(start.Add(time.Second))
	handle.acceptedAt(start.Add(2 * time.Second))
	handle.completeAt(start.Add(3*time.Second), 200, errors.New("upstream websocket failed"))

	snapshot := tracker.snapshot(start, start.Add(time.Minute))
	require.Equal(t, uint64(1), snapshot.CompletedCount)
	require.Equal(t, uint64(1), snapshot.FailedCount)
	require.Zero(t, snapshot.CanceledCount)
}

func TestOpsRequestLifecycleClassifiesTerminalStatusWithoutAcceptedAttempt(t *testing.T) {
	start := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	tracker := newOpsRequestLifecycleTracker(start)

	localSuccess := tracker.begin(start.Add(time.Second))
	localSuccess.completeAt(start.Add(2*time.Second), 200, nil)

	upstreamUnavailable := tracker.begin(start.Add(3 * time.Second))
	upstreamUnavailable.completeAt(start.Add(4*time.Second), 503, nil)

	snapshot := tracker.snapshot(start, start.Add(time.Minute))
	require.Equal(t, uint64(2), snapshot.CompletedCount)
	require.Equal(t, uint64(1), snapshot.SucceededCount)
	require.Equal(t, uint64(1), snapshot.FailedCount)
	require.Zero(t, snapshot.RejectedCount)
	require.Zero(t, snapshot.AcceptedCount)
}

func TestOpsRequestLifecycleTerminalErrorWinsOverUnaccepted(t *testing.T) {
	start := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	tracker := newOpsRequestLifecycleTracker(start)
	handle := tracker.begin(start.Add(time.Second))
	handle.completeAt(start.Add(2*time.Second), 200, errors.New("panic recovered"))

	snapshot := tracker.snapshot(start, start.Add(time.Minute))
	require.Equal(t, uint64(1), snapshot.CompletedCount)
	require.Equal(t, uint64(1), snapshot.FailedCount)
	require.Zero(t, snapshot.RejectedCount)
}

func TestOpsRequestLifecycleWindowAndPrewarm(t *testing.T) {
	start := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	tracker := newOpsRequestLifecycleTracker(start)
	old := tracker.begin(start.Add(time.Second))
	old.completeAt(start.Add(2*time.Second), 429, nil)

	recentAt := start.Add(50 * time.Second)
	recent := tracker.begin(recentAt)
	recent.acceptedAt(recentAt)
	recent.completeAt(recentAt, 200, nil)
	tracker.recordPrewarm(recentAt, "started", 0)
	tracker.recordPrewarm(recentAt, "succeeded", 120*time.Millisecond)
	tracker.recordPrewarm(recentAt, "skipped", 0)

	snapshot := tracker.snapshot(start.Add(45*time.Second), start.Add(time.Minute))
	require.Equal(t, uint64(1), snapshot.OfferedCount)
	require.Equal(t, uint64(1), snapshot.CompletedCount)
	require.Equal(t, uint64(1), snapshot.CachePrewarm.StartedCount)
	require.Equal(t, uint64(1), snapshot.CachePrewarm.SucceededCount)
	require.Equal(t, uint64(1), snapshot.CachePrewarm.SkippedCount)
	require.Equal(t, uint64(120), snapshot.CachePrewarm.TotalDurationMs)
	require.InDelta(t, 120, snapshot.CachePrewarm.AverageDurationMs, 0.0001)
}

func TestOpsRequestLifecycleSnapshotUsesProcessUptimeDenominator(t *testing.T) {
	start := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	tracker := newOpsRequestLifecycleTracker(start)
	handle := tracker.begin(start.Add(5 * time.Second))
	handle.completeAt(start.Add(6*time.Second), 200, nil)

	snapshot := tracker.snapshot(start.Add(-time.Minute), start.Add(30*time.Second))
	require.Equal(t, int64(30), snapshot.WindowSeconds)
	require.NotEmpty(t, snapshot.ProcessInstanceID)
	require.Equal(t, "process_current", snapshot.ActiveCountScope)
}

func TestOpsRequestLifecycleConcurrentUpdates(t *testing.T) {
	tracker := newOpsRequestLifecycleTracker(time.Now().UTC())
	const workers = 8
	const iterations = 100
	done := make(chan struct{}, workers)
	for i := 0; i < workers; i++ {
		go func() {
			for j := 0; j < iterations; j++ {
				at := time.Now().UTC()
				handle := tracker.begin(at)
				handle.acceptedAt(at)
				handle.completeAt(at, 200, nil)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < workers; i++ {
		<-done
	}
	snapshot := tracker.snapshot(time.Now().UTC().Add(-time.Minute), time.Now().UTC().Add(time.Second))
	require.Equal(t, uint64(workers*iterations), snapshot.OfferedCount)
	require.Equal(t, uint64(workers*iterations), snapshot.CompletedCount)
	require.Equal(t, uint64(workers*iterations), snapshot.AcceptedAttemptCount)
	require.Zero(t, snapshot.ActiveCount)
}

func BenchmarkOpsRequestLifecycleConcurrent(b *testing.B) {
	tracker := newOpsRequestLifecycleTracker(time.Now().UTC())
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			at := time.Now().UTC()
			handle := tracker.begin(at)
			handle.acceptedAt(at)
			handle.completeAt(at, 200, nil)
		}
	})
}
