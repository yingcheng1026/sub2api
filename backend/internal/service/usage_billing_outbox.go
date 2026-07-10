package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

const (
	UsageBillingEnvelopeVersion  int16 = 1
	UsageBillingEnvelopeMaxBytes       = 16 * 1024
)

var (
	ErrUsageBillingEnvelopeInvalid             = errors.New("usage billing envelope is invalid")
	ErrUsageBillingEnvelopeVersion             = errors.New("usage billing envelope version is unsupported")
	ErrUsageBillingEnvelopeFingerprintMismatch = errors.New("usage billing envelope fingerprint mismatch")
)

// UsageBillingEnvelopeInput is an explicit allow-list of immutable billing
// facts. Request bodies, credentials, headers and client metadata have no place
// in this type and therefore cannot be serialized into the durable outbox.
type UsageBillingEnvelopeInput struct {
	RequestID      string
	APIKeyID       int64
	UserID         int64
	AccountID      int64
	SubscriptionID *int64
	GroupID        *int64

	AccountType     string
	BillingModel    string
	ServiceTier     string
	ReasoningEffort string
	BillingType     int8

	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	ImageCount          int
	MediaType           string

	PricingSource   string
	PricingRevision string
	PricingHash     string

	RateMultiplier        float64
	AccountRateMultiplier float64

	BalanceCost         float64
	SubscriptionCost    float64
	WalletCost          float64
	APIKeyQuotaCost     float64
	APIKeyRateLimitCost float64
	AccountQuotaCost    float64
}

// UsageBillingEnvelope is immutable after construction. Its payload is kept
// private and accessors return values or defensive copies.
type UsageBillingEnvelope struct {
	payload usageBillingEnvelopePayload
}

type usageBillingEnvelopePayload struct {
	Version            int16  `json:"version"`
	RequestID          string `json:"request_id"`
	APIKeyID           int64  `json:"api_key_id"`
	RequestFingerprint string `json:"request_fingerprint"`
	UserID             int64  `json:"user_id"`
	AccountID          int64  `json:"account_id"`
	SubscriptionID     *int64 `json:"subscription_id,omitempty"`
	GroupID            *int64 `json:"group_id,omitempty"`

	AccountType     string `json:"account_type"`
	BillingModel    string `json:"billing_model"`
	ServiceTier     string `json:"service_tier,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	BillingType     int8   `json:"billing_type"`

	InputTokens         int    `json:"input_tokens"`
	OutputTokens        int    `json:"output_tokens"`
	CacheCreationTokens int    `json:"cache_creation_tokens"`
	CacheReadTokens     int    `json:"cache_read_tokens"`
	ImageCount          int    `json:"image_count"`
	MediaType           string `json:"media_type,omitempty"`

	PricingSource   string `json:"pricing_source"`
	PricingRevision string `json:"pricing_revision"`
	PricingHash     string `json:"pricing_hash"`

	RateMultiplier        float64 `json:"rate_multiplier"`
	AccountRateMultiplier float64 `json:"account_rate_multiplier"`

	BalanceCost         float64 `json:"balance_cost"`
	SubscriptionCost    float64 `json:"subscription_cost"`
	WalletCost          float64 `json:"wallet_cost"`
	APIKeyQuotaCost     float64 `json:"api_key_quota_cost"`
	APIKeyRateLimitCost float64 `json:"api_key_rate_limit_cost"`
	AccountQuotaCost    float64 `json:"account_quota_cost"`
}

func NewUsageBillingEnvelope(input UsageBillingEnvelopeInput) (UsageBillingEnvelope, error) {
	payload := usageBillingEnvelopePayload{
		Version:               UsageBillingEnvelopeVersion,
		RequestID:             strings.TrimSpace(input.RequestID),
		APIKeyID:              input.APIKeyID,
		UserID:                input.UserID,
		AccountID:             input.AccountID,
		SubscriptionID:        copyInt64(input.SubscriptionID),
		GroupID:               copyInt64(input.GroupID),
		AccountType:           strings.TrimSpace(input.AccountType),
		BillingModel:          strings.TrimSpace(input.BillingModel),
		ServiceTier:           strings.TrimSpace(input.ServiceTier),
		ReasoningEffort:       strings.TrimSpace(input.ReasoningEffort),
		BillingType:           input.BillingType,
		InputTokens:           input.InputTokens,
		OutputTokens:          input.OutputTokens,
		CacheCreationTokens:   input.CacheCreationTokens,
		CacheReadTokens:       input.CacheReadTokens,
		ImageCount:            input.ImageCount,
		MediaType:             strings.TrimSpace(input.MediaType),
		PricingSource:         strings.TrimSpace(input.PricingSource),
		PricingRevision:       strings.TrimSpace(input.PricingRevision),
		PricingHash:           strings.ToLower(strings.TrimSpace(input.PricingHash)),
		RateMultiplier:        input.RateMultiplier,
		AccountRateMultiplier: input.AccountRateMultiplier,
		BalanceCost:           input.BalanceCost,
		SubscriptionCost:      input.SubscriptionCost,
		WalletCost:            input.WalletCost,
		APIKeyQuotaCost:       input.APIKeyQuotaCost,
		APIKeyRateLimitCost:   input.APIKeyRateLimitCost,
		AccountQuotaCost:      input.AccountQuotaCost,
	}
	if err := validateUsageBillingEnvelopePayload(payload, false); err != nil {
		return UsageBillingEnvelope{}, err
	}
	fingerprint, err := usageBillingEnvelopeFingerprint(payload)
	if err != nil {
		return UsageBillingEnvelope{}, err
	}
	payload.RequestFingerprint = fingerprint
	return UsageBillingEnvelope{payload: payload}, nil
}

func DecodeUsageBillingEnvelope(data []byte) (UsageBillingEnvelope, error) {
	if len(data) == 0 || len(data) > UsageBillingEnvelopeMaxBytes {
		return UsageBillingEnvelope{}, fmt.Errorf("%w: invalid encoded size", ErrUsageBillingEnvelopeInvalid)
	}
	var payload usageBillingEnvelopePayload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return UsageBillingEnvelope{}, fmt.Errorf("%w: decode: %v", ErrUsageBillingEnvelopeInvalid, err)
	}
	if err := ensureUsageBillingEnvelopeEOF(decoder); err != nil {
		return UsageBillingEnvelope{}, err
	}
	if payload.Version != UsageBillingEnvelopeVersion {
		return UsageBillingEnvelope{}, fmt.Errorf("%w: %d", ErrUsageBillingEnvelopeVersion, payload.Version)
	}
	if err := validateUsageBillingEnvelopePayload(payload, true); err != nil {
		return UsageBillingEnvelope{}, err
	}
	expected, err := usageBillingEnvelopeFingerprint(payload)
	if err != nil {
		return UsageBillingEnvelope{}, err
	}
	if !strings.EqualFold(expected, strings.TrimSpace(payload.RequestFingerprint)) {
		return UsageBillingEnvelope{}, ErrUsageBillingEnvelopeFingerprintMismatch
	}
	payload.RequestID = strings.TrimSpace(payload.RequestID)
	payload.RequestFingerprint = strings.ToLower(strings.TrimSpace(payload.RequestFingerprint))
	payload.SubscriptionID = copyInt64(payload.SubscriptionID)
	payload.GroupID = copyInt64(payload.GroupID)
	return UsageBillingEnvelope{payload: payload}, nil
}

func (e UsageBillingEnvelope) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.payload)
}

func (e UsageBillingEnvelope) Version() int16             { return e.payload.Version }
func (e UsageBillingEnvelope) RequestID() string          { return e.payload.RequestID }
func (e UsageBillingEnvelope) APIKeyID() int64            { return e.payload.APIKeyID }
func (e UsageBillingEnvelope) UserID() int64              { return e.payload.UserID }
func (e UsageBillingEnvelope) AccountID() int64           { return e.payload.AccountID }
func (e UsageBillingEnvelope) RequestFingerprint() string { return e.payload.RequestFingerprint }
func (e UsageBillingEnvelope) SubscriptionID() *int64     { return copyInt64(e.payload.SubscriptionID) }
func (e UsageBillingEnvelope) GroupID() *int64            { return copyInt64(e.payload.GroupID) }
func (e UsageBillingEnvelope) AccountType() string        { return e.payload.AccountType }
func (e UsageBillingEnvelope) BillingModel() string       { return e.payload.BillingModel }
func (e UsageBillingEnvelope) BillingType() int8          { return e.payload.BillingType }
func (e UsageBillingEnvelope) BalanceCost() float64       { return e.payload.BalanceCost }
func (e UsageBillingEnvelope) SubscriptionCost() float64  { return e.payload.SubscriptionCost }
func (e UsageBillingEnvelope) WalletCost() float64        { return e.payload.WalletCost }

func (e UsageBillingEnvelope) IsBillable() bool {
	return e.payload.BalanceCost > 0 || e.payload.SubscriptionCost > 0 || e.payload.WalletCost > 0 ||
		e.payload.APIKeyQuotaCost > 0 || e.payload.APIKeyRateLimitCost > 0 || e.payload.AccountQuotaCost > 0
}

func (e UsageBillingEnvelope) Validate() error {
	if err := validateUsageBillingEnvelopePayload(e.payload, true); err != nil {
		return err
	}
	expected, err := usageBillingEnvelopeFingerprint(e.payload)
	if err != nil {
		return err
	}
	if !strings.EqualFold(expected, e.payload.RequestFingerprint) {
		return ErrUsageBillingEnvelopeFingerprintMismatch
	}
	return nil
}

func (e UsageBillingEnvelope) Command() *UsageBillingCommand {
	return &UsageBillingCommand{
		RequestID:           e.payload.RequestID,
		APIKeyID:            e.payload.APIKeyID,
		RequestFingerprint:  e.payload.RequestFingerprint,
		UserID:              e.payload.UserID,
		AccountID:           e.payload.AccountID,
		SubscriptionID:      copyInt64(e.payload.SubscriptionID),
		AccountType:         e.payload.AccountType,
		Model:               e.payload.BillingModel,
		ServiceTier:         e.payload.ServiceTier,
		ReasoningEffort:     e.payload.ReasoningEffort,
		BillingType:         e.payload.BillingType,
		BindingsFrozen:      true,
		InputTokens:         e.payload.InputTokens,
		OutputTokens:        e.payload.OutputTokens,
		CacheCreationTokens: e.payload.CacheCreationTokens,
		CacheReadTokens:     e.payload.CacheReadTokens,
		ImageCount:          e.payload.ImageCount,
		MediaType:           e.payload.MediaType,
		BalanceCost:         e.payload.BalanceCost,
		SubscriptionCost:    e.payload.SubscriptionCost,
		WalletCost:          e.payload.WalletCost,
		APIKeyQuotaCost:     e.payload.APIKeyQuotaCost,
		APIKeyRateLimitCost: e.payload.APIKeyRateLimitCost,
		AccountQuotaCost:    e.payload.AccountQuotaCost,
	}
}

func validateUsageBillingEnvelopePayload(payload usageBillingEnvelopePayload, requireFingerprint bool) error {
	if payload.Version != UsageBillingEnvelopeVersion {
		return fmt.Errorf("%w: version=%d", ErrUsageBillingEnvelopeVersion, payload.Version)
	}
	if strings.TrimSpace(payload.RequestID) == "" || len(strings.TrimSpace(payload.RequestID)) > 255 ||
		payload.APIKeyID <= 0 || payload.UserID <= 0 || payload.AccountID <= 0 {
		return fmt.Errorf("%w: invalid identity", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.SubscriptionID != nil && *payload.SubscriptionID <= 0 {
		return fmt.Errorf("%w: invalid subscription_id", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.GroupID != nil && *payload.GroupID <= 0 {
		return fmt.Errorf("%w: invalid group_id", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.GroupID == nil {
		return fmt.Errorf("%w: group_id is required", ErrUsageBillingEnvelopeInvalid)
	}
	if !validUsageBillingEnvelopeText(payload.AccountType, 32, true) ||
		!validUsageBillingEnvelopeText(payload.BillingModel, 128, true) ||
		!validUsageBillingEnvelopeText(payload.ServiceTier, 64, false) ||
		!validUsageBillingEnvelopeText(payload.ReasoningEffort, 64, false) ||
		!validUsageBillingEnvelopeText(payload.MediaType, 128, false) {
		return fmt.Errorf("%w: missing billing identity", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.BillingType != BillingTypeBalance && payload.BillingType != BillingTypeSubscription {
		return fmt.Errorf("%w: invalid billing_type", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.InputTokens < 0 || payload.OutputTokens < 0 || payload.CacheCreationTokens < 0 ||
		payload.CacheReadTokens < 0 || payload.ImageCount < 0 {
		return fmt.Errorf("%w: negative usage", ErrUsageBillingEnvelopeInvalid)
	}
	if !validUsageBillingPricingSource(payload.PricingSource) ||
		!validUsageBillingEnvelopeText(payload.PricingRevision, 128, true) ||
		!validSHA256(payload.PricingHash) {
		return fmt.Errorf("%w: invalid pricing evidence", ErrUsageBillingEnvelopeInvalid)
	}
	values := []float64{
		payload.RateMultiplier, payload.AccountRateMultiplier, payload.BalanceCost,
		payload.SubscriptionCost, payload.WalletCost, payload.APIKeyQuotaCost,
		payload.APIKeyRateLimitCost, payload.AccountQuotaCost,
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return fmt.Errorf("%w: invalid numeric value", ErrUsageBillingEnvelopeInvalid)
		}
	}
	primaryCosts := 0
	for _, value := range []float64{payload.BalanceCost, payload.SubscriptionCost, payload.WalletCost} {
		if value > 0 {
			primaryCosts++
		}
	}
	if primaryCosts > 1 {
		return fmt.Errorf("%w: incompatible primary billing costs", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.BalanceCost > 0 && payload.BillingType != BillingTypeBalance {
		return fmt.Errorf("%w: balance cost with subscription billing", ErrUsageBillingEnvelopeInvalid)
	}
	if (payload.SubscriptionCost > 0 || payload.WalletCost > 0) &&
		(payload.BillingType != BillingTypeSubscription || payload.SubscriptionID == nil) {
		return fmt.Errorf("%w: subscription cost without frozen subscription", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.BillingType == BillingTypeSubscription && payload.SubscriptionID == nil {
		return fmt.Errorf("%w: subscription billing without frozen subscription", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.BillingType == BillingTypeBalance && payload.SubscriptionID != nil {
		return fmt.Errorf("%w: balance billing contains subscription", ErrUsageBillingEnvelopeInvalid)
	}
	if requireFingerprint && !validSHA256(payload.RequestFingerprint) {
		return fmt.Errorf("%w: invalid request fingerprint", ErrUsageBillingEnvelopeInvalid)
	}
	return nil
}

func usageBillingEnvelopeFingerprint(payload usageBillingEnvelopePayload) (string, error) {
	payload.RequestFingerprint = ""
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: fingerprint: %v", ErrUsageBillingEnvelopeInvalid, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func validSHA256(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func ensureUsageBillingEnvelopeEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("%w: trailing JSON value", ErrUsageBillingEnvelopeInvalid)
	}
	return fmt.Errorf("%w: trailing data: %v", ErrUsageBillingEnvelopeInvalid, err)
}

func validUsageBillingEnvelopeText(value string, maxLen int, required bool) bool {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return false
	}
	if len(value) > maxLen {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validUsageBillingPricingSource(value string) bool {
	switch strings.TrimSpace(value) {
	case PricingSourceChannel,
		PricingSourceLiteLLM,
		PricingSourceFallback,
		PricingSourceBuiltinGPT56,
		PricingSourceBuiltinFallback,
		PricingSourceGroupImage:
		return true
	default:
		return false
	}
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
