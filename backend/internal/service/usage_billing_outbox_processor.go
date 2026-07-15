package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	usageBillingOutboxMaxRetryBackoff          = 5 * time.Minute
	usageBillingPreparedReconcileGrace         = 5 * time.Minute
	usageBillingDispatchedOrphanReconcileGrace = 24 * time.Hour
	usageBillingAdmissionReconcileInterval     = time.Minute
	usageBillingAdmissionReconcileBatchSize    = 16
)

// UsageBillingOutboxProcessor replays durable billing facts through the
// existing atomic UsageBillingRepository. It never invokes postUsageBilling.
type UsageBillingOutboxProcessor struct {
	outboxRepo       UsageBillingOutboxRepository
	bindingValidator UsageBillingBindingValidator
	billingRepo      UsageBillingRepository
	replayWriter     UsageBillingReplayWriter
	replayFinalizer  UsageBillingReplayFinalizer
	now              func() time.Time
	reconcileMu      sync.Mutex
	nextReconcileAt  time.Time
}

func NewUsageBillingOutboxProcessor(
	outboxRepo UsageBillingOutboxRepository,
	bindingValidator UsageBillingBindingValidator,
	billingRepo UsageBillingRepository,
	replayWriter UsageBillingReplayWriter,
	replayFinalizers ...UsageBillingReplayFinalizer,
) *UsageBillingOutboxProcessor {
	var replayFinalizer UsageBillingReplayFinalizer
	if len(replayFinalizers) > 0 {
		replayFinalizer = replayFinalizers[0]
	}
	return &UsageBillingOutboxProcessor{
		outboxRepo:       outboxRepo,
		bindingValidator: bindingValidator,
		billingRepo:      billingRepo,
		replayWriter:     replayWriter,
		replayFinalizer:  replayFinalizer,
		now:              time.Now,
	}
}

func (p *UsageBillingOutboxProcessor) ProcessBatch(ctx context.Context, owner string, limit int) (int, error) {
	if p == nil || p.outboxRepo == nil {
		return 0, errors.New("usage billing outbox repository is nil")
	}
	events, err := p.outboxRepo.Claim(ctx, owner, limit, UsageBillingOutboxLeaseDuration)
	if err != nil {
		return 0, err
	}
	var processErrors []error
	for i := range events {
		if err := p.ProcessEvent(ctx, events[i]); err != nil {
			processErrors = append(processErrors, fmt.Errorf("event %d: %w", events[i].ID, err))
		}
	}
	processed := len(events)
	if reconciler, ok := p.outboxRepo.(UsageBillingAdmissionReconciler); ok && p.claimUsageBillingReconcileSlot() {
		reconciled, reconcileErr := reconciler.ReconcileStaleAdmissions(
			ctx,
			usageBillingPreparedReconcileGrace,
			usageBillingDispatchedOrphanReconcileGrace,
			usageBillingAdmissionReconcileBatchSize,
		)
		if reconcileErr != nil {
			processErrors = append(processErrors, fmt.Errorf("reconcile stale admissions: %w", reconcileErr))
		} else {
			processed += reconciled.Total()
		}
	}
	return processed, errors.Join(processErrors...)
}

func (p *UsageBillingOutboxProcessor) claimUsageBillingReconcileSlot() bool {
	if p == nil {
		return false
	}
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	p.reconcileMu.Lock()
	defer p.reconcileMu.Unlock()
	if !p.nextReconcileAt.IsZero() && now.Before(p.nextReconcileAt) {
		return false
	}
	p.nextReconcileAt = now.Add(usageBillingAdmissionReconcileInterval)
	return true
}

func (p *UsageBillingOutboxProcessor) ProcessEvent(ctx context.Context, event UsageBillingOutboxEvent) error {
	if p == nil || p.outboxRepo == nil {
		return errors.New("usage billing outbox processor is not configured")
	}
	if event.ID <= 0 || strings.TrimSpace(event.LockedBy) == "" || strings.TrimSpace(event.LeaseToken) == "" {
		return ErrUsageBillingOutboxLeaseLost
	}
	if event.MaxAttempts <= 0 {
		event.MaxAttempts = UsageBillingOutboxDefaultMaxAttempts
	}

	if event.EnvelopeError != nil {
		return p.deadLetter(ctx, event, "invalid_envelope", event.EnvelopeError)
	}
	if err := event.Envelope.Validate(); err != nil {
		return p.deadLetter(ctx, event, envelopeErrorCode(err), err)
	}
	if p.bindingValidator == nil {
		return p.retry(ctx, event, "binding_validator_unavailable", errors.New("usage billing binding validator is nil"))
	}
	if err := p.bindingValidator.ValidateBindings(ctx, event.Envelope); err != nil {
		if errors.Is(err, ErrUsageBillingCrossTenant) {
			return p.deadLetter(ctx, event, "cross_tenant", err)
		}
		if errors.Is(err, ErrUsageBillingOutboxTargetNotFound) {
			return p.deadLetter(ctx, event, "binding_target_not_found", err)
		}
		return p.retry(ctx, event, "binding_validation_failed", err)
	}
	if p.billingRepo == nil {
		return p.retry(ctx, event, "billing_repository_unavailable", errors.New("usage billing repository is nil"))
	}

	result, err := p.billingRepo.Apply(ctx, event.Envelope.Command())
	if err != nil {
		if errors.Is(err, ErrUsageBillingRequestConflict) {
			return p.deadLetter(ctx, event, "fingerprint_conflict", err)
		}
		return p.retry(ctx, event, "billing_apply_failed", err)
	}
	if result == nil {
		return p.retry(ctx, event, "billing_result_missing", errors.New("usage billing apply result is nil"))
	}

	// The replay hook is intentionally after Apply and before Complete. A hook
	// failure retries the event; billing dedup makes the subsequent Apply safe.
	if p.replayWriter != nil {
		if err := p.replayWriter.WriteUsageBillingReplay(ctx, event.Envelope); err != nil {
			return p.retry(ctx, event, "usage_log_replay_failed", err)
		}
	}
	if p.replayFinalizer != nil {
		if err := p.replayFinalizer.FinalizeUsageBillingReplay(ctx, event.Envelope, result); err != nil {
			return p.retry(ctx, event, "billing_replay_finalize_failed", err)
		}
	}

	resultCode := UsageBillingOutboxResultDuplicate
	if result.Applied {
		resultCode = UsageBillingOutboxResultApplied
	}
	if result.WalletInsufficient {
		resultCode = UsageBillingOutboxResultWalletInsufficient
	}
	// An acknowledgement failure deliberately leaves the lease intact. After
	// lease expiry another worker replays Apply and is stopped by billing dedup.
	return p.outboxRepo.Complete(ctx, event.ID, event.LockedBy, event.LeaseToken, resultCode)
}

func (p *UsageBillingOutboxProcessor) retry(ctx context.Context, event UsageBillingOutboxEvent, code string, cause error) error {
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	return p.outboxRepo.Retry(
		ctx,
		event.ID,
		event.LockedBy,
		event.LeaseToken,
		now.Add(usageBillingOutboxRetryBackoff(event.ID, event.AttemptCount)),
		code,
		safeUsageBillingOutboxError(cause),
	)
}

func (p *UsageBillingOutboxProcessor) deadLetter(ctx context.Context, event UsageBillingOutboxEvent, code string, cause error) error {
	return p.outboxRepo.DeadLetter(ctx, event.ID, event.LockedBy, event.LeaseToken, code, safeUsageBillingOutboxError(cause))
}

func usageBillingOutboxRetryBackoff(eventID int64, attempt int16) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	exponent := attempt - 1
	if exponent > 8 {
		exponent = 8
	}
	base := time.Second * time.Duration(1<<exponent)
	if base > usageBillingOutboxMaxRetryBackoff {
		base = usageBillingOutboxMaxRetryBackoff
	}
	// Deterministic jitter in [0, 25%): stable in tests and across restarts.
	seed := uint64(eventID)*1103515245 + uint64(attempt)*12345
	jitter := time.Duration(seed%1000) * base / 4000
	if base+jitter > usageBillingOutboxMaxRetryBackoff {
		return usageBillingOutboxMaxRetryBackoff
	}
	return base + jitter
}

func envelopeErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrUsageBillingEnvelopeVersion):
		return "unknown_envelope_version"
	case errors.Is(err, ErrUsageBillingEnvelopeFingerprintMismatch):
		return "fingerprint_mismatch"
	default:
		return "invalid_envelope"
	}
}

func safeUsageBillingOutboxError(err error) string {
	if err == nil {
		return ""
	}
	// Error sources include the future Task 2B replay writer and database
	// adapters. Persist only an allow-listed message; the typed error code carries
	// the actionable classification without risking credentials or request data.
	return "usage billing outbox processing failed"
}
