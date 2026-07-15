package service

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	UsageBillingReconciliationIdempotencyScope         = "admin.usage.billing_reconciliation.resolve"
	UsageBillingReconciliationActionRetryDeadLetter    = "retry_dead_letter"
	UsageBillingReconciliationActionReleaseUndelivered = "release_undelivered"
	UsageBillingReconciliationActionSettleDelivered    = "settle_delivered"
)

var (
	ErrUsageBillingReconciliationInvalid = infraerrors.BadRequest(
		"USAGE_BILLING_RECONCILIATION_INVALID", "invalid usage billing reconciliation request",
	)
	ErrUsageBillingReconciliationConflict = infraerrors.Conflict(
		"USAGE_BILLING_RECONCILIATION_CONFLICT", "usage billing reconciliation state changed; reload the case",
	)
	ErrUsageBillingReconciliationNotFound = infraerrors.NotFound(
		"USAGE_BILLING_RECONCILIATION_NOT_FOUND", "usage billing reconciliation case not found",
	)
	ErrUsageBillingReconciliationUnavailable = infraerrors.ServiceUnavailable(
		"USAGE_BILLING_RECONCILIATION_UNAVAILABLE", "usage billing reconciliation is unavailable",
	)

	usageBillingEvidenceRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/#-]{7,499}$`)
)

type UsageBillingReconciliationCase struct {
	RequestID              string     `json:"request_id"`
	APIKeyID               int64      `json:"api_key_id"`
	State                  string     `json:"state"`
	UserID                 int64      `json:"user_id"`
	SubscriptionID         *int64     `json:"subscription_id,omitempty"`
	WalletSubscriptionID   *int64     `json:"wallet_subscription_id,omitempty"`
	WalletReservedUSD      float64    `json:"wallet_reserved_usd"`
	OutboxID               *int64     `json:"outbox_id,omitempty"`
	OutboxStatus           string     `json:"outbox_status,omitempty"`
	LastErrorCode          string     `json:"last_error_code,omitempty"`
	PreparedAttempts       int        `json:"prepared_attempts"`
	DispatchedAttempts     int        `json:"dispatched_attempts"`
	FailedAttempts         int        `json:"failed_attempts"`
	FinalizedAttempts      int        `json:"finalized_attempts"`
	EverDispatchedAttempts int        `json:"ever_dispatched_attempts"`
	PreparedAt             time.Time  `json:"prepared_at"`
	DispatchedAt           *time.Time `json:"dispatched_at,omitempty"`
	OrphanedAt             *time.Time `json:"orphaned_at,omitempty"`
	ReconcileStartedAt     *time.Time `json:"reconcile_started_at,omitempty"`
	AllowedActions         []string   `json:"allowed_actions"`
}

type UsageBillingReconciliationResolveInput struct {
	RequestID       string               `json:"request_id"`
	APIKeyID        int64                `json:"api_key_id"`
	OperatorID      int64                `json:"-"`
	Action          string               `json:"action"`
	AttemptID       string               `json:"attempt_id,omitempty"`
	BillingEnvelope json.RawMessage      `json:"billing_envelope,omitempty"`
	EvidenceRef     string               `json:"evidence_ref"`
	Envelope        UsageBillingEnvelope `json:"-"`
}

type UsageBillingReconciliationRepository interface {
	ListUsageBillingReconciliationCases(ctx context.Context, limit int) ([]UsageBillingReconciliationCase, error)
	ResolveUsageBillingReconciliation(ctx context.Context, input UsageBillingReconciliationResolveInput) error
}

type UsageBillingReconciliationService struct {
	repo UsageBillingReconciliationRepository
}

func NewUsageBillingReconciliationService(repo UsageBillingReconciliationRepository) *UsageBillingReconciliationService {
	return &UsageBillingReconciliationService{repo: repo}
}

func (s *UsageBillingReconciliationService) List(
	ctx context.Context,
	limit int,
) ([]UsageBillingReconciliationCase, error) {
	if s == nil || s.repo == nil {
		return nil, ErrUsageBillingReconciliationUnavailable
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	items, err := s.repo.ListUsageBillingReconciliationCases(ctx, limit)
	if err != nil {
		return nil, err
	}
	return append([]UsageBillingReconciliationCase(nil), items...), nil
}

func (s *UsageBillingReconciliationService) Resolve(
	ctx context.Context,
	input UsageBillingReconciliationResolveInput,
) error {
	if s == nil || s.repo == nil {
		return ErrUsageBillingReconciliationUnavailable
	}
	normalized, err := normalizeUsageBillingReconciliationInput(input)
	if err != nil {
		return err
	}
	return s.repo.ResolveUsageBillingReconciliation(ctx, normalized)
}

func normalizeUsageBillingReconciliationInput(
	input UsageBillingReconciliationResolveInput,
) (UsageBillingReconciliationResolveInput, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	input.AttemptID = strings.ToLower(strings.TrimSpace(input.AttemptID))
	input.EvidenceRef = strings.TrimSpace(input.EvidenceRef)
	if input.RequestID == "" || len(input.RequestID) > 255 || input.APIKeyID <= 0 || input.OperatorID <= 0 ||
		!usageBillingEvidenceRefPattern.MatchString(input.EvidenceRef) {
		return UsageBillingReconciliationResolveInput{}, ErrUsageBillingReconciliationInvalid
	}
	switch input.Action {
	case UsageBillingReconciliationActionRetryDeadLetter, UsageBillingReconciliationActionReleaseUndelivered:
		if input.AttemptID != "" || len(input.BillingEnvelope) != 0 {
			return UsageBillingReconciliationResolveInput{}, ErrUsageBillingReconciliationInvalid
		}
	case UsageBillingReconciliationActionSettleDelivered:
		if !validFenceToken(input.AttemptID) || len(input.BillingEnvelope) == 0 {
			return UsageBillingReconciliationResolveInput{}, ErrUsageBillingReconciliationInvalid
		}
		envelope, err := DecodeUsageBillingEnvelope(input.BillingEnvelope)
		if err != nil || envelope.RequestID() != input.RequestID || envelope.APIKeyID() != input.APIKeyID ||
			envelope.AdmissionAttemptID() != input.AttemptID || envelope.SubscriptionID() == nil ||
			ValidateUsageBillingReconciliationWalletCosts(envelope) != nil {
			return UsageBillingReconciliationResolveInput{}, ErrUsageBillingReconciliationInvalid
		}
		canonical, err := envelope.MarshalJSON()
		if err != nil {
			return UsageBillingReconciliationResolveInput{}, ErrUsageBillingReconciliationInvalid
		}
		input.BillingEnvelope = canonical
		input.Envelope = envelope
	default:
		return UsageBillingReconciliationResolveInput{}, ErrUsageBillingReconciliationInvalid
	}
	return input, nil
}

func ValidateUsageBillingReconciliationWalletCosts(envelope UsageBillingEnvelope) error {
	if err := envelope.Validate(); err != nil {
		return ErrUsageBillingReconciliationInvalid
	}
	walletCost := envelope.WalletCost()
	totalCost := envelope.TotalCost()
	actualCost := envelope.ActualCost()
	if envelope.BalanceCost() != 0 || envelope.SubscriptionCost() != 0 || walletCost <= 0 ||
		totalCost <= 0 || actualCost <= 0 || math.IsNaN(walletCost) || math.IsInf(walletCost, 0) ||
		!usageBillingReconciliationCostsEqual(walletCost, actualCost) ||
		!usageBillingReconciliationCostsEqual(actualCost, totalCost*envelope.RateMultiplier()) ||
		!usageBillingReconciliationOptionalCostMatches(envelope.APIKeyQuotaCost(), actualCost) ||
		!usageBillingReconciliationOptionalCostMatches(envelope.APIKeyRateLimitCost(), actualCost) ||
		!usageBillingReconciliationOptionalCostMatches(
			envelope.AccountQuotaCost(), totalCost*envelope.AccountRateMultiplier(),
		) {
		return ErrUsageBillingReconciliationInvalid
	}
	return nil
}

func usageBillingReconciliationOptionalCostMatches(actual, expected float64) bool {
	return actual == 0 || usageBillingReconciliationCostsEqual(actual, expected)
}

func usageBillingReconciliationCostsEqual(left, right float64) bool {
	scale := math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
	return math.Abs(left-right) <= 1e-9*scale
}
