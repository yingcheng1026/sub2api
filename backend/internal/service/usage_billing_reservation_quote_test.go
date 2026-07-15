package service

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildUsageBillingReservationQuoteBoundsTokenRequest(t *testing.T) {
	pricing, err := freezeResolvedPricingQuote(&ResolvedPricing{
		Mode: BillingModeToken, Source: PricingSourceBuiltinGPT56, Revision: GPT56PricingRevision,
		BasePricing: &ModelPricing{
			InputPricePerToken:         0.000001,
			OutputPricePerToken:        0.000002,
			CacheCreationPricePerToken: 0.000003,
		},
	})
	require.NoError(t, err)
	body := []byte(`{"model":"gpt-5.6-terra","max_output_tokens":10,"input":"hello"}`)

	quote, err := BuildUsageBillingReservationQuote(UsageBillingReservationQuoteBuildInput{
		RequestPayloadHash: HashUsageRequestPayload(body),
		BillingModel:       "gpt-5.6-terra",
		Pricing:            pricing,
		RateMultiplier:     1.5,
		RequestBody:        body,
	})
	require.NoError(t, err)
	require.Equal(t, pricing.Evidence.Hash, quote.PricingHash)
	require.Equal(t, 1.5, quote.RateMultiplier)
	// Every request-body byte is a conservative upper bound for one input
	// token; cache creation is the most expensive input dimension here.
	wantFloor := (float64(len(body))*0.000003 + 10*0.000002) * 1.5
	require.GreaterOrEqual(t, quote.WorstCaseCostUSD, wantFloor)
	require.NoError(t, quote.Validate())
}

func TestBuildUsageBillingReservationQuoteUsesConservativeOutputDefault(t *testing.T) {
	pricing, err := freezeResolvedPricingQuote(&ResolvedPricing{
		Mode: BillingModeToken, Source: PricingSourceBuiltinGPT56, Revision: GPT56PricingRevision,
		BasePricing: &ModelPricing{InputPricePerToken: 0.000001, OutputPricePerToken: 0.000002},
	})
	require.NoError(t, err)
	body := []byte(`{"model":"gpt-5.6-terra","input":"hello"}`)

	quote, err := BuildUsageBillingReservationQuote(UsageBillingReservationQuoteBuildInput{
		RequestPayloadHash: HashUsageRequestPayload(body), BillingModel: "gpt-5.6-terra",
		Pricing: pricing, RateMultiplier: 1, RequestBody: body,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, quote.WorstCaseCostUSD, float64(usageBillingReservationDefaultMaxOutputTokens)*0.000002)
}

func TestBuildUsageBillingReservationQuoteDoesNotTrustSmallClientOutputLimit(t *testing.T) {
	pricing, err := freezeResolvedPricingQuote(&ResolvedPricing{
		Mode: BillingModeToken, Source: PricingSourceBuiltinGPT56, Revision: GPT56PricingRevision,
		BasePricing: &ModelPricing{InputPricePerToken: 0.000001, OutputPricePerToken: 0.000002},
	})
	require.NoError(t, err)
	body := []byte(`{"model":"gpt-5.6-luna","max_output_tokens":1,"input":"hello"}`)

	quote, err := BuildUsageBillingReservationQuote(UsageBillingReservationQuoteBuildInput{
		RequestPayloadHash: HashUsageRequestPayload(body), BillingModel: "gpt-5.6-luna",
		Pricing: pricing, RateMultiplier: 1, RequestBody: body, MaxOutputTokens: 1,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, quote.WorstCaseCostUSD,
		float64(usageBillingReservationDefaultMaxOutputTokens)*0.000002,
		"wallet reservation must remain safe when a compatible upstream clamps or ignores a tiny client limit",
	)
}

func TestBuildUsageBillingReservationQuoteBoundsHighestPerRequestTier(t *testing.T) {
	cheap := 0.1
	expensive := 0.75
	pricing, err := freezeResolvedPricingQuote(&ResolvedPricing{
		Mode: BillingModeImage, Source: PricingSourceChannel, Revision: channelPricingRevision,
		DefaultPerRequestPrice: cheap,
		RequestTiers:           []PricingInterval{{TierLabel: "1K", PerRequestPrice: &cheap}, {TierLabel: "4K", PerRequestPrice: &expensive}},
	})
	require.NoError(t, err)
	body := []byte(`{"model":"gpt-image-2","n":3,"size":"1K"}`)

	quote, err := BuildUsageBillingReservationQuote(UsageBillingReservationQuoteBuildInput{
		RequestPayloadHash: HashUsageRequestPayload(body), BillingModel: "gpt-image-2",
		Pricing: pricing, RateMultiplier: 2, RequestBody: body,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, quote.WorstCaseCostUSD, 0.75*3*2)
}

func TestBuildUsageBillingReservationQuoteFailsClosedOnInvalidFacts(t *testing.T) {
	pricing, err := freezeResolvedPricingQuote(&ResolvedPricing{
		Mode: BillingModeToken, Source: PricingSourceBuiltinGPT56, Revision: GPT56PricingRevision,
		BasePricing: &ModelPricing{InputPricePerToken: 0.000001, OutputPricePerToken: 0.000002},
	})
	require.NoError(t, err)
	body := []byte(`{"model":"gpt-5.6-terra"}`)
	valid := UsageBillingReservationQuoteBuildInput{
		RequestPayloadHash: strings.Repeat("a", 64), BillingModel: "gpt-5.6-terra",
		Pricing: pricing, RateMultiplier: 1, RequestBody: body,
	}

	for _, mutate := range []func(*UsageBillingReservationQuoteBuildInput){
		func(input *UsageBillingReservationQuoteBuildInput) { input.RequestPayloadHash = "" },
		func(input *UsageBillingReservationQuoteBuildInput) { input.BillingModel = "" },
		func(input *UsageBillingReservationQuoteBuildInput) { input.Pricing = nil },
		func(input *UsageBillingReservationQuoteBuildInput) { input.RateMultiplier = 0 },
		func(input *UsageBillingReservationQuoteBuildInput) { input.RateMultiplier = math.Inf(1) },
		func(input *UsageBillingReservationQuoteBuildInput) { input.RequestBody = nil },
	} {
		input := valid
		mutate(&input)
		_, err := BuildUsageBillingReservationQuote(input)
		require.ErrorIs(t, err, ErrUsageBillingLifecycleContractInvalid)
	}
}
