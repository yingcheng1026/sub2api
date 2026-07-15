package service

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validUsageBillingReservationQuote() UsageBillingReservationQuote {
	return UsageBillingReservationQuote{
		RequestPayloadHash: strings.Repeat("a", 64),
		BillingModel:       "gpt-5.6-sol",
		PricingSource:      PricingSourceBuiltinGPT56,
		PricingRevision:    GPT56PricingRevision,
		PricingHash:        strings.Repeat("b", 64),
		RateMultiplier:     1.25,
		WorstCaseCostUSD:   2.50,
	}
}

func TestUsageBillingAttemptRefValidate(t *testing.T) {
	ref := UsageBillingAttemptRef{
		RequestID:  "req-contract-1",
		APIKeyID:   11,
		OwnerToken: strings.Repeat("a", 32),
		AttemptID:  strings.Repeat("b", 32),
	}
	require.NoError(t, ref.Validate())
	require.Equal(t, UsageBillingAdmissionRef{
		RequestID:  ref.RequestID,
		APIKeyID:   ref.APIKeyID,
		OwnerToken: ref.OwnerToken,
	}, ref.Admission())

	ref.AttemptID = ""
	require.ErrorIs(t, ref.Validate(), ErrUsageBillingLifecycleContractInvalid)
}

func TestUsageBillingReservationQuoteValidateFailsClosed(t *testing.T) {
	require.NoError(t, validUsageBillingReservationQuote().Validate())

	tests := []struct {
		name   string
		mutate func(*UsageBillingReservationQuote)
	}{
		{"missing request payload hash", func(q *UsageBillingReservationQuote) { q.RequestPayloadHash = "" }},
		{"missing billing model", func(q *UsageBillingReservationQuote) { q.BillingModel = "" }},
		{"missing pricing source", func(q *UsageBillingReservationQuote) { q.PricingSource = "" }},
		{"missing pricing revision", func(q *UsageBillingReservationQuote) { q.PricingRevision = "" }},
		{"missing pricing hash", func(q *UsageBillingReservationQuote) { q.PricingHash = "" }},
		{"zero rate multiplier", func(q *UsageBillingReservationQuote) { q.RateMultiplier = 0 }},
		{"zero reservation", func(q *UsageBillingReservationQuote) { q.WorstCaseCostUSD = 0 }},
		{"nan rate multiplier", func(q *UsageBillingReservationQuote) { q.RateMultiplier = math.NaN() }},
		{"infinite reservation", func(q *UsageBillingReservationQuote) { q.WorstCaseCostUSD = math.Inf(1) }},
		{"negative reservation", func(q *UsageBillingReservationQuote) { q.WorstCaseCostUSD = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quote := validUsageBillingReservationQuote()
			tt.mutate(&quote)
			require.ErrorIs(t, quote.Validate(), ErrUsageBillingLifecycleContractInvalid)
		})
	}
}

func TestUsageBillingDeliveryOutcomeNeverFailoversUnknownDelivery(t *testing.T) {
	require.True(t, UsageBillingDeliveryNotSent.AllowsFailover())
	require.True(t, UsageBillingDeliveryDefinitelyRejected.AllowsFailover())
	require.False(t, UsageBillingDeliveryAccepted.AllowsFailover())
	require.False(t, UsageBillingDeliveryUnknown.AllowsFailover())
	require.False(t, UsageBillingDeliveryOutcome("").AllowsFailover())

	require.True(t, UsageBillingDeliveryUnknown.RequiresReconciliation())
	require.True(t, UsageBillingDeliveryOutcome("").RequiresReconciliation())
	require.False(t, UsageBillingDeliveryNotSent.RequiresReconciliation())
	require.False(t, UsageBillingDeliveryDefinitelyRejected.RequiresReconciliation())
	require.False(t, UsageBillingDeliveryAccepted.RequiresReconciliation())
}

func TestUsageBillingCommitCallbackContract(t *testing.T) {
	called := false
	var callback UsageBillingCommitCallback = func(_ context.Context, _ UsageBillingEnvelope) error {
		called = true
		return nil
	}
	require.NoError(t, callback(context.Background(), UsageBillingEnvelope{}))
	require.True(t, called)
}
