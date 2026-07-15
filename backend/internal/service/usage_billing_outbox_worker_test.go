package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type usageBillingBatchProcessorStub struct {
	mu        sync.Mutex
	pending   int
	processed chan struct{}
}

type usageBillingFailureProcessorStub struct {
	mu    sync.Mutex
	calls int
}

func (s *usageBillingFailureProcessorStub) ProcessBatch(context.Context, string, int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls == 1 {
		return 0, errors.New("database unavailable access_token=secret-worker-token")
	}
	return 0, nil
}

func (s *usageBillingBatchProcessorStub) ProcessBatch(context.Context, string, int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == 0 {
		return 0, nil
	}
	s.pending--
	select {
	case s.processed <- struct{}{}:
	default:
	}
	return 1, nil
}

func TestUsageBillingOutboxWorker_StartImmediatelyProcessesPendingBeforeTicker(t *testing.T) {
	processor := &usageBillingBatchProcessorStub{pending: 1, processed: make(chan struct{}, 1)}
	worker := NewUsageBillingOutboxWorkerWithOptions(processor, UsageBillingOutboxWorkerOptions{
		Owner: "worker-test", BatchSize: 1, PollInterval: time.Hour, ErrorBackoff: time.Hour,
	})
	worker.Start()
	t.Cleanup(func() { _ = worker.Stop(context.Background()) })

	select {
	case <-processor.processed:
	case <-time.After(time.Second):
		t.Fatal("pending outbox event was not processed immediately on start")
	}
}

func TestUsageBillingOutboxWorker_StopThenNewWorkerProcessesPersistedPendingEvent(t *testing.T) {
	processor := &usageBillingBatchProcessorStub{pending: 0, processed: make(chan struct{}, 1)}
	first := NewUsageBillingOutboxWorkerWithOptions(processor, UsageBillingOutboxWorkerOptions{
		Owner: "worker-one", BatchSize: 1, PollInterval: time.Hour, ErrorBackoff: time.Hour,
	})
	first.Start()
	require.NoError(t, first.Stop(context.Background()))

	processor.mu.Lock()
	processor.pending = 1
	processor.mu.Unlock()
	second := NewUsageBillingOutboxWorkerWithOptions(processor, UsageBillingOutboxWorkerOptions{
		Owner: "worker-two", BatchSize: 1, PollInterval: time.Hour, ErrorBackoff: time.Hour,
	})
	second.Start()
	t.Cleanup(func() { _ = second.Stop(context.Background()) })

	select {
	case <-processor.processed:
	case <-time.After(time.Second):
		t.Fatal("new worker did not recover the persisted pending event")
	}
}

func TestUsageBillingOutboxWorker_StartRejectsNilProcessor(t *testing.T) {
	worker := NewUsageBillingOutboxWorkerWithOptions(nil, UsageBillingOutboxWorkerOptions{})
	require.Error(t, worker.Start())
	health := worker.Health()
	require.False(t, health.Running)
	require.NotEmpty(t, health.LastError)
}

func TestUsageBillingOutboxWorker_HealthReportsFailureAndRecoveryWithoutSecrets(t *testing.T) {
	processor := &usageBillingFailureProcessorStub{}
	worker := NewUsageBillingOutboxWorkerWithOptions(processor, UsageBillingOutboxWorkerOptions{
		Owner: "worker-health-test", PollInterval: time.Millisecond, ErrorBackoff: time.Millisecond,
	})
	require.NoError(t, worker.Start())
	t.Cleanup(func() { _ = worker.Stop(context.Background()) })

	require.Eventually(t, func() bool {
		health := worker.Health()
		return !health.LastErrorAt.IsZero()
	}, time.Second, time.Millisecond)
	failureHealth := worker.Health()
	require.NotContains(t, failureHealth.LastError, "secret-worker-token")
	require.Contains(t, failureHealth.LastError, "access_token=***")

	require.Eventually(t, func() bool {
		health := worker.Health()
		return health.ConsecutiveFailures == 0 && !health.LastSuccessAt.IsZero()
	}, time.Second, time.Millisecond)
	require.True(t, worker.Health().Running)
}
