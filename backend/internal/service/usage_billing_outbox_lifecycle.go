package service

import (
	"context"
	"errors"
	"time"
)

const (
	UsageBillingOutboxStatusPending    = "pending"
	UsageBillingOutboxStatusRetry      = "retry"
	UsageBillingOutboxStatusCompleted  = "completed"
	UsageBillingOutboxStatusDeadLetter = "dead_letter"

	UsageBillingOutboxResultApplied            = "applied"
	UsageBillingOutboxResultDuplicate          = "duplicate"
	UsageBillingOutboxResultWalletInsufficient = "wallet_insufficient"

	UsageBillingOutboxLeaseDuration            = 30 * time.Second
	UsageBillingOutboxDefaultMaxAttempts int16 = 8
)

var (
	ErrUsageBillingOutboxLeaseLost      = errors.New("usage billing outbox lease lost")
	ErrUsageBillingCrossTenant          = errors.New("usage billing binding crosses tenants")
	ErrUsageBillingOutboxTargetNotFound = errors.New("usage billing binding target not found")
	ErrUsageBillingOutboxUnavailable    = errors.New("usage billing durable outbox is unavailable")
)

type UsageBillingOutboxEvent struct {
	ID             int64
	Envelope       UsageBillingEnvelope
	EnvelopeError  error
	Status         string
	AttemptCount   int16
	MaxAttempts    int16
	AvailableAt    time.Time
	LockedAt       *time.Time
	LockedBy       string
	LeaseToken     string
	LastAttemptAt  *time.Time
	CompletedAt    *time.Time
	DeadLetteredAt *time.Time
	ResultCode     string
	LastErrorCode  string
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type UsageBillingOutboxRepository interface {
	Enqueue(ctx context.Context, envelope UsageBillingEnvelope) (*UsageBillingOutboxEvent, bool, error)
	Claim(ctx context.Context, owner string, limit int, lease time.Duration) ([]UsageBillingOutboxEvent, error)
	Complete(ctx context.Context, id int64, owner, leaseToken, resultCode string) error
	Retry(ctx context.Context, id int64, owner, leaseToken string, availableAt time.Time, errorCode, errorMessage string) error
	DeadLetter(ctx context.Context, id int64, owner, leaseToken, errorCode, errorMessage string) error
}

type UsageBillingBindingValidator interface {
	ValidateBindings(ctx context.Context, envelope UsageBillingEnvelope) error
}

// UsageBillingReplayWriter is the Task 2A seam for the eventual idempotent
// usage-log replay. Task 2B owns the concrete producer/log wiring.
type UsageBillingReplayWriter interface {
	WriteUsageBillingReplay(ctx context.Context, envelope UsageBillingEnvelope) error
}

// UsageBillingReplayFinalizer performs only idempotent post-commit effects such
// as authoritative cache invalidation. It must be safe to run again after an
// Apply commit followed by a process crash.
type UsageBillingReplayFinalizer interface {
	FinalizeUsageBillingReplay(ctx context.Context, envelope UsageBillingEnvelope, result *UsageBillingApplyResult) error
}
