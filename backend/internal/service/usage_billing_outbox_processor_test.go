package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type outboxProcessorRepoStub struct {
	claimed      []UsageBillingOutboxEvent
	claimErr     error
	completeErr  error
	retried      bool
	dead         bool
	completed    bool
	resultCode   string
	retryAt      time.Time
	errorCode    string
	errorMessage string
	trace        *[]string
}

func (s *outboxProcessorRepoStub) Enqueue(context.Context, UsageBillingEnvelope) (*UsageBillingOutboxEvent, bool, error) {
	panic("not used")
}

func (s *outboxProcessorRepoStub) Claim(context.Context, string, int, time.Duration) ([]UsageBillingOutboxEvent, error) {
	return s.claimed, s.claimErr
}

func (s *outboxProcessorRepoStub) Complete(_ context.Context, _ int64, _, _ string, resultCode string) error {
	if s.trace != nil {
		*s.trace = append(*s.trace, "complete")
	}
	if s.completeErr != nil {
		err := s.completeErr
		s.completeErr = nil
		return err
	}
	s.completed = true
	s.resultCode = resultCode
	return nil
}

func (s *outboxProcessorRepoStub) Retry(_ context.Context, _ int64, _, _ string, availableAt time.Time, code, message string) error {
	s.retried = true
	s.retryAt = availableAt
	s.errorCode = code
	s.errorMessage = message
	return nil
}

func (s *outboxProcessorRepoStub) DeadLetter(_ context.Context, _ int64, _, _ string, code, message string) error {
	s.dead = true
	s.errorCode = code
	s.errorMessage = message
	return nil
}

type outboxBindingStub struct {
	err   error
	calls int
}

func (s *outboxBindingStub) ValidateBindings(context.Context, UsageBillingEnvelope) error {
	s.calls++
	return s.err
}

type outboxBillingStub struct {
	err            error
	calls          int
	appliedCharges int
	commands       []*UsageBillingCommand
	trace          *[]string
}

func (s *outboxBillingStub) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	s.calls++
	s.commands = append(s.commands, cmd)
	if s.trace != nil {
		*s.trace = append(*s.trace, "apply")
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.appliedCharges == 0 {
		s.appliedCharges++
		return &UsageBillingApplyResult{Applied: true}, nil
	}
	return &UsageBillingApplyResult{Applied: false}, nil
}

type outboxReplayWriterStub struct {
	err    error
	writes int
	trace  *[]string
}

func (s *outboxReplayWriterStub) WriteUsageBillingReplay(context.Context, UsageBillingEnvelope) error {
	if s.trace != nil {
		*s.trace = append(*s.trace, "usage_log")
	}
	if s.err == nil && s.writes == 0 {
		s.writes++
	}
	return s.err
}

func processorEnvelope(t *testing.T, requestID string) UsageBillingEnvelope {
	t.Helper()
	input := validUsageBillingEnvelopeInput()
	input.RequestID = requestID
	envelope, err := NewUsageBillingEnvelope(input)
	require.NoError(t, err)
	return envelope
}

func TestUsageBillingOutboxProcessor_CrossTenantDeadLettersBeforeApply(t *testing.T) {
	repo := &outboxProcessorRepoStub{}
	binding := &outboxBindingStub{err: ErrUsageBillingCrossTenant}
	billing := &outboxBillingStub{}
	processor := NewUsageBillingOutboxProcessor(repo, binding, billing, nil)

	event := UsageBillingOutboxEvent{ID: 1, Envelope: processorEnvelope(t, "processor-cross-tenant"), AttemptCount: 1, MaxAttempts: 8, LockedBy: "worker", LeaseToken: "lease-1"}
	require.NoError(t, processor.ProcessEvent(context.Background(), event))
	require.True(t, repo.dead)
	require.Equal(t, "cross_tenant", repo.errorCode)
	require.Zero(t, billing.calls)
}

func TestUsageBillingOutboxProcessor_TransientRetryThenSuccess(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	repo := &outboxProcessorRepoStub{}
	binding := &outboxBindingStub{}
	billing := &outboxBillingStub{err: errors.New("database temporarily unavailable")}
	processor := NewUsageBillingOutboxProcessor(repo, binding, billing, nil)
	processor.now = func() time.Time { return now }

	event := UsageBillingOutboxEvent{ID: 7, Envelope: processorEnvelope(t, "processor-retry"), AttemptCount: 1, MaxAttempts: 8, LockedBy: "worker", LeaseToken: "lease-1"}
	require.NoError(t, processor.ProcessEvent(context.Background(), event))
	require.True(t, repo.retried)
	require.True(t, repo.retryAt.After(now))

	billing.err = nil
	repo.retried = false
	require.NoError(t, processor.ProcessEvent(context.Background(), event))
	require.True(t, repo.completed)
	require.Equal(t, UsageBillingOutboxResultApplied, repo.resultCode)
}

func TestUsageBillingOutboxProcessor_PoisonAndMaxAttemptsDeadLetter(t *testing.T) {
	t.Run("decode poison", func(t *testing.T) {
		repo := &outboxProcessorRepoStub{}
		billing := &outboxBillingStub{}
		processor := NewUsageBillingOutboxProcessor(repo, &outboxBindingStub{}, billing, nil)
		event := UsageBillingOutboxEvent{ID: 8, EnvelopeError: ErrUsageBillingEnvelopeVersion, AttemptCount: 1, MaxAttempts: 8, LockedBy: "worker", LeaseToken: "lease-1"}

		require.NoError(t, processor.ProcessEvent(context.Background(), event))
		require.True(t, repo.dead)
		require.Zero(t, billing.calls)
	})

	t.Run("max attempts", func(t *testing.T) {
		repo := &outboxProcessorRepoStub{}
		billing := &outboxBillingStub{err: errors.New("still unavailable")}
		processor := NewUsageBillingOutboxProcessor(repo, &outboxBindingStub{}, billing, nil)
		event := UsageBillingOutboxEvent{ID: 9, Envelope: processorEnvelope(t, "processor-max"), AttemptCount: 8, MaxAttempts: 8, LockedBy: "worker", LeaseToken: "lease-1"}

		require.NoError(t, processor.ProcessEvent(context.Background(), event))
		require.True(t, repo.dead)
		require.Equal(t, "max_attempts", repo.errorCode)
	})
}

func TestUsageBillingOutboxProcessor_ApplyAckFailureReplayChargesOnce(t *testing.T) {
	repo := &outboxProcessorRepoStub{completeErr: errors.New("ack failed")}
	billing := &outboxBillingStub{}
	writes := &outboxReplayWriterStub{}
	processor := NewUsageBillingOutboxProcessor(repo, &outboxBindingStub{}, billing, writes)
	event := UsageBillingOutboxEvent{ID: 10, Envelope: processorEnvelope(t, "processor-ack-crash"), AttemptCount: 1, MaxAttempts: 8, LockedBy: "worker-one", LeaseToken: "lease-1"}

	require.Error(t, processor.ProcessEvent(context.Background(), event))
	event.AttemptCount = 2
	event.LockedBy = "worker-two"
	event.LeaseToken = "lease-2"
	require.NoError(t, processor.ProcessEvent(context.Background(), event))
	require.Equal(t, 2, billing.calls)
	require.Equal(t, 1, billing.appliedCharges)
	require.Equal(t, UsageBillingOutboxResultDuplicate, repo.resultCode)
	require.Equal(t, 1, writes.writes, "usage log callback must be idempotent across replay")
}

func TestUsageBillingOutboxProcessor_OrdersApplyUsageLogComplete(t *testing.T) {
	trace := make([]string, 0, 3)
	repo := &outboxProcessorRepoStub{trace: &trace}
	billing := &outboxBillingStub{trace: &trace}
	writes := &outboxReplayWriterStub{trace: &trace}
	processor := NewUsageBillingOutboxProcessor(repo, &outboxBindingStub{}, billing, writes)
	event := UsageBillingOutboxEvent{ID: 11, Envelope: processorEnvelope(t, "processor-order"), AttemptCount: 1, MaxAttempts: 8, LockedBy: "worker", LeaseToken: "lease-1"}

	require.NoError(t, processor.ProcessEvent(context.Background(), event))
	require.Equal(t, []string{"apply", "usage_log", "complete"}, trace)
}

func TestUsageBillingOutboxProcessor_PreservesFrozenBalanceMonthlyWalletCommands(t *testing.T) {
	tests := []struct {
		name  string
		input UsageBillingEnvelopeInput
		check func(*testing.T, *UsageBillingCommand)
	}{
		{
			name:  "balance",
			input: validUsageBillingEnvelopeInput(),
			check: func(t *testing.T, cmd *UsageBillingCommand) { require.Equal(t, 2.5, cmd.BalanceCost) },
		},
		{
			name: "monthly",
			input: func() UsageBillingEnvelopeInput {
				subscriptionID := int64(55)
				input := validUsageBillingEnvelopeInput()
				input.BillingType = BillingTypeSubscription
				input.BalanceCost = 0
				input.SubscriptionID = &subscriptionID
				input.SubscriptionCost = 2.5
				return input
			}(),
			check: func(t *testing.T, cmd *UsageBillingCommand) {
				require.Equal(t, int64(55), *cmd.SubscriptionID)
				require.Equal(t, 2.5, cmd.SubscriptionCost)
			},
		},
		{
			name: "wallet",
			input: func() UsageBillingEnvelopeInput {
				subscriptionID := int64(66)
				input := validUsageBillingEnvelopeInput()
				input.BillingType = BillingTypeSubscription
				input.BalanceCost = 0
				input.SubscriptionID = &subscriptionID
				input.WalletCost = 2.5
				return input
			}(),
			check: func(t *testing.T, cmd *UsageBillingCommand) {
				require.Equal(t, int64(66), *cmd.SubscriptionID)
				require.Equal(t, 2.5, cmd.WalletCost)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.RequestID = "processor-frozen-" + tt.name
			envelope, err := NewUsageBillingEnvelope(tt.input)
			require.NoError(t, err)
			billing := &outboxBillingStub{}
			processor := NewUsageBillingOutboxProcessor(&outboxProcessorRepoStub{}, &outboxBindingStub{}, billing, nil)
			event := UsageBillingOutboxEvent{ID: 20, Envelope: envelope, AttemptCount: 1, MaxAttempts: 8, LockedBy: "worker", LeaseToken: "lease-1"}
			require.NoError(t, processor.ProcessEvent(context.Background(), event))
			require.Len(t, billing.commands, 1)
			tt.check(t, billing.commands[0])
		})
	}
}

func TestUsageBillingOutboxProcessor_ProcessBatchContinuesAfterOneEventFails(t *testing.T) {
	first := UsageBillingOutboxEvent{ID: 31, Envelope: processorEnvelope(t, "processor-batch-1"), AttemptCount: 1, MaxAttempts: 8, LockedBy: "worker", LeaseToken: "lease-1"}
	second := UsageBillingOutboxEvent{ID: 32, Envelope: processorEnvelope(t, "processor-batch-2"), AttemptCount: 1, MaxAttempts: 8, LockedBy: "worker", LeaseToken: "lease-2"}
	repo := &outboxProcessorRepoStub{claimed: []UsageBillingOutboxEvent{first, second}, completeErr: errors.New("first ack failed")}
	billing := &outboxBillingStub{}
	processor := NewUsageBillingOutboxProcessor(repo, &outboxBindingStub{}, billing, nil)

	processed, err := processor.ProcessBatch(context.Background(), "worker", 2)
	require.Error(t, err)
	require.Equal(t, 2, processed)
	require.Equal(t, 2, billing.calls)
	require.True(t, repo.completed, "second event must still be acknowledged")
}

func TestUsageBillingOutboxProcessor_PersistedErrorsAreRedacted(t *testing.T) {
	repo := &outboxProcessorRepoStub{}
	billing := &outboxBillingStub{err: errors.New("Bearer secret prompt=private postgres://user:pass@example customer@example.com")}
	processor := NewUsageBillingOutboxProcessor(repo, &outboxBindingStub{}, billing, nil)
	event := UsageBillingOutboxEvent{ID: 33, Envelope: processorEnvelope(t, "processor-redact"), AttemptCount: 1, MaxAttempts: 8, LockedBy: "worker", LeaseToken: "lease-1"}

	require.NoError(t, processor.ProcessEvent(context.Background(), event))
	require.Equal(t, "usage billing outbox processing failed", repo.errorMessage)
	require.NotContains(t, repo.errorMessage, "secret")
	require.NotContains(t, repo.errorMessage, "private")
}
