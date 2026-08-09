package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const (
	opsRequestLifecycleRetention   = time.Hour
	opsRequestLifecyclePrunePeriod = 10 * time.Second
)

type opsRequestLifecycleBucket struct {
	offered           uint64
	accepted          uint64
	acceptedAttempts  uint64
	completed         uint64
	succeeded         uint64
	rejected          uint64
	failed            uint64
	canceled          uint64
	prewarmStarted    uint64
	prewarmSucceeded  uint64
	prewarmFailed     uint64
	prewarmSkipped    uint64
	prewarmDurationMs uint64
}

// OpsCachePrewarmSnapshot reports process-local WS generate=false prewarm
// outcomes. Prewarm activity is deliberately excluded from user throughput.
type OpsCachePrewarmSnapshot struct {
	StartedCount      uint64  `json:"started_count"`
	SucceededCount    uint64  `json:"succeeded_count"`
	FailedCount       uint64  `json:"failed_count"`
	SkippedCount      uint64  `json:"skipped_count"`
	TotalDurationMs   uint64  `json:"total_duration_ms"`
	AverageDurationMs float64 `json:"average_duration_ms"`
}

// OpsRequestLifecycleSnapshot is a process-local rolling view of validated
// logical requests. Retries never increase offered_count; accepted attempts are
// exposed separately so failover demand is still visible.
type OpsRequestLifecycleSnapshot struct {
	Scope                string    `json:"scope"`
	WindowSeconds        int64     `json:"window_seconds"`
	ProcessStartedAt     time.Time `json:"process_started_at"`
	ProcessInstanceID    string    `json:"process_instance_id"`
	OfferedCount         uint64    `json:"offered_count"`
	AcceptedCount        uint64    `json:"accepted_count"`
	AcceptedAttemptCount uint64    `json:"accepted_attempt_count"`
	CompletedCount       uint64    `json:"completed_count"`
	SucceededCount       uint64    `json:"succeeded_count"`
	RejectedCount        uint64    `json:"rejected_count"`
	FailedCount          uint64    `json:"failed_count"`
	CanceledCount        uint64    `json:"canceled_count"`
	// ActiveCount is the process-current number of in-flight logical requests,
	// independent of the requested observation window.
	ActiveCount      int64  `json:"active_count"`
	ActiveCountScope string `json:"active_count_scope"`

	// Rate fields use the returned observation window; prewarm is process-local.
	OfferedQPS   float64                 `json:"offered_qps"`
	AcceptedQPS  float64                 `json:"accepted_qps"`
	CompletedQPS float64                 `json:"completed_qps"`
	OfferedRPM   float64                 `json:"offered_rpm"`
	AcceptedRPM  float64                 `json:"accepted_rpm"`
	CompletedRPM float64                 `json:"completed_rpm"`
	CachePrewarm OpsCachePrewarmSnapshot `json:"cache_prewarm"`
}

type opsRequestLifecycleTracker struct {
	mu        sync.Mutex
	buckets   map[int64]*opsRequestLifecycleBucket
	active    int64
	startedAt time.Time
	processID string
	lastPrune int64
}

type OpsRequestLifecycleHandle struct {
	tracker   *opsRequestLifecycleTracker
	accepted  atomic.Bool
	completed atomic.Bool
}

var globalOpsRequestLifecycle = newOpsRequestLifecycleTracker(time.Now().UTC())

func newOpsRequestLifecycleTracker(startedAt time.Time) *opsRequestLifecycleTracker {
	processID := make([]byte, 12)
	if _, err := rand.Read(processID); err != nil {
		// A timestamp-derived value still gives each process instance a stable,
		// anonymous identity if the system RNG is temporarily unavailable.
		copy(processID, []byte(startedAt.UTC().Format("20060102150405.000000000")))
	}
	return &opsRequestLifecycleTracker{
		buckets: make(map[int64]*opsRequestLifecycleBucket),
		// Buckets are second-granular. Align process start to the same boundary so
		// the first bucket is not excluded when startedAt carries sub-second data.
		startedAt: startedAt.UTC().Truncate(time.Second),
		processID: hex.EncodeToString(processID),
	}
}

// BeginOpsRequestLifecycle records one validated logical client request.
func BeginOpsRequestLifecycle() *OpsRequestLifecycleHandle {
	return globalOpsRequestLifecycle.begin(time.Now().UTC())
}

func (t *opsRequestLifecycleTracker) begin(at time.Time) *OpsRequestLifecycleHandle {
	if t == nil {
		return &OpsRequestLifecycleHandle{}
	}
	at = at.UTC()
	t.mu.Lock()
	bucket := t.bucketLocked(at)
	bucket.offered++
	t.active++
	t.pruneLocked(at)
	t.mu.Unlock()
	return &OpsRequestLifecycleHandle{tracker: t}
}

// Accepted records a successful upstream admission. Multiple accepted attempts
// for the same logical request are counted once in accepted_count and once per
// attempt in accepted_attempt_count.
func (h *OpsRequestLifecycleHandle) Accepted() {
	h.acceptedAt(time.Now().UTC())
}

func (h *OpsRequestLifecycleHandle) acceptedAt(at time.Time) {
	if h == nil || h.tracker == nil {
		return
	}
	at = at.UTC()
	h.tracker.mu.Lock()
	if h.completed.Load() {
		h.tracker.mu.Unlock()
		return
	}
	bucket := h.tracker.bucketLocked(at)
	bucket.acceptedAttempts++
	if h.accepted.CompareAndSwap(false, true) {
		bucket.accepted++
	}
	h.tracker.pruneLocked(at)
	h.tracker.mu.Unlock()
}

// Complete records exactly one logical terminal outcome.
func (h *OpsRequestLifecycleHandle) Complete(statusCode int, terminalErr error) {
	h.completeAt(time.Now().UTC(), statusCode, terminalErr)
}

func (h *OpsRequestLifecycleHandle) completeAt(at time.Time, statusCode int, terminalErr error) {
	if h == nil || h.tracker == nil || !h.completed.CompareAndSwap(false, true) {
		return
	}
	at = at.UTC()
	h.tracker.mu.Lock()
	bucket := h.tracker.bucketLocked(at)
	bucket.completed++
	switch {
	case errors.Is(terminalErr, context.Canceled), errors.Is(terminalErr, context.DeadlineExceeded):
		bucket.canceled++
	case terminalErr != nil:
		bucket.failed++
	case statusCode >= 200 && statusCode < 400:
		bucket.succeeded++
	case statusCode >= 400 && statusCode < 500:
		bucket.rejected++
	default:
		bucket.failed++
	}
	if h.tracker.active > 0 {
		h.tracker.active--
	}
	h.tracker.pruneLocked(at)
	h.tracker.mu.Unlock()
}

func (h *OpsRequestLifecycleHandle) AcceptedState() bool {
	return h != nil && h.accepted.Load()
}

func RecordOpsCachePrewarmStarted() {
	globalOpsRequestLifecycle.recordPrewarm(time.Now().UTC(), "started", 0)
}

func RecordOpsCachePrewarmSucceeded(duration time.Duration) {
	globalOpsRequestLifecycle.recordPrewarm(time.Now().UTC(), "succeeded", duration)
}

func RecordOpsCachePrewarmFailed(duration time.Duration) {
	globalOpsRequestLifecycle.recordPrewarm(time.Now().UTC(), "failed", duration)
}

func RecordOpsCachePrewarmSkipped() {
	globalOpsRequestLifecycle.recordPrewarm(time.Now().UTC(), "skipped", 0)
}

func (t *opsRequestLifecycleTracker) recordPrewarm(at time.Time, outcome string, duration time.Duration) {
	if t == nil {
		return
	}
	at = at.UTC()
	t.mu.Lock()
	bucket := t.bucketLocked(at)
	switch outcome {
	case "started":
		bucket.prewarmStarted++
	case "succeeded":
		bucket.prewarmSucceeded++
		if durationMs := duration.Milliseconds(); durationMs > 0 {
			bucket.prewarmDurationMs += uint64(durationMs)
		}
	case "failed":
		bucket.prewarmFailed++
		if durationMs := duration.Milliseconds(); durationMs > 0 {
			bucket.prewarmDurationMs += uint64(durationMs)
		}
	case "skipped":
		bucket.prewarmSkipped++
	}
	t.pruneLocked(at)
	t.mu.Unlock()
}

func SnapshotOpsRequestLifecycle(start, end time.Time) OpsRequestLifecycleSnapshot {
	return globalOpsRequestLifecycle.snapshot(start, end)
}

func (t *opsRequestLifecycleTracker) snapshot(start, end time.Time) OpsRequestLifecycleSnapshot {
	if end.IsZero() {
		end = time.Now().UTC()
	}
	end = end.UTC()
	if start.IsZero() || !start.Before(end) {
		start = end.Add(-time.Minute)
	}
	if end.Sub(start) > opsRequestLifecycleRetention {
		start = end.Add(-opsRequestLifecycleRetention)
	}
	snapshot := OpsRequestLifecycleSnapshot{
		Scope:            "process_local",
		ActiveCountScope: "process_current",
	}
	if t == nil {
		windowSeconds := int64(end.Sub(start).Seconds())
		if windowSeconds < 1 {
			windowSeconds = 1
		}
		snapshot.WindowSeconds = windowSeconds
		return snapshot
	}

	t.mu.Lock()
	if start.Before(t.startedAt) {
		start = t.startedAt
	}
	if !start.Before(end) {
		start = end.Add(-time.Second)
	}
	windowSeconds := int64(end.Sub(start).Seconds())
	if windowSeconds < 1 {
		windowSeconds = 1
	}
	snapshot.WindowSeconds = windowSeconds
	snapshot.ProcessStartedAt = t.startedAt
	snapshot.ProcessInstanceID = t.processID
	for second, bucket := range t.buckets {
		bucketAt := time.Unix(second, 0).UTC()
		if bucketAt.Before(start) || !bucketAt.Before(end) {
			continue
		}
		snapshot.OfferedCount += bucket.offered
		snapshot.AcceptedCount += bucket.accepted
		snapshot.AcceptedAttemptCount += bucket.acceptedAttempts
		snapshot.CompletedCount += bucket.completed
		snapshot.SucceededCount += bucket.succeeded
		snapshot.RejectedCount += bucket.rejected
		snapshot.FailedCount += bucket.failed
		snapshot.CanceledCount += bucket.canceled
		snapshot.CachePrewarm.StartedCount += bucket.prewarmStarted
		snapshot.CachePrewarm.SucceededCount += bucket.prewarmSucceeded
		snapshot.CachePrewarm.FailedCount += bucket.prewarmFailed
		snapshot.CachePrewarm.SkippedCount += bucket.prewarmSkipped
		snapshot.CachePrewarm.TotalDurationMs += bucket.prewarmDurationMs
	}
	snapshot.ActiveCount = t.active
	t.pruneLocked(end)
	t.mu.Unlock()

	seconds := float64(windowSeconds)
	snapshot.OfferedQPS = float64(snapshot.OfferedCount) / seconds
	snapshot.AcceptedQPS = float64(snapshot.AcceptedCount) / seconds
	snapshot.CompletedQPS = float64(snapshot.CompletedCount) / seconds
	snapshot.OfferedRPM = snapshot.OfferedQPS * 60
	snapshot.AcceptedRPM = snapshot.AcceptedQPS * 60
	snapshot.CompletedRPM = snapshot.CompletedQPS * 60
	terminalPrewarms := snapshot.CachePrewarm.SucceededCount + snapshot.CachePrewarm.FailedCount
	if terminalPrewarms > 0 {
		snapshot.CachePrewarm.AverageDurationMs = float64(snapshot.CachePrewarm.TotalDurationMs) / float64(terminalPrewarms)
	}
	return snapshot
}

func (t *opsRequestLifecycleTracker) bucketLocked(at time.Time) *opsRequestLifecycleBucket {
	// Callers hold t.mu. Normalizing here keeps bucket keys stable even when a
	// caller supplies a local-time timestamp and avoids a subtle split bucket in
	// tests that mix locations.
	at = at.UTC()
	if t.buckets == nil {
		t.buckets = make(map[int64]*opsRequestLifecycleBucket)
	}
	second := at.UTC().Unix()
	bucket := t.buckets[second]
	if bucket == nil {
		bucket = &opsRequestLifecycleBucket{}
		t.buckets[second] = bucket
	}
	return bucket
}

func (t *opsRequestLifecycleTracker) pruneLocked(now time.Time) {
	if t == nil {
		return
	}
	nowUnix := now.Unix()
	if t.lastPrune != 0 && nowUnix-t.lastPrune < int64(opsRequestLifecyclePrunePeriod/time.Second) {
		return
	}
	t.lastPrune = nowUnix
	cutoff := now.Add(-opsRequestLifecycleRetention).Unix()
	for second := range t.buckets {
		if second < cutoff {
			delete(t.buckets, second)
		}
	}
}

func OpsRequestTerminalError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
