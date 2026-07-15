package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	PricingSourceBuiltinGPT56    = "builtin_gpt56"
	PricingSourceBuiltinFallback = "builtin_fallback"
	PricingSourceGroupImage      = "group_image"
	GPT56PricingRevision         = "gpt56-policy-v1"
	channelPricingRevision       = "channel-effective-v1"
	builtinFallbackRevision      = "builtin-fallback-v1"
	groupImagePricingRevision    = "group-image-effective-v1"
)

var (
	ErrOpenAIBillingPreflight   = errors.New("openai billing preflight failed")
	ErrOpenAIPricingUnavailable = errors.New("openai pricing unavailable")
)

type OpenAIBillingPreflightError struct {
	Reason string
	Cause  error
}

func (e *OpenAIBillingPreflightError) Error() string {
	if e == nil {
		return ErrOpenAIBillingPreflight.Error()
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", ErrOpenAIBillingPreflight, e.Reason, e.Cause)
	}
	return fmt.Sprintf("%s: %s", ErrOpenAIBillingPreflight, e.Reason)
}

func (e *OpenAIBillingPreflightError) Unwrap() error { return e.Cause }

func (e *OpenAIBillingPreflightError) Is(target error) bool {
	return target == ErrOpenAIBillingPreflight || (e != nil && e.Cause != nil && errors.Is(e.Cause, target))
}

type PricingEvidence struct {
	Source   string
	Revision string
	Hash     string
}

type PricingQuote struct {
	Resolved *ResolvedPricing
	Evidence PricingEvidence
}

func (q *PricingQuote) CloneResolved() *ResolvedPricing {
	if q == nil {
		return nil
	}
	return cloneResolvedPricing(q.Resolved)
}

type ResolvedOpenAIBillingIdentity struct {
	RequestedModel     string
	CompatModel        string
	DispatchModel      string
	ChannelMappedModel string
	AccountMappedModel string
	UpstreamModel      string
	BillingModel       string
	BillingModelSource string
	ChannelID          int64
	ModelMappingChain  string
	Pricing            *PricingQuote
	TextBillingModel   string
	TextPricing        *PricingQuote
	AdmissionAttemptID string
	AdmissionRef       UsageBillingAdmissionAttemptRef
}

type OpenAIBillingIdentityInput struct {
	RequestedModel        string
	CompatModel           string
	DispatchModel         string
	ChannelMappedModel    string
	ChannelMappingApplied bool
	ChannelMappingExact   bool
	AccountMappedModel    string
	UpstreamModel         string
	BillingModelSource    string
	ChannelID             int64
	ModelMappingChain     string
	GroupID               *int64
}

type OpenAIForwardOptions struct {
	RequestedModel          string
	ChannelMapping          ChannelMappingResult
	GroupID                 *int64
	ImagePriceConfig        *ImagePriceConfig
	RequirePricingPreflight bool
	RequireBillingAdmission bool
	UsageBilling            *OpenAIUsageBillingAdmissionInput
}

func (s *OpenAIGatewayService) resolveForwardBillingIdentity(
	ctx context.Context,
	opts OpenAIForwardOptions,
	originalModel string,
	compatModel string,
	accountMappedModel string,
	upstreamModel string,
) (*ResolvedOpenAIBillingIdentity, error) {
	required := opts.RequirePricingPreflight
	for _, model := range []string{originalModel, accountMappedModel, upstreamModel} {
		if _, family := classifyOpenAIGPT56PreviewModel(model); family {
			required = true
			break
		}
	}
	if !required {
		return nil, nil
	}
	requested := firstNonEmptyModel(opts.RequestedModel, originalModel)
	channelMapped := firstNonEmptyModel(opts.ChannelMapping.MappedModel, originalModel)
	return s.ResolveOpenAIBillingIdentity(ctx, OpenAIBillingIdentityInput{
		RequestedModel: requested, CompatModel: compatModel, DispatchModel: originalModel,
		ChannelMappedModel: channelMapped, ChannelMappingApplied: opts.ChannelMapping.Mapped,
		ChannelMappingExact: opts.ChannelMapping.MappingExact, AccountMappedModel: accountMappedModel,
		UpstreamModel: upstreamModel, BillingModelSource: opts.ChannelMapping.BillingModelSource,
		ChannelID:         opts.ChannelMapping.ChannelID,
		ModelMappingChain: opts.ChannelMapping.BuildModelMappingChain(requested, upstreamModel),
		GroupID:           opts.GroupID,
	})
}

func attachOpenAIBillingIdentity(result *OpenAIForwardResult, identity *ResolvedOpenAIBillingIdentity) {
	if result == nil || identity == nil {
		return
	}
	result.BillingIdentity = identity
	result.BillingModel = identity.BillingModel
	result.UpstreamModel = identity.UpstreamModel
}

// ResolveOpenAIWSBillingIdentity creates one immutable quote for one WS turn.
// Callers must resolve it again for every turn; a connection-level call is only
// an early gate before token refresh/dial and must never be reused for billing.
func (s *OpenAIGatewayService) ResolveOpenAIWSBillingIdentity(
	ctx context.Context,
	account *Account,
	opts OpenAIForwardOptions,
) (*ResolvedOpenAIBillingIdentity, error) {
	return s.ResolveOpenAIWSBillingIdentityForPayload(ctx, account, opts, nil)
}

func (s *OpenAIGatewayService) ResolveOpenAIWSBillingIdentityForPayload(
	ctx context.Context,
	account *Account,
	opts OpenAIForwardOptions,
	payload []byte,
) (*ResolvedOpenAIBillingIdentity, error) {
	requested := strings.TrimSpace(opts.RequestedModel)
	channelMapped := firstNonEmptyModel(opts.ChannelMapping.MappedModel, requested)
	accountMapped := resolveOpenAIForwardModel(account, channelMapped, "")
	upstream := normalizeOpenAIModelForUpstream(account, accountMapped)
	if IsImageGenerationIntent(openAIResponsesEndpoint, requested, payload) {
		imageModel, imageSizeTier, err := resolveOpenAIResponsesImageBillingConfigFromBody(payload, upstream)
		if err != nil {
			return nil, preflightError("invalid image billing configuration", err)
		}
		return s.ResolveOpenAIImageBillingIdentity(ctx, OpenAIBillingIdentityInput{
			RequestedModel: requested, DispatchModel: channelMapped, ChannelMappedModel: channelMapped,
			ChannelMappingApplied: opts.ChannelMapping.Mapped, ChannelMappingExact: opts.ChannelMapping.MappingExact,
			AccountMappedModel: accountMapped, UpstreamModel: upstream,
			BillingModelSource: BillingModelSourceUpstream, ChannelID: opts.ChannelMapping.ChannelID,
			ModelMappingChain: opts.ChannelMapping.BuildModelMappingChain(requested, upstream), GroupID: opts.GroupID,
		}, imageModel, imageSizeTier, opts.ImagePriceConfig)
	}
	return s.resolveForwardBillingIdentity(ctx, opts, channelMapped, channelMapped, accountMapped, upstream)
}

// AttachOpenAIBillingIdentity applies a preflight result to a completed turn.
func AttachOpenAIBillingIdentity(result *OpenAIForwardResult, identity *ResolvedOpenAIBillingIdentity) {
	if result != nil && result.ImageCount > 0 && pricingQuoteMode(identity) != BillingModeImage && pricingQuoteMode(identity) != BillingModePerRequest {
		// Never replace an image result's billing model with a text-turn quote.
		return
	}
	attachOpenAIBillingIdentity(result, identity)
}

func pricingQuoteMode(identity *ResolvedOpenAIBillingIdentity) BillingMode {
	if identity == nil || identity.Pricing == nil || identity.Pricing.Resolved == nil {
		return ""
	}
	return identity.Pricing.Resolved.Mode
}

// ResolveOpenAIImageBillingIdentity freezes the exact image schedule selected
// before token acquisition or transport.
func (s *OpenAIGatewayService) ResolveOpenAIImageBillingIdentity(
	ctx context.Context,
	input OpenAIBillingIdentityInput,
	imageModel string,
	imageSizeTier string,
	groupConfig *ImagePriceConfig,
) (*ResolvedOpenAIBillingIdentity, error) {
	imageModel = strings.TrimSpace(imageModel)
	if imageModel == "" {
		return nil, preflightError("image billing model is empty", ErrOpenAIPricingUnavailable)
	}
	quote, err := s.ResolveOpenAIImagePricingQuote(ctx, imageModel, imageSizeTier, input.GroupID, groupConfig)
	if err != nil {
		return nil, preflightError("image billing model is not priceable", err)
	}
	requested := strings.TrimSpace(input.RequestedModel)
	channelMapped := firstNonEmptyModel(input.ChannelMappedModel, input.DispatchModel, requested)
	accountMapped := firstNonEmptyModel(input.AccountMappedModel, channelMapped)
	upstream := firstNonEmptyModel(input.UpstreamModel, accountMapped)
	identity := &ResolvedOpenAIBillingIdentity{
		RequestedModel: requested, CompatModel: strings.TrimSpace(input.CompatModel),
		DispatchModel: strings.TrimSpace(input.DispatchModel), ChannelMappedModel: channelMapped,
		AccountMappedModel: accountMapped, UpstreamModel: upstream, BillingModel: imageModel,
		BillingModelSource: BillingModelSourceUpstream, ChannelID: input.ChannelID,
		ModelMappingChain: strings.TrimSpace(input.ModelMappingChain), Pricing: quote,
	}
	if !strings.EqualFold(strings.TrimSpace(upstream), imageModel) {
		textIdentity, textErr := s.ResolveOpenAIBillingIdentity(ctx, input)
		if textErr != nil {
			return nil, preflightError("text fallback billing model is not priceable", textErr)
		}
		identity.TextBillingModel = textIdentity.BillingModel
		identity.TextPricing = textIdentity.Pricing
	}
	return identity, nil
}

func (s *OpenAIGatewayService) ResolveOpenAIImagePricingQuote(
	ctx context.Context,
	model string,
	imageSizeTier string,
	groupID *int64,
	groupConfig *ImagePriceConfig,
) (*PricingQuote, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, fmt.Errorf("%w: image model is empty", ErrOpenAIPricingUnavailable)
	}
	resolver := s.resolver
	if resolver == nil && s.billingService != nil {
		resolver = NewModelPricingResolver(s.channelService, s.billingService)
	}
	if resolver != nil && groupID != nil {
		resolved := resolver.Resolve(ctx, PricingInput{Model: model, GroupID: groupID})
		if resolved != nil && (resolved.Mode == BillingModeImage || resolved.Mode == BillingModePerRequest) {
			if !resolvedPricingIsUsable(resolved) {
				return nil, fmt.Errorf("%w for image model %s", ErrOpenAIPricingUnavailable, model)
			}
			selectedPrice := resolver.GetRequestTierPrice(resolved, imageSizeTier)
			if selectedPrice == 0 {
				selectedPrice = resolved.DefaultPerRequestPrice
			}
			if !positiveFinitePrice(selectedPrice) {
				return nil, fmt.Errorf("%w: no %s price for image model %s", ErrOpenAIPricingUnavailable, imageSizeTier, model)
			}
			selected := &ResolvedPricing{
				Mode: resolved.Mode, Source: resolved.Source, Revision: resolved.Revision,
				SourceExact: resolved.SourceExact, DefaultPerRequestPrice: selectedPrice,
				RequestTiers: []PricingInterval{{TierLabel: imageSizeTier, PerRequestPrice: pricingFloat64Ptr(selectedPrice)}},
			}
			return freezeResolvedPricingQuote(selected)
		}
	}

	basePrice := 0.0
	source := ""
	if s.billingService != nil && s.billingService.pricingService != nil {
		if pricing := s.billingService.pricingService.GetModelPricing(model); pricing != nil && positiveFinitePrice(pricing.OutputCostPerImage) {
			basePrice = pricing.OutputCostPerImage
			source = PricingSourceLiteLLM
		}
	}
	if basePrice == 0 && isOpenAIImageGenerationModel(model) {
		basePrice = 0.134
		source = PricingSourceBuiltinFallback
	}
	prices := map[string]float64{
		"1K": basePrice,
		"2K": basePrice * 1.5,
		"4K": basePrice * 2,
	}
	imageSizeTier = strings.TrimSpace(imageSizeTier)
	if imageSizeTier == "" {
		imageSizeTier = "2K"
	}
	var groupPrice *float64
	if groupConfig != nil {
		switch imageSizeTier {
		case "1K":
			groupPrice = groupConfig.Price1K
		case "2K":
			groupPrice = groupConfig.Price2K
		case "4K":
			groupPrice = groupConfig.Price4K
		}
	}
	if groupPrice != nil {
		if !positiveFinitePrice(*groupPrice) {
			return nil, fmt.Errorf("%w: invalid %s image price", ErrOpenAIPricingUnavailable, imageSizeTier)
		}
		prices[imageSizeTier] = *groupPrice
		source = PricingSourceGroupImage
	}
	selectedPrice := prices[imageSizeTier]
	if source == "" || !positiveFinitePrice(selectedPrice) {
		return nil, fmt.Errorf("%w for image model %s", ErrOpenAIPricingUnavailable, model)
	}
	resolved := &ResolvedPricing{
		Mode: BillingModeImage, Source: source, DefaultPerRequestPrice: selectedPrice, SourceExact: true,
		RequestTiers: []PricingInterval{
			{TierLabel: imageSizeTier, PerRequestPrice: pricingFloat64Ptr(selectedPrice)},
		},
	}
	return freezeResolvedPricingQuote(resolved)
}

func (s *OpenAIGatewayService) ResolveOpenAIBillingIdentity(ctx context.Context, input OpenAIBillingIdentityInput) (*ResolvedOpenAIBillingIdentity, error) {
	requested := strings.TrimSpace(input.RequestedModel)
	channelMapped := firstNonEmptyModel(input.ChannelMappedModel, input.DispatchModel, requested)
	accountMapped := firstNonEmptyModel(input.AccountMappedModel, channelMapped)
	upstream := firstNonEmptyModel(input.UpstreamModel, accountMapped)
	source := strings.TrimSpace(input.BillingModelSource)
	if source == "" {
		source = BillingModelSourceUpstream
	}

	billingModel := upstream
	switch source {
	case BillingModelSourceRequested:
		billingModel = requested
	case BillingModelSourceChannelMapped:
		billingModel = channelMapped
	case BillingModelSourceUpstream:
		billingModel = upstream
	default:
		return nil, preflightError("unknown billing model source", nil)
	}

	stages := []string{requested, channelMapped, accountMapped, upstream, billingModel}
	strictGPT56 := false
	for _, model := range stages {
		if _, family := classifyOpenAIGPT56PreviewModel(model); family {
			strictGPT56 = true
			break
		}
	}
	if strictGPT56 {
		if source == BillingModelSourceRequested {
			return nil, preflightError("GPT-5.6 cannot use requested billing", nil)
		}
		if input.ChannelMappingApplied && !input.ChannelMappingExact {
			return nil, preflightError("GPT-5.6 channel mapping must be exact", nil)
		}
		for i := 0; i+1 < len(stages); i++ {
			if !ValidateOpenAIGPT56ModelTransition(stages[i], stages[i+1]) {
				return nil, preflightError("invalid GPT-5.6 model transition", nil)
			}
		}
		normalized, family := classifyOpenAIGPT56PreviewModel(billingModel)
		if !family || normalized == "" {
			return nil, preflightError("invalid GPT-5.6 billing model", nil)
		}
		billingModel = normalized
	}
	if strings.TrimSpace(billingModel) == "" {
		return nil, preflightError("billing model is empty", nil)
	}

	resolver := s.resolver
	if resolver == nil && s.billingService != nil {
		resolver = NewModelPricingResolver(s.channelService, s.billingService)
	}
	if resolver == nil && strictGPT56 {
		billing := NewBillingService(&config.Config{}, nil)
		resolver = NewModelPricingResolver(nil, billing)
	}
	if resolver == nil {
		return nil, preflightError("pricing resolver is unavailable", ErrOpenAIPricingUnavailable)
	}
	quote, err := resolver.ResolveQuote(ctx, PricingInput{Model: billingModel, GroupID: input.GroupID})
	if err != nil {
		return nil, preflightError("final billing model is not priceable", err)
	}

	return &ResolvedOpenAIBillingIdentity{
		RequestedModel: requested, CompatModel: strings.TrimSpace(input.CompatModel),
		DispatchModel: strings.TrimSpace(input.DispatchModel), ChannelMappedModel: channelMapped,
		AccountMappedModel: accountMapped, UpstreamModel: upstream, BillingModel: billingModel,
		BillingModelSource: source, ChannelID: input.ChannelID,
		ModelMappingChain: strings.TrimSpace(input.ModelMappingChain), Pricing: quote,
	}, nil
}

func preflightError(reason string, cause error) error {
	return &OpenAIBillingPreflightError{Reason: reason, Cause: cause}
}

func firstNonEmptyModel(models ...string) string {
	for _, model := range models {
		if model = strings.TrimSpace(model); model != "" {
			return model
		}
	}
	return ""
}

func (r *ModelPricingResolver) ResolveQuote(ctx context.Context, input PricingInput) (*PricingQuote, error) {
	if r == nil || r.billingService == nil {
		return nil, fmt.Errorf("%w: pricing resolver is unavailable", ErrOpenAIPricingUnavailable)
	}
	resolved := r.Resolve(ctx, input)
	if !resolvedPricingIsUsable(resolved) {
		return nil, fmt.Errorf("%w for model %s", ErrOpenAIPricingUnavailable, strings.TrimSpace(input.Model))
	}

	if normalized, family := classifyOpenAIGPT56PreviewModel(input.Model); family {
		if normalized == "" {
			return nil, fmt.Errorf("%w for model %s", ErrOpenAIPricingUnavailable, strings.TrimSpace(input.Model))
		}
		if resolved.Source == PricingSourceChannel && !resolved.SourceExact {
			return nil, fmt.Errorf("%w: GPT-5.6 channel pricing must be exact", ErrOpenAIPricingUnavailable)
		}
		if resolved.Source == PricingSourceBuiltinFallback {
			resolved.Source = PricingSourceBuiltinGPT56
			resolved.Revision = GPT56PricingRevision
		}
	}
	return freezeResolvedPricingQuote(resolved)
}

func freezeResolvedPricingQuote(resolved *ResolvedPricing) (*PricingQuote, error) {
	if resolved == nil || !resolvedPricingIsUsable(resolved) {
		return nil, ErrOpenAIPricingUnavailable
	}
	if resolved.Revision == "" {
		resolved.Revision = pricingRevisionForResolved(resolved)
	}
	snapshot := cloneResolvedPricing(resolved)
	hash, err := hashResolvedPricing(snapshot)
	if err != nil {
		return nil, fmt.Errorf("hash pricing quote: %w", err)
	}
	if snapshot.Source == PricingSourceLiteLLM && snapshot.Revision == "" {
		// localHash may be a remote synchronization anchor rather than the
		// bytes currently loaded in memory. Bind the revision to the effective
		// immutable schedule instead of asserting unverified file provenance.
		snapshot.Revision = "effective-" + hash[:16]
	}
	return &PricingQuote{
		Resolved: snapshot,
		Evidence: PricingEvidence{Source: snapshot.Source, Revision: snapshot.Revision, Hash: hash},
	}, nil
}

func pricingRevisionForResolved(resolved *ResolvedPricing) string {
	switch resolved.Source {
	case PricingSourceChannel:
		return channelPricingRevision
	case PricingSourceLiteLLM:
		return ""
	case PricingSourceBuiltinGPT56:
		return GPT56PricingRevision
	case PricingSourceGroupImage:
		return groupImagePricingRevision
	default:
		return builtinFallbackRevision
	}
}

func resolvedPricingIsUsable(resolved *ResolvedPricing) bool {
	if resolved == nil {
		return false
	}
	switch resolved.Mode {
	case BillingModePerRequest, BillingModeImage:
		if resolved.DefaultPerRequestPrice != 0 && !positiveFinitePrice(resolved.DefaultPerRequestPrice) {
			return false
		}
		usable := positiveFinitePrice(resolved.DefaultPerRequestPrice)
		for _, tier := range resolved.RequestTiers {
			if tier.PerRequestPrice != nil {
				if !positiveFinitePrice(*tier.PerRequestPrice) {
					return false
				}
				usable = true
			}
		}
		return usable
	default:
		usable, valid := usableModelPricing(resolved.BasePricing)
		for _, interval := range resolved.Intervals {
			intervalUsable := false
			for _, price := range []*float64{interval.InputPrice, interval.OutputPrice, interval.CacheWritePrice, interval.CacheReadPrice} {
				if price == nil {
					continue
				}
				if !nonNegativeFinitePrice(*price) {
					return false
				}
				intervalUsable = intervalUsable || *price > 0
			}
			if !intervalUsable {
				return false
			}
			usable = true
		}
		return valid && usable
	}
}

func usableModelPricing(pricing *ModelPricing) (usable bool, valid bool) {
	if pricing == nil {
		return false, true
	}
	valid = true
	prices := []float64{
		pricing.InputPricePerToken, pricing.InputPricePerTokenPriority,
		pricing.OutputPricePerToken, pricing.OutputPricePerTokenPriority,
		pricing.CacheCreationPricePerToken, pricing.CacheReadPricePerToken,
		pricing.CacheReadPricePerTokenPriority, pricing.CacheCreation5mPrice,
		pricing.CacheCreation1hPrice, pricing.ImageOutputPricePerToken,
	}
	for _, price := range prices {
		if price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
			return false, false
		}
		usable = usable || price > 0
	}
	return usable, valid
}

func positiveFinitePrice(price float64) bool {
	return price > 0 && !math.IsNaN(price) && !math.IsInf(price, 0)
}

func nonNegativeFinitePrice(price float64) bool {
	return price >= 0 && !math.IsNaN(price) && !math.IsInf(price, 0)
}

func pricingFloat64Ptr(price float64) *float64 {
	return &price
}

func cloneResolvedPricing(in *ResolvedPricing) *ResolvedPricing {
	if in == nil {
		return nil
	}
	out := *in
	if in.BasePricing != nil {
		base := *in.BasePricing
		out.BasePricing = &base
	}
	out.Intervals = clonePricingIntervals(in.Intervals)
	out.RequestTiers = clonePricingIntervals(in.RequestTiers)
	return &out
}

func clonePricingIntervals(in []PricingInterval) []PricingInterval {
	if in == nil {
		return nil
	}
	out := make([]PricingInterval, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].MaxTokens = cloneIntPtr(in[i].MaxTokens)
		out[i].InputPrice = cloneFloat64Ptr(in[i].InputPrice)
		out[i].OutputPrice = cloneFloat64Ptr(in[i].OutputPrice)
		out[i].CacheWritePrice = cloneFloat64Ptr(in[i].CacheWritePrice)
		out[i].CacheReadPrice = cloneFloat64Ptr(in[i].CacheReadPrice)
		out[i].PerRequestPrice = cloneFloat64Ptr(in[i].PerRequestPrice)
	}
	return out
}

func cloneIntPtr(v *int) *int {
	if v == nil {
		return nil
	}
	copy := *v
	return &copy
}

func cloneFloat64Ptr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	copy := *v
	return &copy
}

type canonicalResolvedPricing struct {
	Mode                   BillingMode       `json:"mode"`
	Base                   *ModelPricing     `json:"base,omitempty"`
	Intervals              []PricingInterval `json:"intervals,omitempty"`
	RequestTiers           []PricingInterval `json:"request_tiers,omitempty"`
	DefaultPerRequestPrice float64           `json:"default_per_request_price"`
	SupportsCacheBreakdown bool              `json:"supports_cache_breakdown"`
}

func hashResolvedPricing(resolved *ResolvedPricing) (string, error) {
	if resolved == nil {
		return "", errors.New("resolved pricing is nil")
	}
	canonical := canonicalResolvedPricing{
		Mode: resolved.Mode, Base: resolved.BasePricing,
		Intervals:              clonePricingIntervals(resolved.Intervals),
		RequestTiers:           clonePricingIntervals(resolved.RequestTiers),
		DefaultPerRequestPrice: resolved.DefaultPerRequestPrice,
		SupportsCacheBreakdown: resolved.SupportsCacheBreakdown,
	}
	sortPricingIntervals(canonical.Intervals)
	sortPricingIntervals(canonical.RequestTiers)
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func sortPricingIntervals(intervals []PricingInterval) {
	sort.SliceStable(intervals, func(i, j int) bool {
		left, _ := json.Marshal(canonicalPricingInterval(intervals[i]))
		right, _ := json.Marshal(canonicalPricingInterval(intervals[j]))
		return string(left) < string(right)
	})
}

func canonicalPricingInterval(iv PricingInterval) PricingInterval {
	iv.ID = 0
	iv.PricingID = 0
	iv.SortOrder = 0
	iv.CreatedAt = time.Time{}
	iv.UpdatedAt = time.Time{}
	return iv
}
