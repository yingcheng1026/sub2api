package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrUsageBillingAdmissionInvalid   = errors.New("usage billing admission is invalid")
	ErrUsageBillingAdmissionMissing   = errors.New("usage billing admission is missing")
	ErrUsageBillingAdmissionFinalized = errors.New("usage billing admission is already finalized")
	ErrUsageBillingAdmissionLeaseLost = errors.New("usage billing admission fencing lease is lost")
	ErrUsageBillingAdmissionOrphaned  = errors.New("usage billing admission requires reconciliation")
)

const (
	UsageBillingAdmissionStatePrepared      = "prepared"
	UsageBillingAdmissionStateDispatched    = "dispatched"
	UsageBillingAdmissionStateOutboxPending = "outbox_pending"
	UsageBillingAdmissionStateSettled       = "settled"
	UsageBillingAdmissionStateOrphaned      = "orphaned"
	UsageBillingAdmissionStateReconcile     = "reconcile"
	UsageBillingAdmissionStateAbandoned     = "abandoned"

	UsageBillingAttemptStatePrepared   = "prepared"
	UsageBillingAttemptStateDispatched = "dispatched"
	UsageBillingAttemptStateFailed     = "failed"
	UsageBillingAttemptStateFinalized  = "finalized"

	// Compatibility aliases retained while repository settlement tests move to
	// the explicit lifecycle names.
	UsageBillingAdmissionStatusAdmitted  = UsageBillingAdmissionStatePrepared
	UsageBillingAdmissionStatusFinalized = UsageBillingAdmissionStateOutboxPending
	UsageBillingAdmissionStatusAbandoned = UsageBillingAdmissionStateAbandoned
)

type UsageBillingAdmissionInput struct {
	RequestID               string
	APIKeyID                int64
	AuthCacheLocator        string
	UserID                  int64
	SubscriptionID          *int64
	GroupID                 *int64
	EffectiveBillingGroupID *int64
	BillingType             int8

	OwnerToken  string
	AttemptID   string
	AccountID   int64
	AccountType string

	BillingModel       string
	RequestPayloadHash string
	PricingSource      string
	PricingRevision    string
	PricingHash        string
	RateMultiplier     float64
	WorstCaseCostUSD   float64

	AlternateBillingModel    string
	AlternatePricingSource   string
	AlternatePricingRevision string
	AlternatePricingHash     string
	AlternateRateMultiplier  float64
}

type UsageBillingAdmission struct {
	payload usageBillingAdmissionPayload
}

type usageBillingAdmissionPayload struct {
	RequestID               string `json:"request_id"`
	APIKeyID                int64  `json:"api_key_id"`
	AuthCacheLocator        string `json:"auth_cache_locator,omitempty"`
	UserID                  int64  `json:"user_id"`
	SubscriptionID          *int64 `json:"subscription_id,omitempty"`
	GroupID                 *int64 `json:"group_id"`
	EffectiveBillingGroupID *int64 `json:"effective_billing_group_id"`
	BillingType             int8   `json:"billing_type"`

	OwnerToken  string `json:"-"`
	AttemptID   string `json:"-"`
	AccountID   int64  `json:"-"`
	AccountType string `json:"-"`

	BillingModel       string  `json:"billing_model,omitempty"`
	RequestPayloadHash string  `json:"request_payload_hash,omitempty"`
	PricingSource      string  `json:"pricing_source,omitempty"`
	PricingRevision    string  `json:"pricing_revision,omitempty"`
	PricingHash        string  `json:"pricing_hash,omitempty"`
	RateMultiplier     float64 `json:"rate_multiplier"`
	WorstCaseCostUSD   float64 `json:"worst_case_cost_usd"`

	AlternateBillingModel    string  `json:"alternate_billing_model,omitempty"`
	AlternatePricingSource   string  `json:"alternate_pricing_source,omitempty"`
	AlternatePricingRevision string  `json:"alternate_pricing_revision,omitempty"`
	AlternatePricingHash     string  `json:"alternate_pricing_hash,omitempty"`
	AlternateRateMultiplier  float64 `json:"alternate_rate_multiplier,omitempty"`
}

type usageBillingAdmissionBaseFingerprint struct {
	RequestID                string  `json:"request_id"`
	APIKeyID                 int64   `json:"api_key_id"`
	AuthCacheLocator         string  `json:"auth_cache_locator,omitempty"`
	UserID                   int64   `json:"user_id"`
	SubscriptionID           *int64  `json:"subscription_id,omitempty"`
	GroupID                  *int64  `json:"group_id"`
	EffectiveBillingGroupID  *int64  `json:"effective_billing_group_id"`
	BillingType              int8    `json:"billing_type"`
	BillingModel             string  `json:"billing_model,omitempty"`
	RequestPayloadHash       string  `json:"request_payload_hash,omitempty"`
	PricingSource            string  `json:"pricing_source,omitempty"`
	PricingRevision          string  `json:"pricing_revision,omitempty"`
	PricingHash              string  `json:"pricing_hash,omitempty"`
	RateMultiplier           float64 `json:"rate_multiplier"`
	WorstCaseCostUSD         float64 `json:"worst_case_cost_usd"`
	AlternateBillingModel    string  `json:"alternate_billing_model,omitempty"`
	AlternatePricingSource   string  `json:"alternate_pricing_source,omitempty"`
	AlternatePricingRevision string  `json:"alternate_pricing_revision,omitempty"`
	AlternatePricingHash     string  `json:"alternate_pricing_hash,omitempty"`
	AlternateRateMultiplier  float64 `json:"alternate_rate_multiplier,omitempty"`
}

func NewUsageBillingAdmission(input UsageBillingAdmissionInput) (UsageBillingAdmission, error) {
	effectiveGroupID := copyInt64(input.EffectiveBillingGroupID)
	if effectiveGroupID == nil {
		effectiveGroupID = copyInt64(input.GroupID)
	}
	admission := UsageBillingAdmission{payload: usageBillingAdmissionPayload{
		RequestID: strings.TrimSpace(input.RequestID), APIKeyID: input.APIKeyID,
		AuthCacheLocator: strings.ToLower(strings.TrimSpace(input.AuthCacheLocator)), UserID: input.UserID,
		SubscriptionID: copyInt64(input.SubscriptionID), GroupID: copyInt64(input.GroupID),
		EffectiveBillingGroupID: effectiveGroupID, BillingType: input.BillingType,
		OwnerToken: strings.ToLower(strings.TrimSpace(input.OwnerToken)), AttemptID: strings.ToLower(strings.TrimSpace(input.AttemptID)),
		AccountID: input.AccountID, AccountType: strings.TrimSpace(input.AccountType),
		BillingModel: strings.TrimSpace(input.BillingModel), RequestPayloadHash: strings.ToLower(strings.TrimSpace(input.RequestPayloadHash)),
		PricingSource:   strings.TrimSpace(input.PricingSource),
		PricingRevision: strings.TrimSpace(input.PricingRevision), PricingHash: strings.ToLower(strings.TrimSpace(input.PricingHash)),
		RateMultiplier:           canonicalUsageBillingRate(input.RateMultiplier),
		WorstCaseCostUSD:         canonicalUsageBillingReservation(input.WorstCaseCostUSD),
		AlternateBillingModel:    strings.TrimSpace(input.AlternateBillingModel),
		AlternatePricingSource:   strings.TrimSpace(input.AlternatePricingSource),
		AlternatePricingRevision: strings.TrimSpace(input.AlternatePricingRevision),
		AlternatePricingHash:     strings.ToLower(strings.TrimSpace(input.AlternatePricingHash)),
		AlternateRateMultiplier:  canonicalUsageBillingOptionalRate(input.AlternateRateMultiplier),
	}}
	if err := admission.Validate(); err != nil {
		return UsageBillingAdmission{}, err
	}
	return admission, nil
}

func (a UsageBillingAdmission) Validate() error {
	p := a.payload
	if p.RequestID == "" || len(p.RequestID) > 255 || p.APIKeyID <= 0 || p.UserID <= 0 || p.AccountID <= 0 {
		return ErrUsageBillingAdmissionInvalid
	}
	if p.AuthCacheLocator != "" && !validSHA256(p.AuthCacheLocator) {
		return ErrUsageBillingAdmissionInvalid
	}
	if p.GroupID == nil || *p.GroupID <= 0 || p.EffectiveBillingGroupID == nil || *p.EffectiveBillingGroupID <= 0 {
		return ErrUsageBillingAdmissionInvalid
	}
	if p.SubscriptionID != nil && *p.SubscriptionID <= 0 {
		return ErrUsageBillingAdmissionInvalid
	}
	if p.AccountType == "" || len(p.AccountType) > 32 || !validFenceToken(p.OwnerToken) || !validFenceToken(p.AttemptID) {
		return ErrUsageBillingAdmissionInvalid
	}
	if p.BillingType != BillingTypeBalance && p.BillingType != BillingTypeSubscription {
		return ErrUsageBillingAdmissionInvalid
	}
	if (p.BillingType == BillingTypeBalance) == (p.SubscriptionID != nil) {
		return ErrUsageBillingAdmissionInvalid
	}
	if (UsageBillingReservationQuote{
		RequestPayloadHash:       p.RequestPayloadHash,
		BillingModel:             p.BillingModel,
		PricingSource:            p.PricingSource,
		PricingRevision:          p.PricingRevision,
		PricingHash:              p.PricingHash,
		RateMultiplier:           p.RateMultiplier,
		WorstCaseCostUSD:         p.WorstCaseCostUSD,
		AlternateBillingModel:    p.AlternateBillingModel,
		AlternatePricingSource:   p.AlternatePricingSource,
		AlternatePricingRevision: p.AlternatePricingRevision,
		AlternatePricingHash:     p.AlternatePricingHash,
		AlternateRateMultiplier:  p.AlternateRateMultiplier,
	}).Validate() != nil {
		return ErrUsageBillingAdmissionInvalid
	}
	return nil
}

func validFenceToken(value string) bool {
	if len(value) < 16 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'f') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func (a UsageBillingAdmission) Fingerprint() string {
	p := a.payload
	canonical := usageBillingAdmissionBaseFingerprint{
		RequestID: p.RequestID, APIKeyID: p.APIKeyID, AuthCacheLocator: p.AuthCacheLocator,
		UserID: p.UserID, SubscriptionID: copyInt64(p.SubscriptionID), GroupID: copyInt64(p.GroupID),
		EffectiveBillingGroupID: copyInt64(p.EffectiveBillingGroupID), BillingType: p.BillingType,
		BillingModel: p.BillingModel, RequestPayloadHash: p.RequestPayloadHash,
		PricingSource: p.PricingSource, PricingRevision: p.PricingRevision,
		PricingHash: p.PricingHash, RateMultiplier: p.RateMultiplier, WorstCaseCostUSD: p.WorstCaseCostUSD,
		AlternateBillingModel: p.AlternateBillingModel, AlternatePricingSource: p.AlternatePricingSource,
		AlternatePricingRevision: p.AlternatePricingRevision, AlternatePricingHash: p.AlternatePricingHash,
		AlternateRateMultiplier: p.AlternateRateMultiplier,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (a UsageBillingAdmission) AttemptFingerprint() string {
	raw, err := json.Marshal(struct {
		RequestID   string `json:"request_id"`
		APIKeyID    int64  `json:"api_key_id"`
		AttemptID   string `json:"attempt_id"`
		AccountID   int64  `json:"account_id"`
		AccountType string `json:"account_type"`
	}{a.RequestID(), a.APIKeyID(), a.AttemptID(), a.AccountID(), a.AccountType()})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (a UsageBillingAdmission) MatchesEnvelopeBase(envelope UsageBillingEnvelope) bool {
	if a.Validate() != nil || envelope.Validate() != nil {
		return false
	}
	pricingMatches := a.matchesEnvelopePricing(envelope)
	return a.RequestID() == envelope.RequestID() && a.APIKeyID() == envelope.APIKeyID() &&
		a.RequestPayloadHash() == envelope.RequestPayloadHash() &&
		a.AuthCacheLocator() == envelope.AuthCacheLocator() && a.UserID() == envelope.UserID() &&
		equalOptionalInt64(a.SubscriptionID(), envelope.SubscriptionID()) &&
		equalOptionalInt64(a.GroupID(), envelope.GroupID()) &&
		equalOptionalInt64(a.EffectiveBillingGroupID(), envelope.EffectiveBillingGroupID()) &&
		a.BillingType() == envelope.BillingType() && pricingMatches
}

func (a UsageBillingAdmission) matchesEnvelopePricing(envelope UsageBillingEnvelope) bool {
	primary := a.BillingModel() == envelope.BillingModel() &&
		a.PricingSource() == envelope.PricingSource() && a.PricingRevision() == envelope.PricingRevision() &&
		a.PricingHash() == envelope.PricingHash() && a.RateMultiplier() == envelope.RateMultiplier()
	if primary {
		return true
	}
	return a.AlternateBillingModel() != "" &&
		a.AlternateBillingModel() == envelope.BillingModel() &&
		a.AlternatePricingSource() == envelope.PricingSource() &&
		a.AlternatePricingRevision() == envelope.PricingRevision() &&
		a.AlternatePricingHash() == envelope.PricingHash() &&
		a.AlternateRateMultiplier() == envelope.RateMultiplier()
}

func (a UsageBillingAdmission) AttemptMatchesEnvelope(envelope UsageBillingEnvelope) bool {
	return a.AttemptID() == envelope.AdmissionAttemptID() &&
		a.AccountID() == envelope.AccountID() && a.AccountType() == envelope.AccountType()
}

// MatchesEnvelope is retained for callers that need the complete base+attempt check.
func (a UsageBillingAdmission) MatchesEnvelope(envelope UsageBillingEnvelope) bool {
	return a.MatchesEnvelopeBase(envelope) && a.AttemptMatchesEnvelope(envelope)
}

func equalOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (a UsageBillingAdmission) RequestID() string        { return a.payload.RequestID }
func (a UsageBillingAdmission) APIKeyID() int64          { return a.payload.APIKeyID }
func (a UsageBillingAdmission) AuthCacheLocator() string { return a.payload.AuthCacheLocator }
func (a UsageBillingAdmission) UserID() int64            { return a.payload.UserID }
func (a UsageBillingAdmission) SubscriptionID() *int64   { return copyInt64(a.payload.SubscriptionID) }
func (a UsageBillingAdmission) GroupID() *int64          { return copyInt64(a.payload.GroupID) }
func (a UsageBillingAdmission) EffectiveBillingGroupID() *int64 {
	return copyInt64(a.payload.EffectiveBillingGroupID)
}
func (a UsageBillingAdmission) BillingType() int8          { return a.payload.BillingType }
func (a UsageBillingAdmission) OwnerToken() string         { return a.payload.OwnerToken }
func (a UsageBillingAdmission) AttemptID() string          { return a.payload.AttemptID }
func (a UsageBillingAdmission) AccountID() int64           { return a.payload.AccountID }
func (a UsageBillingAdmission) AccountType() string        { return a.payload.AccountType }
func (a UsageBillingAdmission) BillingModel() string       { return a.payload.BillingModel }
func (a UsageBillingAdmission) RequestPayloadHash() string { return a.payload.RequestPayloadHash }
func (a UsageBillingAdmission) PricingSource() string      { return a.payload.PricingSource }
func (a UsageBillingAdmission) PricingRevision() string    { return a.payload.PricingRevision }
func (a UsageBillingAdmission) PricingHash() string        { return a.payload.PricingHash }
func (a UsageBillingAdmission) RateMultiplier() float64    { return a.payload.RateMultiplier }
func (a UsageBillingAdmission) WorstCaseCostUSD() float64  { return a.payload.WorstCaseCostUSD }
func (a UsageBillingAdmission) AlternateBillingModel() string {
	return a.payload.AlternateBillingModel
}
func (a UsageBillingAdmission) AlternatePricingSource() string {
	return a.payload.AlternatePricingSource
}
func (a UsageBillingAdmission) AlternatePricingRevision() string {
	return a.payload.AlternatePricingRevision
}
func (a UsageBillingAdmission) AlternatePricingHash() string {
	return a.payload.AlternatePricingHash
}
func (a UsageBillingAdmission) AlternateRateMultiplier() float64 {
	return a.payload.AlternateRateMultiplier
}

func canonicalUsageBillingOptionalRate(value float64) float64 {
	if value == 0 {
		return 0
	}
	return canonicalUsageBillingRate(value)
}

type UsageBillingAdmissionRepository interface {
	Admit(ctx context.Context, admission UsageBillingAdmission) error
	MarkDispatched(ctx context.Context, ref UsageBillingAdmissionAttemptRef) error
	MarkAttemptFailed(ctx context.Context, ref UsageBillingAdmissionAttemptRef) error
	MarkOrphaned(ctx context.Context, ref UsageBillingAdmissionAttemptRef) error
	Heartbeat(ctx context.Context, ref UsageBillingAdmissionAttemptRef, lease time.Duration) error
	Abandon(ctx context.Context, ref UsageBillingAdmissionAttemptRef) error
	WaitSettled(ctx context.Context, ref UsageBillingAdmissionAttemptRef) error
}

type UsageBillingAdmissionReconcileResult struct {
	AbandonedPrepared         int
	AbandonedFailedDispatched int
	OrphanedDispatched        int
	FlaggedDeadLetter         int
}

func (r UsageBillingAdmissionReconcileResult) Total() int {
	return r.AbandonedPrepared + r.AbandonedFailedDispatched + r.OrphanedDispatched + r.FlaggedDeadLetter
}

// UsageBillingAdmissionReconciler repairs only lifecycle states whose
// financial outcome is provable. Prepared attempts are safe to release after
// the pre-transport grace period. Dispatched holds are released only when every
// attempt is durably failed; unknown delivery is retained for evidence-based
// reconciliation, and dead-lettered settlements are explicitly flagged.
type UsageBillingAdmissionReconciler interface {
	ReconcileStaleAdmissions(
		ctx context.Context,
		preparedGrace time.Duration,
		dispatchedGrace time.Duration,
		limit int,
	) (UsageBillingAdmissionReconcileResult, error)
}
