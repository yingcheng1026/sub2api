package service

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func validUsageBillingEnvelopeInput() UsageBillingEnvelopeInput {
	groupID := int64(44)
	return UsageBillingEnvelopeInput{
		RequestID:               "req-outbox-1",
		APIKeyID:                11,
		AuthCacheLocator:        strings.Repeat("c", 64),
		UserID:                  22,
		AccountID:               33,
		GroupID:                 &groupID,
		EffectiveBillingGroupID: &groupID,
		AccountType:             AccountTypeAPIKey,
		BillingModel:            "gpt-5.6-sol",
		ServiceTier:             "priority",
		ReasoningEffort:         "high",
		BillingType:             BillingTypeBalance,
		InputTokens:             100,
		OutputTokens:            20,
		CacheReadTokens:         10,
		PricingSource:           PricingSourceBuiltinGPT56,
		PricingRevision:         GPT56PricingRevision,
		PricingHash:             strings.Repeat("a", 64),
		RateMultiplier:          1.25,
		AccountRateMultiplier:   1.1,
		BalanceCost:             2.5,
		APIKeyQuotaCost:         2.5,
		APIKeyRateLimitCost:     2.5,
		AccountQuotaCost:        2,
	}
}

func TestUsageBillingEnvelope_DecodeRejectsUnknownFieldsAndOversizedPayload(t *testing.T) {
	envelope, err := NewUsageBillingEnvelope(validUsageBillingEnvelopeInput())
	require.NoError(t, err)
	raw, err := envelope.MarshalJSON()
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	payload["prompt"] = "must never be accepted"
	withUnknownField, err := json.Marshal(payload)
	require.NoError(t, err)
	_, err = DecodeUsageBillingEnvelope(withUnknownField)
	require.ErrorIs(t, err, ErrUsageBillingEnvelopeInvalid)

	_, err = DecodeUsageBillingEnvelope(make([]byte, UsageBillingEnvelopeMaxBytes+1))
	require.ErrorIs(t, err, ErrUsageBillingEnvelopeInvalid)
}

func TestUsageBillingEnvelope_RejectsUnboundedTextAndUnknownPricingSource(t *testing.T) {
	input := validUsageBillingEnvelopeInput()
	input.RequestID = strings.Repeat("r", 256)
	_, err := NewUsageBillingEnvelope(input)
	require.ErrorIs(t, err, ErrUsageBillingEnvelopeInvalid)

	input = validUsageBillingEnvelopeInput()
	input.PricingSource = "unverified_provider"
	_, err = NewUsageBillingEnvelope(input)
	require.ErrorIs(t, err, ErrUsageBillingEnvelopeInvalid)
}

func TestUsageBillingEnvelope_SerializesOnlyExplicitBillingFacts(t *testing.T) {
	envelope, err := NewUsageBillingEnvelope(validUsageBillingEnvelopeInput())
	require.NoError(t, err)

	raw, err := envelope.MarshalJSON()
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	for _, forbidden := range []string{
		"key", "api_key", "credentials", "cookie", "headers", "body", "prompt",
		"messages", "tools", "ip", "ip_address", "user_agent", "request_payload_hash",
	} {
		_, exists := payload[forbidden]
		require.Falsef(t, exists, "serialized envelope must not contain %q", forbidden)
	}
	require.Equal(t, "gpt-5.6-sol", payload["billing_model"])
	require.Equal(t, strings.Repeat("a", 64), payload["pricing_hash"])
	require.Equal(t, strings.Repeat("c", 64), payload["auth_cache_locator"])
	require.NotEmpty(t, payload["request_fingerprint"])
}

func TestUsageBillingEnvelope_RejectsMissingAuthCacheLocatorForQuotaEffect(t *testing.T) {
	input := validUsageBillingEnvelopeInput()
	input.AuthCacheLocator = ""

	_, err := NewUsageBillingEnvelope(input)
	require.ErrorIs(t, err, ErrUsageBillingEnvelopeInvalid)
}

func TestUsageBillingEnvelope_DecodeRejectsTamperingAndUnknownVersion(t *testing.T) {
	envelope, err := NewUsageBillingEnvelope(validUsageBillingEnvelopeInput())
	require.NoError(t, err)
	raw, err := envelope.MarshalJSON()
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	payload["balance_cost"] = 9.5
	tampered, err := json.Marshal(payload)
	require.NoError(t, err)
	_, err = DecodeUsageBillingEnvelope(tampered)
	require.ErrorIs(t, err, ErrUsageBillingEnvelopeFingerprintMismatch)

	payload["version"] = float64(99)
	unknownVersion, err := json.Marshal(payload)
	require.NoError(t, err)
	_, err = DecodeUsageBillingEnvelope(unknownVersion)
	require.ErrorIs(t, err, ErrUsageBillingEnvelopeVersion)
}

func TestUsageBillingEnvelope_FingerprintIncludesAuthCacheLocator(t *testing.T) {
	firstInput := validUsageBillingEnvelopeInput()
	secondInput := validUsageBillingEnvelopeInput()
	secondInput.AuthCacheLocator = strings.Repeat("d", 64)

	first, err := NewUsageBillingEnvelope(firstInput)
	require.NoError(t, err)
	second, err := NewUsageBillingEnvelope(secondInput)
	require.NoError(t, err)
	require.NotEqual(t, first.RequestFingerprint(), second.RequestFingerprint())
}

func TestUsageBillingCommandFingerprint_IgnoresAuthCacheLocatorForLegacyCompatibility(t *testing.T) {
	first := validUsageBillingEnvelopeInput()
	firstEnvelope, err := NewUsageBillingEnvelope(first)
	require.NoError(t, err)
	firstCommand := firstEnvelope.Command()
	firstCommand.RequestFingerprint = ""
	firstCommand.Normalize()

	secondCommand := *firstCommand
	secondCommand.AuthCacheLocator = strings.Repeat("d", 64)
	secondCommand.RequestFingerprint = ""
	secondCommand.Normalize()

	require.Equal(t, firstCommand.RequestFingerprint, secondCommand.RequestFingerprint)
}

func TestUsageBillingEnvelope_RejectsInvalidCostsAndMutuallyExclusiveBilling(t *testing.T) {
	input := validUsageBillingEnvelopeInput()
	input.BalanceCost = math.NaN()
	_, err := NewUsageBillingEnvelope(input)
	require.ErrorIs(t, err, ErrUsageBillingEnvelopeInvalid)

	input = validUsageBillingEnvelopeInput()
	input.BalanceCost = 1
	input.SubscriptionCost = 1
	_, err = NewUsageBillingEnvelope(input)
	require.ErrorIs(t, err, ErrUsageBillingEnvelopeInvalid)

	input = validUsageBillingEnvelopeInput()
	input.BalanceCost = -1
	_, err = NewUsageBillingEnvelope(input)
	require.ErrorIs(t, err, ErrUsageBillingEnvelopeInvalid)
}

func TestUsageBillingEnvelope_CommandReturnsIndependentSubscriptionID(t *testing.T) {
	subscriptionID := int64(44)
	input := validUsageBillingEnvelopeInput()
	input.BillingType = BillingTypeSubscription
	input.BalanceCost = 0
	input.SubscriptionID = &subscriptionID
	input.WalletCost = 2.5

	envelope, err := NewUsageBillingEnvelope(input)
	require.NoError(t, err)
	first := envelope.Command()
	require.NotNil(t, first.SubscriptionID)
	*first.SubscriptionID = 999

	second := envelope.Command()
	require.Equal(t, int64(44), *second.SubscriptionID)
	require.Equal(t, int64(44), *second.EffectiveBillingGroupID)
	require.Equal(t, 2.5, second.WalletCost)
}

func TestUsageBillingEnvelope_ZeroBalanceCostWithQuotaEffectIsBillable(t *testing.T) {
	input := validUsageBillingEnvelopeInput()
	input.BalanceCost = 0
	input.APIKeyQuotaCost = 0.75

	envelope, err := NewUsageBillingEnvelope(input)
	require.NoError(t, err)
	require.True(t, envelope.IsBillable())
}

func TestUsageBillingEnvelopeFromUsageLog_RoundTripsSafeReplaySnapshot(t *testing.T) {
	groupID := int64(44)
	effectiveBillingGroupID := int64(77)
	subscriptionID := int64(55)
	channelID := int64(66)
	billingModel := "gpt-5.6-sol"
	upstreamModel := "gpt-5.6-sol"
	pricingSource := PricingSourceBuiltinGPT56
	pricingRevision := GPT56PricingRevision
	pricingHash := strings.Repeat("b", 64)
	mappingChain := "claude-sonnet-4-6 -> gpt-5.6-sol"
	billingMode := string(BillingModeToken)
	accountRate := 1.2
	accountStatsCost := 0.42
	durationMs := 1234
	firstTokenMs := 210
	serviceTier := "priority"
	reasoningEffort := "high"
	inbound := "/v1/messages"
	upstream := "/v1/responses"
	userAgent := "must-not-persist"
	ipAddress := "203.0.113.10"
	occurredAt := time.Date(2026, 7, 10, 15, 4, 5, 123000000, time.UTC)
	log := &UsageLog{
		UserID: 22, APIKeyID: 11, AccountID: 33, RequestID: "req-outbox-replay",
		Model: billingModel, RequestedModel: "claude-sonnet-4-6", UpstreamModel: &upstreamModel,
		BillingModel: &billingModel, PricingSource: &pricingSource, PricingRevision: &pricingRevision,
		PricingHash: &pricingHash, GroupID: &groupID, SubscriptionID: &subscriptionID,
		ChannelID: &channelID, ModelMappingChain: &mappingChain, BillingMode: &billingMode,
		ServiceTier: &serviceTier, ReasoningEffort: &reasoningEffort,
		InboundEndpoint: &inbound, UpstreamEndpoint: &upstream,
		InputTokens: 100, OutputTokens: 20, CacheCreationTokens: 3, CacheReadTokens: 10,
		CacheCreation5mTokens: 2, CacheCreation1hTokens: 1, ImageOutputTokens: 4,
		InputCost: 0.1, OutputCost: 0.2, CacheCreationCost: 0.03, CacheReadCost: 0.01,
		ImageOutputCost: 0.04, TotalCost: 0.38, ActualCost: 0.57,
		RateMultiplier: 1.5, AccountRateMultiplier: &accountRate, AccountStatsCost: &accountStatsCost,
		BillingType: BillingTypeSubscription, RequestType: RequestTypeStream,
		DurationMs: &durationMs, FirstTokenMs: &firstTokenMs, UserAgent: &userAgent, IPAddress: &ipAddress,
		CreatedAt: occurredAt,
	}
	cmd := &UsageBillingCommand{
		RequestID: log.RequestID, APIKeyID: log.APIKeyID, UserID: log.UserID, AccountID: log.AccountID,
		AuthCacheLocator: strings.Repeat("c", 64),
		SubscriptionID:   &subscriptionID, EffectiveBillingGroupID: &effectiveBillingGroupID,
		AccountType: AccountTypeAPIKey, Model: billingModel,
		ServiceTier: serviceTier, ReasoningEffort: reasoningEffort, BillingType: BillingTypeSubscription,
		InputTokens: 100, OutputTokens: 20, CacheCreationTokens: 3, CacheReadTokens: 10,
		SubscriptionCost: 0.57, APIKeyQuotaCost: 0.57, APIKeyRateLimitCost: 0.57,
		AccountQuotaCost: 0.456,
	}

	envelope, err := NewUsageBillingEnvelopeFromUsageLog(log, cmd)
	require.NoError(t, err)
	raw, err := envelope.MarshalJSON()
	require.NoError(t, err)
	require.NotContains(t, string(raw), userAgent)
	require.NotContains(t, string(raw), ipAddress)

	decoded, err := DecodeUsageBillingEnvelope(raw)
	require.NoError(t, err)
	require.Equal(t, effectiveBillingGroupID, *decoded.EffectiveBillingGroupID())
	replayed := decoded.UsageLog()
	require.Equal(t, billingModel, replayed.Model)
	require.Equal(t, "claude-sonnet-4-6", replayed.RequestedModel)
	require.Equal(t, upstreamModel, *replayed.UpstreamModel)
	require.Equal(t, billingModel, *replayed.BillingModel)
	require.Equal(t, mappingChain, *replayed.ModelMappingChain)
	require.Equal(t, pricingHash, *replayed.PricingHash)
	require.Equal(t, RequestTypeStream, replayed.RequestType)
	require.Equal(t, occurredAt, replayed.CreatedAt)
	require.InDelta(t, 0.57, replayed.ActualCost, 0.000001)
	require.Nil(t, replayed.UserAgent)
	require.Nil(t, replayed.IPAddress)
}
