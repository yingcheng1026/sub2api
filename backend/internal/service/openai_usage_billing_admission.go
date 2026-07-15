package service

import (
	"context"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// OpenAIUsageBillingAdmissionInput binds authenticated principals and the
// original client payload to the exact pricing identity resolved by the
// forward preflight. The raw body is used only for a conservative cost bound.
type OpenAIUsageBillingAdmissionInput struct {
	APIKey       *APIKey
	User         *User
	Subscription *UserSubscription
	RequestBody  []byte
	// ReservationBody is the final transformed upstream payload. It is used
	// only for a conservative in-memory token bound and is never persisted.
	ReservationBody    []byte
	RequestPayloadHash string
	MaxOutputTokens    int
	RequestCount       int
	BillingIdentity    *ResolvedOpenAIBillingIdentity
	AdmissionRef       UsageBillingAdmissionAttemptRef
	DeliveryOutcome    UsageBillingDeliveryOutcome
	ForwardAccepted    bool
}

func (s *OpenAIGatewayService) PrepareOpenAIUsageBillingAdmission(
	ctx context.Context,
	account *Account,
	input *OpenAIUsageBillingAdmissionInput,
) (context.Context, *UsageBillingAdmissionSession, error) {
	if s == nil {
		return ctx, nil, ErrUsageBillingOutboxUnavailable
	}
	if (s.cfg != nil && s.cfg.RunMode == config.RunModeSimple) || !s.requireUsageBillingOutbox {
		return ctx, nil, nil
	}
	if input == nil || input.APIKey == nil || input.User == nil || account == nil ||
		input.BillingIdentity == nil || input.BillingIdentity.Pricing == nil || len(input.RequestBody) == 0 {
		return ctx, nil, ErrUsageBillingLifecycleContractInvalid
	}
	if !usageBillingRequestContextPrepared(ctx) {
		return ctx, nil, ErrUsageBillingLifecycleContractInvalid
	}
	wantPayloadHash := HashUsageRequestPayload(input.RequestBody)
	if wantPayloadHash == "" || !strings.EqualFold(wantPayloadHash, strings.TrimSpace(input.RequestPayloadHash)) {
		return ctx, nil, ErrUsageBillingLifecycleContractInvalid
	}
	reservationBody := input.RequestBody
	if len(input.ReservationBody) > len(reservationBody) {
		reservationBody = input.ReservationBody
	}
	textMultiplier := s.resolveOpenAIUsageBillingAdmissionRate(ctx, input.APIKey, input.User, input.Subscription)
	multiplier := textMultiplier
	if mode := pricingQuoteMode(input.BillingIdentity); mode == BillingModeImage || mode == BillingModePerRequest {
		multiplier = resolveImageRateMultiplier(input.APIKey, multiplier)
	}
	quote, err := BuildUsageBillingReservationQuote(UsageBillingReservationQuoteBuildInput{
		RequestPayloadHash: input.RequestPayloadHash,
		BillingModel:       input.BillingIdentity.BillingModel,
		Pricing:            input.BillingIdentity.Pricing,
		RateMultiplier:     multiplier,
		RequestBody:        reservationBody,
		MaxOutputTokens:    input.MaxOutputTokens,
		RequestCount:       input.RequestCount,
	})
	if err != nil {
		return ctx, nil, errors.Join(ErrUsageBillingLifecycleContractInvalid, err)
	}
	if input.BillingIdentity.TextPricing != nil {
		alternate, alternateErr := BuildUsageBillingReservationQuote(UsageBillingReservationQuoteBuildInput{
			RequestPayloadHash: input.RequestPayloadHash,
			BillingModel:       input.BillingIdentity.TextBillingModel,
			Pricing:            input.BillingIdentity.TextPricing,
			RateMultiplier:     textMultiplier,
			RequestBody:        reservationBody,
			MaxOutputTokens:    input.MaxOutputTokens,
			RequestCount:       input.RequestCount,
		})
		if alternateErr != nil {
			return ctx, nil, errors.Join(ErrUsageBillingLifecycleContractInvalid, alternateErr)
		}
		quote.AlternateBillingModel = alternate.BillingModel
		quote.AlternatePricingSource = alternate.PricingSource
		quote.AlternatePricingRevision = alternate.PricingRevision
		quote.AlternatePricingHash = alternate.PricingHash
		quote.AlternateRateMultiplier = alternate.RateMultiplier
		if alternate.WorstCaseCostUSD > quote.WorstCaseCostUSD {
			quote.WorstCaseCostUSD = alternate.WorstCaseCostUSD
		}
		quote.WorstCaseCostUSD = canonicalUsageBillingReservation(quote.WorstCaseCostUSD)
		if validateErr := quote.Validate(); validateErr != nil {
			return ctx, nil, errors.Join(ErrUsageBillingLifecycleContractInvalid, validateErr)
		}
	}
	return admitUsageBillingRequest(
		ctx, s.cfg, s.requireUsageBillingOutbox, s.usageBillingAdmissionRepo,
		input.APIKey, input.User, account, input.Subscription, quote,
	)
}

func setOpenAIUsageBillingReservationBody(input *OpenAIUsageBillingAdmissionInput, body []byte) {
	if input == nil || len(body) == 0 || len(body) <= len(input.ReservationBody) {
		return
	}
	input.ReservationBody = append([]byte(nil), body...)
}

func (s *OpenAIGatewayService) prepareOpenAIForwardUsageBilling(
	ctx context.Context,
	account *Account,
	identity *ResolvedOpenAIBillingIdentity,
	opts OpenAIForwardOptions,
) error {
	if !opts.RequireBillingAdmission {
		return nil
	}
	if opts.UsageBilling == nil || identity == nil {
		return ErrUsageBillingLifecycleContractInvalid
	}
	input := *opts.UsageBilling
	input.BillingIdentity = identity
	_, session, err := s.PrepareOpenAIUsageBillingAdmission(ctx, account, &input)
	if err != nil {
		return err
	}
	if err := s.DispatchUsageBillingRequest(ctx, session); err != nil {
		return err
	}
	if session != nil {
		identity.AdmissionAttemptID = session.AttemptID
		identity.AdmissionRef = session.Ref()
		opts.UsageBilling.AdmissionRef = session.Ref()
		opts.UsageBilling.DeliveryOutcome = UsageBillingDeliveryUnknown
		opts.UsageBilling.ForwardAccepted = false
	}
	return nil
}

// DispatchUsageBillingRequest advances the fenced attempt before any billable
// upstream bytes may be sent. A dispatch fencing failure happens before
// transport, so the prepared wallet hold is abandoned and released.
func (s *OpenAIGatewayService) DispatchUsageBillingRequest(ctx context.Context, session *UsageBillingAdmissionSession) error {
	if session == nil {
		return nil
	}
	if s == nil || s.usageBillingAdmissionRepo == nil {
		return ErrUsageBillingOutboxUnavailable
	}
	return dispatchUsageBillingRequest(ctx, s.usageBillingAdmissionRepo, session)
}

func (s *OpenAIGatewayService) MarkOpenAIUsageBillingAttemptFailed(
	ctx context.Context,
	input *OpenAIUsageBillingAdmissionInput,
) error {
	if input == nil || input.AdmissionRef.Validate() != nil {
		return nil
	}
	if s == nil || s.usageBillingAdmissionRepo == nil {
		return ErrUsageBillingOutboxUnavailable
	}
	mutationCtx, cancel := newUsageBillingLifecycleMutationContext(ctx)
	defer cancel()
	if err := s.usageBillingAdmissionRepo.MarkAttemptFailed(mutationCtx, input.AdmissionRef); err != nil {
		return err
	}
	input.DeliveryOutcome = UsageBillingDeliveryDefinitelyRejected
	return nil
}

func (s *OpenAIGatewayService) MarkOpenAIUsageBillingAccepted(input *OpenAIUsageBillingAdmissionInput) {
	if input == nil || input.AdmissionRef.Validate() != nil {
		return
	}
	input.ForwardAccepted = true
	input.DeliveryOutcome = UsageBillingDeliveryAccepted
}

func (s *OpenAIGatewayService) FinalizeOpenAIUsageBillingRequest(
	ctx context.Context,
	input *OpenAIUsageBillingAdmissionInput,
) error {
	if input == nil || input.ForwardAccepted || input.AdmissionRef.Validate() != nil {
		return nil
	}
	if s == nil || s.usageBillingAdmissionRepo == nil {
		return ErrUsageBillingOutboxUnavailable
	}
	mutationCtx, cancel := newUsageBillingLifecycleMutationContext(ctx)
	defer cancel()
	if input.DeliveryOutcome == UsageBillingDeliveryDefinitelyRejected {
		session := &UsageBillingAdmissionSession{
			RequestID: input.AdmissionRef.RequestID, APIKeyID: input.AdmissionRef.APIKeyID,
			OwnerToken: input.AdmissionRef.OwnerToken, AttemptID: input.AdmissionRef.AttemptID,
		}
		return abandonUsageBillingRequest(mutationCtx, s.usageBillingAdmissionRepo, session)
	}
	if err := s.usageBillingAdmissionRepo.MarkOrphaned(mutationCtx, input.AdmissionRef); err != nil {
		return err
	}
	input.DeliveryOutcome = UsageBillingDeliveryUnknown
	return nil
}

func (s *OpenAIGatewayService) MarkOpenAIUsageBillingResultOrphaned(
	ctx context.Context,
	result *OpenAIForwardResult,
) error {
	if result == nil || result.BillingIdentity == nil || result.BillingIdentity.AdmissionRef.Validate() != nil {
		return nil
	}
	if s == nil || s.usageBillingAdmissionRepo == nil {
		return ErrUsageBillingOutboxUnavailable
	}
	mutationCtx, cancel := newUsageBillingLifecycleMutationContext(ctx)
	defer cancel()
	return s.usageBillingAdmissionRepo.MarkOrphaned(mutationCtx, result.BillingIdentity.AdmissionRef)
}

func usageBillingRequestContextPrepared(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	requestID, _ := ctx.Value(ctxkey.UsageBillingRequestID).(string)
	ownerToken, _ := ctx.Value(ctxkey.UsageBillingOwnerToken).(string)
	return strings.TrimSpace(requestID) != "" && validFenceToken(strings.TrimSpace(ownerToken))
}

func (s *OpenAIGatewayService) resolveOpenAIUsageBillingAdmissionRate(
	ctx context.Context,
	apiKey *APIKey,
	user *User,
	subscription *UserSubscription,
) float64 {
	systemDefault := 1.0
	if s != nil && s.cfg != nil {
		systemDefault = s.cfg.Default.RateMultiplier
	}
	resolver := s.userGroupRateResolver
	if resolver == nil {
		resolver = newUserGroupRateResolver(nil, nil, resolveUserGroupRateCacheTTL(s.cfg), nil, "service.openai_gateway.admission")
	}
	return resolveEffectiveRateMultiplier(ctx, resolver, user.ID, apiKey.GroupID, apiKey.Group, subscription, systemDefault).Multiplier
}
