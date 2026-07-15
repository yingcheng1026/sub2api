package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestPrepareUsageBillingRequestContextFreezesIdentityAcrossFailover(t *testing.T) {
	base := context.WithValue(context.Background(), ctxkey.RequestID, "request-123")
	first, err := PrepareUsageBillingRequestContext(base)
	require.NoError(t, err)
	second, err := PrepareUsageBillingRequestContext(first)
	require.NoError(t, err)

	requestID, _ := first.Value(ctxkey.UsageBillingRequestID).(string)
	ownerToken, _ := first.Value(ctxkey.UsageBillingOwnerToken).(string)
	require.Equal(t, "local:request-123", requestID)
	require.Len(t, ownerToken, 32)
	require.Equal(t, requestID, second.Value(ctxkey.UsageBillingRequestID))
	require.Equal(t, ownerToken, second.Value(ctxkey.UsageBillingOwnerToken))
}

func validUsageBillingAdmissionInput() UsageBillingAdmissionInput {
	groupID := int64(31)
	subscriptionID := int64(71)
	return UsageBillingAdmissionInput{
		RequestID:               "usage-admission-quote",
		APIKeyID:                11,
		AuthCacheLocator:        strings.Repeat("a", 64),
		UserID:                  22,
		SubscriptionID:          &subscriptionID,
		GroupID:                 &groupID,
		EffectiveBillingGroupID: &groupID,
		BillingType:             BillingTypeSubscription,
		OwnerToken:              strings.Repeat("b", 32),
		AttemptID:               strings.Repeat("c", 32),
		AccountID:               33,
		AccountType:             AccountTypeOAuth,
		BillingModel:            "gpt-5.6-terra",
		RequestPayloadHash:      strings.Repeat("d", 64),
		PricingSource:           PricingSourceBuiltinGPT56,
		PricingRevision:         GPT56PricingRevision,
		PricingHash:             strings.Repeat("e", 64),
		RateMultiplier:          1,
		WorstCaseCostUSD:        0.25,
	}
}

func TestUsageBillingAdmissionRequiresImmutableRequestAndPricingQuote(t *testing.T) {
	valid := validUsageBillingAdmissionInput()
	admission, err := NewUsageBillingAdmission(valid)
	require.NoError(t, err)
	require.Equal(t, valid.RequestPayloadHash, admission.RequestPayloadHash())
	require.Equal(t, valid.PricingHash, admission.PricingHash())
	require.Equal(t, valid.WorstCaseCostUSD, admission.WorstCaseCostUSD())

	for _, mutate := range []func(*UsageBillingAdmissionInput){
		func(input *UsageBillingAdmissionInput) { input.RequestPayloadHash = "" },
		func(input *UsageBillingAdmissionInput) { input.BillingModel = "" },
		func(input *UsageBillingAdmissionInput) { input.PricingSource = "" },
		func(input *UsageBillingAdmissionInput) { input.PricingRevision = "" },
		func(input *UsageBillingAdmissionInput) { input.PricingHash = "" },
		func(input *UsageBillingAdmissionInput) { input.RateMultiplier = 0 },
		func(input *UsageBillingAdmissionInput) { input.WorstCaseCostUSD = 0 },
		func(input *UsageBillingAdmissionInput) { input.AlternateBillingModel = "gpt-image-2" },
	} {
		input := validUsageBillingAdmissionInput()
		mutate(&input)
		_, err := NewUsageBillingAdmission(input)
		require.ErrorIs(t, err, ErrUsageBillingAdmissionInvalid)
	}
}

func TestUsageBillingAdmissionAllowsOnlyFrozenAlternatePricing(t *testing.T) {
	input := validUsageBillingAdmissionInput()
	input.AlternateBillingModel = "gpt-image-2"
	input.AlternatePricingSource = PricingSourceBuiltinFallback
	input.AlternatePricingRevision = builtinFallbackRevision
	input.AlternatePricingHash = strings.Repeat("f", 64)
	input.AlternateRateMultiplier = 1.5
	admission, err := NewUsageBillingAdmission(input)
	require.NoError(t, err)

	envelopeInput := UsageBillingEnvelopeInput{
		RequestID: input.RequestID, RequestPayloadHash: input.RequestPayloadHash,
		AdmissionAttemptID: input.AttemptID,
		APIKeyID:           input.APIKeyID, AuthCacheLocator: input.AuthCacheLocator,
		UserID: input.UserID, AccountID: input.AccountID, SubscriptionID: input.SubscriptionID,
		GroupID: input.GroupID, EffectiveBillingGroupID: input.EffectiveBillingGroupID,
		AccountType: input.AccountType, BillingModel: input.AlternateBillingModel, BillingType: input.BillingType,
		PricingSource: input.AlternatePricingSource, PricingRevision: input.AlternatePricingRevision,
		PricingHash: input.AlternatePricingHash, RateMultiplier: input.AlternateRateMultiplier,
		AccountRateMultiplier: 1,
	}
	envelope, err := NewUsageBillingEnvelope(envelopeInput)
	require.NoError(t, err)
	require.True(t, admission.MatchesEnvelope(envelope))

	envelopeInput.AdmissionAttemptID = strings.Repeat("8", 32)
	wrongAttempt, err := NewUsageBillingEnvelope(envelopeInput)
	require.NoError(t, err)
	require.False(t, admission.MatchesEnvelope(wrongAttempt))
	envelopeInput.AdmissionAttemptID = input.AttemptID

	envelopeInput.PricingHash = strings.Repeat("9", 64)
	tampered, err := NewUsageBillingEnvelope(envelopeInput)
	require.NoError(t, err)
	require.False(t, admission.MatchesEnvelope(tampered))
}

func TestUsageBillingAdmissionCanonicalizesDatabaseNumericPrecision(t *testing.T) {
	input := validUsageBillingAdmissionInput()
	input.RateMultiplier = 1.1234567890123
	input.WorstCaseCostUSD = 0.1234567890123
	admission, err := NewUsageBillingAdmission(input)
	require.NoError(t, err)
	require.Equal(t, 1.123456789, admission.RateMultiplier())
	require.Equal(t, 0.1234567891, admission.WorstCaseCostUSD())

	envelope, err := NewUsageBillingEnvelope(UsageBillingEnvelopeInput{
		RequestID: input.RequestID, RequestPayloadHash: input.RequestPayloadHash,
		APIKeyID: input.APIKeyID, AuthCacheLocator: input.AuthCacheLocator,
		UserID: input.UserID, AccountID: input.AccountID, SubscriptionID: input.SubscriptionID,
		GroupID: input.GroupID, EffectiveBillingGroupID: input.EffectiveBillingGroupID,
		AccountType: input.AccountType, BillingModel: input.BillingModel, BillingType: input.BillingType,
		PricingSource: input.PricingSource, PricingRevision: input.PricingRevision, PricingHash: input.PricingHash,
		RateMultiplier: input.RateMultiplier, AccountRateMultiplier: 1,
	})
	require.NoError(t, err)
	require.True(t, admission.MatchesEnvelopeBase(envelope))
}

func TestBuildUsageBillingAdmissionCopiesReservationQuote(t *testing.T) {
	groupID := int64(31)
	subscriptionID := int64(71)
	quote := UsageBillingReservationQuote{
		RequestPayloadHash: strings.Repeat("d", 64),
		BillingModel:       "gpt-5.6-terra",
		PricingSource:      PricingSourceBuiltinGPT56,
		PricingRevision:    GPT56PricingRevision,
		PricingHash:        strings.Repeat("e", 64),
		RateMultiplier:     1.25,
		WorstCaseCostUSD:   0.5,
	}
	walletBalance := 10.0
	ctx, admission, err := buildUsageBillingAdmission(
		context.Background(),
		&APIKey{ID: 11, Key: "sk-admission", GroupID: &groupID, Group: &Group{ID: groupID}},
		&User{ID: 22},
		&Account{ID: 33, Type: AccountTypeOAuth},
		&UserSubscription{ID: subscriptionID, UserID: 22, WalletBalanceUSD: &walletBalance},
		quote,
	)
	require.NoError(t, err)
	require.NotNil(t, ctx)
	require.Equal(t, quote.RequestPayloadHash, admission.RequestPayloadHash())
	require.Equal(t, quote.BillingModel, admission.BillingModel())
	require.Equal(t, quote.PricingSource, admission.PricingSource())
	require.Equal(t, quote.PricingRevision, admission.PricingRevision())
	require.Equal(t, quote.PricingHash, admission.PricingHash())
	require.Equal(t, quote.RateMultiplier, admission.RateMultiplier())
	require.Equal(t, quote.WorstCaseCostUSD, admission.WorstCaseCostUSD())
}
