package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type usageBillingAdmissionRepoCapture struct {
	admissions  []UsageBillingAdmission
	dispatched  []UsageBillingAdmissionAttemptRef
	failed      []UsageBillingAdmissionAttemptRef
	orphaned    []UsageBillingAdmissionAttemptRef
	abandoned   []UsageBillingAdmissionAttemptRef
	err         error
	dispatchErr error
}

func (s *usageBillingAdmissionRepoCapture) Admit(_ context.Context, admission UsageBillingAdmission) error {
	s.admissions = append(s.admissions, admission)
	return s.err
}

func TestOpenAIChatForwardStopsBeforeTokenAndTransportWhenWalletAdmissionFails(t *testing.T) {
	repo := &usageBillingAdmissionRepoCapture{err: ErrWalletInsufficient}
	cfg := &config.Config{}
	cfg.Gateway.ForcedCodexInstructionsTemplate = strings.Repeat("x", 32*1024)
	billing := NewBillingService(cfg, nil)
	svc := &OpenAIGatewayService{
		cfg: cfg, requireUsageBillingOutbox: true,
		usageBillingAdmissionRepo: repo, billingService: billing,
		resolver: NewModelPricingResolver(nil, billing),
	}
	body := []byte(`{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"hello"}],"max_tokens":64,"stream":false}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = request
	ctx, err := PrepareUsageBillingRequestContext(request.Context())
	require.NoError(t, err)
	groupID := int64(31)
	apiKey := &APIKey{ID: 11, Key: "sk-wallet", GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}}

	result, err := svc.ForwardAsChatCompletionsWithOptions(ctx, c, &Account{
		ID: 33, Type: AccountTypeAPIKey, Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-terra": "gpt-5.6-terra"},
		},
	}, body, "", "", OpenAIForwardOptions{
		RequestedModel: "gpt-5.6-terra", GroupID: &groupID,
		RequirePricingPreflight: true, RequireBillingAdmission: true,
		UsageBilling: &OpenAIUsageBillingAdmissionInput{
			APIKey: apiKey, User: &User{ID: 22}, RequestBody: body,
			RequestPayloadHash: HashUsageRequestPayload(body),
		},
	})
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrWalletInsufficient)
	require.Len(t, repo.admissions, 1)
}

func TestOpenAIMessagesAdmissionBoundsTransformedUpstreamRequest(t *testing.T) {
	repo := &usageBillingAdmissionRepoCapture{err: ErrWalletInsufficient}
	billing := NewBillingService(&config.Config{}, nil)
	svc := &OpenAIGatewayService{
		cfg: &config.Config{}, requireUsageBillingOutbox: true,
		usageBillingAdmissionRepo: repo, billingService: billing,
		resolver: NewModelPricingResolver(nil, billing),
	}
	body := []byte(`{"model":"gpt-5.6-luna","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"stream":false}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = request
	ctx, err := PrepareUsageBillingRequestContext(request.Context())
	require.NoError(t, err)
	groupID := int64(3)
	walletBalance := 30.0
	apiKey := &APIKey{ID: 424, Key: "sk-wallet", GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}}
	account := &Account{
		ID: 1693, Type: AccountTypeOAuth, Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-luna": "gpt-5.6-luna"},
		},
	}
	usageInput := &OpenAIUsageBillingAdmissionInput{
		APIKey: apiKey, User: &User{ID: 178}, Subscription: &UserSubscription{
			ID: 169, UserID: 178, GroupID: &groupID, WalletBalanceUSD: &walletBalance,
		},
		RequestBody: body, RequestPayloadHash: HashUsageRequestPayload(body),
	}

	result, err := svc.ForwardAsAnthropicWithOptions(ctx, c, account, body, "", "gpt-5.6-luna", OpenAIForwardOptions{
		RequestedModel: "gpt-5.6-luna", GroupID: &groupID,
		RequirePricingPreflight: true, RequireBillingAdmission: true,
		UsageBilling: usageInput,
	})
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrWalletInsufficient)
	require.Len(t, repo.admissions, 1)

	pricing, err := svc.resolver.ResolveQuote(ctx, PricingInput{Model: "gpt-5.6-luna", GroupID: &groupID})
	require.NoError(t, err)
	originalQuote, err := BuildUsageBillingReservationQuote(UsageBillingReservationQuoteBuildInput{
		RequestPayloadHash: HashUsageRequestPayload(body), BillingModel: "gpt-5.6-luna",
		Pricing: pricing, RateMultiplier: repo.admissions[0].RateMultiplier(), RequestBody: body,
	})
	require.NoError(t, err)
	require.Equal(t, HashUsageRequestPayload(body), repo.admissions[0].RequestPayloadHash())
	require.Greater(t, repo.admissions[0].WorstCaseCostUSD(), originalQuote.WorstCaseCostUSD+0.00001,
		"reservation must include compatibility fields added to the actual upstream request",
	)
}
func (s *usageBillingAdmissionRepoCapture) MarkDispatched(_ context.Context, ref UsageBillingAdmissionAttemptRef) error {
	s.dispatched = append(s.dispatched, ref)
	return s.dispatchErr
}
func (s *usageBillingAdmissionRepoCapture) MarkAttemptFailed(_ context.Context, ref UsageBillingAdmissionAttemptRef) error {
	s.failed = append(s.failed, ref)
	return nil
}
func (s *usageBillingAdmissionRepoCapture) MarkOrphaned(_ context.Context, ref UsageBillingAdmissionAttemptRef) error {
	s.orphaned = append(s.orphaned, ref)
	return nil
}
func (*usageBillingAdmissionRepoCapture) Heartbeat(context.Context, UsageBillingAdmissionAttemptRef, time.Duration) error {
	return nil
}
func (s *usageBillingAdmissionRepoCapture) Abandon(_ context.Context, ref UsageBillingAdmissionAttemptRef) error {
	s.abandoned = append(s.abandoned, ref)
	return nil
}

func TestOpenAIForwardUsageBillingDispatchesAdmissionBeforeTransport(t *testing.T) {
	repo := &usageBillingAdmissionRepoCapture{}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{}, requireUsageBillingOutbox: true,
		usageBillingAdmissionRepo: repo,
	}
	pricing, err := freezeResolvedPricingQuote(&ResolvedPricing{
		Mode: BillingModeToken, Source: PricingSourceBuiltinGPT56, Revision: GPT56PricingRevision,
		BasePricing: &ModelPricing{InputPricePerToken: 0.000001, OutputPricePerToken: 0.000002},
	})
	require.NoError(t, err)
	body := []byte(`{"model":"gpt-5.6-terra","max_output_tokens":100,"input":"hello"}`)
	groupID := int64(31)
	ctx, err := PrepareUsageBillingRequestContext(context.Background())
	require.NoError(t, err)

	identity := &ResolvedOpenAIBillingIdentity{
		BillingModel: "gpt-5.6-terra", Pricing: pricing,
	}
	usageInput := &OpenAIUsageBillingAdmissionInput{
		APIKey: &APIKey{ID: 11, Key: "sk-preflight", GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}},
		User:   &User{ID: 22}, RequestBody: body, RequestPayloadHash: HashUsageRequestPayload(body),
	}
	err = svc.prepareOpenAIForwardUsageBilling(ctx, &Account{ID: 33, Type: AccountTypeOAuth}, identity, OpenAIForwardOptions{
		RequireBillingAdmission: true,
		UsageBilling:            usageInput,
	})
	require.NoError(t, err)
	require.Len(t, repo.admissions, 1)
	require.Len(t, repo.dispatched, 1)
	require.Equal(t, repo.admissions[0].AttemptID(), repo.dispatched[0].AttemptID)
	require.Equal(t, repo.dispatched[0].AttemptID, identity.AdmissionAttemptID)
	require.Equal(t, repo.dispatched[0], usageInput.AdmissionRef)
	require.Equal(t, UsageBillingDeliveryUnknown, usageInput.DeliveryOutcome)
	require.Empty(t, repo.abandoned)

	require.NoError(t, svc.MarkOpenAIUsageBillingAttemptFailed(ctx, usageInput))
	require.Equal(t, UsageBillingDeliveryDefinitelyRejected, usageInput.DeliveryOutcome)
	require.NoError(t, svc.FinalizeOpenAIUsageBillingRequest(ctx, usageInput))
	require.Len(t, repo.failed, 1)
	require.Len(t, repo.abandoned, 1)
}

func TestOpenAIForwardUsageBillingAbandonsPreparedHoldWhenDispatchFenceFails(t *testing.T) {
	repo := &usageBillingAdmissionRepoCapture{dispatchErr: ErrUsageBillingAdmissionLeaseLost}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{}, requireUsageBillingOutbox: true,
		usageBillingAdmissionRepo: repo,
	}
	pricing, err := freezeResolvedPricingQuote(&ResolvedPricing{
		Mode: BillingModeToken, Source: PricingSourceBuiltinGPT56, Revision: GPT56PricingRevision,
		BasePricing: &ModelPricing{InputPricePerToken: 0.000001, OutputPricePerToken: 0.000002},
	})
	require.NoError(t, err)
	body := []byte(`{"model":"gpt-5.6-terra","input":"hello"}`)
	groupID := int64(31)
	ctx, err := PrepareUsageBillingRequestContext(context.Background())
	require.NoError(t, err)

	err = svc.prepareOpenAIForwardUsageBilling(ctx, &Account{ID: 33, Type: AccountTypeOAuth}, &ResolvedOpenAIBillingIdentity{
		BillingModel: "gpt-5.6-terra", Pricing: pricing,
	}, OpenAIForwardOptions{
		RequireBillingAdmission: true,
		UsageBilling: &OpenAIUsageBillingAdmissionInput{
			APIKey: &APIKey{ID: 11, Key: "sk-preflight", GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}},
			User:   &User{ID: 22}, RequestBody: body, RequestPayloadHash: HashUsageRequestPayload(body),
		},
	})
	require.ErrorIs(t, err, ErrUsageBillingAdmissionLeaseLost)
	require.Len(t, repo.abandoned, 1)
}

func TestOpenAIUsageBillingUnknownDeliveryBecomesOrphaned(t *testing.T) {
	repo := &usageBillingAdmissionRepoCapture{}
	svc := &OpenAIGatewayService{usageBillingAdmissionRepo: repo}
	input := &OpenAIUsageBillingAdmissionInput{
		AdmissionRef: UsageBillingAdmissionAttemptRef{
			RequestID: "unknown-delivery", APIKeyID: 11,
			OwnerToken: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32),
		},
		DeliveryOutcome: UsageBillingDeliveryUnknown,
	}

	require.NoError(t, svc.FinalizeOpenAIUsageBillingRequest(context.Background(), input))
	require.Len(t, repo.orphaned, 1)
	require.Empty(t, repo.abandoned)
}

func (*usageBillingAdmissionRepoCapture) WaitSettled(context.Context, UsageBillingAdmissionAttemptRef) error {
	return nil
}

func TestOpenAIUsageBillingAdmissionUsesFrozenPreflightPricing(t *testing.T) {
	repo := &usageBillingAdmissionRepoCapture{}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{}, requireUsageBillingOutbox: true,
		usageBillingAdmissionRepo: repo,
	}
	pricing, err := freezeResolvedPricingQuote(&ResolvedPricing{
		Mode: BillingModeToken, Source: PricingSourceBuiltinGPT56, Revision: GPT56PricingRevision,
		BasePricing: &ModelPricing{InputPricePerToken: 0.000001, OutputPricePerToken: 0.000002},
	})
	require.NoError(t, err)
	body := []byte(`{"model":"gpt-5.6-terra","max_output_tokens":100,"input":"hello"}`)
	groupID := int64(31)
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "preflight-123")
	ctx, err = PrepareUsageBillingRequestContext(ctx)
	require.NoError(t, err)

	preparedCtx, session, err := svc.PrepareOpenAIUsageBillingAdmission(ctx, &Account{ID: 33, Type: AccountTypeOAuth}, &OpenAIUsageBillingAdmissionInput{
		APIKey: &APIKey{ID: 11, Key: "sk-preflight", GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}},
		User:   &User{ID: 22}, RequestBody: body, RequestPayloadHash: HashUsageRequestPayload(body),
		BillingIdentity: &ResolvedOpenAIBillingIdentity{BillingModel: "gpt-5.6-terra", Pricing: pricing},
	})
	require.NoError(t, err)
	require.NotNil(t, session)
	require.Equal(t, "local:preflight-123", preparedCtx.Value(ctxkey.UsageBillingRequestID))
	require.Len(t, repo.admissions, 1)
	require.Equal(t, pricing.Evidence.Hash, repo.admissions[0].PricingHash())
	require.Equal(t, HashUsageRequestPayload(body), repo.admissions[0].RequestPayloadHash())
	require.Greater(t, repo.admissions[0].WorstCaseCostUSD(), 0.0)
}

func TestOpenAIUsageBillingAdmissionFreezesImageAndTextSettlementOptions(t *testing.T) {
	repo := &usageBillingAdmissionRepoCapture{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, requireUsageBillingOutbox: true, usageBillingAdmissionRepo: repo}
	imagePricing, err := freezeResolvedPricingQuote(&ResolvedPricing{
		Mode: BillingModeImage, Source: PricingSourceBuiltinFallback, Revision: builtinFallbackRevision,
		DefaultPerRequestPrice: 0.25,
	})
	require.NoError(t, err)
	textPricing, err := freezeResolvedPricingQuote(&ResolvedPricing{
		Mode: BillingModeToken, Source: PricingSourceBuiltinGPT56, Revision: GPT56PricingRevision,
		BasePricing: &ModelPricing{InputPricePerToken: 0.000001, OutputPricePerToken: 0.000002},
	})
	require.NoError(t, err)
	body := []byte(`{"model":"gpt-image-2","n":1}`)
	groupID := int64(31)
	ctx, err := PrepareUsageBillingRequestContext(context.Background())
	require.NoError(t, err)

	_, session, err := svc.PrepareOpenAIUsageBillingAdmission(ctx, &Account{ID: 33, Type: AccountTypeOAuth}, &OpenAIUsageBillingAdmissionInput{
		APIKey: &APIKey{ID: 11, Key: "sk-preflight", GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}},
		User:   &User{ID: 22}, RequestBody: body, RequestPayloadHash: HashUsageRequestPayload(body),
		BillingIdentity: &ResolvedOpenAIBillingIdentity{
			BillingModel: "gpt-image-2", Pricing: imagePricing,
			TextBillingModel: "gpt-5.6-terra", TextPricing: textPricing,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, session)
	require.Len(t, repo.admissions, 1)
	admission := repo.admissions[0]
	require.Equal(t, "gpt-image-2", admission.BillingModel())
	require.Equal(t, imagePricing.Evidence.Hash, admission.PricingHash())
	require.Equal(t, "gpt-5.6-terra", admission.AlternateBillingModel())
	require.Equal(t, textPricing.Evidence.Hash, admission.AlternatePricingHash())
	require.GreaterOrEqual(t, admission.WorstCaseCostUSD(), 0.25)
}

func TestOpenAIUsageBillingAdmissionRejectsMismatchedPayloadHash(t *testing.T) {
	repo := &usageBillingAdmissionRepoCapture{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, requireUsageBillingOutbox: true, usageBillingAdmissionRepo: repo}
	body := []byte(`{"model":"gpt-5.6-terra"}`)
	groupID := int64(31)
	pricing, err := freezeResolvedPricingQuote(&ResolvedPricing{
		Mode: BillingModeToken, Source: PricingSourceBuiltinGPT56, Revision: GPT56PricingRevision,
		BasePricing: &ModelPricing{InputPricePerToken: 0.000001, OutputPricePerToken: 0.000002},
	})
	require.NoError(t, err)
	ctx, err := PrepareUsageBillingRequestContext(context.Background())
	require.NoError(t, err)

	_, _, err = svc.PrepareOpenAIUsageBillingAdmission(ctx, &Account{ID: 33, Type: AccountTypeOAuth}, &OpenAIUsageBillingAdmissionInput{
		APIKey: &APIKey{ID: 11, Key: "sk-preflight", GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}},
		User:   &User{ID: 22}, RequestBody: body, RequestPayloadHash: strings.Repeat("a", 64),
		BillingIdentity: &ResolvedOpenAIBillingIdentity{BillingModel: "gpt-5.6-terra", Pricing: pricing},
	})
	require.ErrorIs(t, err, ErrUsageBillingLifecycleContractInvalid)
	require.Empty(t, repo.admissions)
}

func TestOpenAIUsageBillingAdmissionRequiresFrozenRequestContext(t *testing.T) {
	repo := &usageBillingAdmissionRepoCapture{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, requireUsageBillingOutbox: true, usageBillingAdmissionRepo: repo}
	body := []byte(`{"model":"gpt-5.6-terra"}`)
	groupID := int64(31)
	pricing, err := freezeResolvedPricingQuote(&ResolvedPricing{
		Mode: BillingModeToken, Source: PricingSourceBuiltinGPT56, Revision: GPT56PricingRevision,
		BasePricing: &ModelPricing{InputPricePerToken: 0.000001, OutputPricePerToken: 0.000002},
	})
	require.NoError(t, err)

	_, _, err = svc.PrepareOpenAIUsageBillingAdmission(context.Background(), &Account{ID: 33, Type: AccountTypeOAuth}, &OpenAIUsageBillingAdmissionInput{
		APIKey: &APIKey{ID: 11, Key: "sk-preflight", GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}},
		User:   &User{ID: 22}, RequestBody: body, RequestPayloadHash: HashUsageRequestPayload(body),
		BillingIdentity: &ResolvedOpenAIBillingIdentity{BillingModel: "gpt-5.6-terra", Pricing: pricing},
	})
	require.ErrorIs(t, err, ErrUsageBillingLifecycleContractInvalid)
	require.Empty(t, repo.admissions)
}
