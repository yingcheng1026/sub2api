//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWalletAdmission_ConcurrentRequestsReserveAffordableQuotesWithoutOverbooking(t *testing.T) {
	ctx := context.Background()
	repo, admissionBase, walletID := newWalletAdmissionIntegrationFixture(t, ctx, 25)

	const contenders = 16
	start := make(chan struct{})
	results := make(chan error, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			input := admissionBase
			input.RequestID = fmt.Sprintf("wallet-concurrent-%d-%s", i, uuid.NewString())
			admission, err := service.NewUsageBillingAdmission(input)
			if err == nil {
				err = repo.Admit(ctx, admission)
			}
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	succeeded := 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		default:
			t.Fatalf("unexpected admission result: %v", err)
		}
	}
	require.Equal(t, contenders, succeeded)

	var unresolved int
	var reserved float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(wallet_reserved_usd), 0)
		FROM usage_billing_admissions
		WHERE wallet_subscription_id = $1
		  AND wallet_consumed_at IS NULL
		  AND wallet_released_at IS NULL
	`, walletID).Scan(&unresolved, &reserved))
	require.Equal(t, contenders, unresolved)
	require.InDelta(t, float64(contenders), reserved, 0.000001)
}

func TestWalletAdmission_FinalizedHoldBlocksPastLeaseUntilSettlement(t *testing.T) {
	ctx := context.Background()
	repo, admissionBase, walletID := newWalletAdmissionIntegrationFixture(t, ctx, 1)
	first := admissionBase
	first.RequestID = "wallet-finalized-" + uuid.NewString()
	admission, err := service.NewUsageBillingAdmission(first)
	require.NoError(t, err)
	require.NoError(t, repo.Admit(ctx, admission))
	require.NoError(t, repo.MarkDispatched(ctx, service.UsageBillingAdmissionAttemptRef{
		RequestID: admission.RequestID(), APIKeyID: admission.APIKeyID(),
		OwnerToken: admission.OwnerToken(), AttemptID: admission.AttemptID(),
	}))

	_, err = integrationDB.ExecContext(ctx, `
		UPDATE usage_billing_admissions
		SET state = 'outbox_pending', outbox_pending_at = NOW(),
			wallet_lease_expires_at = NOW() - INTERVAL '1 hour'
		WHERE request_id = $1 AND api_key_id = $2
	`, admission.RequestID(), admission.APIKeyID())
	require.NoError(t, err)

	second := admissionBase
	second.RequestID = "wallet-after-expired-lease-" + uuid.NewString()
	secondAdmission, err := service.NewUsageBillingAdmission(second)
	require.NoError(t, err)
	require.ErrorIs(t, repo.Admit(ctx, secondAdmission), service.ErrWalletInsufficient)

	var unresolved int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM usage_billing_admissions
		WHERE wallet_subscription_id = $1
		  AND wallet_consumed_at IS NULL AND wallet_released_at IS NULL
	`, walletID).Scan(&unresolved))
	require.Equal(t, 1, unresolved)
}

func TestWalletAdmission_EnqueueBeforeDispatchFailsClosed(t *testing.T) {
	ctx := context.Background()
	repo, input, _ := newWalletAdmissionIntegrationFixture(t, ctx, 10)
	input.RequestID = "wallet-undispatched-" + uuid.NewString()
	admission, err := service.NewUsageBillingAdmission(input)
	require.NoError(t, err)
	require.NoError(t, repo.Admit(ctx, admission))

	envelope, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID: input.RequestID, RequestPayloadHash: input.RequestPayloadHash,
		AdmissionAttemptID: input.AttemptID, APIKeyID: input.APIKeyID,
		AuthCacheLocator: input.AuthCacheLocator, UserID: input.UserID, AccountID: input.AccountID,
		SubscriptionID: input.SubscriptionID, GroupID: input.GroupID,
		EffectiveBillingGroupID: input.EffectiveBillingGroupID, AccountType: input.AccountType,
		BillingModel: input.BillingModel, BillingType: input.BillingType,
		PricingSource: input.PricingSource, PricingRevision: input.PricingRevision,
		PricingHash: input.PricingHash, RateMultiplier: input.RateMultiplier, AccountRateMultiplier: 1,
		WalletCost: 0.5,
	})
	require.NoError(t, err)

	_, _, err = repo.Enqueue(ctx, envelope)
	require.ErrorIs(t, err, service.ErrUsageBillingAdmissionLeaseLost)
}

func newWalletAdmissionIntegrationFixture(
	t *testing.T,
	ctx context.Context,
	balance float64,
) (*usageBillingOutboxRepository, service.UsageBillingAdmissionInput, int64) {
	t.Helper()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{
		Email: "wallet-admission-" + uuid.NewString() + "@example.com",
	})
	group := mustGetOrCreateWalletBusinessGroup(t, client,
		service.WalletDefaultOpenAIGroupName, service.PlatformOpenAI, false, 1)
	wallet := mustCreateCreditsWallet(t, client, user.ID, balance)
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		Name:    service.WalletUniversalAPIKeyName,
		Purpose: service.APIKeyPurposeWalletUniversal,
		Key:     "sk-wallet-admission-" + uuid.NewString(),
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "wallet-admission-" + uuid.NewString(),
		Type: service.AccountTypeOAuth,
	})
	mustBindAccountToGroup(t, client, account.ID, group.ID, 1)

	return NewUsageBillingOutboxRepository(integrationDB), service.UsageBillingAdmissionInput{
		APIKeyID: apiKey.ID, AuthCacheLocator: apiKey.KeyHash,
		UserID: user.ID, AccountID: account.ID, SubscriptionID: &wallet.ID,
		GroupID: &group.ID, EffectiveBillingGroupID: &group.ID,
		AccountType: account.Type, BillingType: service.BillingTypeSubscription,
		OwnerToken: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32),
		BillingModel: "gpt-5.6-terra", RequestPayloadHash: strings.Repeat("c", 64),
		PricingSource: service.PricingSourceBuiltinGPT56, PricingRevision: service.GPT56PricingRevision,
		PricingHash: strings.Repeat("d", 64), RateMultiplier: 1, WorstCaseCostUSD: 1,
	}, wallet.ID
}
