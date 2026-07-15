package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingAdmissionCostFamilyCannotMixWalletMonthlyAndBalance(t *testing.T) {
	subscriptionID := int64(71)
	walletAdmission := sql.NullInt64{Int64: subscriptionID, Valid: true}
	monthlyAdmission := sql.NullInt64{}
	frozenSubscription := sql.NullInt64{Int64: subscriptionID, Valid: true}

	require.True(t, usageBillingAdmissionCostFamilyMatches(walletAdmission, service.BillingTypeSubscription, frozenSubscription,
		usageBillingCostFamilyEnvelope(t, service.BillingTypeSubscription, &subscriptionID, 0, 0, 1)))
	require.True(t, usageBillingAdmissionCostFamilyMatches(walletAdmission, service.BillingTypeSubscription, frozenSubscription,
		usageBillingCostFamilyEnvelope(t, service.BillingTypeSubscription, &subscriptionID, 0, 0, 0)))
	require.False(t, usageBillingAdmissionCostFamilyMatches(walletAdmission, service.BillingTypeSubscription, frozenSubscription,
		usageBillingCostFamilyEnvelope(t, service.BillingTypeSubscription, &subscriptionID, 0, 1, 0)))

	require.True(t, usageBillingAdmissionCostFamilyMatches(monthlyAdmission, service.BillingTypeSubscription, frozenSubscription,
		usageBillingCostFamilyEnvelope(t, service.BillingTypeSubscription, &subscriptionID, 0, 1, 0)))
	require.False(t, usageBillingAdmissionCostFamilyMatches(monthlyAdmission, service.BillingTypeSubscription, frozenSubscription,
		usageBillingCostFamilyEnvelope(t, service.BillingTypeSubscription, &subscriptionID, 0, 0, 1)))

	require.True(t, usageBillingAdmissionCostFamilyMatches(monthlyAdmission, service.BillingTypeBalance, sql.NullInt64{},
		usageBillingCostFamilyEnvelope(t, service.BillingTypeBalance, nil, 1, 0, 0)))
}

func usageBillingCostFamilyEnvelope(
	t *testing.T,
	billingType int8,
	subscriptionID *int64,
	balanceCost float64,
	subscriptionCost float64,
	walletCost float64,
) service.UsageBillingEnvelope {
	t.Helper()
	groupID := int64(31)
	envelope, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID: "usage-cost-family", RequestPayloadHash: strings.Repeat("a", 64),
		APIKeyID: 11, UserID: 22, AccountID: 33, SubscriptionID: subscriptionID,
		GroupID: &groupID, EffectiveBillingGroupID: &groupID,
		AccountType: service.AccountTypeOAuth, BillingModel: "gpt-5.6-terra", BillingType: billingType,
		PricingSource: service.PricingSourceBuiltinGPT56, PricingRevision: service.GPT56PricingRevision,
		PricingHash: strings.Repeat("b", 64), RateMultiplier: 1, AccountRateMultiplier: 1,
		BalanceCost: balanceCost, SubscriptionCost: subscriptionCost, WalletCost: walletCost,
	})
	require.NoError(t, err)
	return envelope
}

func TestUsageBillingMarkDispatchedAllowsOwnedFailoverAttempt(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE usage_billing_admissions[\\s\\S]+state IN \\('prepared','dispatched'\\)").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE usage_billing_admission_attempts").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	repo := NewUsageBillingOutboxRepository(db)
	err = repo.MarkDispatched(context.Background(), service.UsageBillingAdmissionAttemptRef{
		RequestID: "usage-failover-dispatch", APIKeyID: 11,
		OwnerToken: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32),
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingAdmissionSettlementRequiresDispatchedFence(t *testing.T) {
	require.False(t, usageBillingAdmissionSettlementStateMatches(
		service.UsageBillingAdmissionStatePrepared,
		service.UsageBillingAttemptStatePrepared,
	))
	require.True(t, usageBillingAdmissionSettlementStateMatches(
		service.UsageBillingAdmissionStateDispatched,
		service.UsageBillingAttemptStateDispatched,
	))
	require.True(t, usageBillingAdmissionSettlementStateMatches(
		service.UsageBillingAdmissionStateOutboxPending,
		service.UsageBillingAttemptStateFinalized,
	))
	require.False(t, usageBillingAdmissionSettlementStateMatches(
		service.UsageBillingAdmissionStateDispatched,
		service.UsageBillingAttemptStatePrepared,
	))
}

func TestUsageBillingAbandonReleasesDispatchedHoldOnlyAfterAllAttemptsFailed(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec("UPDATE usage_billing_admissions[\\s\\S]+state = 'dispatched' AND NOT EXISTS[\\s\\S]+attempt.state IN \\('prepared','dispatched','finalized'\\)").
		WillReturnResult(sqlmock.NewResult(0, 1))

	repo := NewUsageBillingOutboxRepository(db)
	err = repo.Abandon(context.Background(), service.UsageBillingAdmissionAttemptRef{
		RequestID: "usage-failed-attempts", APIKeyID: 11,
		OwnerToken: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32),
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableUsageBillingAdmissionRolloutConsumesExistingAdmissionWithoutBreakingLegacyProducers(t *testing.T) {
	repo := newDurableUsageBillingOutboxInner(nil)
	require.NotNil(t, repo)
	require.True(t, repo.useAdmissionIfPresent)
	require.False(t, repo.requireAdmission, "global enforcement stays off until every producer has pre-dispatch admission")
}

func TestUsageBillingAdmissionExistsDetectsOptionalHold(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT EXISTS").WithArgs("request-1", int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	exists, err := usageBillingAdmissionExists(context.Background(), db, "request-1", 11)
	require.NoError(t, err)
	require.True(t, exists)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingUniversalWalletAuthorizationUsesExactBusinessGroups(t *testing.T) {
	openAIDefault := service.Group{
		ID: 3, Name: service.WalletDefaultOpenAIGroupName, Platform: service.PlatformOpenAI,
		Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard,
	}
	require.True(t, usageBillingUniversalWalletGroupAuthorized(openAIDefault, false))

	vip := service.Group{
		ID: 22, Name: service.WalletDefaultVIPGroupName, Platform: service.PlatformAnthropic,
		Status: service.StatusActive, Hydrated: true, IsExclusive: true, SubscriptionType: service.SubscriptionTypeStandard,
	}
	require.False(t, usageBillingUniversalWalletGroupAuthorized(vip, false))
	require.True(t, usageBillingUniversalWalletGroupAuthorized(vip, true))

	wrongName := openAIDefault
	wrongName.Name = "openai_default"
	require.False(t, usageBillingUniversalWalletGroupAuthorized(wrongName, true))
	wrongPlatform := vip
	wrongPlatform.Platform = service.PlatformOpenAI
	require.False(t, usageBillingUniversalWalletGroupAuthorized(wrongPlatform, true))
	drifted := openAIDefault
	drifted.ClaudeCodeOnly = true
	require.False(t, usageBillingUniversalWalletGroupAuthorized(drifted, true))
	fallbackID := int64(99)
	drifted = vip
	drifted.FallbackGroupID = &fallbackID
	require.False(t, usageBillingUniversalWalletGroupAuthorized(drifted, true))
}

func TestUsageBillingUniversalWalletKeyRequiresPurposeNameAndNullGroup(t *testing.T) {
	require.True(t, validUsageBillingUniversalWalletKeyShape(service.WalletUniversalAPIKeyName, service.APIKeyPurposeWalletUniversal, false))
	require.False(t, validUsageBillingUniversalWalletKeyShape(service.WalletUniversalAPIKeyName, "", false))
	require.False(t, validUsageBillingUniversalWalletKeyShape("renamed", service.APIKeyPurposeWalletUniversal, false))
	require.False(t, validUsageBillingUniversalWalletKeyShape(service.WalletUniversalAPIKeyName, service.APIKeyPurposeWalletUniversal, true))
}

func TestUsageBillingWalletGroupPolicyAppliesToGroupedAndLegacyKeys(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	openAIDefault := service.Group{
		ID: 3, Name: service.WalletDefaultOpenAIGroupName, Platform: service.PlatformOpenAI,
		Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard,
	}
	legacyEnvelope := usageBillingWalletPolicyEnvelope(t, openAIDefault.ID, "")
	require.NoError(t, validateUsageBillingWalletGroupAuthorization(context.Background(), db, legacyEnvelope, openAIDefault, false))

	arbitrary := openAIDefault
	arbitrary.Name = "arbitrary-standard"
	require.ErrorIs(t,
		validateUsageBillingWalletGroupAuthorization(context.Background(), db, legacyEnvelope, arbitrary, false),
		service.ErrUsageBillingCrossTenant,
	)

	vip := service.Group{
		ID: 22, Name: service.WalletDefaultVIPGroupName, Platform: service.PlatformAnthropic,
		Status: service.StatusActive, Hydrated: true, IsExclusive: true, SubscriptionType: service.SubscriptionTypeStandard,
	}
	vipEnvelope := usageBillingWalletPolicyEnvelope(t, vip.ID, "")
	mock.ExpectQuery("SELECT TRUE[\\s\\S]+FROM user_allowed_groups").
		WithArgs(vipEnvelope.UserID(), vip.ID).WillReturnError(sql.ErrNoRows)
	require.ErrorIs(t,
		validateUsageBillingWalletGroupAuthorization(context.Background(), db, vipEnvelope, vip, false),
		service.ErrUsageBillingCrossTenant,
	)

	mock.ExpectQuery("SELECT TRUE[\\s\\S]+FROM user_allowed_groups").
		WithArgs(vipEnvelope.UserID(), vip.ID).
		WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(true))
	require.NoError(t, validateUsageBillingWalletGroupAuthorization(context.Background(), db, vipEnvelope, vip, false))

	frozenEnvelope := usageBillingWalletPolicyEnvelope(t, vip.ID, strings.Repeat("c", 32))
	mock.ExpectQuery("SELECT EXISTS[\\s\\S]+FROM usage_billing_admissions").
		WithArgs(frozenEnvelope.RequestID(), frozenEnvelope.APIKeyID(), frozenEnvelope.AdmissionAttemptID()).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	require.NoError(t, validateUsageBillingWalletGroupAuthorization(context.Background(), db, frozenEnvelope, vip, false))

	spoofedEnvelope := usageBillingWalletPolicyEnvelope(t, vip.ID, strings.Repeat("d", 32))
	mock.ExpectQuery("SELECT EXISTS[\\s\\S]+FROM usage_billing_admissions").
		WithArgs(spoofedEnvelope.RequestID(), spoofedEnvelope.APIKeyID(), spoofedEnvelope.AdmissionAttemptID()).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT TRUE[\\s\\S]+FROM user_allowed_groups").
		WithArgs(spoofedEnvelope.UserID(), vip.ID).WillReturnError(sql.ErrNoRows)
	require.ErrorIs(t,
		validateUsageBillingWalletGroupAuthorization(context.Background(), db, spoofedEnvelope, vip, false),
		service.ErrUsageBillingCrossTenant,
	)
	require.NoError(t, mock.ExpectationsWereMet())
}

func usageBillingWalletPolicyEnvelope(t *testing.T, groupID int64, attemptID string) service.UsageBillingEnvelope {
	t.Helper()
	subscriptionID := int64(71)
	envelope, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID: "wallet-policy", RequestPayloadHash: strings.Repeat("a", 64), AdmissionAttemptID: attemptID,
		APIKeyID: 11, UserID: 22, AccountID: 33, SubscriptionID: &subscriptionID,
		GroupID: &groupID, EffectiveBillingGroupID: &groupID,
		AccountType: service.AccountTypeOAuth, BillingModel: "gpt-5.6-terra", BillingType: service.BillingTypeSubscription,
		PricingSource: service.PricingSourceBuiltinGPT56, PricingRevision: service.GPT56PricingRevision,
		PricingHash: strings.Repeat("b", 64), RateMultiplier: 1, AccountRateMultiplier: 1, WalletCost: 1,
	})
	require.NoError(t, err)
	return envelope
}

func repositoryUsageBillingAdmission(t *testing.T, worstCaseCostUSD float64) service.UsageBillingAdmission {
	t.Helper()
	groupID := int64(31)
	subscriptionID := int64(71)
	admission, err := service.NewUsageBillingAdmission(service.UsageBillingAdmissionInput{
		RequestID:               "usage-admission-repository-quote",
		APIKeyID:                11,
		AuthCacheLocator:        strings.Repeat("a", 64),
		UserID:                  22,
		SubscriptionID:          &subscriptionID,
		GroupID:                 &groupID,
		EffectiveBillingGroupID: &groupID,
		BillingType:             service.BillingTypeSubscription,
		OwnerToken:              strings.Repeat("b", 32),
		AttemptID:               strings.Repeat("c", 32),
		AccountID:               33,
		AccountType:             service.AccountTypeOAuth,
		BillingModel:            "gpt-5.6-terra",
		RequestPayloadHash:      strings.Repeat("d", 64),
		PricingSource:           service.PricingSourceBuiltinGPT56,
		PricingRevision:         service.GPT56PricingRevision,
		PricingHash:             strings.Repeat("e", 64),
		RateMultiplier:          1.25,
		WorstCaseCostUSD:        worstCaseCostUSD,
	})
	require.NoError(t, err)
	return admission
}

func TestUsageBillingAdmissionValidationEnvelopeCopiesFrozenQuote(t *testing.T) {
	admission := repositoryUsageBillingAdmission(t, 0.75)
	envelope, err := usageBillingAdmissionValidationEnvelope(admission)
	require.NoError(t, err)
	require.True(t, admission.MatchesEnvelopeBase(envelope))

	raw, err := envelope.MarshalJSON()
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	require.Equal(t, admission.RequestPayloadHash(), payload["request_payload_hash"])
	require.Equal(t, admission.BillingModel(), payload["billing_model"])
	require.Equal(t, admission.PricingSource(), payload["pricing_source"])
	require.Equal(t, admission.PricingRevision(), payload["pricing_revision"])
	require.Equal(t, admission.PricingHash(), payload["pricing_hash"])
	require.Equal(t, admission.RateMultiplier(), payload["rate_multiplier"])
}

func TestPrepareUsageBillingWalletHoldReservesOnlyWorstCaseCost(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	admission := repositoryUsageBillingAdmission(t, 0.75)
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(wallet_reserved_usd\\), 0\\)").
		WithArgs(int64(71), admission.RequestID(), admission.APIKeyID()).
		WillReturnRows(sqlmock.NewRows([]string{"outstanding_usd"}).AddRow(0))

	walletID, reservedUSD, _, err := prepareUsageBillingWalletHold(
		context.Background(), tx, admission, &usageBillingAdmissionWallet{id: 71, balance: 25}, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, walletID)
	require.Equal(t, int64(71), *walletID)
	require.InDelta(t, 0.75, reservedUSD, 0.0000001)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPrepareUsageBillingWalletHoldRejectsUnaffordableQuote(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	admission := repositoryUsageBillingAdmission(t, 25.01)
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(wallet_reserved_usd\\), 0\\)").
		WithArgs(int64(71), admission.RequestID(), admission.APIKeyID()).
		WillReturnRows(sqlmock.NewRows([]string{"outstanding_usd"}).AddRow(0))
	_, _, _, err = prepareUsageBillingWalletHold(
		context.Background(), tx, admission, &usageBillingAdmissionWallet{id: 71, balance: 25}, nil,
	)
	require.ErrorIs(t, err, service.ErrWalletInsufficient)
	require.NoError(t, mock.ExpectationsWereMet())
}
