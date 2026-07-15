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
	"time"
)

const (
	UsageBillingEnvelopeVersion  int16 = UsageBillingFencedEnvelopeVersion
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
	RequestID               string
	RequestPayloadHash      string
	AdmissionAttemptID      string
	APIKeyID                int64
	AuthCacheLocator        string
	UserID                  int64
	AccountID               int64
	SubscriptionID          *int64
	GroupID                 *int64
	EffectiveBillingGroupID *int64

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
	ImageSize           string

	ExecutionModel     string
	RequestedModel     string
	UpstreamModel      string
	ChannelID          *int64
	ModelMappingChain  string
	BillingTier        string
	BillingMode        string
	InboundEndpoint    string
	UpstreamEndpoint   string
	RequestType        RequestType
	OccurredAtUnixMs   int64
	DurationMs         *int
	FirstTokenMs       *int
	CacheTTLOverridden bool

	CacheCreation5mTokens int
	CacheCreation1hTokens int
	ImageOutputTokens     int

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

	InputCost         float64
	OutputCost        float64
	CacheCreationCost float64
	CacheReadCost     float64
	ImageOutputCost   float64
	TotalCost         float64
	ActualCost        float64
	AccountStatsCost  *float64
}

// UsageBillingEnvelope is immutable after construction. Its payload is kept
// private and accessors return values or defensive copies.
type UsageBillingEnvelope struct {
	payload usageBillingEnvelopePayload
}

type usageBillingEnvelopePayload struct {
	Version                 int16  `json:"version"`
	RequestID               string `json:"request_id"`
	RequestPayloadHash      string `json:"request_payload_hash,omitempty"`
	AdmissionAttemptID      string `json:"admission_attempt_id,omitempty"`
	APIKeyID                int64  `json:"api_key_id"`
	AuthCacheLocator        string `json:"auth_cache_locator,omitempty"`
	RequestFingerprint      string `json:"request_fingerprint"`
	UserID                  int64  `json:"user_id"`
	AccountID               int64  `json:"account_id"`
	SubscriptionID          *int64 `json:"subscription_id,omitempty"`
	GroupID                 *int64 `json:"group_id,omitempty"`
	EffectiveBillingGroupID *int64 `json:"effective_billing_group_id,omitempty"`

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
	ImageSize           string `json:"image_size,omitempty"`

	ExecutionModel     string `json:"execution_model,omitempty"`
	RequestedModel     string `json:"requested_model,omitempty"`
	UpstreamModel      string `json:"upstream_model,omitempty"`
	ChannelID          *int64 `json:"channel_id,omitempty"`
	ModelMappingChain  string `json:"model_mapping_chain,omitempty"`
	BillingTier        string `json:"billing_tier,omitempty"`
	BillingMode        string `json:"billing_mode,omitempty"`
	InboundEndpoint    string `json:"inbound_endpoint,omitempty"`
	UpstreamEndpoint   string `json:"upstream_endpoint,omitempty"`
	RequestType        int16  `json:"request_type,omitempty"`
	OccurredAtUnixMs   int64  `json:"occurred_at_unix_ms,omitempty"`
	DurationMs         *int   `json:"duration_ms,omitempty"`
	FirstTokenMs       *int   `json:"first_token_ms,omitempty"`
	CacheTTLOverridden bool   `json:"cache_ttl_overridden,omitempty"`

	CacheCreation5mTokens int `json:"cache_creation_5m_tokens"`
	CacheCreation1hTokens int `json:"cache_creation_1h_tokens"`
	ImageOutputTokens     int `json:"image_output_tokens"`

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

	InputCost         float64  `json:"input_cost"`
	OutputCost        float64  `json:"output_cost"`
	CacheCreationCost float64  `json:"cache_creation_cost"`
	CacheReadCost     float64  `json:"cache_read_cost"`
	ImageOutputCost   float64  `json:"image_output_cost"`
	TotalCost         float64  `json:"total_cost"`
	ActualCost        float64  `json:"actual_cost"`
	AccountStatsCost  *float64 `json:"account_stats_cost,omitempty"`
}

func NewUsageBillingEnvelope(input UsageBillingEnvelopeInput) (UsageBillingEnvelope, error) {
	effectiveBillingGroupID := input.EffectiveBillingGroupID
	if effectiveBillingGroupID == nil {
		effectiveBillingGroupID = input.GroupID
	}
	payload := usageBillingEnvelopePayload{
		Version:                 UsageBillingEnvelopeVersion,
		RequestID:               strings.TrimSpace(input.RequestID),
		RequestPayloadHash:      strings.ToLower(strings.TrimSpace(input.RequestPayloadHash)),
		AdmissionAttemptID:      strings.ToLower(strings.TrimSpace(input.AdmissionAttemptID)),
		APIKeyID:                input.APIKeyID,
		AuthCacheLocator:        strings.ToLower(strings.TrimSpace(input.AuthCacheLocator)),
		UserID:                  input.UserID,
		AccountID:               input.AccountID,
		SubscriptionID:          copyInt64(input.SubscriptionID),
		GroupID:                 copyInt64(input.GroupID),
		EffectiveBillingGroupID: copyInt64(effectiveBillingGroupID),
		AccountType:             strings.TrimSpace(input.AccountType),
		BillingModel:            strings.TrimSpace(input.BillingModel),
		ServiceTier:             strings.TrimSpace(input.ServiceTier),
		ReasoningEffort:         strings.TrimSpace(input.ReasoningEffort),
		BillingType:             input.BillingType,
		InputTokens:             input.InputTokens,
		OutputTokens:            input.OutputTokens,
		CacheCreationTokens:     input.CacheCreationTokens,
		CacheReadTokens:         input.CacheReadTokens,
		ImageCount:              input.ImageCount,
		MediaType:               strings.TrimSpace(input.MediaType),
		ImageSize:               strings.TrimSpace(input.ImageSize),
		ExecutionModel:          strings.TrimSpace(input.ExecutionModel),
		RequestedModel:          strings.TrimSpace(input.RequestedModel),
		UpstreamModel:           strings.TrimSpace(input.UpstreamModel),
		ChannelID:               copyInt64(input.ChannelID),
		ModelMappingChain:       strings.TrimSpace(input.ModelMappingChain),
		BillingTier:             strings.TrimSpace(input.BillingTier),
		BillingMode:             strings.TrimSpace(input.BillingMode),
		InboundEndpoint:         strings.TrimSpace(input.InboundEndpoint),
		UpstreamEndpoint:        strings.TrimSpace(input.UpstreamEndpoint),
		RequestType:             int16(input.RequestType.Normalize()),
		OccurredAtUnixMs:        input.OccurredAtUnixMs,
		DurationMs:              copyInt(input.DurationMs),
		FirstTokenMs:            copyInt(input.FirstTokenMs),
		CacheTTLOverridden:      input.CacheTTLOverridden,
		CacheCreation5mTokens:   input.CacheCreation5mTokens,
		CacheCreation1hTokens:   input.CacheCreation1hTokens,
		ImageOutputTokens:       input.ImageOutputTokens,
		PricingSource:           strings.TrimSpace(input.PricingSource),
		PricingRevision:         strings.TrimSpace(input.PricingRevision),
		PricingHash:             strings.ToLower(strings.TrimSpace(input.PricingHash)),
		RateMultiplier:          canonicalUsageBillingRate(input.RateMultiplier),
		AccountRateMultiplier:   input.AccountRateMultiplier,
		BalanceCost:             input.BalanceCost,
		SubscriptionCost:        input.SubscriptionCost,
		WalletCost:              input.WalletCost,
		APIKeyQuotaCost:         input.APIKeyQuotaCost,
		APIKeyRateLimitCost:     input.APIKeyRateLimitCost,
		AccountQuotaCost:        input.AccountQuotaCost,
		InputCost:               input.InputCost,
		OutputCost:              input.OutputCost,
		CacheCreationCost:       input.CacheCreationCost,
		CacheReadCost:           input.CacheReadCost,
		ImageOutputCost:         input.ImageOutputCost,
		TotalCost:               input.TotalCost,
		ActualCost:              input.ActualCost,
		AccountStatsCost:        copyFloat64(input.AccountStatsCost),
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

// NewUsageBillingEnvelopeFromUsageLog freezes the exact billing command and
// the safe subset of usage-log facts needed for deterministic replay. Request
// bodies, credentials, headers, IP addresses and user agents are deliberately
// not copied.
func NewUsageBillingEnvelopeFromUsageLog(log *UsageLog, cmd *UsageBillingCommand) (UsageBillingEnvelope, error) {
	if log == nil || cmd == nil {
		return UsageBillingEnvelope{}, fmt.Errorf("%w: usage log or command is nil", ErrUsageBillingEnvelopeInvalid)
	}
	if strings.TrimSpace(log.RequestID) == "" || strings.TrimSpace(cmd.RequestID) != strings.TrimSpace(log.RequestID) ||
		cmd.APIKeyID != log.APIKeyID || cmd.UserID != log.UserID || cmd.AccountID != log.AccountID {
		return UsageBillingEnvelope{}, fmt.Errorf("%w: usage log identity mismatch", ErrUsageBillingEnvelopeInvalid)
	}
	if log.GroupID == nil || log.CreatedAt.IsZero() {
		return UsageBillingEnvelope{}, fmt.Errorf("%w: replay group or occurrence time is missing", ErrUsageBillingEnvelopeInvalid)
	}
	billingModel := optionalStringValue(log.BillingModel)
	if billingModel == "" {
		billingModel = strings.TrimSpace(cmd.Model)
	}
	executionModel := strings.TrimSpace(log.Model)
	if executionModel == "" {
		executionModel = billingModel
	}
	requestedModel := strings.TrimSpace(log.RequestedModel)
	if requestedModel == "" {
		requestedModel = executionModel
	}
	upstreamModel := optionalStringValue(log.UpstreamModel)
	if upstreamModel == "" {
		upstreamModel = executionModel
	}
	requestType := log.EffectiveRequestType()

	return NewUsageBillingEnvelope(UsageBillingEnvelopeInput{
		RequestID:               log.RequestID,
		RequestPayloadHash:      cmd.RequestPayloadHash,
		AdmissionAttemptID:      log.UsageBillingAttemptID,
		APIKeyID:                log.APIKeyID,
		AuthCacheLocator:        cmd.AuthCacheLocator,
		UserID:                  log.UserID,
		AccountID:               log.AccountID,
		SubscriptionID:          cmd.SubscriptionID,
		GroupID:                 log.GroupID,
		EffectiveBillingGroupID: cmd.EffectiveBillingGroupID,
		AccountType:             cmd.AccountType,
		BillingModel:            billingModel,
		ServiceTier:             optionalStringValue(log.ServiceTier),
		ReasoningEffort:         optionalStringValue(log.ReasoningEffort),
		BillingType:             cmd.BillingType,
		InputTokens:             cmd.InputTokens,
		OutputTokens:            cmd.OutputTokens,
		CacheCreationTokens:     cmd.CacheCreationTokens,
		CacheReadTokens:         cmd.CacheReadTokens,
		ImageCount:              cmd.ImageCount,
		MediaType:               cmd.MediaType,
		ImageSize:               optionalStringValue(log.ImageSize),
		ExecutionModel:          executionModel,
		RequestedModel:          requestedModel,
		UpstreamModel:           upstreamModel,
		ChannelID:               log.ChannelID,
		ModelMappingChain:       optionalStringValue(log.ModelMappingChain),
		BillingTier:             optionalStringValue(log.BillingTier),
		BillingMode:             optionalStringValue(log.BillingMode),
		InboundEndpoint:         optionalStringValue(log.InboundEndpoint),
		UpstreamEndpoint:        optionalStringValue(log.UpstreamEndpoint),
		RequestType:             requestType,
		OccurredAtUnixMs:        log.CreatedAt.UTC().UnixMilli(),
		DurationMs:              log.DurationMs,
		FirstTokenMs:            log.FirstTokenMs,
		CacheTTLOverridden:      log.CacheTTLOverridden,
		CacheCreation5mTokens:   log.CacheCreation5mTokens,
		CacheCreation1hTokens:   log.CacheCreation1hTokens,
		ImageOutputTokens:       log.ImageOutputTokens,
		PricingSource:           optionalStringValue(log.PricingSource),
		PricingRevision:         optionalStringValue(log.PricingRevision),
		PricingHash:             optionalStringValue(log.PricingHash),
		RateMultiplier:          log.RateMultiplier,
		AccountRateMultiplier:   float64Value(log.AccountRateMultiplier),
		BalanceCost:             cmd.BalanceCost,
		SubscriptionCost:        cmd.SubscriptionCost,
		WalletCost:              cmd.WalletCost,
		APIKeyQuotaCost:         cmd.APIKeyQuotaCost,
		APIKeyRateLimitCost:     cmd.APIKeyRateLimitCost,
		AccountQuotaCost:        cmd.AccountQuotaCost,
		InputCost:               log.InputCost,
		OutputCost:              log.OutputCost,
		CacheCreationCost:       log.CacheCreationCost,
		CacheReadCost:           log.CacheReadCost,
		ImageOutputCost:         log.ImageOutputCost,
		TotalCost:               log.TotalCost,
		ActualCost:              log.ActualCost,
		AccountStatsCost:        log.AccountStatsCost,
	})
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
	if payload.Version != UsageBillingLegacyEnvelopeVersion && payload.Version != UsageBillingFencedEnvelopeVersion {
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
	payload.RequestPayloadHash = strings.ToLower(strings.TrimSpace(payload.RequestPayloadHash))
	payload.RequestFingerprint = strings.ToLower(strings.TrimSpace(payload.RequestFingerprint))
	payload.SubscriptionID = copyInt64(payload.SubscriptionID)
	payload.GroupID = copyInt64(payload.GroupID)
	payload.EffectiveBillingGroupID = copyInt64(payload.EffectiveBillingGroupID)
	payload.ChannelID = copyInt64(payload.ChannelID)
	payload.DurationMs = copyInt(payload.DurationMs)
	payload.FirstTokenMs = copyInt(payload.FirstTokenMs)
	payload.AccountStatsCost = copyFloat64(payload.AccountStatsCost)
	return UsageBillingEnvelope{payload: payload}, nil
}

func (e UsageBillingEnvelope) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.payload)
}

func (e UsageBillingEnvelope) Version() int16             { return e.payload.Version }
func (e UsageBillingEnvelope) RequestID() string          { return e.payload.RequestID }
func (e UsageBillingEnvelope) RequestPayloadHash() string { return e.payload.RequestPayloadHash }
func (e UsageBillingEnvelope) AdmissionAttemptID() string { return e.payload.AdmissionAttemptID }
func (e UsageBillingEnvelope) APIKeyID() int64            { return e.payload.APIKeyID }
func (e UsageBillingEnvelope) AuthCacheLocator() string   { return e.payload.AuthCacheLocator }
func (e UsageBillingEnvelope) UserID() int64              { return e.payload.UserID }
func (e UsageBillingEnvelope) AccountID() int64           { return e.payload.AccountID }
func (e UsageBillingEnvelope) RequestFingerprint() string { return e.payload.RequestFingerprint }
func (e UsageBillingEnvelope) SubscriptionID() *int64     { return copyInt64(e.payload.SubscriptionID) }
func (e UsageBillingEnvelope) GroupID() *int64            { return copyInt64(e.payload.GroupID) }
func (e UsageBillingEnvelope) EffectiveBillingGroupID() *int64 {
	return copyInt64(e.payload.EffectiveBillingGroupID)
}
func (e UsageBillingEnvelope) AccountType() string     { return e.payload.AccountType }
func (e UsageBillingEnvelope) BillingModel() string    { return e.payload.BillingModel }
func (e UsageBillingEnvelope) BillingType() int8       { return e.payload.BillingType }
func (e UsageBillingEnvelope) PricingSource() string   { return e.payload.PricingSource }
func (e UsageBillingEnvelope) PricingRevision() string { return e.payload.PricingRevision }
func (e UsageBillingEnvelope) PricingHash() string     { return e.payload.PricingHash }
func (e UsageBillingEnvelope) RateMultiplier() float64 { return e.payload.RateMultiplier }
func (e UsageBillingEnvelope) AccountRateMultiplier() float64 {
	return e.payload.AccountRateMultiplier
}
func (e UsageBillingEnvelope) ActualCost() float64 { return e.payload.ActualCost }
func (e UsageBillingEnvelope) TotalCost() float64  { return e.payload.TotalCost }
func (e UsageBillingEnvelope) PrimaryBillingCost() float64 {
	return e.payload.BalanceCost + e.payload.SubscriptionCost + e.payload.WalletCost
}
func (e UsageBillingEnvelope) BalanceCost() float64      { return e.payload.BalanceCost }
func (e UsageBillingEnvelope) SubscriptionCost() float64 { return e.payload.SubscriptionCost }
func (e UsageBillingEnvelope) WalletCost() float64       { return e.payload.WalletCost }
func (e UsageBillingEnvelope) APIKeyQuotaCost() float64  { return e.payload.APIKeyQuotaCost }
func (e UsageBillingEnvelope) APIKeyRateLimitCost() float64 {
	return e.payload.APIKeyRateLimitCost
}
func (e UsageBillingEnvelope) AccountQuotaCost() float64 { return e.payload.AccountQuotaCost }

func (e UsageBillingEnvelope) UsageLog() *UsageLog {
	model := strings.TrimSpace(e.payload.ExecutionModel)
	if model == "" {
		model = e.payload.BillingModel
	}
	requestedModel := strings.TrimSpace(e.payload.RequestedModel)
	if requestedModel == "" {
		requestedModel = model
	}
	billingModel := e.payload.BillingModel
	pricingSource := e.payload.PricingSource
	pricingRevision := e.payload.PricingRevision
	pricingHash := e.payload.PricingHash
	accountRateMultiplier := e.payload.AccountRateMultiplier
	log := &UsageLog{
		UserID:                e.payload.UserID,
		APIKeyID:              e.payload.APIKeyID,
		AccountID:             e.payload.AccountID,
		RequestID:             e.payload.RequestID,
		UsageBillingAttemptID: e.payload.AdmissionAttemptID,
		Model:                 model,
		RequestedModel:        requestedModel,
		UpstreamModel:         optionalTrimmedStringPtr(e.payload.UpstreamModel),
		BillingModel:          &billingModel,
		PricingSource:         &pricingSource,
		PricingRevision:       &pricingRevision,
		PricingHash:           &pricingHash,
		ChannelID:             copyInt64(e.payload.ChannelID),
		ModelMappingChain:     optionalTrimmedStringPtr(e.payload.ModelMappingChain),
		BillingTier:           optionalTrimmedStringPtr(e.payload.BillingTier),
		BillingMode:           optionalTrimmedStringPtr(e.payload.BillingMode),
		ServiceTier:           optionalTrimmedStringPtr(e.payload.ServiceTier),
		ReasoningEffort:       optionalTrimmedStringPtr(e.payload.ReasoningEffort),
		InboundEndpoint:       optionalTrimmedStringPtr(e.payload.InboundEndpoint),
		UpstreamEndpoint:      optionalTrimmedStringPtr(e.payload.UpstreamEndpoint),
		GroupID:               copyInt64(e.payload.GroupID),
		SubscriptionID:        copyInt64(e.payload.SubscriptionID),
		InputTokens:           e.payload.InputTokens,
		OutputTokens:          e.payload.OutputTokens,
		CacheCreationTokens:   e.payload.CacheCreationTokens,
		CacheReadTokens:       e.payload.CacheReadTokens,
		CacheCreation5mTokens: e.payload.CacheCreation5mTokens,
		CacheCreation1hTokens: e.payload.CacheCreation1hTokens,
		ImageOutputTokens:     e.payload.ImageOutputTokens,
		ImageOutputCost:       e.payload.ImageOutputCost,
		InputCost:             e.payload.InputCost,
		OutputCost:            e.payload.OutputCost,
		CacheCreationCost:     e.payload.CacheCreationCost,
		CacheReadCost:         e.payload.CacheReadCost,
		TotalCost:             e.payload.TotalCost,
		ActualCost:            e.payload.ActualCost,
		RateMultiplier:        e.payload.RateMultiplier,
		AccountRateMultiplier: &accountRateMultiplier,
		AccountStatsCost:      copyFloat64(e.payload.AccountStatsCost),
		BillingType:           e.payload.BillingType,
		RequestType:           RequestTypeFromInt16(e.payload.RequestType),
		DurationMs:            copyInt(e.payload.DurationMs),
		FirstTokenMs:          copyInt(e.payload.FirstTokenMs),
		CacheTTLOverridden:    e.payload.CacheTTLOverridden,
		ImageCount:            e.payload.ImageCount,
		ImageSize:             optionalTrimmedStringPtr(e.payload.ImageSize),
		MediaType:             optionalTrimmedStringPtr(e.payload.MediaType),
	}
	if e.payload.OccurredAtUnixMs > 0 {
		log.CreatedAt = time.UnixMilli(e.payload.OccurredAtUnixMs).UTC()
	}
	log.SyncRequestTypeAndLegacyFields()
	return log
}

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
		RequestID:               e.payload.RequestID,
		RequestPayloadHash:      e.payload.RequestPayloadHash,
		APIKeyID:                e.payload.APIKeyID,
		AuthCacheLocator:        e.payload.AuthCacheLocator,
		RequestFingerprint:      e.payload.RequestFingerprint,
		UserID:                  e.payload.UserID,
		AccountID:               e.payload.AccountID,
		SubscriptionID:          copyInt64(e.payload.SubscriptionID),
		EffectiveBillingGroupID: copyInt64(e.payload.EffectiveBillingGroupID),
		AccountType:             e.payload.AccountType,
		Model:                   e.payload.BillingModel,
		ServiceTier:             e.payload.ServiceTier,
		ReasoningEffort:         e.payload.ReasoningEffort,
		BillingType:             e.payload.BillingType,
		BindingsFrozen:          true,
		InputTokens:             e.payload.InputTokens,
		OutputTokens:            e.payload.OutputTokens,
		CacheCreationTokens:     e.payload.CacheCreationTokens,
		CacheReadTokens:         e.payload.CacheReadTokens,
		ImageCount:              e.payload.ImageCount,
		MediaType:               e.payload.MediaType,
		BalanceCost:             e.payload.BalanceCost,
		SubscriptionCost:        e.payload.SubscriptionCost,
		WalletCost:              e.payload.WalletCost,
		APIKeyQuotaCost:         e.payload.APIKeyQuotaCost,
		APIKeyRateLimitCost:     e.payload.APIKeyRateLimitCost,
		AccountQuotaCost:        e.payload.AccountQuotaCost,
	}
}

func validateUsageBillingEnvelopePayload(payload usageBillingEnvelopePayload, requireFingerprint bool) error {
	if payload.Version != UsageBillingLegacyEnvelopeVersion && payload.Version != UsageBillingFencedEnvelopeVersion {
		return fmt.Errorf("%w: version=%d", ErrUsageBillingEnvelopeVersion, payload.Version)
	}
	if strings.TrimSpace(payload.RequestID) == "" || len(strings.TrimSpace(payload.RequestID)) > 255 ||
		payload.APIKeyID <= 0 || payload.UserID <= 0 || payload.AccountID <= 0 {
		return fmt.Errorf("%w: invalid identity", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.Version == UsageBillingFencedEnvelopeVersion {
		if !validSHA256(payload.RequestPayloadHash) {
			return fmt.Errorf("%w: request_payload_hash is required", ErrUsageBillingEnvelopeInvalid)
		}
	} else if payload.RequestPayloadHash != "" && !validSHA256(payload.RequestPayloadHash) {
		return fmt.Errorf("%w: invalid request_payload_hash", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.AuthCacheLocator != "" && !validSHA256(payload.AuthCacheLocator) {
		return fmt.Errorf("%w: invalid auth_cache_locator", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.AdmissionAttemptID != "" && !validFenceToken(payload.AdmissionAttemptID) {
		return fmt.Errorf("%w: invalid admission_attempt_id", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.APIKeyQuotaCost > 0 && !validSHA256(payload.AuthCacheLocator) {
		return fmt.Errorf("%w: auth_cache_locator is required for quota billing", ErrUsageBillingEnvelopeInvalid)
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
	if payload.EffectiveBillingGroupID == nil || *payload.EffectiveBillingGroupID <= 0 {
		return fmt.Errorf("%w: effective_billing_group_id is required", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.ChannelID != nil && *payload.ChannelID <= 0 {
		return fmt.Errorf("%w: invalid channel_id", ErrUsageBillingEnvelopeInvalid)
	}
	if (payload.DurationMs != nil && *payload.DurationMs < 0) || (payload.FirstTokenMs != nil && *payload.FirstTokenMs < 0) {
		return fmt.Errorf("%w: invalid timing", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.OccurredAtUnixMs < 0 || !RequestType(payload.RequestType).IsValid() {
		return fmt.Errorf("%w: invalid replay occurrence", ErrUsageBillingEnvelopeInvalid)
	}
	if !validUsageBillingEnvelopeText(payload.AccountType, 32, true) ||
		!validUsageBillingEnvelopeText(payload.BillingModel, 128, true) ||
		!validUsageBillingEnvelopeText(payload.ServiceTier, 64, false) ||
		!validUsageBillingEnvelopeText(payload.ReasoningEffort, 64, false) ||
		!validUsageBillingEnvelopeText(payload.MediaType, 128, false) ||
		!validUsageBillingEnvelopeText(payload.ImageSize, 64, false) ||
		!validUsageBillingEnvelopeText(payload.ExecutionModel, 128, false) ||
		!validUsageBillingEnvelopeText(payload.RequestedModel, 128, false) ||
		!validUsageBillingEnvelopeText(payload.UpstreamModel, 128, false) ||
		!validUsageBillingEnvelopeText(payload.ModelMappingChain, 1024, false) ||
		!validUsageBillingEnvelopeText(payload.BillingTier, 64, false) ||
		!validUsageBillingEnvelopeText(payload.BillingMode, 32, false) ||
		!validUsageBillingEnvelopeText(payload.InboundEndpoint, 255, false) ||
		!validUsageBillingEnvelopeText(payload.UpstreamEndpoint, 255, false) {
		return fmt.Errorf("%w: missing billing identity", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.BillingType != BillingTypeBalance && payload.BillingType != BillingTypeSubscription {
		return fmt.Errorf("%w: invalid billing_type", ErrUsageBillingEnvelopeInvalid)
	}
	if payload.InputTokens < 0 || payload.OutputTokens < 0 || payload.CacheCreationTokens < 0 ||
		payload.CacheReadTokens < 0 || payload.CacheCreation5mTokens < 0 || payload.CacheCreation1hTokens < 0 ||
		payload.ImageOutputTokens < 0 || payload.ImageCount < 0 {
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
		payload.InputCost, payload.OutputCost, payload.CacheCreationCost, payload.CacheReadCost,
		payload.ImageOutputCost, payload.TotalCost, payload.ActualCost,
	}
	if payload.AccountStatsCost != nil {
		values = append(values, *payload.AccountStatsCost)
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
	if payload.OccurredAtUnixMs > 0 || strings.TrimSpace(payload.ExecutionModel) != "" {
		primaryCost := payload.BalanceCost + payload.SubscriptionCost + payload.WalletCost
		if math.Abs(primaryCost-payload.ActualCost) > 1e-9 {
			return fmt.Errorf("%w: replay cost does not match billing effect", ErrUsageBillingEnvelopeInvalid)
		}
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

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func copyFloat64(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func float64Value(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
