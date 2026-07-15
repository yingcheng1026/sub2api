package service

import (
	"context"
	"errors"
	"math"
	"strings"
)

const usageBillingDatabaseNumericScale = 10_000_000_000.0

func canonicalUsageBillingRate(value float64) float64 {
	return math.Round(value*usageBillingDatabaseNumericScale) / usageBillingDatabaseNumericScale
}

func canonicalUsageBillingReservation(value float64) float64 {
	return math.Ceil(value*usageBillingDatabaseNumericScale) / usageBillingDatabaseNumericScale
}

const (
	UsageBillingLegacyEnvelopeVersion int16 = 1
	UsageBillingFencedEnvelopeVersion int16 = 2
)

var ErrUsageBillingLifecycleContractInvalid = errors.New("usage billing lifecycle contract is invalid")

// UsageBillingAdmissionRef fences mutations to the immutable admission base.
type UsageBillingAdmissionRef struct {
	RequestID  string
	APIKeyID   int64
	OwnerToken string
}

func (r UsageBillingAdmissionRef) Validate() error {
	if strings.TrimSpace(r.RequestID) == "" || len(strings.TrimSpace(r.RequestID)) > 255 ||
		r.APIKeyID <= 0 || !validFenceToken(strings.ToLower(strings.TrimSpace(r.OwnerToken))) {
		return ErrUsageBillingLifecycleContractInvalid
	}
	return nil
}

// UsageBillingAttemptRef fences mutations to one concrete upstream attempt.
// Account failover creates a new attempt; it never rewrites this identity.
type UsageBillingAttemptRef struct {
	RequestID  string
	APIKeyID   int64
	OwnerToken string
	AttemptID  string
}

func (r UsageBillingAttemptRef) Validate() error {
	if err := r.Admission().Validate(); err != nil ||
		!validFenceToken(strings.ToLower(strings.TrimSpace(r.AttemptID))) {
		return ErrUsageBillingLifecycleContractInvalid
	}
	return nil
}

func (r UsageBillingAttemptRef) Admission() UsageBillingAdmissionRef {
	return UsageBillingAdmissionRef{
		RequestID:  strings.TrimSpace(r.RequestID),
		APIKeyID:   r.APIKeyID,
		OwnerToken: strings.ToLower(strings.TrimSpace(r.OwnerToken)),
	}
}

// UsageBillingAdmissionAttemptRef remains the repository-facing name while
// both repository and transport code move to the shared lifecycle contract.
type UsageBillingAdmissionAttemptRef = UsageBillingAttemptRef

// UsageBillingReservationQuote freezes the exact normalized request and price
// evidence used for the pre-upstream affordability decision.
type UsageBillingReservationQuote struct {
	RequestPayloadHash string
	BillingModel       string
	PricingSource      string
	PricingRevision    string
	PricingHash        string
	RateMultiplier     float64
	WorstCaseCostUSD   float64

	// Alternate* freezes one additional settlement schedule for endpoints
	// whose result type is not known until the upstream response (for example,
	// an OpenAI Responses turn that may return either text or an image). The
	// reservation must cover the larger of both schedules.
	AlternateBillingModel    string
	AlternatePricingSource   string
	AlternatePricingRevision string
	AlternatePricingHash     string
	AlternateRateMultiplier  float64
}

func (q UsageBillingReservationQuote) Validate() error {
	if !validSHA256(strings.ToLower(strings.TrimSpace(q.RequestPayloadHash))) ||
		!validUsageBillingEnvelopeText(q.BillingModel, 128, true) ||
		!validUsageBillingPricingSource(q.PricingSource) ||
		!validUsageBillingEnvelopeText(q.PricingRevision, 128, true) ||
		!validSHA256(strings.ToLower(strings.TrimSpace(q.PricingHash))) ||
		math.IsNaN(q.RateMultiplier) || math.IsInf(q.RateMultiplier, 0) || q.RateMultiplier <= 0 ||
		math.IsNaN(q.WorstCaseCostUSD) || math.IsInf(q.WorstCaseCostUSD, 0) || q.WorstCaseCostUSD <= 0 {
		return ErrUsageBillingLifecycleContractInvalid
	}
	hasAlternate := strings.TrimSpace(q.AlternateBillingModel) != "" ||
		strings.TrimSpace(q.AlternatePricingSource) != "" ||
		strings.TrimSpace(q.AlternatePricingRevision) != "" ||
		strings.TrimSpace(q.AlternatePricingHash) != "" || q.AlternateRateMultiplier != 0
	if hasAlternate && (!validUsageBillingEnvelopeText(q.AlternateBillingModel, 128, true) ||
		!validUsageBillingPricingSource(q.AlternatePricingSource) ||
		!validUsageBillingEnvelopeText(q.AlternatePricingRevision, 128, true) ||
		!validSHA256(strings.ToLower(strings.TrimSpace(q.AlternatePricingHash))) ||
		math.IsNaN(q.AlternateRateMultiplier) || math.IsInf(q.AlternateRateMultiplier, 0) || q.AlternateRateMultiplier <= 0) {
		return ErrUsageBillingLifecycleContractInvalid
	}
	return nil
}

// UsageBillingDeliveryOutcome classifies whether account failover is safe.
// The zero/unknown value is deliberately fail-closed.
type UsageBillingDeliveryOutcome string

const (
	UsageBillingDeliveryUnknown            UsageBillingDeliveryOutcome = "unknown"
	UsageBillingDeliveryNotSent            UsageBillingDeliveryOutcome = "not_sent"
	UsageBillingDeliveryDefinitelyRejected UsageBillingDeliveryOutcome = "definitely_rejected"
	UsageBillingDeliveryAccepted           UsageBillingDeliveryOutcome = "accepted"
)

func (o UsageBillingDeliveryOutcome) AllowsFailover() bool {
	return o == UsageBillingDeliveryNotSent || o == UsageBillingDeliveryDefinitelyRejected
}

func (o UsageBillingDeliveryOutcome) RequiresReconciliation() bool {
	return o == "" || o == UsageBillingDeliveryUnknown
}

// UsageBillingCommitCallback is the response-commit gate. Non-stream bodies,
// stream success terminals and WebSocket turn terminals must call it before
// exposing success to the client.
type UsageBillingCommitCallback func(context.Context, UsageBillingEnvelope) error
