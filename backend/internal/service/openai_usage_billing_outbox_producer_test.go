package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAIUsageOutboxRepoStub struct {
	envelopes   []UsageBillingEnvelope
	err         error
	lastCtxErr  error
	hadDeadline bool
}

func (s *openAIUsageOutboxRepoStub) Enqueue(ctx context.Context, envelope UsageBillingEnvelope) (*UsageBillingOutboxEvent, bool, error) {
	s.lastCtxErr = ctx.Err()
	_, s.hadDeadline = ctx.Deadline()
	if s.err != nil {
		return nil, false, s.err
	}
	s.envelopes = append(s.envelopes, envelope)
	return &UsageBillingOutboxEvent{ID: int64(len(s.envelopes)), Envelope: envelope}, true, nil
}

func (*openAIUsageOutboxRepoStub) Claim(context.Context, string, int, time.Duration) ([]UsageBillingOutboxEvent, error) {
	panic("not used")
}
func (*openAIUsageOutboxRepoStub) Complete(context.Context, int64, string, string, string) error {
	panic("not used")
}
func (*openAIUsageOutboxRepoStub) Retry(context.Context, int64, string, string, time.Time, string, string) error {
	panic("not used")
}
func (*openAIUsageOutboxRepoStub) DeadLetter(context.Context, int64, string, string, string, string) error {
	panic("not used")
}

type openAIUsageOutboxWakeStub struct{ calls int }

func (s *openAIUsageOutboxWakeStub) Wake() { s.calls++ }

func openAIUsageProducerQuote(mode BillingMode) *PricingQuote {
	resolved := &ResolvedPricing{
		Mode: mode, Source: PricingSourceBuiltinGPT56,
		BasePricing: &ModelPricing{InputPricePerToken: 0.01, OutputPricePerToken: 0.02},
	}
	if mode == BillingModeImage {
		resolved.BasePricing = nil
		resolved.DefaultPerRequestPrice = 0.25
	}
	return &PricingQuote{
		Resolved: resolved,
		Evidence: PricingEvidence{Source: PricingSourceBuiltinGPT56, Revision: GPT56PricingRevision, Hash: strings.Repeat("a", 64)},
	}
}

func TestOpenAIUsageBillingProducer_PersistsAllEntrypointSnapshotsBeforeWake(t *testing.T) {
	tests := []struct {
		name            string
		inboundEndpoint string
		requestedModel  string
		billingModel    string
		ws              bool
		imageCount      int
		imageSize       string
	}{
		{name: "responses", inboundEndpoint: "/v1/responses", requestedModel: "gpt-5.6-sol", billingModel: "gpt-5.6-sol"},
		{name: "chat", inboundEndpoint: "/v1/chat/completions", requestedModel: "gpt-5.6-sol", billingModel: "gpt-5.6-sol"},
		{name: "messages compat", inboundEndpoint: "/v1/messages", requestedModel: "claude-sonnet-4-6", billingModel: "gpt-5.6-sol"},
		{name: "native mapped GPT", inboundEndpoint: "/v1/responses", requestedModel: "gpt-5.4", billingModel: "gpt-5.6-sol"},
		{name: "images", inboundEndpoint: "/v1/images/generations", requestedModel: "gpt-image-2", billingModel: "gpt-image-2", imageCount: 1, imageSize: "1024x1024"},
		{name: "websocket", inboundEndpoint: "/v1/responses", requestedModel: "gpt-5.6-sol", billingModel: "gpt-5.6-sol", ws: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{}
			billingRepo := &openAIRecordUsageBillingRepoStub{}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			outbox := &openAIUsageOutboxRepoStub{}
			wake := &openAIUsageOutboxWakeStub{}
			svc.requireUsageBillingOutbox = true
			svc.usageBillingOutboxRepo = outbox
			svc.usageBillingOutboxWake = wake
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			groupID := int64(44)
			mode := BillingModeToken
			if tt.imageCount > 0 {
				mode = BillingModeImage
			}
			identity := &ResolvedOpenAIBillingIdentity{
				RequestedModel: tt.requestedModel, CompatModel: tt.requestedModel,
				UpstreamModel: tt.billingModel, BillingModel: tt.billingModel,
				BillingModelSource: BillingModelSourceUpstream,
				ModelMappingChain:  tt.requestedModel + " -> " + tt.billingModel,
				Pricing:            openAIUsageProducerQuote(mode),
			}
			result := &OpenAIForwardResult{
				RequestID: "producer-" + tt.name, Model: tt.requestedModel, UpstreamModel: tt.billingModel,
				BillingModel: tt.billingModel, BillingIdentity: identity,
				Usage: OpenAIUsage{InputTokens: 10, OutputTokens: 2}, Duration: time.Second,
				OpenAIWSMode: tt.ws, Stream: tt.ws, ImageCount: tt.imageCount, ImageSize: tt.imageSize,
			}

			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: result,
				APIKey: &APIKey{ID: 11, Key: "sk-producer-test", GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}},
				User:   &User{ID: 22}, Account: &Account{ID: 33, Type: AccountTypeAPIKey},
				InboundEndpoint: tt.inboundEndpoint, UpstreamEndpoint: "/v1/responses",
				ChannelUsageFields: ChannelUsageFields{OriginalModel: tt.requestedModel, ChannelMappedModel: tt.billingModel},
			})
			require.NoError(t, err)
			require.Len(t, outbox.envelopes, 1)
			require.Equal(t, 1, wake.calls)
			require.Zero(t, billingRepo.calls, "producer must not apply before durable replay")
			require.Zero(t, usageRepo.calls, "producer must not write usage before billing replay")
			log := outbox.envelopes[0].UsageLog()
			require.Equal(t, tt.requestedModel, log.RequestedModel)
			require.Equal(t, tt.billingModel, log.Model)
			require.Equal(t, tt.billingModel, *log.BillingModel)
			require.Equal(t, APIKeyAuthCacheLocator("sk-producer-test"), outbox.envelopes[0].AuthCacheLocator())
			require.Equal(t, tt.inboundEndpoint, *log.InboundEndpoint)
			if tt.ws {
				require.Equal(t, RequestTypeWSV2, log.RequestType)
			}
		})
	}
}

func TestOpenAIUsageBillingProducer_CanceledContextStillEnqueuesDetached(t *testing.T) {
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(&openAIRecordUsageLogRepoStub{}, &openAIRecordUsageBillingRepoStub{}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	outbox := &openAIUsageOutboxRepoStub{}
	svc.requireUsageBillingOutbox = true
	svc.usageBillingOutboxRepo = outbox
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	groupID := int64(44)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "producer-canceled", Model: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol",
			BillingModel: "gpt-5.6-sol", BillingIdentity: &ResolvedOpenAIBillingIdentity{
				BillingModel: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol", Pricing: openAIUsageProducerQuote(BillingModeToken),
			},
			Usage: OpenAIUsage{InputTokens: 1, OutputTokens: 1}, Duration: time.Second,
		},
		APIKey: &APIKey{ID: 11, Key: "sk-canceled-test", GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}},
		User:   &User{ID: 22}, Account: &Account{ID: 33, Type: AccountTypeAPIKey},
	})
	require.NoError(t, err)
	require.NoError(t, outbox.lastCtxErr)
	require.True(t, outbox.hadDeadline)
}

func TestOpenAIUsageBillingProducer_FreezesMonthlyAnchorGroupSeparatelyFromRoutingGroup(t *testing.T) {
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(&openAIRecordUsageLogRepoStub{}, &openAIRecordUsageBillingRepoStub{}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	outbox := &openAIUsageOutboxRepoStub{}
	svc.requireUsageBillingOutbox = true
	svc.usageBillingOutboxRepo = outbox
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	routingGroupID := int64(44)
	anchorGroupID := int64(77)
	subscriptionID := int64(88)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "producer-monthly-anchor", Model: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol",
			BillingModel: "gpt-5.6-sol", BillingIdentity: &ResolvedOpenAIBillingIdentity{
				BillingModel: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol", Pricing: openAIUsageProducerQuote(BillingModeToken),
			},
			Usage: OpenAIUsage{InputTokens: 1, OutputTokens: 1}, Duration: time.Second,
		},
		APIKey: &APIKey{ID: 11, Key: "sk-monthly-test", GroupID: &routingGroupID, Group: &Group{ID: routingGroupID, RateMultiplier: 1}},
		User:   &User{ID: 22}, Account: &Account{ID: 33, Type: AccountTypeAPIKey},
		Subscription: &UserSubscription{
			ID: subscriptionID, UserID: 22, GroupID: &anchorGroupID,
			Group: &Group{ID: anchorGroupID, SubscriptionType: SubscriptionTypeSubscription, RateMultiplier: 1},
		},
	})
	require.NoError(t, err)
	require.Len(t, outbox.envelopes, 1)
	require.Equal(t, routingGroupID, *outbox.envelopes[0].GroupID())
	require.Equal(t, anchorGroupID, *outbox.envelopes[0].EffectiveBillingGroupID())
}

func TestOpenAIUsageBillingProducer_FailsClosedWithoutFallback(t *testing.T) {
	sentinel := errors.New("outbox database unavailable")
	for _, tt := range []struct {
		name string
		repo UsageBillingOutboxRepository
		want error
	}{
		{name: "missing repository", want: ErrUsageBillingOutboxUnavailable},
		{name: "enqueue failure", repo: &openAIUsageOutboxRepoStub{err: sentinel}, want: sentinel},
	} {
		t.Run(tt.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{}
			billingRepo := &openAIRecordUsageBillingRepoStub{}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.requireUsageBillingOutbox = true
			svc.usageBillingOutboxRepo = tt.repo
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			groupID := int64(44)
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID: "producer-fail-closed", Model: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol",
					BillingModel: "gpt-5.6-sol", BillingIdentity: &ResolvedOpenAIBillingIdentity{
						BillingModel: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol", Pricing: openAIUsageProducerQuote(BillingModeToken),
					},
					Usage: OpenAIUsage{InputTokens: 1, OutputTokens: 1}, Duration: time.Second,
				},
				APIKey: &APIKey{ID: 11, Key: "sk-fail-closed-test", GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}},
				User:   &User{ID: 22}, Account: &Account{ID: 33, Type: AccountTypeAPIKey},
			})
			require.ErrorIs(t, err, tt.want)
			require.Zero(t, billingRepo.calls)
			require.Zero(t, usageRepo.calls)
		})
	}
}
