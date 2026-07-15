//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestWalletUnknownDeliveryReleaseGuard(t *testing.T) {
	t.Run("ever-dispatched hold remains frozen", func(t *testing.T) {
		ctx := context.Background()
		repo, input, _ := newWalletAdmissionIntegrationFixture(t, ctx, 10)
		input.RequestID = "wallet-unknown-delivery-" + uuid.NewString()
		admission, err := service.NewUsageBillingAdmission(input)
		require.NoError(t, err)
		require.NoError(t, repo.Admit(ctx, admission))
		ref := service.UsageBillingAdmissionAttemptRef{
			RequestID: admission.RequestID(), APIKeyID: admission.APIKeyID(),
			OwnerToken: admission.OwnerToken(), AttemptID: admission.AttemptID(),
		}
		require.NoError(t, repo.MarkDispatched(ctx, ref))
		require.NoError(t, repo.MarkOrphaned(ctx, ref))
		require.NoError(t, repo.MarkAttemptFailed(ctx, ref), "current failed state must not erase dispatch history")
		_, err = integrationDB.ExecContext(ctx, `
			UPDATE usage_billing_admission_attempts
			SET dispatched_at = NULL
			WHERE request_id = $1 AND api_key_id = $2 AND attempt_id = $3
		`, admission.RequestID(), admission.APIKeyID(), admission.AttemptID())
		require.NoError(t, err, "simulate incomplete historical attempt dispatch metadata")
		_, err = integrationDB.ExecContext(ctx, `
			UPDATE usage_billing_admissions
			SET state = 'reconcile', reconcile_started_at = NOW(), updated_at = NOW()
			WHERE request_id = $1 AND api_key_id = $2
		`, admission.RequestID(), admission.APIKeyID())
		require.NoError(t, err)

		_, err = integrationDB.ExecContext(ctx, `
			UPDATE usage_billing_admissions
			SET state = 'abandoned', abandoned_at = NOW(), wallet_released_at = NOW(), updated_at = NOW()
			WHERE request_id = $1 AND api_key_id = $2
		`, admission.RequestID(), admission.APIKeyID())
		requirePostgresConstraint(t, err, "hfc_wallet_unknown_delivery_release")

		var state string
		var released bool
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			SELECT state, wallet_released_at IS NOT NULL
			FROM usage_billing_admissions
			WHERE request_id = $1 AND api_key_id = $2
		`, admission.RequestID(), admission.APIKeyID()).Scan(&state, &released))
		require.Equal(t, service.UsageBillingAdmissionStateReconcile, state)
		require.False(t, released)
	})

	t.Run("never-dispatched hold remains releasable", func(t *testing.T) {
		ctx := context.Background()
		repo, input, _ := newWalletAdmissionIntegrationFixture(t, ctx, 10)
		input.RequestID = "wallet-never-dispatched-" + uuid.NewString()
		admission, err := service.NewUsageBillingAdmission(input)
		require.NoError(t, err)
		require.NoError(t, repo.Admit(ctx, admission))
		ref := service.UsageBillingAdmissionAttemptRef{
			RequestID: admission.RequestID(), APIKeyID: admission.APIKeyID(),
			OwnerToken: admission.OwnerToken(), AttemptID: admission.AttemptID(),
		}
		require.NoError(t, repo.MarkOrphaned(ctx, ref))
		require.NoError(t, repo.MarkAttemptFailed(ctx, ref))
		_, err = integrationDB.ExecContext(ctx, `
			UPDATE usage_billing_admissions
			SET state = 'reconcile', reconcile_started_at = NOW(), updated_at = NOW()
			WHERE request_id = $1 AND api_key_id = $2
		`, admission.RequestID(), admission.APIKeyID())
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `
			UPDATE usage_billing_admissions
			SET state = 'abandoned', abandoned_at = NOW(), wallet_released_at = NOW(), updated_at = NOW()
			WHERE request_id = $1 AND api_key_id = $2
		`, admission.RequestID(), admission.APIKeyID())
		require.NoError(t, err)
	})

	t.Run("known rejection still releases from dispatched state", func(t *testing.T) {
		ctx := context.Background()
		repo, input, _ := newWalletAdmissionIntegrationFixture(t, ctx, 10)
		input.RequestID = "wallet-known-rejection-" + uuid.NewString()
		admission, err := service.NewUsageBillingAdmission(input)
		require.NoError(t, err)
		require.NoError(t, repo.Admit(ctx, admission))
		ref := service.UsageBillingAdmissionAttemptRef{
			RequestID: admission.RequestID(), APIKeyID: admission.APIKeyID(),
			OwnerToken: admission.OwnerToken(), AttemptID: admission.AttemptID(),
		}
		require.NoError(t, repo.MarkDispatched(ctx, ref))
		require.NoError(t, repo.MarkAttemptFailed(ctx, ref))
		require.NoError(t, repo.Abandon(ctx, ref))

		var state string
		var released bool
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			SELECT state, wallet_released_at IS NOT NULL
			FROM usage_billing_admissions
			WHERE request_id = $1 AND api_key_id = $2
		`, admission.RequestID(), admission.APIKeyID()).Scan(&state, &released))
		require.Equal(t, service.UsageBillingAdmissionStateAbandoned, state)
		require.True(t, released)
	})

	t.Run("stale known rejection remains automatically releasable", func(t *testing.T) {
		ctx := context.Background()
		repo, input, _ := newWalletAdmissionIntegrationFixture(t, ctx, 10)
		input.RequestID = "wallet-stale-known-rejection-" + uuid.NewString()
		admission, err := service.NewUsageBillingAdmission(input)
		require.NoError(t, err)
		require.NoError(t, repo.Admit(ctx, admission))
		ref := service.UsageBillingAdmissionAttemptRef{
			RequestID: admission.RequestID(), APIKeyID: admission.APIKeyID(),
			OwnerToken: admission.OwnerToken(), AttemptID: admission.AttemptID(),
		}
		require.NoError(t, repo.MarkDispatched(ctx, ref))
		require.NoError(t, repo.MarkAttemptFailed(ctx, ref))
		_, err = integrationDB.ExecContext(ctx, `
			UPDATE usage_billing_admissions
			SET updated_at = NOW() - INTERVAL '1 hour'
			WHERE request_id = $1 AND api_key_id = $2
		`, admission.RequestID(), admission.APIKeyID())
		require.NoError(t, err)

		result, err := repo.ReconcileStaleAdmissions(ctx, time.Millisecond, time.Millisecond, 10)
		require.NoError(t, err)
		require.GreaterOrEqual(t, result.AbandonedFailedDispatched, 1)

		var state string
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			SELECT state FROM usage_billing_admissions
			WHERE request_id = $1 AND api_key_id = $2
		`, admission.RequestID(), admission.APIKeyID()).Scan(&state))
		require.Equal(t, service.UsageBillingAdmissionStateAbandoned, state)
	})

	t.Run("node B cannot release node A local spool", func(t *testing.T) {
		ctx := context.Background()
		dbRepo, input, _ := newWalletAdmissionIntegrationFixture(t, ctx, 10)
		input.RequestID = "wallet-cross-node-spool-" + uuid.NewString()
		admission, err := service.NewUsageBillingAdmission(input)
		require.NoError(t, err)
		require.NoError(t, dbRepo.Admit(ctx, admission))
		ref := service.UsageBillingAdmissionAttemptRef{
			RequestID: admission.RequestID(), APIKeyID: admission.APIKeyID(),
			OwnerToken: admission.OwnerToken(), AttemptID: admission.AttemptID(),
		}
		require.NoError(t, dbRepo.MarkDispatched(ctx, ref))
		require.NoError(t, dbRepo.MarkOrphaned(ctx, ref))

		envelope, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
			RequestID: input.RequestID, RequestPayloadHash: input.RequestPayloadHash,
			AdmissionAttemptID: input.AttemptID, APIKeyID: input.APIKeyID,
			AuthCacheLocator: input.AuthCacheLocator, UserID: input.UserID, AccountID: input.AccountID,
			SubscriptionID: input.SubscriptionID, GroupID: input.GroupID,
			EffectiveBillingGroupID: input.EffectiveBillingGroupID, AccountType: input.AccountType,
			BillingModel: input.BillingModel, BillingType: input.BillingType,
			PricingSource: input.PricingSource, PricingRevision: input.PricingRevision,
			PricingHash: input.PricingHash, RateMultiplier: input.RateMultiplier, AccountRateMultiplier: 1,
			WalletCost: 0.5, ActualCost: 0.5, TotalCost: 0.5,
			APIKeyQuotaCost: 0.5, APIKeyRateLimitCost: 0.5, AccountQuotaCost: 0.5,
		})
		require.NoError(t, err)
		nodeAInner := &durableUsageOutboxInnerStub{
			enqueueErr: service.MarkUsageBillingOutboxAdmissionRetryable(errors.New("postgres unavailable")),
		}
		nodeA, err := newDurableUsageBillingOutboxRepository(
			nodeAInner, nodeAInner, t.TempDir(), durableUsageBillingOutboxOptions{},
		)
		require.NoError(t, err)
		_, inserted, err := nodeA.Enqueue(ctx, envelope)
		require.NoError(t, err)
		require.True(t, inserted)
		stored, found, err := nodeA.spool.GetByIdentity(input.RequestID, input.APIKeyID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, envelope.RequestFingerprint(), stored.RequestFingerprint())

		nodeB, err := newDurableUsageBillingOutboxRepository(
			dbRepo, dbRepo, t.TempDir(), durableUsageBillingOutboxOptions{},
		)
		require.NoError(t, err)
		err = nodeB.ResolveUsageBillingReconciliation(ctx, service.UsageBillingReconciliationResolveInput{
			RequestID: input.RequestID, APIKeyID: input.APIKeyID, OperatorID: 9,
			Action:      service.UsageBillingReconciliationActionReleaseUndelivered,
			EvidenceRef: "incident:HFC-cross-node",
		})
		require.ErrorIs(t, err, service.ErrUsageBillingReconciliationConflict)
	})
}

func requirePostgresConstraint(t *testing.T, err error, constraint string) {
	t.Helper()
	require.Error(t, err)
	var pqErr *pq.Error
	require.True(t, errors.As(err, &pqErr), "expected PostgreSQL error, got %T: %v", err, err)
	require.Equal(t, pq.ErrorCode("23514"), pqErr.Code)
	require.Equal(t, constraint, pqErr.Constraint)
}
