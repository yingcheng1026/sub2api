package handler

import (
	"context"
	"sync"
	"time"
)

type imageConcurrencyOutcome string

const (
	imageConcurrencyOutcomeAcquired  imageConcurrencyOutcome = "acquired"
	imageConcurrencyOutcomeRejected  imageConcurrencyOutcome = "rejected"
	imageConcurrencyOutcomeTimeout   imageConcurrencyOutcome = "timeout"
	imageConcurrencyOutcomeQueueFull imageConcurrencyOutcome = "queue_full"
	imageConcurrencyOutcomeCanceled  imageConcurrencyOutcome = "canceled"
	imageConcurrencyOutcomeSkipped   imageConcurrencyOutcome = "skipped"
)

type imageConcurrencySnapshot struct {
	Enabled bool
	Limit   int
	Active  int
	Waiting int
}

type imageConcurrencyObservation struct {
	Outcome      imageConcurrencyOutcome
	WaitDuration time.Duration
	Snapshot     imageConcurrencySnapshot
}

type imageConcurrencyAdmission struct {
	Acquired    bool
	Distributed bool
	Observation imageConcurrencyObservation
}

type imageConcurrencyObservationSnapshot struct {
	Acquired  uint64
	Rejected  uint64
	Timeout   uint64
	QueueFull uint64
	Canceled  uint64
	Skipped   uint64
	Last      imageConcurrencyObservation
	Snapshot  imageConcurrencySnapshot
}

type imageConcurrencyLimiter struct {
	mu      sync.Mutex
	notify  chan struct{}
	limit   int
	active  int
	waiting int
	enabled bool

	observer func(imageConcurrencyObservation)
	observed imageConcurrencyObservationSnapshot
}

func (l *imageConcurrencyLimiter) TryAcquire(enabled bool, limit int) (func(), bool) {
	release, result := l.AcquireObserved(context.Background(), enabled, limit, false, 0, 0)
	return release, result.Acquired
}

func (l *imageConcurrencyLimiter) Acquire(ctx context.Context, enabled bool, limit int, wait bool, timeout time.Duration, maxWaiting int) (func(), bool) {
	release, result := l.AcquireObserved(ctx, enabled, limit, wait, timeout, maxWaiting)
	return release, result.Acquired
}

func (l *imageConcurrencyLimiter) AcquireObserved(ctx context.Context, enabled bool, limit int, wait bool, timeout time.Duration, maxWaiting int) (func(), imageConcurrencyAdmission) {
	startedAt := time.Now()
	if !enabled || limit <= 0 {
		observation := imageConcurrencyObservation{
			Outcome:      imageConcurrencyOutcomeAcquired,
			WaitDuration: time.Since(startedAt),
			Snapshot:     l.snapshotForConfig(enabled, limit),
		}
		l.recordObservation(observation)
		return nil, imageConcurrencyAdmission{Acquired: true, Observation: observation}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if wait {
		if timeout <= 0 {
			observation := imageConcurrencyObservation{
				Outcome:      imageConcurrencyOutcomeTimeout,
				WaitDuration: time.Since(startedAt),
				Snapshot:     l.snapshotForConfig(enabled, limit),
			}
			l.recordObservation(observation)
			return nil, imageConcurrencyAdmission{Observation: observation}
		}
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		ctx = waitCtx
	}
	if maxWaiting < 0 {
		maxWaiting = 0
	}
	for {
		if wait && ctx.Err() != nil {
			observation := imageConcurrencyObservation{
				Outcome:      imageConcurrencyWaitOutcome(ctx),
				WaitDuration: time.Since(startedAt),
				Snapshot:     l.snapshotForConfig(enabled, limit),
			}
			l.recordObservation(observation)
			return nil, imageConcurrencyAdmission{Observation: observation}
		}
		release, acquired, waitRelease, notify, decisionSnapshot := l.tryAcquireLocked(enabled, limit, wait, maxWaiting)
		if acquired {
			observation := imageConcurrencyObservation{
				Outcome:      imageConcurrencyOutcomeAcquired,
				WaitDuration: time.Since(startedAt),
				Snapshot:     decisionSnapshot,
			}
			l.recordObservation(observation)
			return release, imageConcurrencyAdmission{Acquired: true, Observation: observation}
		}
		if !wait || notify == nil {
			outcome := imageConcurrencyOutcomeRejected
			if wait && notify == nil {
				outcome = imageConcurrencyOutcomeQueueFull
			}
			observation := imageConcurrencyObservation{
				Outcome:      outcome,
				WaitDuration: time.Since(startedAt),
				Snapshot:     decisionSnapshot,
			}
			l.recordObservation(observation)
			return nil, imageConcurrencyAdmission{Observation: observation}
		}
		if !l.waitForSlot(ctx, notify) {
			timeoutSnapshot := l.Snapshot()
			if waitRelease != nil {
				timeoutSnapshot = waitRelease()
			}
			observation := imageConcurrencyObservation{
				Outcome:      imageConcurrencyWaitOutcome(ctx),
				WaitDuration: time.Since(startedAt),
				Snapshot:     timeoutSnapshot,
			}
			l.recordObservation(observation)
			return nil, imageConcurrencyAdmission{Observation: observation}
		}
		if waitRelease != nil {
			waitRelease()
		}
	}
}

func (l *imageConcurrencyLimiter) tryAcquireLocked(enabled bool, limit int, wait bool, maxWaiting int) (func(), bool, func() imageConcurrencySnapshot, <-chan struct{}, imageConcurrencySnapshot) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.notify == nil {
		l.notify = make(chan struct{})
	}
	if l.enabled != enabled || l.limit != limit {
		l.enabled = enabled
		l.limit = limit
	}
	if l.active < l.limit {
		l.active++
		return l.releaseFunc(), true, nil, nil, l.snapshotLocked()
	}
	if !wait {
		return nil, false, nil, nil, l.snapshotLocked()
	}
	if maxWaiting > 0 && l.waiting >= maxWaiting {
		return nil, false, nil, nil, l.snapshotLocked()
	}
	l.waiting++
	return nil, false, l.waiterReleaseFunc(), l.notify, l.snapshotLocked()
}

func (l *imageConcurrencyLimiter) waitForSlot(ctx context.Context, notify <-chan struct{}) bool {
	select {
	case <-notify:
		return true
	case <-ctx.Done():
		return false
	}
}

func (l *imageConcurrencyLimiter) releaseFunc() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			if l.active > 0 {
				l.active--
			}
			if l.notify != nil {
				close(l.notify)
				l.notify = make(chan struct{})
			}
			l.mu.Unlock()
		})
	}
}

func (l *imageConcurrencyLimiter) waiterReleaseFunc() func() imageConcurrencySnapshot {
	var once sync.Once
	var snapshot imageConcurrencySnapshot
	return func() imageConcurrencySnapshot {
		once.Do(func() {
			l.mu.Lock()
			if l.waiting > 0 {
				l.waiting--
			}
			snapshot = l.snapshotLocked()
			l.mu.Unlock()
		})
		return snapshot
	}
}

// registerExternalWait accounts for a Redis-backed distributed waiter in the
// same process-local queue bound used by ordinary image admission.
func (l *imageConcurrencyLimiter) registerExternalWait(maxWaiting int) (func() imageConcurrencySnapshot, imageConcurrencySnapshot, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if maxWaiting > 0 && l.waiting >= maxWaiting {
		return nil, l.snapshotLocked(), false
	}
	l.waiting++
	return l.waiterReleaseFunc(), l.snapshotLocked(), true
}

func (l *imageConcurrencyLimiter) SetObserver(observer func(imageConcurrencyObservation)) {
	l.mu.Lock()
	l.observer = observer
	l.mu.Unlock()
}

func (l *imageConcurrencyLimiter) Snapshot() imageConcurrencySnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.snapshotLocked()
}

func (l *imageConcurrencyLimiter) ObservationSnapshot() imageConcurrencyObservationSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	snapshot := l.observed
	snapshot.Snapshot = l.snapshotLocked()
	return snapshot
}

func (l *imageConcurrencyLimiter) snapshotForConfig(enabled bool, limit int) imageConcurrencySnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.enabled != enabled || l.limit != limit {
		l.enabled = enabled
		l.limit = limit
	}
	return l.snapshotLocked()
}

func (l *imageConcurrencyLimiter) snapshotLocked() imageConcurrencySnapshot {
	return imageConcurrencySnapshot{
		Enabled: l.enabled,
		Limit:   l.limit,
		Active:  l.active,
		Waiting: l.waiting,
	}
}

func (l *imageConcurrencyLimiter) recordObservation(observation imageConcurrencyObservation) {
	l.mu.Lock()
	l.observed.Last = observation
	l.observed.Snapshot = l.snapshotLocked()
	switch observation.Outcome {
	case imageConcurrencyOutcomeAcquired:
		l.observed.Acquired++
	case imageConcurrencyOutcomeRejected:
		l.observed.Rejected++
	case imageConcurrencyOutcomeTimeout:
		l.observed.Timeout++
	case imageConcurrencyOutcomeQueueFull:
		l.observed.QueueFull++
	case imageConcurrencyOutcomeCanceled:
		l.observed.Canceled++
	case imageConcurrencyOutcomeSkipped:
		l.observed.Skipped++
	}
	observer := l.observer
	l.mu.Unlock()
	if observer != nil {
		observer(observation)
	}
}

func imageConcurrencyWaitOutcome(ctx context.Context) imageConcurrencyOutcome {
	if ctx != nil && ctx.Err() == context.DeadlineExceeded {
		return imageConcurrencyOutcomeTimeout
	}
	return imageConcurrencyOutcomeCanceled
}
