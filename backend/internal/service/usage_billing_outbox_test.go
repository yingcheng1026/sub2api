package service

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validUsageBillingEnvelopeInput() UsageBillingEnvelopeInput {
	groupID := int64(44)
	return UsageBillingEnvelopeInput{
		RequestID:             "req-outbox-1",
		APIKeyID:              11,
		UserID:                22,
		AccountID:             33,
		GroupID:               &groupID,
		AccountType:           AccountTypeAPIKey,
		BillingModel:          "gpt-5.6-sol",
		ServiceTier:           "priority",
		ReasoningEffort:       "high",
		BillingType:           BillingTypeBalance,
		InputTokens:           100,
		OutputTokens:          20,
		CacheReadTokens:       10,
		PricingSource:         PricingSourceBuiltinGPT56,
		PricingRevision:       GPT56PricingRevision,
		PricingHash:           strings.Repeat("a", 64),
		RateMultiplier:        1.25,
		AccountRateMultiplier: 1.1,
		BalanceCost:           2.5,
		APIKeyQuotaCost:       2.5,
		APIKeyRateLimitCost:   2.5,
		AccountQuotaCost:      2,
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
	require.NotEmpty(t, payload["request_fingerprint"])
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
