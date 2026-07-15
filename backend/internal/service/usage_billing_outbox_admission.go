package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	usageBillingOutboxAdmissionMaxAttempts    = 3
	usageBillingOutboxAdmissionInitialBackoff = 25 * time.Millisecond
	usageBillingOutboxAdmissionTimeout        = 750 * time.Millisecond
)

// enqueueUsageBillingOutboxDurably detaches billing identity values from the
// client lifetime while keeping admission bounded. Production injects a
// repository with an fsync-backed local spool, so exhausting the database
// attempt does not turn this loop into an unbounded HTTP goroutine.
func enqueueUsageBillingOutboxDurably(
	ctx context.Context,
	repo UsageBillingOutboxRepository,
	envelope UsageBillingEnvelope,
	component string,
) error {
	if repo == nil {
		return ErrUsageBillingOutboxUnavailable
	}
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	admissionCtx, cancel := context.WithTimeout(baseCtx, usageBillingOutboxAdmissionTimeout)
	defer cancel()

	backoff := usageBillingOutboxAdmissionInitialBackoff
	var lastErr error
	for attempt := 1; attempt <= usageBillingOutboxAdmissionMaxAttempts; attempt++ {
		if _, _, err := repo.Enqueue(admissionCtx, envelope); err == nil {
			return nil
		} else if !errors.Is(err, ErrUsageBillingOutboxAdmissionRetryable) {
			return err
		} else {
			lastErr = err
			logger.L().With(
				zap.String("component", component),
				zap.String("request_id", envelope.RequestID()),
				zap.Int64("api_key_id", envelope.APIKeyID()),
				zap.Int("attempt", attempt),
				zap.Error(err),
			).Warn("usage_billing_outbox.admission_retry")
		}
		if attempt == usageBillingOutboxAdmissionMaxAttempts {
			break
		}
		timer := time.NewTimer(backoff)
		select {
		case <-admissionCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf("%w: %w", ErrUsageBillingOutboxAdmissionRetryable, errors.Join(lastErr, admissionCtx.Err()))
		case <-timer.C:
		}
		backoff *= 2
	}
	return lastErr
}
