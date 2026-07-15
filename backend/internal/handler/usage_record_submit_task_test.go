package handler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newUsageRecordTestPool(t *testing.T) *service.UsageRecordWorkerPool {
	t.Helper()
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             8,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	t.Cleanup(pool.Stop)
	return pool
}

func TestOpenAIWSBillingRequestID_IsStablePerConnectionTurnAndDistinctAcrossTurns(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "connection-request-1")
	connectionID := openAIWSBillingConnectionID(ctx, "session-hash")
	first := deriveOpenAIWSBillingRequestID(connectionID, 1)
	second := deriveOpenAIWSBillingRequestID(connectionID, 2)

	require.Equal(t, first, deriveOpenAIWSBillingRequestID(openAIWSBillingConnectionID(ctx, "different-session"), 1))
	require.NotEqual(t, first, second)
	require.Contains(t, first, ":turn:1")
}

func TestGatewayHandlerSubmitUsageRecordTask_BypassesInMemoryPool(t *testing.T) {
	pool := newUsageRecordTestPool(t)
	h := &GatewayHandler{usageRecordWorkerPool: pool}

	var called atomic.Bool
	h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
		_, hasDeadline := ctx.Deadline()
		require.False(t, hasDeadline)
		called.Store(true)
	})

	require.True(t, called.Load(), "durable producer must finish before helper returns")
	require.Zero(t, pool.Stats().SubmittedTasks, "billable gateway usage must not enter an in-memory queue")
}

func TestGatewayHandlerSubmitUsageRecordTask_WithoutPoolSyncFallbackHasNoShortDeadline(t *testing.T) {
	h := &GatewayHandler{}
	var called atomic.Bool

	h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
		_, hasDeadline := ctx.Deadline()
		require.False(t, hasDeadline)
		called.Store(true)
	})

	require.True(t, called.Load())
}

func TestGatewayHandlerSubmitUsageRecordTask_NilTask(t *testing.T) {
	h := &GatewayHandler{}
	require.NotPanics(t, func() {
		h.submitUsageRecordTask(context.Background(), nil)
	})
}

func TestGatewayHandlerSubmitUsageRecordTask_WithoutPool_TaskPanicRecovered(t *testing.T) {
	h := &GatewayHandler{}
	var called atomic.Bool

	require.NotPanics(t, func() {
		h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
			panic("usage task panic")
		})
	})

	h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
		called.Store(true)
	})
	require.True(t, called.Load(), "panic 后后续任务应仍可执行")
}

func TestOpenAIGatewayHandlerSubmitUsageRecordTask_BypassesInMemoryPool(t *testing.T) {
	pool := newUsageRecordTestPool(t)
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}

	var called atomic.Bool
	h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
		_, hasDeadline := ctx.Deadline()
		require.False(t, hasDeadline)
		called.Store(true)
	})

	require.True(t, called.Load(), "durable producer must finish before helper returns")
	require.Zero(t, pool.Stats().SubmittedTasks, "billable gateway usage must not enter an in-memory queue")
}

func TestOpenAIGatewayHandlerSubmitUsageRecordTask_WithoutPoolSyncFallbackHasNoShortDeadline(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	var called atomic.Bool

	h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
		_, hasDeadline := ctx.Deadline()
		require.False(t, hasDeadline)
		called.Store(true)
	})

	require.True(t, called.Load())
}

func TestOpenAIGatewayHandlerSubmitUsageRecordTask_NilTask(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	require.NotPanics(t, func() {
		h.submitUsageRecordTask(context.Background(), nil)
	})
}

func TestOpenAIGatewayHandlerSubmitUsageRecordTask_WithoutPool_TaskPanicRecovered(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	var called atomic.Bool

	require.NotPanics(t, func() {
		h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
			panic("usage task panic")
		})
	})

	h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
		called.Store(true)
	})
	require.True(t, called.Load(), "panic 后后续任务应仍可执行")
}

func TestOpenAIGatewayHandlerSubmitMandatoryUsageRecordTask_DroppedTaskSyncFallback(t *testing.T) {
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	t.Cleanup(pool.Stop)
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}

	block := make(chan struct{})
	release := make(chan struct{})
	pool.Submit(func(ctx context.Context) {
		close(block)
		<-release
	})
	<-block
	pool.Submit(func(ctx context.Context) {})

	var called atomic.Bool
	h.submitMandatoryUsageRecordTask(context.Background(), func(ctx context.Context) {
		called.Store(true)
	})
	close(release)

	require.True(t, called.Load(), "mandatory usage task must run synchronously when async submit is dropped")
}

func TestOpenAIGatewayHandlerSubmitOpenAIUsageRecordTask_ImageResultUsesMandatoryFallback(t *testing.T) {
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	t.Cleanup(pool.Stop)
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}

	block := make(chan struct{})
	release := make(chan struct{})
	pool.Submit(func(ctx context.Context) {
		close(block)
		<-release
	})
	<-block
	pool.Submit(func(ctx context.Context) {})

	var called atomic.Bool
	h.submitOpenAIUsageRecordTask(context.Background(), &service.OpenAIForwardResult{ImageCount: 1}, func(ctx context.Context) {
		called.Store(true)
	})
	close(release)

	require.True(t, called.Load(), "image usage task must be mandatory when async submit is dropped")
}

func TestOpenAIGatewayHandlerSubmitOpenAIUsageRecordTask_BypassesInMemoryPool(t *testing.T) {
	pool := newUsageRecordTestPool(t)
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}
	var called atomic.Bool

	h.submitOpenAIUsageRecordTask(context.Background(), &service.OpenAIForwardResult{}, func(ctx context.Context) {
		_, hasDeadline := ctx.Deadline()
		require.False(t, hasDeadline, "durable outbox admission must not inherit the legacy short task timeout")
		called.Store(true)
	})

	require.True(t, called.Load(), "durable producer must finish before helper returns")
	require.Zero(t, pool.Stats().SubmittedTasks, "billable OpenAI usage must not enter drop/sample worker pool")
}
