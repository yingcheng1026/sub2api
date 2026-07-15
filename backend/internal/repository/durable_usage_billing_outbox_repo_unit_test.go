package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type durableUsageOutboxInnerStub struct {
	enqueueErr error
	envelopes  []service.UsageBillingEnvelope
	claimed    []service.UsageBillingOutboxEvent
	claimCalls int
	resolved   []service.UsageBillingReconciliationResolveInput
}

func (s *durableUsageOutboxInnerStub) Enqueue(ctx context.Context, envelope service.UsageBillingEnvelope) (*service.UsageBillingOutboxEvent, bool, error) {
	if s.enqueueErr != nil {
		return nil, false, s.enqueueErr
	}
	s.envelopes = append(s.envelopes, envelope)
	return &service.UsageBillingOutboxEvent{ID: int64(len(s.envelopes)), Envelope: envelope}, true, nil
}
func (s *durableUsageOutboxInnerStub) Claim(context.Context, string, int, time.Duration) ([]service.UsageBillingOutboxEvent, error) {
	s.claimCalls++
	return s.claimed, nil
}
func (s *durableUsageOutboxInnerStub) Complete(context.Context, int64, string, string, string) error {
	return nil
}
func (s *durableUsageOutboxInnerStub) Retry(context.Context, int64, string, string, time.Time, string, string) error {
	return nil
}
func (s *durableUsageOutboxInnerStub) DeadLetter(context.Context, int64, string, string, string, string) error {
	return nil
}
func (s *durableUsageOutboxInnerStub) ValidateBindings(context.Context, service.UsageBillingEnvelope) error {
	return nil
}
func (s *durableUsageOutboxInnerStub) ListUsageBillingReconciliationCases(context.Context, int) ([]service.UsageBillingReconciliationCase, error) {
	return nil, nil
}
func (s *durableUsageOutboxInnerStub) ResolveUsageBillingReconciliation(_ context.Context, input service.UsageBillingReconciliationResolveInput) error {
	s.resolved = append(s.resolved, input)
	return nil
}

func durableUsageOutboxTestEnvelope(t *testing.T) service.UsageBillingEnvelope {
	t.Helper()
	groupID := int64(4)
	envelope, err := service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID: "durable-spool-test", RequestPayloadHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		APIKeyID: 1, UserID: 2, AccountID: 3,
		GroupID: &groupID, AccountType: service.AccountTypeOAuth,
		BillingModel: "claude-sonnet-4", ExecutionModel: "claude-sonnet-4",
		BillingType: service.BillingTypeBalance, InputTokens: 1,
		OccurredAtUnixMs: time.Now().UnixMilli(),
		PricingSource:    service.PricingSourceBuiltinFallback, PricingRevision: "fallback-v1",
		PricingHash:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RateMultiplier: 1, AccountRateMultiplier: 1,
	})
	require.NoError(t, err)
	return envelope
}

func TestDurableUsageBillingOutboxRepository_SpoolsRetryableAdmissionAndReplaysAfterRestart(t *testing.T) {
	spoolDir := t.TempDir()
	sentinel := errors.New("postgres unavailable")
	firstInner := &durableUsageOutboxInnerStub{enqueueErr: service.MarkUsageBillingOutboxAdmissionRetryable(sentinel)}
	first, err := newDurableUsageBillingOutboxRepository(firstInner, firstInner, spoolDir, durableUsageBillingOutboxOptions{AdmissionTimeout: 25 * time.Millisecond})
	require.NoError(t, err)
	envelope := durableUsageOutboxTestEnvelope(t)

	_, inserted, err := first.Enqueue(context.Background(), envelope)
	require.NoError(t, err)
	require.True(t, inserted)
	require.Empty(t, firstInner.envelopes)
	files, err := filepath.Glob(filepath.Join(spoolDir, "*.json"))
	require.NoError(t, err)
	require.Len(t, files, 1, "successful admission must be fsync-backed before returning")

	recoveredInner := &durableUsageOutboxInnerStub{}
	restarted, err := newDurableUsageBillingOutboxRepository(recoveredInner, recoveredInner, spoolDir, durableUsageBillingOutboxOptions{AdmissionTimeout: 25 * time.Millisecond})
	require.NoError(t, err)
	_, err = restarted.Claim(context.Background(), "restart-worker", 10, time.Second)
	require.NoError(t, err)
	require.Len(t, recoveredInner.envelopes, 1)
	require.Equal(t, envelope.RequestFingerprint(), recoveredInner.envelopes[0].RequestFingerprint())
	files, err = filepath.Glob(filepath.Join(spoolDir, "*.json"))
	require.NoError(t, err)
	require.Empty(t, files, "spool file is deleted only after idempotent DB enqueue succeeds")
}

func TestDurableUsageBillingOutboxRepository_BoundsHungDatabaseBeforeSpooling(t *testing.T) {
	inner := &blockingUsageBillingOutboxInnerStub{}
	repo, err := newDurableUsageBillingOutboxRepository(inner, inner, t.TempDir(), durableUsageBillingOutboxOptions{AdmissionTimeout: 20 * time.Millisecond})
	require.NoError(t, err)

	started := time.Now()
	_, _, err = repo.Enqueue(context.Background(), durableUsageOutboxTestEnvelope(t))
	require.NoError(t, err)
	require.Less(t, time.Since(started), 250*time.Millisecond)
}

type blockingUsageBillingOutboxInnerStub struct{ durableUsageOutboxInnerStub }

func (s *blockingUsageBillingOutboxInnerStub) Enqueue(ctx context.Context, envelope service.UsageBillingEnvelope) (*service.UsageBillingOutboxEvent, bool, error) {
	<-ctx.Done()
	return nil, false, service.MarkUsageBillingOutboxAdmissionRetryable(ctx.Err())
}

func TestUsageBillingOutboxSpool_DoesNotOverwriteConflictingIdentity(t *testing.T) {
	dir := t.TempDir()
	spool, err := newUsageBillingOutboxSpool(dir)
	require.NoError(t, err)
	first := durableUsageOutboxTestEnvelope(t)
	require.NoError(t, spool.Put(first))

	raw, err := first.MarshalJSON()
	require.NoError(t, err)
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.NoError(t, os.WriteFile(files[0], append(raw, '\n'), 0o600))
	require.NoError(t, spool.Put(first), "same immutable envelope is idempotent")
}

func TestDurableUsageBillingReconciliationRefusesReleaseWhenLocalSpoolExists(t *testing.T) {
	inner := &durableUsageOutboxInnerStub{}
	repo, err := newDurableUsageBillingOutboxRepository(inner, inner, t.TempDir(), durableUsageBillingOutboxOptions{})
	require.NoError(t, err)
	envelope := durableUsageOutboxTestEnvelope(t)
	require.NoError(t, repo.spool.Put(envelope))

	err = repo.ResolveUsageBillingReconciliation(context.Background(), service.UsageBillingReconciliationResolveInput{
		RequestID: envelope.RequestID(), APIKeyID: envelope.APIKeyID(),
		Action: service.UsageBillingReconciliationActionReleaseUndelivered,
	})

	require.ErrorIs(t, err, service.ErrUsageBillingReconciliationConflict)
	require.Empty(t, inner.resolved)
}

func TestDurableUsageBillingReconciliationConsumesMatchingLocalSpoolForDeliveredReplay(t *testing.T) {
	spoolDir := t.TempDir()
	inner := &durableUsageOutboxInnerStub{}
	repo, err := newDurableUsageBillingOutboxRepository(inner, inner, spoolDir, durableUsageBillingOutboxOptions{})
	require.NoError(t, err)
	envelope := durableUsageOutboxTestEnvelope(t)
	require.NoError(t, repo.spool.Put(envelope))

	err = repo.ResolveUsageBillingReconciliation(context.Background(), service.UsageBillingReconciliationResolveInput{
		RequestID: envelope.RequestID(), APIKeyID: envelope.APIKeyID(),
		Action:   service.UsageBillingReconciliationActionSettleDelivered,
		Envelope: envelope,
	})

	require.NoError(t, err)
	require.Len(t, inner.resolved, 1)
	require.Equal(t, envelope.RequestFingerprint(), inner.resolved[0].Envelope.RequestFingerprint())
	files, err := filepath.Glob(filepath.Join(spoolDir, "*.json"))
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestDurableUsageBillingOutboxQuarantinesPermanentPoisonAndContinuesClaim(t *testing.T) {
	spoolDir := t.TempDir()
	inner := &durableUsageOutboxInnerStub{enqueueErr: service.ErrUsageBillingAdmissionLeaseLost}
	repo, err := newDurableUsageBillingOutboxRepository(inner, inner, spoolDir, durableUsageBillingOutboxOptions{})
	require.NoError(t, err)
	require.NoError(t, repo.spool.Put(durableUsageOutboxTestEnvelope(t)))

	_, err = repo.Claim(context.Background(), "worker", 10, time.Second)

	require.NoError(t, err)
	rootFiles, err := filepath.Glob(filepath.Join(spoolDir, "*.json"))
	require.NoError(t, err)
	require.Empty(t, rootFiles)
	quarantined, err := filepath.Glob(filepath.Join(spoolDir, "quarantine", "*.json"))
	require.NoError(t, err)
	require.Len(t, quarantined, 1)
}

func TestDurableUsageBillingOutboxQuarantinesMissingTargetAndContinuesClaim(t *testing.T) {
	spoolDir := t.TempDir()
	claimed := service.UsageBillingOutboxEvent{ID: 42}
	inner := &durableUsageOutboxInnerStub{
		enqueueErr: service.ErrUsageBillingOutboxTargetNotFound,
		claimed:    []service.UsageBillingOutboxEvent{claimed},
	}
	repo, err := newDurableUsageBillingOutboxRepository(inner, inner, spoolDir, durableUsageBillingOutboxOptions{})
	require.NoError(t, err)
	require.NoError(t, repo.spool.Put(durableUsageOutboxTestEnvelope(t)))

	events, err := repo.Claim(context.Background(), "worker", 10, time.Second)

	require.NoError(t, err)
	require.Equal(t, []service.UsageBillingOutboxEvent{claimed}, events)
	require.Equal(t, 1, inner.claimCalls, "a permanent spool poison must not block database claims")
	rootFiles, err := filepath.Glob(filepath.Join(spoolDir, "*.json"))
	require.NoError(t, err)
	require.Empty(t, rootFiles)
	quarantined, err := filepath.Glob(filepath.Join(spoolDir, "quarantine", "*.json"))
	require.NoError(t, err)
	require.Len(t, quarantined, 1)
}
