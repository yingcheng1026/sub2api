package service

import (
	"context"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// GatewayUsageBillingIdentity is the immutable gateway-path pricing and
// attempt identity that must be reused by settlement after the upstream result
// arrives.
type GatewayUsageBillingIdentity struct {
	BillingModel       string
	Pricing            *PricingQuote
	RateMultiplier     float64
	AdmissionAttemptID string
	AdmissionRef       UsageBillingAdmissionAttemptRef
}

// PrepareGatewayWalletUsageBillingAdmission reserves a credits wallet before
// supported gateway transports. Monthly subscriptions and legacy balance
// billing retain their existing path and never share this reservation.
func (s *GatewayService) PrepareGatewayWalletUsageBillingAdmission(
	ctx context.Context,
	apiKey *APIKey,
	user *User,
	account *Account,
	subscription *UserSubscription,
	parsed *ParsedRequest,
) (*GatewayUsageBillingIdentity, error) {
	if subscription == nil || !subscription.IsWalletMode() {
		return nil, nil
	}
	if s == nil || apiKey == nil || user == nil || account == nil || parsed == nil ||
		apiKey.GroupID == nil || apiKey.Group == nil || !gatewayWalletUsageBillingPlatformSupported(account.Platform) ||
		len(parsed.Body) == 0 || strings.TrimSpace(parsed.Model) == "" || !usageBillingRequestContextPrepared(ctx) {
		return nil, ErrUsageBillingLifecycleContractInvalid
	}
	billingModel, _ := resolveGatewayAccountMappedModel(account, parsed.Model)
	resolver := s.resolver
	if resolver == nil {
		resolver = NewModelPricingResolver(s.channelService, s.billingService)
	}
	pricing, err := resolver.ResolveQuote(ctx, PricingInput{Model: billingModel, GroupID: apiKey.GroupID})
	if err != nil {
		return nil, errors.Join(ErrUsageBillingLifecycleContractInvalid, err)
	}
	resolved := pricing.CloneResolved()
	if resolved == nil || (resolved.Mode != "" && resolved.Mode != BillingModeToken) {
		return nil, ErrUsageBillingLifecycleContractInvalid
	}
	rate := canonicalUsageBillingRate(s.resolveUsageRateMultiplier(ctx, user.ID, apiKey, subscription).Multiplier)
	quote, err := BuildUsageBillingReservationQuote(UsageBillingReservationQuoteBuildInput{
		RequestPayloadHash: HashUsageRequestPayload(parsed.Body),
		BillingModel:       billingModel,
		Pricing:            pricing,
		RateMultiplier:     rate,
		RequestBody:        parsed.Body,
		MaxOutputTokens:    parsed.MaxTokens,
	})
	if err != nil {
		return nil, errors.Join(ErrUsageBillingLifecycleContractInvalid, err)
	}
	_, session, err := s.AdmitUsageBillingRequest(ctx, apiKey, user, account, subscription, quote)
	if err != nil {
		return nil, err
	}
	if err := dispatchUsageBillingRequest(ctx, s.usageBillingAdmissionRepo, session); err != nil {
		return nil, err
	}
	identity := &GatewayUsageBillingIdentity{
		BillingModel: billingModel, Pricing: pricing, RateMultiplier: quote.RateMultiplier,
	}
	if session != nil {
		identity.AdmissionAttemptID = session.AttemptID
		identity.AdmissionRef = session.Ref()
	}
	return identity, nil
}

func gatewayWalletUsageBillingPlatformSupported(platform string) bool {
	switch platform {
	case PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformKiro, PlatformCursor:
		return true
	default:
		return false
	}
}

func (s *GatewayService) MarkGatewayUsageBillingAttemptFailed(ctx context.Context, identity *GatewayUsageBillingIdentity) error {
	if identity == nil {
		return nil
	}
	if s == nil || s.usageBillingAdmissionRepo == nil || identity.AdmissionRef.Validate() != nil {
		return ErrUsageBillingOutboxUnavailable
	}
	mutationCtx, cancel := newUsageBillingLifecycleMutationContext(ctx)
	defer cancel()
	return s.usageBillingAdmissionRepo.MarkAttemptFailed(mutationCtx, identity.AdmissionRef)
}

func (s *GatewayService) MarkGatewayUsageBillingOrphaned(ctx context.Context, identity *GatewayUsageBillingIdentity) error {
	if identity == nil {
		return nil
	}
	if s == nil || s.usageBillingAdmissionRepo == nil || identity.AdmissionRef.Validate() != nil {
		return ErrUsageBillingOutboxUnavailable
	}
	mutationCtx, cancel := newUsageBillingLifecycleMutationContext(ctx)
	defer cancel()
	return s.usageBillingAdmissionRepo.MarkOrphaned(mutationCtx, identity.AdmissionRef)
}

func (s *GatewayService) AbandonGatewayUsageBilling(ctx context.Context, identity *GatewayUsageBillingIdentity) error {
	if identity == nil {
		return nil
	}
	if s == nil || s.usageBillingAdmissionRepo == nil || identity.AdmissionRef.Validate() != nil {
		return ErrUsageBillingAdmissionInvalid
	}
	session := &UsageBillingAdmissionSession{
		RequestID: identity.AdmissionRef.RequestID, APIKeyID: identity.AdmissionRef.APIKeyID,
		OwnerToken: identity.AdmissionRef.OwnerToken, AttemptID: identity.AdmissionRef.AttemptID,
	}
	return abandonUsageBillingRequest(ctx, s.usageBillingAdmissionRepo, session)
}

func resolveGatewayAccountMappedModel(account *Account, requestedModel string) (string, string) {
	requestedModel = strings.TrimSpace(requestedModel)
	if account == nil {
		return requestedModel, ""
	}
	mappedModel := requestedModel
	mappingSource := ""
	if account.Type == AccountTypeAPIKey {
		mappedModel = account.GetMappedModel(requestedModel)
		if mappedModel != requestedModel {
			mappingSource = "account"
		}
	}
	if mappingSource == "" && account.Platform == PlatformAnthropic && account.Type == AccountTypeServiceAccount {
		if candidate, matched := account.ResolveMappedModel(requestedModel); matched {
			mappedModel = candidate
			mappingSource = "account"
		} else {
			normalized := normalizeVertexAnthropicModelID(claude.NormalizeModelID(requestedModel))
			if normalized != requestedModel {
				mappedModel = normalized
				mappingSource = "vertex"
			}
		}
	}
	if mappingSource == "" && account.Platform == PlatformAnthropic && account.Type != AccountTypeAPIKey {
		normalized := claude.NormalizeModelID(requestedModel)
		if normalized != requestedModel {
			mappedModel = normalized
			mappingSource = "prefix"
		}
	}
	return mappedModel, mappingSource
}

func (s *GatewayService) calculateFrozenGatewayUsageCost(
	ctx context.Context,
	result *ForwardResult,
	apiKey *APIKey,
	identity *GatewayUsageBillingIdentity,
) (*CostBreakdown, error) {
	if s == nil || s.billingService == nil || result == nil || apiKey == nil || identity == nil || identity.Pricing == nil ||
		result.ImageCount > 0 {
		return nil, ErrUsageBillingLifecycleContractInvalid
	}
	resolved := identity.Pricing.CloneResolved()
	if resolved == nil || (resolved.Mode != "" && resolved.Mode != BillingModeToken) {
		return nil, ErrUsageBillingLifecycleContractInvalid
	}
	resolver := s.resolver
	if resolver == nil {
		resolver = NewModelPricingResolver(s.channelService, s.billingService)
	}
	tokens := UsageTokens{
		InputTokens:           result.Usage.InputTokens,
		OutputTokens:          result.Usage.OutputTokens,
		CacheCreationTokens:   result.Usage.CacheCreationInputTokens,
		CacheReadTokens:       result.Usage.CacheReadInputTokens,
		CacheCreation5mTokens: result.Usage.CacheCreation5mTokens,
		CacheCreation1hTokens: result.Usage.CacheCreation1hTokens,
		ImageOutputTokens:     result.Usage.ImageOutputTokens,
	}
	return s.billingService.CalculateCostUnified(CostInput{
		Ctx: ctx, Model: identity.BillingModel, GroupID: apiKey.GroupID, Tokens: tokens,
		RequestCount: 1, RateMultiplier: identity.RateMultiplier,
		Resolver: resolver, Resolved: resolved,
	})
}
