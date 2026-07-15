package service

import (
	"math"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	usageBillingReservationDefaultMaxOutputTokens = 131072
	usageBillingReservationMaxTokenBound          = 10_000_000
	usageBillingReservationMaxRequestCount        = 10_000
	usageBillingReservationMinimumUSD             = 0.0000000001
	usageBillingReservationMaximumUSD             = 9_000_000_000
)

// UsageBillingReservationQuoteBuildInput contains only facts available before
// any upstream bytes are sent. Pricing is an immutable snapshot; RequestBody
// is used only to derive conservative token/request-count bounds and is never
// persisted in the admission or outbox.
type UsageBillingReservationQuoteBuildInput struct {
	RequestPayloadHash string
	BillingModel       string
	Pricing            *PricingQuote
	RateMultiplier     float64
	RequestBody        []byte
	MaxOutputTokens    int
	RequestCount       int
}

func BuildUsageBillingReservationQuote(input UsageBillingReservationQuoteBuildInput) (UsageBillingReservationQuote, error) {
	if !validSHA256(strings.ToLower(strings.TrimSpace(input.RequestPayloadHash))) ||
		!validUsageBillingEnvelopeText(input.BillingModel, 128, true) ||
		input.Pricing == nil || len(input.RequestBody) == 0 ||
		math.IsNaN(input.RateMultiplier) || math.IsInf(input.RateMultiplier, 0) || input.RateMultiplier <= 0 {
		return UsageBillingReservationQuote{}, ErrUsageBillingLifecycleContractInvalid
	}
	resolved := input.Pricing.CloneResolved()
	evidence := input.Pricing.Evidence
	if resolved == nil || !validUsageBillingPricingSource(evidence.Source) ||
		!validUsageBillingEnvelopeText(evidence.Revision, 128, true) || !validSHA256(evidence.Hash) {
		return UsageBillingReservationQuote{}, ErrUsageBillingLifecycleContractInvalid
	}
	if resolved.Mode != BillingModeImage && resolved.Mode != BillingModePerRequest && !gjson.ValidBytes(input.RequestBody) {
		return UsageBillingReservationQuote{}, ErrUsageBillingLifecycleContractInvalid
	}
	if (resolved.Mode == BillingModeImage || resolved.Mode == BillingModePerRequest) && input.RequestCount <= 0 && !gjson.ValidBytes(input.RequestBody) {
		return UsageBillingReservationQuote{}, ErrUsageBillingLifecycleContractInvalid
	}
	input.RateMultiplier = canonicalUsageBillingRate(input.RateMultiplier)

	worstCaseUSD, err := estimateUsageBillingWorstCaseUSD(input, resolved)
	if err != nil {
		return UsageBillingReservationQuote{}, err
	}
	quote := UsageBillingReservationQuote{
		RequestPayloadHash: strings.ToLower(strings.TrimSpace(input.RequestPayloadHash)),
		BillingModel:       strings.TrimSpace(input.BillingModel),
		PricingSource:      strings.TrimSpace(evidence.Source),
		PricingRevision:    strings.TrimSpace(evidence.Revision),
		PricingHash:        strings.ToLower(strings.TrimSpace(evidence.Hash)),
		RateMultiplier:     input.RateMultiplier,
		WorstCaseCostUSD:   canonicalUsageBillingReservation(worstCaseUSD),
	}
	if err := quote.Validate(); err != nil {
		return UsageBillingReservationQuote{}, ErrUsageBillingLifecycleContractInvalid
	}
	return quote, nil
}

func estimateUsageBillingWorstCaseUSD(input UsageBillingReservationQuoteBuildInput, resolved *ResolvedPricing) (float64, error) {
	var cost float64
	switch resolved.Mode {
	case BillingModeImage, BillingModePerRequest:
		count, err := usageBillingReservationRequestCount(input)
		if err != nil {
			return 0, err
		}
		unitPrice := maxPositiveFinite(resolved.DefaultPerRequestPrice)
		for _, tier := range resolved.RequestTiers {
			unitPrice = maxPositiveFinite(unitPrice, float64PtrValue(tier.PerRequestPrice))
		}
		if unitPrice <= 0 {
			return 0, ErrUsageBillingLifecycleContractInvalid
		}
		cost = unitPrice * float64(count) * input.RateMultiplier
	case BillingModeToken, "":
		maxOutputTokens, err := usageBillingReservationMaxOutputTokens(input)
		if err != nil {
			return 0, err
		}
		inputPrice, outputPrice := usageBillingReservationTokenPrices(resolved)
		if inputPrice <= 0 || outputPrice <= 0 {
			return 0, ErrUsageBillingLifecycleContractInvalid
		}
		// A token contains at least one encoded byte. The complete JSON byte
		// length therefore safely over-bounds input tokens without retaining or
		// tokenizing sensitive request content.
		inputTokens := len(input.RequestBody)
		if inputTokens > usageBillingReservationMaxTokenBound {
			return 0, ErrUsageBillingLifecycleContractInvalid
		}
		cost = (float64(inputTokens)*inputPrice + float64(maxOutputTokens)*outputPrice) * input.RateMultiplier
	default:
		return 0, ErrUsageBillingLifecycleContractInvalid
	}
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost <= 0 || cost > usageBillingReservationMaximumUSD {
		return 0, ErrUsageBillingLifecycleContractInvalid
	}
	if cost < usageBillingReservationMinimumUSD {
		cost = usageBillingReservationMinimumUSD
	}
	return cost, nil
}

func usageBillingReservationMaxOutputTokens(input UsageBillingReservationQuoteBuildInput) (int, error) {
	maxTokens := input.MaxOutputTokens
	for _, path := range []string{
		"max_output_tokens", "max_tokens", "generationConfig.maxOutputTokens", "generation_config.max_output_tokens",
	} {
		value := gjson.GetBytes(input.RequestBody, path)
		if !value.Exists() {
			continue
		}
		if value.Type != gjson.Number || value.Num != math.Trunc(value.Num) || value.Int() <= 0 {
			return 0, ErrUsageBillingLifecycleContractInvalid
		}
		if intValue := int(value.Int()); intValue > maxTokens {
			maxTokens = intValue
		}
	}
	// A compatible upstream may clamp, reinterpret, or ignore a small client
	// limit, and reasoning tokens can exceed the visible response budget. The
	// wallet hold therefore keeps the same conservative output floor used when
	// no limit is supplied instead of trusting an unenforceable lower bound.
	if maxTokens < usageBillingReservationDefaultMaxOutputTokens {
		maxTokens = usageBillingReservationDefaultMaxOutputTokens
	}
	if maxTokens > usageBillingReservationMaxTokenBound {
		return 0, ErrUsageBillingLifecycleContractInvalid
	}
	return maxTokens, nil
}

func usageBillingReservationRequestCount(input UsageBillingReservationQuoteBuildInput) (int, error) {
	count := input.RequestCount
	for _, path := range []string{"n", "num_images", "image_count"} {
		value := gjson.GetBytes(input.RequestBody, path)
		if !value.Exists() {
			continue
		}
		if value.Type != gjson.Number || value.Num != math.Trunc(value.Num) || value.Int() <= 0 {
			return 0, ErrUsageBillingLifecycleContractInvalid
		}
		if intValue := int(value.Int()); intValue > count {
			count = intValue
		}
	}
	if count <= 0 {
		count = 1
	}
	if count > usageBillingReservationMaxRequestCount {
		return 0, ErrUsageBillingLifecycleContractInvalid
	}
	return count, nil
}

func usageBillingReservationTokenPrices(resolved *ResolvedPricing) (float64, float64) {
	if resolved == nil {
		return 0, 0
	}
	inputPrice := 0.0
	outputPrice := 0.0
	collect := func(pricing *ModelPricing) {
		if pricing == nil {
			return
		}
		inputMultiplier := maxPositiveFinite(1, pricing.LongContextInputMultiplier)
		outputMultiplier := maxPositiveFinite(1, pricing.LongContextOutputMultiplier)
		inputPrice = maxPositiveFinite(inputPrice,
			pricing.InputPricePerToken*inputMultiplier,
			pricing.InputPricePerTokenPriority*inputMultiplier,
			pricing.CacheCreationPricePerToken,
			pricing.CacheReadPricePerToken,
			pricing.CacheReadPricePerTokenPriority,
			pricing.CacheCreation5mPrice,
			pricing.CacheCreation1hPrice,
		)
		outputPrice = maxPositiveFinite(outputPrice,
			pricing.OutputPricePerToken*outputMultiplier,
			pricing.OutputPricePerTokenPriority*outputMultiplier,
			pricing.ImageOutputPricePerToken*outputMultiplier,
		)
	}
	collect(resolved.BasePricing)
	for _, interval := range resolved.Intervals {
		inputPrice = maxPositiveFinite(inputPrice,
			float64PtrValue(interval.InputPrice), float64PtrValue(interval.CacheWritePrice), float64PtrValue(interval.CacheReadPrice),
		)
		outputPrice = maxPositiveFinite(outputPrice, float64PtrValue(interval.OutputPrice))
	}
	return inputPrice, outputPrice
}

func maxPositiveFinite(values ...float64) float64 {
	maxValue := 0.0
	for _, value := range values {
		if value > maxValue && !math.IsNaN(value) && !math.IsInf(value, 0) {
			maxValue = value
		}
	}
	return maxValue
}

func float64PtrValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
