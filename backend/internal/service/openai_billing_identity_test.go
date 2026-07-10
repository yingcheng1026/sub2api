package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResolveOpenAIBillingIdentityGPT56Contract(t *testing.T) {
	t.Parallel()

	svc := newOpenAIBillingPreflightServiceForTest()
	tests := []struct {
		name    string
		input   OpenAIBillingIdentityInput
		wantErr bool
	}{
		{
			name: "requested billing rejected even for same tier",
			input: OpenAIBillingIdentityInput{
				RequestedModel: "gpt-5.6-terra", ChannelMappedModel: "gpt-5.6-terra",
				AccountMappedModel: "gpt-5.6-terra", UpstreamModel: "gpt-5.6-terra",
				BillingModelSource: BillingModelSourceRequested,
			},
			wantErr: true,
		},
		{
			name: "exact same tier channel mapping accepted",
			input: OpenAIBillingIdentityInput{
				RequestedModel: "gpt-5.6-terra", ChannelMappedModel: "openai/gpt-5.6-terra-high",
				ChannelMappingExact: true, AccountMappedModel: "gpt-5.6-terra-high",
				UpstreamModel: "gpt-5.6-terra-high", BillingModelSource: BillingModelSourceChannelMapped,
			},
		},
		{
			name: "wildcard channel mapping rejected",
			input: OpenAIBillingIdentityInput{
				RequestedModel: "gpt-5.6-terra", ChannelMappedModel: "gpt-5.6-terra",
				ChannelMappingExact: false, ChannelMappingApplied: true,
				AccountMappedModel: "gpt-5.6-terra", UpstreamModel: "gpt-5.6-terra",
				BillingModelSource: BillingModelSourceChannelMapped,
			},
			wantErr: true,
		},
		{
			name: "cross tier rejected",
			input: OpenAIBillingIdentityInput{
				RequestedModel: "gpt-5.6-terra", ChannelMappedModel: "gpt-5.6-sol",
				ChannelMappingExact: true, ChannelMappingApplied: true,
				AccountMappedModel: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol",
				BillingModelSource: BillingModelSourceChannelMapped,
			},
			wantErr: true,
		},
		{
			name: "unknown family rejected",
			input: OpenAIBillingIdentityInput{
				RequestedModel: "gpt-5.6-unknown", ChannelMappedModel: "gpt-5.6-unknown",
				ChannelMappingExact: true, AccountMappedModel: "gpt-5.6-unknown",
				UpstreamModel: "gpt-5.6-unknown", BillingModelSource: BillingModelSourceUpstream,
			},
			wantErr: true,
		},
		{
			name: "legacy requested billing remains compatible",
			input: OpenAIBillingIdentityInput{
				RequestedModel: "gpt-5.4", ChannelMappedModel: "gpt-5.4",
				AccountMappedModel: "gpt-5.4", UpstreamModel: "gpt-5.4",
				BillingModelSource: BillingModelSourceRequested,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity, err := svc.ResolveOpenAIBillingIdentity(context.Background(), tt.input)
			if tt.wantErr {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrOpenAIBillingPreflight)
				require.Nil(t, identity)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, identity)
			require.NotNil(t, identity.Pricing)
			require.NotEmpty(t, identity.Pricing.Evidence.Hash)
		})
	}
}

func TestResolvePricingQuoteGPT56BuiltinEvidenceAndImmutability(t *testing.T) {
	t.Parallel()

	resolver := newOpenAIBillingPreflightServiceForTest().resolver
	quote, err := resolver.ResolveQuote(context.Background(), PricingInput{Model: "gpt-5.6-sol"})
	require.NoError(t, err)
	require.Equal(t, PricingSourceBuiltinGPT56, quote.Evidence.Source)
	require.Equal(t, GPT56PricingRevision, quote.Evidence.Revision)
	require.Len(t, quote.Evidence.Hash, 64)

	first := quote.CloneResolved()
	require.NotNil(t, first)
	require.NotNil(t, first.BasePricing)
	wantInput := first.BasePricing.InputPricePerToken
	first.BasePricing.InputPricePerToken = 999
	require.Equal(t, wantInput, quote.CloneResolved().BasePricing.InputPricePerToken)

	quote2, err := resolver.ResolveQuote(context.Background(), PricingInput{Model: "openai/gpt-5.6-sol-high"})
	require.NoError(t, err)
	require.Equal(t, quote.Evidence.Hash, quote2.Evidence.Hash)
}

func TestResolvePricingQuoteRemainsFrozenAfterPricingRefresh(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	pricing := NewPricingService(cfg, nil)
	pricing.pricingData["custom-priced"] = &LiteLLMModelPricing{
		InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6,
	}
	pricing.localHash = "revision-one"
	billing := NewBillingService(cfg, pricing)
	resolver := NewModelPricingResolver(nil, billing)

	quote, err := resolver.ResolveQuote(context.Background(), PricingInput{Model: "custom-priced"})
	require.NoError(t, err)
	wantHash := quote.Evidence.Hash
	wantInput := quote.CloneResolved().BasePricing.InputPricePerToken

	pricing.mu.Lock()
	pricing.pricingData = map[string]*LiteLLMModelPricing{
		"custom-priced": {InputCostPerToken: 9e-6, OutputCostPerToken: 10e-6},
	}
	pricing.localHash = "revision-two"
	pricing.mu.Unlock()

	require.Equal(t, wantInput, quote.CloneResolved().BasePricing.InputPricePerToken)
	require.Equal(t, wantHash, quote.Evidence.Hash)
	refreshed, err := resolver.ResolveQuote(context.Background(), PricingInput{Model: "custom-priced"})
	require.NoError(t, err)
	require.NotEqual(t, wantHash, refreshed.Evidence.Hash)
}

func TestPricingEvidenceHashCanonicalAndSensitive(t *testing.T) {
	t.Parallel()

	one := 1e-6
	two := 2e-6
	a := &ResolvedPricing{
		Mode:        BillingModeToken,
		BasePricing: &ModelPricing{InputPricePerToken: one, OutputPricePerToken: two},
		Intervals: []PricingInterval{
			{MinTokens: 100, InputPrice: &two},
			{MinTokens: 0, InputPrice: &one},
		},
		Source: PricingSourceChannel,
	}
	oneCopy := one
	twoCopy := two
	b := &ResolvedPricing{
		Mode:        BillingModeToken,
		BasePricing: &ModelPricing{InputPricePerToken: oneCopy, OutputPricePerToken: twoCopy},
		Intervals: []PricingInterval{
			{MinTokens: 0, InputPrice: &oneCopy},
			{MinTokens: 100, InputPrice: &twoCopy},
		},
		Source: PricingSourceChannel,
	}

	hashA, err := hashResolvedPricing(a)
	require.NoError(t, err)
	hashB, err := hashResolvedPricing(b)
	require.NoError(t, err)
	require.Equal(t, hashA, hashB)

	b.BasePricing.OutputPricePerToken = 3e-6
	hashChanged, err := hashResolvedPricing(b)
	require.NoError(t, err)
	require.NotEqual(t, hashA, hashChanged)
}

func TestForwardWithOptionsUnpriceablePreflightNeverCallsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"custom-unpriceable","stream":false,"input":"hello"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK}}
	svc := newOpenAIBillingPreflightServiceForTest()
	svc.httpUpstream = upstream
	account := &Account{
		ID: 1, Name: "preflight", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":       "redacted-test-value",
			"model_mapping": map[string]any{"custom-unpriceable": "custom-unpriceable"},
		},
	}

	result, err := svc.ForwardWithOptions(context.Background(), c, account, body, OpenAIForwardOptions{
		RequestedModel:          "custom-unpriceable",
		RequirePricingPreflight: true,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOpenAIPricingUnavailable)
	require.Nil(t, result)
	require.Nil(t, upstream.lastReq)
}

func TestOpenAISharedHTTPDispatchUnpriceablePreflightNeverCallsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		path         string
		body         []byte
		accountExtra map[string]any
		forward      func(*OpenAIGatewayService, *gin.Context, *Account, []byte, OpenAIForwardOptions) (*OpenAIForwardResult, error)
	}{
		{
			name: "chat responses conversion", path: "/v1/chat/completions",
			body: []byte(`{"model":"custom-unpriceable","messages":[{"role":"user","content":"hello"}]}`),
			forward: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte, opts OpenAIForwardOptions) (*OpenAIForwardResult, error) {
				return s.ForwardAsChatCompletionsWithOptions(context.Background(), c, a, body, "", "", opts)
			},
		},
		{
			name: "raw chat", path: "/v1/chat/completions",
			body:         []byte(`{"model":"custom-unpriceable","messages":[{"role":"user","content":"hello"}]}`),
			accountExtra: map[string]any{"openai_responses_supported": false},
			forward: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte, opts OpenAIForwardOptions) (*OpenAIForwardResult, error) {
				return s.ForwardAsChatCompletionsWithOptions(context.Background(), c, a, body, "", "", opts)
			},
		},
		{
			name: "messages compatibility", path: "/v1/messages",
			body: []byte(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`),
			forward: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte, opts OpenAIForwardOptions) (*OpenAIForwardResult, error) {
				return s.ForwardAsAnthropicWithOptions(context.Background(), c, a, body, "", "custom-unpriceable", opts)
			},
		},
		{
			name: "responses passthrough", path: "/v1/responses",
			body:         []byte(`{"model":"custom-unpriceable","stream":false,"instructions":"hello","input":"hello"}`),
			accountExtra: map[string]any{"openai_passthrough": true},
			forward: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte, opts OpenAIForwardOptions) (*OpenAIForwardResult, error) {
				return s.ForwardWithOptions(context.Background(), c, a, body, opts)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK}}
			svc := newOpenAIBillingPreflightServiceForTest()
			svc.httpUpstream = upstream
			account := &Account{
				ID: 1, Name: "preflight", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key": "redacted-test-value",
					"model_mapping": map[string]any{
						"custom-unpriceable": "custom-unpriceable",
					},
				},
				Extra: tt.accountExtra,
			}
			requested := "custom-unpriceable"
			mapping := ChannelMappingResult{MappedModel: requested, MappingExact: true, BillingModelSource: BillingModelSourceUpstream}
			if tt.name == "messages compatibility" {
				requested = "claude-sonnet-4-6"
				mapping = ChannelMappingResult{MappedModel: "custom-unpriceable", Mapped: true, MappingExact: true, BillingModelSource: BillingModelSourceUpstream}
			}
			result, err := tt.forward(svc, c, account, tt.body, OpenAIForwardOptions{
				RequestedModel: requested, ChannelMapping: mapping, RequirePricingPreflight: true,
			})
			require.Error(t, err)
			require.ErrorIs(t, err, ErrOpenAIPricingUnavailable)
			require.Nil(t, result)
			require.Nil(t, upstream.lastReq)
		})
	}
}

func TestOpenAIWSBillingPreflightRejectsUnpriceableBeforeTransport(t *testing.T) {
	t.Parallel()

	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK}}
	svc := newOpenAIBillingPreflightServiceForTest()
	svc.httpUpstream = upstream
	account := &Account{
		ID: 1, Name: "ws-preflight", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":       "redacted-test-value",
			"model_mapping": map[string]any{"custom-unpriceable": "custom-unpriceable"},
		},
	}
	identity, err := svc.ResolveOpenAIWSBillingIdentity(context.Background(), account, OpenAIForwardOptions{
		RequestedModel: "custom-unpriceable",
		ChannelMapping: ChannelMappingResult{
			MappedModel: "custom-unpriceable", MappingExact: true,
			BillingModelSource: BillingModelSourceUpstream,
		},
		RequirePricingPreflight: true,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOpenAIPricingUnavailable)
	require.Nil(t, identity)
	require.Nil(t, upstream.lastReq)
}

func TestOpenAIWSBillingIdentityResolvesFreshQuoteForEveryTurn(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	pricing := NewPricingService(cfg, nil)
	pricing.pricingData["custom-ws-priced"] = &LiteLLMModelPricing{InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6}
	billing := NewBillingService(cfg, pricing)
	svc := &OpenAIGatewayService{cfg: cfg, billingService: billing, resolver: NewModelPricingResolver(nil, billing)}
	account := &Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{"custom-ws-priced": "custom-ws-priced"}},
	}
	opts := OpenAIForwardOptions{
		RequestedModel:          "custom-ws-priced",
		ChannelMapping:          ChannelMappingResult{MappedModel: "custom-ws-priced", MappingExact: true, BillingModelSource: BillingModelSourceUpstream},
		RequirePricingPreflight: true,
	}

	firstTurn, err := svc.ResolveOpenAIWSBillingIdentity(context.Background(), account, opts)
	require.NoError(t, err)
	pricing.mu.Lock()
	pricing.pricingData = map[string]*LiteLLMModelPricing{
		"custom-ws-priced": {InputCostPerToken: 7e-6, OutputCostPerToken: 8e-6},
	}
	pricing.mu.Unlock()
	secondTurn, err := svc.ResolveOpenAIWSBillingIdentity(context.Background(), account, opts)
	require.NoError(t, err)

	require.NotEqual(t, firstTurn.Pricing.Evidence.Hash, secondTurn.Pricing.Evidence.Hash)
	require.NotEqual(t, firstTurn.Pricing.Evidence.Revision, secondTurn.Pricing.Evidence.Revision)
	require.Equal(t, 1e-6, firstTurn.Pricing.CloneResolved().BasePricing.InputPricePerToken)
	require.Equal(t, 7e-6, secondTurn.Pricing.CloneResolved().BasePricing.InputPricePerToken)
}

func newOpenAIBillingPreflightServiceForTest() *OpenAIGatewayService {
	cfg := &config.Config{}
	pricing := NewPricingService(cfg, nil)
	billing := NewBillingService(cfg, pricing)
	resolver := NewModelPricingResolver(nil, billing)
	return &OpenAIGatewayService{cfg: cfg, billingService: billing, resolver: resolver}
}

func TestOpenAIBillingPreflightErrorIsTyped(t *testing.T) {
	t.Parallel()
	err := &OpenAIBillingPreflightError{Reason: "test"}
	require.True(t, errors.Is(err, ErrOpenAIBillingPreflight))
}
