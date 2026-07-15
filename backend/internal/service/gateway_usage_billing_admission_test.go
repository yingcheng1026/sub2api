package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGatewayWalletUsageBillingAdmissionFreezesMappedClaudePricingBeforeTransport(t *testing.T) {
	repo := &usageBillingAdmissionRepoCapture{}
	billing := NewBillingService(&config.Config{}, nil)
	svc := &GatewayService{
		cfg: &config.Config{}, billingService: billing,
		resolver:                  NewModelPricingResolver(nil, billing),
		requireUsageBillingOutbox: true, usageBillingAdmissionRepo: repo,
	}
	groupID := int64(22)
	walletBalance := 10.0
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)
	ctx, err := PrepareUsageBillingRequestContext(context.Background())
	require.NoError(t, err)
	apiKey := &APIKey{
		ID: 11, Key: "sk-wallet-vip", GroupID: &groupID,
		Group: &Group{ID: groupID, Name: WalletDefaultVIPGroupName, Platform: PlatformAnthropic, Status: StatusActive,
			Hydrated: true, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1},
	}
	user := &User{ID: 22, AllowedGroups: []int64{groupID}}
	account := &Account{ID: 33, Type: AccountTypeAPIKey, Platform: PlatformAnthropic}
	subscription := &UserSubscription{ID: 71, UserID: user.ID, WalletBalanceUSD: &walletBalance}

	identity, err := svc.PrepareGatewayWalletUsageBillingAdmission(ctx, apiKey, user, account, subscription, &ParsedRequest{
		Body: body, Model: "claude-sonnet-4-6", MaxTokens: 128, GroupID: &groupID,
	})
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, "claude-sonnet-4-6", identity.BillingModel)
	require.NotNil(t, identity.Pricing)
	require.NotEmpty(t, identity.AdmissionAttemptID)
	require.Len(t, repo.admissions, 1)
	require.Len(t, repo.dispatched, 1)
	require.Equal(t, repo.dispatched[0].AttemptID, identity.AdmissionAttemptID)
	require.Equal(t, identity.Pricing.Evidence.Hash, repo.admissions[0].PricingHash())
}

func TestGatewayWalletRecordUsageSettlesWithFrozenAttemptAndPricing(t *testing.T) {
	admissionRepo := &usageBillingAdmissionRepoCapture{}
	outboxRepo := &openAIUsageOutboxRepoStub{}
	billing := NewBillingService(&config.Config{}, nil)
	svc := &GatewayService{
		cfg: &config.Config{}, billingService: billing,
		resolver:                  NewModelPricingResolver(nil, billing),
		requireUsageBillingOutbox: true, usageBillingAdmissionRepo: admissionRepo,
		usageBillingOutboxRepo: outboxRepo,
	}
	groupID := int64(22)
	walletBalance := 10.0
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)
	ctx, err := PrepareUsageBillingRequestContext(context.Background())
	require.NoError(t, err)
	group := &Group{ID: groupID, Name: WalletDefaultVIPGroupName, Platform: PlatformAnthropic, Status: StatusActive,
		Hydrated: true, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1}
	apiKey := &APIKey{ID: 11, Key: "sk-wallet-vip", GroupID: &groupID, Group: group}
	user := &User{ID: 22, AllowedGroups: []int64{groupID}}
	account := &Account{ID: 33, Type: AccountTypeAPIKey, Platform: PlatformAnthropic}
	subscription := &UserSubscription{ID: 71, UserID: user.ID, WalletBalanceUSD: &walletBalance}
	identity, err := svc.PrepareGatewayWalletUsageBillingAdmission(ctx, apiKey, user, account, subscription, &ParsedRequest{
		Body: body, Model: "claude-sonnet-4-6", MaxTokens: 128, GroupID: &groupID,
	})
	require.NoError(t, err)

	err = svc.RecordUsage(ctx, &RecordUsageInput{
		Result: &ForwardResult{
			Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 6}, Model: "claude-sonnet-4-6",
			UpstreamModel: "claude-sonnet-4-6", Duration: time.Second, UsageBillingIdentity: identity,
		},
		APIKey: apiKey, User: user, Account: account, Subscription: subscription,
		RequestPayloadHash: HashUsageRequestPayload(body),
	})
	require.NoError(t, err)
	require.Len(t, outboxRepo.envelopes, 1)
	envelope := outboxRepo.envelopes[0]
	require.Equal(t, identity.AdmissionAttemptID, envelope.AdmissionAttemptID())
	require.Equal(t, identity.Pricing.Evidence.Hash, envelope.PricingHash())
	require.Positive(t, envelope.WalletCost())
	require.Zero(t, envelope.SubscriptionCost())
	require.True(t, admissionRepo.admissions[0].MatchesEnvelope(envelope))
}

func TestGatewayWalletUsageBillingAdmissionRejectsUnsupportedAccount(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	svc := &GatewayService{cfg: &config.Config{}, billingService: billing, resolver: NewModelPricingResolver(nil, billing), requireUsageBillingOutbox: true}
	groupID := int64(22)
	walletBalance := 10.0
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":64}`)
	ctx, err := PrepareUsageBillingRequestContext(context.Background())
	require.NoError(t, err)
	_, err = svc.PrepareGatewayWalletUsageBillingAdmission(ctx,
		&APIKey{ID: 11, Key: "sk-wallet-vip", GroupID: &groupID, Group: &Group{ID: groupID}},
		&User{ID: 22},
		&Account{ID: 33, Type: AccountTypeAPIKey, Platform: PlatformOpenAI},
		&UserSubscription{ID: 71, UserID: 22, WalletBalanceUSD: &walletBalance},
		&ParsedRequest{Body: body, Model: "claude-sonnet-4-6", MaxTokens: 64, GroupID: &groupID},
	)
	require.ErrorIs(t, err, ErrUsageBillingLifecycleContractInvalid)
}

func TestGatewayWalletUsageBillingAdmissionSupportsAdditionalGatewayAccounts(t *testing.T) {
	tests := []struct {
		platform string
		model    string
		body     []byte
	}{
		{PlatformGemini, "gemini-3-1-pro", []byte(`{"contents":[{"parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":128}}`)},
		{PlatformAntigravity, "gemini-3-1-pro", []byte(`{"contents":[{"parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":128}}`)},
		{PlatformKiro, "claude-sonnet-4-6", []byte(`{"model":"claude-sonnet-4-6","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)},
		{PlatformCursor, "claude-sonnet-4-6", []byte(`{"model":"claude-sonnet-4-6","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)},
	}
	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			repo := &usageBillingAdmissionRepoCapture{}
			billing := NewBillingService(&config.Config{}, nil)
			svc := &GatewayService{
				cfg: &config.Config{}, billingService: billing,
				resolver:                  NewModelPricingResolver(nil, billing),
				requireUsageBillingOutbox: true, usageBillingAdmissionRepo: repo,
			}
			groupID := int64(22)
			walletBalance := 10.0
			ctx, err := PrepareUsageBillingRequestContext(context.Background())
			require.NoError(t, err)

			identity, err := svc.PrepareGatewayWalletUsageBillingAdmission(ctx,
				&APIKey{ID: 11, Key: "sk-wallet-gateway", GroupID: &groupID, Group: &Group{
					ID: groupID, Name: WalletDefaultVIPGroupName, Platform: tt.platform, Status: StatusActive,
					Hydrated: true, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1,
				}},
				&User{ID: 22, AllowedGroups: []int64{groupID}},
				&Account{ID: 33, Type: AccountTypeAPIKey, Platform: tt.platform},
				&UserSubscription{ID: 71, UserID: 22, WalletBalanceUSD: &walletBalance},
				&ParsedRequest{Body: tt.body, Model: tt.model, MaxTokens: 128, GroupID: &groupID},
			)

			require.NoError(t, err)
			require.NotNil(t, identity)
			require.Equal(t, tt.model, identity.BillingModel)
			require.Len(t, repo.admissions, 1)
			require.Len(t, repo.dispatched, 1)
		})
	}
}

func TestGatewayWalletRecordUsageRequiresAdmissionIdentityWhenOutboxIsRequired(t *testing.T) {
	walletBalance := 10.0
	svc := &GatewayService{requireUsageBillingOutbox: true}

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result:       &ForwardResult{RequestID: "wallet-missing-admission", Model: "claude-sonnet-4-6"},
		APIKey:       &APIKey{ID: 11},
		User:         &User{ID: 22},
		Account:      &Account{ID: 33},
		Subscription: &UserSubscription{ID: 71, WalletBalanceUSD: &walletBalance},
	})

	require.ErrorIs(t, err, ErrUsageBillingLifecycleContractInvalid)
}
