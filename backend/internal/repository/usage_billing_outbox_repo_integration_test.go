//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type usageBillingOutboxBindingFixture struct {
	userID           int64
	apiKeyID         int64
	authCacheLocator string
	accountID        int64
	groupID          int64
}

func newUsageBillingOutboxBindingFixture(t *testing.T) usageBillingOutboxBindingFixture {
	t.Helper()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("outbox-fixture-%s@example.com", uuid.NewString()), Balance: 100})
	group := mustCreateGroup(t, client, &service.Group{Name: "outbox-fixture-" + uuid.NewString()})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-outbox-fixture-" + uuid.NewString()})
	account := mustCreateAccount(t, client, &service.Account{Name: "outbox-fixture-" + uuid.NewString(), Type: service.AccountTypeAPIKey})
	mustBindAccountToGroup(t, client, account.ID, group.ID, 1)
	return usageBillingOutboxBindingFixture{
		userID: user.ID, apiKeyID: apiKey.ID, authCacheLocator: service.APIKeyAuthCacheLocator(apiKey.Key),
		accountID: account.ID, groupID: group.ID,
	}
}

func newOutboxEnvelope(t *testing.T, fixture usageBillingOutboxBindingFixture, requestID string, balanceCost float64) service.UsageBillingEnvelope {
	t.Helper()
	envelope, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID:             requestID,
		APIKeyID:              fixture.apiKeyID,
		AuthCacheLocator:      fixture.authCacheLocator,
		UserID:                fixture.userID,
		AccountID:             fixture.accountID,
		GroupID:               &fixture.groupID,
		AccountType:           service.AccountTypeAPIKey,
		BillingModel:          "gpt-5.6-sol",
		BillingType:           service.BillingTypeBalance,
		InputTokens:           100,
		OutputTokens:          20,
		PricingSource:         service.PricingSourceBuiltinGPT56,
		PricingRevision:       service.GPT56PricingRevision,
		PricingHash:           "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RateMultiplier:        1,
		AccountRateMultiplier: 1,
		BalanceCost:           balanceCost,
		APIKeyQuotaCost:       balanceCost,
	})
	require.NoError(t, err)
	return envelope
}

func TestUsageBillingOutboxRepository_EnqueueIdempotencyAndConflict(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE usage_billing_outbox RESTART IDENTITY")
	repo := NewUsageBillingOutboxRepository(integrationDB)
	fixture := newUsageBillingOutboxBindingFixture(t)

	requestID := "outbox-enqueue-" + uuid.NewString()
	firstEnvelope := newOutboxEnvelope(t, fixture, requestID, 1.25)
	first, inserted, err := repo.Enqueue(ctx, firstEnvelope)
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, service.UsageBillingOutboxStatusPending, first.Status)

	duplicate, inserted, err := repo.Enqueue(ctx, firstEnvelope)
	require.NoError(t, err)
	require.False(t, inserted)
	require.Equal(t, first.ID, duplicate.ID)

	conflictingEnvelope := newOutboxEnvelope(t, fixture, requestID, 2.5)
	_, _, err = repo.Enqueue(ctx, conflictingEnvelope)
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)

	_, err = integrationDB.ExecContext(ctx, "UPDATE api_keys SET deleted_at = NOW() WHERE id = $1", fixture.apiKeyID)
	require.NoError(t, err)
	duplicateAfterDelete, inserted, err := repo.Enqueue(ctx, firstEnvelope)
	require.NoError(t, err)
	require.False(t, inserted)
	require.Equal(t, first.ID, duplicateAfterDelete.ID, "an already durable event must remain idempotent after target soft-delete")
	_, _, err = repo.Enqueue(ctx, conflictingEnvelope)
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)

	var storedCost float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT (envelope->>'balance_cost')::double precision
		FROM usage_billing_outbox WHERE id = $1
	`, first.ID).Scan(&storedCost))
	require.InDelta(t, 1.25, storedCost, 0.000001, "conflict must never overwrite the original envelope")
}

func TestUsageBillingOutboxRepository_ClaimLeaseAndStaleOwner(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE usage_billing_outbox RESTART IDENTITY")
	repo := NewUsageBillingOutboxRepository(integrationDB)
	fixture := newUsageBillingOutboxBindingFixture(t)

	first, _, err := repo.Enqueue(ctx, newOutboxEnvelope(t, fixture, "outbox-claim-1-"+uuid.NewString(), 1))
	require.NoError(t, err)
	_, _, err = repo.Enqueue(ctx, newOutboxEnvelope(t, fixture, "outbox-claim-2-"+uuid.NewString(), 1))
	require.NoError(t, err)

	claimedByOne, err := repo.Claim(ctx, "worker-one", 1, service.UsageBillingOutboxLeaseDuration)
	require.NoError(t, err)
	require.Len(t, claimedByOne, 1)
	claimedByTwo, err := repo.Claim(ctx, "worker-two", 1, service.UsageBillingOutboxLeaseDuration)
	require.NoError(t, err)
	require.Len(t, claimedByTwo, 1)
	require.NotEqual(t, claimedByOne[0].ID, claimedByTwo[0].ID, "leased rows must not be claimed by another worker")

	_, _, err = repo.Enqueue(ctx, newOutboxEnvelope(t, fixture, "outbox-lease-expiry-"+uuid.NewString(), 1))
	require.NoError(t, err)
	third, err := repo.Claim(ctx, "worker-old", 1, service.UsageBillingOutboxLeaseDuration)
	require.NoError(t, err)
	require.Len(t, third, 1)

	_, err = integrationDB.ExecContext(ctx, `
		UPDATE usage_billing_outbox SET locked_at = NOW() - INTERVAL '31 seconds' WHERE id = $1
	`, third[0].ID)
	require.NoError(t, err)
	reclaimed, err := repo.Claim(ctx, "worker-new", 1, service.UsageBillingOutboxLeaseDuration)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	require.Equal(t, third[0].ID, reclaimed[0].ID)

	err = repo.Complete(ctx, reclaimed[0].ID, "worker-old", third[0].LeaseToken, service.UsageBillingOutboxResultApplied)
	require.ErrorIs(t, err, service.ErrUsageBillingOutboxLeaseLost)
	require.NoError(t, repo.Complete(ctx, reclaimed[0].ID, "worker-new", reclaimed[0].LeaseToken, service.UsageBillingOutboxResultApplied))

	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT status FROM usage_billing_outbox WHERE id = $1", reclaimed[0].ID).Scan(&status))
	require.Equal(t, service.UsageBillingOutboxStatusCompleted, status)
	require.NotZero(t, first.ID)
}

func TestUsageBillingOutboxRepository_RetryAndDeadLetterLifecycle(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE usage_billing_outbox RESTART IDENTITY")
	repo := NewUsageBillingOutboxRepository(integrationDB)
	fixture := newUsageBillingOutboxBindingFixture(t)

	_, _, err := repo.Enqueue(ctx, newOutboxEnvelope(t, fixture, "outbox-retry-"+uuid.NewString(), 1))
	require.NoError(t, err)
	claimed, err := repo.Claim(ctx, "retry-worker", 1, service.UsageBillingOutboxLeaseDuration)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	retryAt := time.Now().UTC().Add(time.Minute)
	require.NoError(t, repo.Retry(ctx, claimed[0].ID, "retry-worker", claimed[0].LeaseToken, retryAt, "db_unavailable", "temporary failure"))

	none, err := repo.Claim(ctx, "other-worker", 1, service.UsageBillingOutboxLeaseDuration)
	require.NoError(t, err)
	require.Empty(t, none)
	_, err = integrationDB.ExecContext(ctx, "UPDATE usage_billing_outbox SET available_at = NOW() - INTERVAL '1 second' WHERE id = $1", claimed[0].ID)
	require.NoError(t, err)

	retried, err := repo.Claim(ctx, "dead-worker", 1, service.UsageBillingOutboxLeaseDuration)
	require.NoError(t, err)
	require.Len(t, retried, 1)
	require.Equal(t, int16(2), retried[0].AttemptCount)
	require.NoError(t, repo.DeadLetter(ctx, retried[0].ID, "dead-worker", retried[0].LeaseToken, "invalid_envelope", "poison"))

	var status, code string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT status, last_error_code FROM usage_billing_outbox WHERE id = $1
	`, retried[0].ID).Scan(&status, &code))
	require.Equal(t, service.UsageBillingOutboxStatusDeadLetter, status)
	require.Equal(t, "invalid_envelope", code)
}

func TestUsageBillingOutboxRepository_EnqueueRejectsCrossTenantAndInvalidBillingMode(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingOutboxRepository(integrationDB)

	userOne := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("outbox-owner-one-%s@example.com", uuid.NewString())})
	userTwo := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("outbox-owner-two-%s@example.com", uuid.NewString())})
	apiKeyOne := mustCreateApiKey(t, client, &service.APIKey{UserID: userOne.ID, Key: "sk-outbox-" + uuid.NewString()})
	account := mustCreateAccount(t, client, &service.Account{Name: "outbox-account-" + uuid.NewString(), Type: service.AccountTypeAPIKey})
	group := mustCreateGroup(t, client, &service.Group{Name: "outbox-group-" + uuid.NewString(), SubscriptionType: service.SubscriptionTypeSubscription})
	apiKeyOne.GroupID = &group.ID
	_, err := integrationDB.ExecContext(ctx, "UPDATE api_keys SET group_id = $1 WHERE id = $2", group.ID, apiKeyOne.ID)
	require.NoError(t, err)
	mustBindAccountToGroup(t, client, account.ID, group.ID, 1)
	subscriptionTwo := mustCreateSubscription(t, client, &service.UserSubscription{UserID: userTwo.ID, GroupID: &group.ID})

	fixture := usageBillingOutboxBindingFixture{
		userID: userOne.ID, apiKeyID: apiKeyOne.ID, authCacheLocator: service.APIKeyAuthCacheLocator(apiKeyOne.Key),
		accountID: account.ID, groupID: group.ID,
	}
	valid := newOutboxEnvelope(t, fixture, "outbox-owner-valid-"+uuid.NewString(), 1)
	_, _, err = repo.Enqueue(ctx, valid)
	require.NoError(t, err)

	wrongLocatorFixture := fixture
	wrongLocatorFixture.authCacheLocator = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	_, _, err = repo.Enqueue(ctx, newOutboxEnvelope(t, wrongLocatorFixture, "outbox-owner-locator-"+uuid.NewString(), 1))
	require.ErrorIs(t, err, service.ErrUsageBillingCrossTenant)

	wrongOwnerFixture := fixture
	wrongOwnerFixture.userID = userTwo.ID
	wrongKeyOwner := newOutboxEnvelope(t, wrongOwnerFixture, "outbox-owner-key-"+uuid.NewString(), 1)
	_, _, err = repo.Enqueue(ctx, wrongKeyOwner)
	require.ErrorIs(t, err, service.ErrUsageBillingCrossTenant)

	subscriptionID := subscriptionTwo.ID
	wrongSubscriptionOwner, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID:             "outbox-owner-sub-" + uuid.NewString(),
		APIKeyID:              apiKeyOne.ID,
		UserID:                userOne.ID,
		AccountID:             account.ID,
		SubscriptionID:        &subscriptionID,
		GroupID:               &group.ID,
		AccountType:           service.AccountTypeAPIKey,
		BillingModel:          "gpt-5.6-sol",
		BillingType:           service.BillingTypeSubscription,
		PricingSource:         service.PricingSourceBuiltinGPT56,
		PricingRevision:       service.GPT56PricingRevision,
		PricingHash:           "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		RateMultiplier:        1,
		AccountRateMultiplier: 1,
		SubscriptionCost:      1,
	})
	require.NoError(t, err)
	_, _, err = repo.Enqueue(ctx, wrongSubscriptionOwner)
	require.ErrorIs(t, err, service.ErrUsageBillingCrossTenant)

	uncoveredAnchorGroup := mustCreateGroup(t, client, &service.Group{
		Name:             "outbox-uncovered-anchor-" + uuid.NewString(),
		SubscriptionType: service.SubscriptionTypeSubscription,
	})
	uncoveredSubscription := mustCreateSubscription(t, client, &service.UserSubscription{UserID: userOne.ID, GroupID: &uncoveredAnchorGroup.ID})
	uncoveredSubscriptionEnvelope, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID:               "outbox-uncovered-sub-" + uuid.NewString(),
		APIKeyID:                apiKeyOne.ID,
		UserID:                  userOne.ID,
		AccountID:               account.ID,
		SubscriptionID:          &uncoveredSubscription.ID,
		GroupID:                 &group.ID,
		EffectiveBillingGroupID: &uncoveredAnchorGroup.ID,
		AccountType:             service.AccountTypeAPIKey,
		BillingModel:            "gpt-5.6-sol",
		BillingType:             service.BillingTypeSubscription,
		PricingSource:           service.PricingSourceBuiltinGPT56,
		PricingRevision:         service.GPT56PricingRevision,
		PricingHash:             "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		RateMultiplier:          1,
		AccountRateMultiplier:   1,
		SubscriptionCost:        1,
	})
	require.NoError(t, err)
	_, _, err = repo.Enqueue(ctx, uncoveredSubscriptionEnvelope)
	require.ErrorIs(t, err, service.ErrUsageBillingCrossTenant)

	otherGroup := mustCreateGroup(t, client, &service.Group{Name: "outbox-other-group-" + uuid.NewString()})
	wrongGroupFixture := fixture
	wrongGroupFixture.groupID = otherGroup.ID
	_, _, err = repo.Enqueue(ctx, newOutboxEnvelope(t, wrongGroupFixture, "outbox-owner-group-"+uuid.NewString(), 1))
	require.ErrorIs(t, err, service.ErrUsageBillingCrossTenant)

	unboundAccount := mustCreateAccount(t, client, &service.Account{Name: "outbox-unbound-" + uuid.NewString(), Type: service.AccountTypeAPIKey})
	unboundFixture := fixture
	unboundFixture.accountID = unboundAccount.ID
	_, _, err = repo.Enqueue(ctx, newOutboxEnvelope(t, unboundFixture, "outbox-owner-account-group-"+uuid.NewString(), 1))
	require.ErrorIs(t, err, service.ErrUsageBillingCrossTenant)

	wrongTypeAccount := mustCreateAccount(t, client, &service.Account{Name: "outbox-wrong-type-" + uuid.NewString(), Type: service.AccountTypeOAuth})
	mustBindAccountToGroup(t, client, wrongTypeAccount.ID, group.ID, 1)
	wrongTypeFixture := fixture
	wrongTypeFixture.accountID = wrongTypeAccount.ID
	_, _, err = repo.Enqueue(ctx, newOutboxEnvelope(t, wrongTypeFixture, "outbox-owner-account-type-"+uuid.NewString(), 1))
	require.ErrorIs(t, err, service.ErrUsageBillingCrossTenant)

	monthly := mustCreateSubscription(t, client, &service.UserSubscription{UserID: userOne.ID, GroupID: &group.ID})
	monthlyID := monthly.ID
	walletCostOnMonthly, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID: "outbox-wallet-on-monthly-" + uuid.NewString(), APIKeyID: apiKeyOne.ID,
		UserID: userOne.ID, AccountID: account.ID, SubscriptionID: &monthlyID, GroupID: &group.ID,
		AccountType: service.AccountTypeAPIKey, BillingModel: "gpt-5.6-sol", BillingType: service.BillingTypeSubscription,
		PricingSource: service.PricingSourceBuiltinGPT56, PricingRevision: service.GPT56PricingRevision,
		PricingHash:    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		RateMultiplier: 1, AccountRateMultiplier: 1, WalletCost: 1,
	})
	require.NoError(t, err)
	_, _, err = repo.Enqueue(ctx, walletCostOnMonthly)
	require.ErrorIs(t, err, service.ErrUsageBillingCrossTenant)

	now := time.Now().UTC()
	wallet, err := client.UserSubscription.Create().
		SetUserID(userOne.ID).
		SetStartsAt(now.Add(-time.Hour)).
		SetExpiresAt(now.Add(time.Hour)).
		SetStatus(service.SubscriptionStatusActive).
		SetWalletBalanceUsd(10).
		SetWalletInitialUsd(10).
		SetAssignedAt(now).
		Save(ctx)
	require.NoError(t, err)
	walletID := wallet.ID
	monthlyCostOnWallet, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID: "outbox-monthly-on-wallet-" + uuid.NewString(), APIKeyID: apiKeyOne.ID,
		UserID: userOne.ID, AccountID: account.ID, SubscriptionID: &walletID, GroupID: &group.ID,
		AccountType: service.AccountTypeAPIKey, BillingModel: "gpt-5.6-sol", BillingType: service.BillingTypeSubscription,
		PricingSource: service.PricingSourceBuiltinGPT56, PricingRevision: service.GPT56PricingRevision,
		PricingHash:    "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		RateMultiplier: 1, AccountRateMultiplier: 1, SubscriptionCost: 1,
	})
	require.NoError(t, err)
	_, _, err = repo.Enqueue(ctx, monthlyCostOnWallet)
	require.ErrorIs(t, err, service.ErrUsageBillingCrossTenant)

	randomUnboundKey := mustCreateApiKey(t, client, &service.APIKey{UserID: userOne.ID, Name: "not-a-wallet-key", Key: "sk-outbox-unbound-" + uuid.NewString()})
	randomKeyWalletEnvelope, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID: "outbox-random-unbound-wallet-" + uuid.NewString(), APIKeyID: randomUnboundKey.ID,
		UserID: userOne.ID, AccountID: account.ID, SubscriptionID: &walletID, GroupID: &group.ID,
		AccountType: service.AccountTypeAPIKey, BillingModel: "gpt-5.6-sol", BillingType: service.BillingTypeSubscription,
		PricingSource: service.PricingSourceBuiltinGPT56, PricingRevision: service.GPT56PricingRevision,
		PricingHash:    "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		RateMultiplier: 1, AccountRateMultiplier: 1, WalletCost: 1,
	})
	require.NoError(t, err)
	_, _, err = repo.Enqueue(ctx, randomKeyWalletEnvelope)
	require.ErrorIs(t, err, service.ErrUsageBillingCrossTenant)

	universalKey := mustCreateApiKey(t, client, &service.APIKey{UserID: userOne.ID, Name: service.WalletUniversalAPIKeyName, Key: "sk-outbox-universal-" + uuid.NewString()})
	universalKeyMonthlyEnvelope, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID: "outbox-universal-monthly-" + uuid.NewString(), APIKeyID: universalKey.ID,
		UserID: userOne.ID, AccountID: account.ID, SubscriptionID: &monthlyID, GroupID: &group.ID,
		AccountType: service.AccountTypeAPIKey, BillingModel: "gpt-5.6-sol", BillingType: service.BillingTypeSubscription,
		PricingSource: service.PricingSourceBuiltinGPT56, PricingRevision: service.GPT56PricingRevision,
		PricingHash:    "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		RateMultiplier: 1, AccountRateMultiplier: 1, SubscriptionCost: 1,
	})
	require.NoError(t, err)
	_, _, err = repo.Enqueue(ctx, universalKeyMonthlyEnvelope)
	require.ErrorIs(t, err, service.ErrUsageBillingCrossTenant)
}

func TestUsageBillingOutboxRepository_FinalAttemptCanBeReclaimedWithFencingToken(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE usage_billing_outbox RESTART IDENTITY")
	repo := NewUsageBillingOutboxRepository(integrationDB)
	fixture := newUsageBillingOutboxBindingFixture(t)
	event, _, err := repo.Enqueue(ctx, newOutboxEnvelope(t, fixture, "outbox-final-attempt-"+uuid.NewString(), 1))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `
		UPDATE usage_billing_outbox
		SET attempt_count = max_attempts - 1
		WHERE id = $1
	`, event.ID)
	require.NoError(t, err)

	firstClaim, err := repo.Claim(ctx, "same-worker", 1, service.UsageBillingOutboxLeaseDuration)
	require.NoError(t, err)
	require.Len(t, firstClaim, 1)
	require.Equal(t, firstClaim[0].MaxAttempts, firstClaim[0].AttemptCount)
	_, err = integrationDB.ExecContext(ctx, "UPDATE usage_billing_outbox SET locked_at = NOW() - INTERVAL '31 seconds' WHERE id = $1", event.ID)
	require.NoError(t, err)

	reclaimed, err := repo.Claim(ctx, "same-worker", 1, service.UsageBillingOutboxLeaseDuration)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	require.NotEqual(t, firstClaim[0].LeaseToken, reclaimed[0].LeaseToken)
	require.ErrorIs(t, repo.Complete(ctx, event.ID, "same-worker", firstClaim[0].LeaseToken, service.UsageBillingOutboxResultApplied), service.ErrUsageBillingOutboxLeaseLost)
	require.NoError(t, repo.Complete(ctx, event.ID, "same-worker", reclaimed[0].LeaseToken, service.UsageBillingOutboxResultApplied))
}
