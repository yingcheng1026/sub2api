package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultUsageBillingOutboxPollInterval = 500 * time.Millisecond
	defaultUsageBillingOutboxErrorBackoff = time.Second
	defaultUsageBillingOutboxBatchSize    = 64
)

type UsageBillingBatchProcessor interface {
	ProcessBatch(ctx context.Context, owner string, limit int) (int, error)
}

type UsageBillingOutboxWorkerOptions struct {
	Owner        string
	BatchSize    int
	PollInterval time.Duration
	ErrorBackoff time.Duration
}

type UsageBillingOutboxWorker struct {
	processor UsageBillingBatchProcessor
	options   UsageBillingOutboxWorkerOptions
	wake      chan struct{}
	done      chan struct{}

	startOnce sync.Once
	mu        sync.Mutex
	cancel    context.CancelFunc
}

func NewUsageBillingOutboxWorker(processor *UsageBillingOutboxProcessor) *UsageBillingOutboxWorker {
	return NewUsageBillingOutboxWorkerWithOptions(processor, UsageBillingOutboxWorkerOptions{})
}

func NewUsageBillingOutboxWorkerWithOptions(
	processor UsageBillingBatchProcessor,
	options UsageBillingOutboxWorkerOptions,
) *UsageBillingOutboxWorker {
	options = normalizeUsageBillingOutboxWorkerOptions(options)
	return &UsageBillingOutboxWorker{
		processor: processor,
		options:   options,
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
}

func (w *UsageBillingOutboxWorker) Start() {
	if w == nil || w.processor == nil {
		return
	}
	w.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		w.mu.Lock()
		w.cancel = cancel
		w.mu.Unlock()
		go w.run(ctx)
	})
}

func (w *UsageBillingOutboxWorker) Wake() {
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *UsageBillingOutboxWorker) Stop(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	cancel := w.cancel
	w.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *UsageBillingOutboxWorker) run(ctx context.Context) {
	defer close(w.done)
	delay := time.Duration(0)
	for {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-w.wake:
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
			}
		}

		processed, err := w.processor.ProcessBatch(ctx, w.options.Owner, w.options.BatchSize)
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil:
			delay = w.options.ErrorBackoff
		case processed >= w.options.BatchSize:
			delay = 0
		default:
			delay = w.options.PollInterval
		}
	}
}

func normalizeUsageBillingOutboxWorkerOptions(options UsageBillingOutboxWorkerOptions) UsageBillingOutboxWorkerOptions {
	options.Owner = strings.TrimSpace(options.Owner)
	if options.Owner == "" {
		options.Owner = defaultUsageBillingOutboxWorkerOwner()
	}
	if len(options.Owner) > 128 {
		options.Owner = options.Owner[:128]
	}
	if options.BatchSize <= 0 || options.BatchSize > 100 {
		options.BatchSize = defaultUsageBillingOutboxBatchSize
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultUsageBillingOutboxPollInterval
	}
	if options.ErrorBackoff <= 0 {
		options.ErrorBackoff = defaultUsageBillingOutboxErrorBackoff
	}
	return options
}

func defaultUsageBillingOutboxWorkerOwner() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "unknown-host"
	}
	return fmt.Sprintf("%s:%d:%s", hostname, os.Getpid(), generateRequestID())
}
