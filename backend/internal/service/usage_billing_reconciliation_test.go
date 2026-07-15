package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type usageBillingReconciliationRepoStub struct {
	items      []UsageBillingReconciliationCase
	resolved   *UsageBillingReconciliationResolveInput
	listErr    error
	resolveErr error
}

func (s *usageBillingReconciliationRepoStub) ListUsageBillingReconciliationCases(
	context.Context,
	int,
) ([]UsageBillingReconciliationCase, error) {
	return s.items, s.listErr
}

func (s *usageBillingReconciliationRepoStub) ResolveUsageBillingReconciliation(
	_ context.Context,
	input UsageBillingReconciliationResolveInput,
) error {
	cloned := input
	s.resolved = &cloned
	return s.resolveErr
}

func TestUsageBillingReconciliationServiceRequiresAuditableEvidence(t *testing.T) {
	repo := &usageBillingReconciliationRepoStub{}
	svc := NewUsageBillingReconciliationService(repo)

	err := svc.Resolve(context.Background(), UsageBillingReconciliationResolveInput{
		RequestID: "req-1", APIKeyID: 2, OperatorID: 3,
		Action: UsageBillingReconciliationActionReleaseUndelivered,
	})

	require.ErrorIs(t, err, ErrUsageBillingReconciliationInvalid)
	require.Nil(t, repo.resolved)
}

func TestUsageBillingReconciliationServiceNormalizesEvidenceAndDispatchesAction(t *testing.T) {
	repo := &usageBillingReconciliationRepoStub{}
	svc := NewUsageBillingReconciliationService(repo)

	err := svc.Resolve(context.Background(), UsageBillingReconciliationResolveInput{
		RequestID: " req-1 ", APIKeyID: 2, OperatorID: 3,
		Action:          UsageBillingReconciliationActionSettleDelivered,
		AttemptID:       " ABCDEF0123456789ABCDEF0123456789 ",
		BillingEnvelope: reconciliationEnvelopeJSON(t, "req-1", 2, "abcdef0123456789abcdef0123456789"),
		EvidenceRef:     " incident:HFC-2026-0712 ",
	})

	require.NoError(t, err)
	require.NotNil(t, repo.resolved)
	require.Equal(t, "req-1", repo.resolved.RequestID)
	require.Equal(t, "abcdef0123456789abcdef0123456789", repo.resolved.AttemptID)
	require.Equal(t, "incident:HFC-2026-0712", repo.resolved.EvidenceRef)
	require.Equal(t, 0.75, repo.resolved.Envelope.WalletCost())
}

func TestUsageBillingReconciliationServiceRejectsInvalidActionShape(t *testing.T) {
	svc := NewUsageBillingReconciliationService(&usageBillingReconciliationRepoStub{})

	err := svc.Resolve(context.Background(), UsageBillingReconciliationResolveInput{
		RequestID: "req-1", APIKeyID: 2, OperatorID: 3,
		Action:      UsageBillingReconciliationActionSettleDelivered,
		AttemptID:   "short",
		EvidenceRef: "incident:HFC-2026-0712",
	})

	require.ErrorIs(t, err, ErrUsageBillingReconciliationInvalid)
	require.Equal(t, 400, infraerrors.Code(err))
}

func TestUsageBillingReconciliationServiceRejectsDeliveredSettlementWithoutEnvelope(t *testing.T) {
	svc := NewUsageBillingReconciliationService(&usageBillingReconciliationRepoStub{})

	err := svc.Resolve(context.Background(), UsageBillingReconciliationResolveInput{
		RequestID: "req-1", APIKeyID: 2, OperatorID: 3,
		Action:      UsageBillingReconciliationActionSettleDelivered,
		AttemptID:   "abcdef0123456789abcdef0123456789",
		EvidenceRef: "incident:HFC-2026-0712",
	})

	require.ErrorIs(t, err, ErrUsageBillingReconciliationInvalid)
}

func TestUsageBillingReconciliationServiceRejectsZeroCostDeliveredSettlement(t *testing.T) {
	repo := &usageBillingReconciliationRepoStub{}
	svc := NewUsageBillingReconciliationService(repo)

	err := svc.Resolve(context.Background(), UsageBillingReconciliationResolveInput{
		RequestID: "req-1", APIKeyID: 2, OperatorID: 3,
		Action:          UsageBillingReconciliationActionSettleDelivered,
		AttemptID:       "abcdef0123456789abcdef0123456789",
		BillingEnvelope: reconciliationEnvelopeJSONWithWalletCost(t, "req-1", 2, "abcdef0123456789abcdef0123456789", 0),
		EvidenceRef:     "incident:HFC-2026-0712",
	})

	require.ErrorIs(t, err, ErrUsageBillingReconciliationInvalid)
	require.Nil(t, repo.resolved)
}

func TestUsageBillingReconciliationServiceRejectsInflatedSecondaryCosts(t *testing.T) {
	for _, testCase := range []struct {
		name             string
		apiKeyQuotaCost  float64
		accountQuotaCost float64
	}{
		{name: "api key quota", apiKeyQuotaCost: 99, accountQuotaCost: 0.66},
		{name: "account quota", apiKeyQuotaCost: 0.75, accountQuotaCost: 99},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo := &usageBillingReconciliationRepoStub{}
			svc := NewUsageBillingReconciliationService(repo)

			err := svc.Resolve(context.Background(), UsageBillingReconciliationResolveInput{
				RequestID: "req-1", APIKeyID: 2, OperatorID: 3,
				Action:    UsageBillingReconciliationActionSettleDelivered,
				AttemptID: "abcdef0123456789abcdef0123456789",
				BillingEnvelope: reconciliationEnvelopeJSONWithCosts(
					t, "req-1", 2, "abcdef0123456789abcdef0123456789",
					0.75, testCase.apiKeyQuotaCost, 0.75, testCase.accountQuotaCost,
				),
				EvidenceRef: "incident:HFC-2026-0712",
			})

			require.ErrorIs(t, err, ErrUsageBillingReconciliationInvalid)
			require.Nil(t, repo.resolved)
		})
	}
}

func TestUsageBillingReconciliationServiceFailsClosedWhenUnavailable(t *testing.T) {
	svc := NewUsageBillingReconciliationService(nil)

	_, err := svc.List(context.Background(), 50)
	require.ErrorIs(t, err, ErrUsageBillingReconciliationUnavailable)
	require.Equal(t, 503, infraerrors.Code(err))
}

func reconciliationEnvelopeJSON(t *testing.T, requestID string, apiKeyID int64, attemptID string) []byte {
	return reconciliationEnvelopeJSONWithWalletCost(t, requestID, apiKeyID, attemptID, 0.75)
}

func reconciliationEnvelopeJSONWithWalletCost(
	t *testing.T,
	requestID string,
	apiKeyID int64,
	attemptID string,
	walletCost float64,
) []byte {
	totalCost := walletCost / 1.25
	accountQuotaCost := totalCost * 1.1
	return reconciliationEnvelopeJSONWithCosts(
		t, requestID, apiKeyID, attemptID,
		walletCost, walletCost, walletCost, accountQuotaCost,
	)
}

func reconciliationEnvelopeJSONWithCosts(
	t *testing.T,
	requestID string,
	apiKeyID int64,
	attemptID string,
	walletCost float64,
	apiKeyQuotaCost float64,
	apiKeyRateLimitCost float64,
	accountQuotaCost float64,
) []byte {
	t.Helper()
	input := validUsageBillingEnvelopeInput()
	walletID := int64(71)
	input.RequestID = requestID
	input.APIKeyID = apiKeyID
	input.AdmissionAttemptID = attemptID
	input.SubscriptionID = &walletID
	input.BillingType = BillingTypeSubscription
	input.BalanceCost = 0
	input.SubscriptionCost = 0
	input.WalletCost = walletCost
	input.APIKeyQuotaCost = apiKeyQuotaCost
	input.APIKeyRateLimitCost = apiKeyRateLimitCost
	input.AccountQuotaCost = accountQuotaCost
	input.TotalCost = walletCost / input.RateMultiplier
	input.ActualCost = walletCost
	envelope, err := NewUsageBillingEnvelope(input)
	require.NoError(t, err)
	raw, err := envelope.MarshalJSON()
	require.NoError(t, err)
	return raw
}
