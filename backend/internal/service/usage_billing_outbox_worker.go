package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const (
	defaultUsageBillingOutboxPollInterval = 500 * time.Millisecond
	defaultUsageBillingOutboxErrorBackoff = time.Second
	defaultUsageBillingOutboxBatchSize    = 64
	usageBillingOutboxErrorLogInterval    = 30 * time.Second
	usageBillingOutboxLastErrorMaxBytes   = 512
)

var ErrUsageBillingOutboxWorkerUnavailable = errors.New("usage billing outbox worker is unavailable")

type UsageBillingBatchProcessor interface {
	ProcessBatch(ctx context.Context, owner string, limit int) (int, error)
}

type UsageBillingOutboxWorkerOptions struct {
	Owner        string
	BatchSize    int
	PollInterval time.Duration
	ErrorBackoff time.Duration
}

type UsageBillingOutboxWorkerHealth struct {
	Running             bool      `json:"running"`
	StartedAt           time.Time `json:"started_at"`
	LastAttemptAt       time.Time `json:"last_attempt_at"`
	LastSuccessAt       time.Time `json:"last_success_at"`
	LastErrorAt         time.Time `json:"last_error_at"`
	LastError           string    `json:"last_error"`
	ConsecutiveFailures uint64    `json:"consecutive_failures"`
	ProcessedTotal      uint64    `json:"processed_total"`
}

type UsageBillingOutboxWorker struct {
	processor UsageBillingBatchProcessor
	options   UsageBillingOutboxWorkerOptions
	wake      chan struct{}
	done      chan struct{}

	startOnce      sync.Once
	mu             sync.Mutex
	cancel         context.CancelFunc
	startErr       error
	health         UsageBillingOutboxWorkerHealth
	lastErrorLogAt time.Time
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

func (w *UsageBillingOutboxWorker) Start() error {
	if w == nil {
		return ErrUsageBillingOutboxWorkerUnavailable
	}
	w.startOnce.Do(func() {
		if w.processor == nil {
			w.mu.Lock()
			w.startErr = fmt.Errorf("%w: processor is nil", ErrUsageBillingOutboxWorkerUnavailable)
			w.health.LastErrorAt = time.Now().UTC()
			w.health.LastError = w.startErr.Error()
			w.mu.Unlock()
			slog.Error("usage billing outbox worker failed to start",
				"component", "service.usage_billing_outbox_worker",
				"error", w.startErr,
			)
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		w.mu.Lock()
		w.cancel = cancel
		w.health.Running = true
		w.health.StartedAt = time.Now().UTC()
		w.mu.Unlock()
		go w.run(ctx)
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.startErr
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
	defer func() {
		w.mu.Lock()
		w.health.Running = false
		w.mu.Unlock()
		close(w.done)
	}()
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
		w.recordBatchResult(processed, err)
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

func (w *UsageBillingOutboxWorker) recordBatchResult(processed int, err error) {
	now := time.Now().UTC()
	shouldLogError := false
	recovered := false
	consecutiveFailures := uint64(0)
	safeError := ""

	w.mu.Lock()
	w.health.LastAttemptAt = now
	if err != nil {
		w.health.ConsecutiveFailures++
		w.health.LastErrorAt = now
		w.health.LastError = sanitizeUsageBillingOutboxWorkerError(err)
		consecutiveFailures = w.health.ConsecutiveFailures
		safeError = w.health.LastError
		if w.lastErrorLogAt.IsZero() || now.Sub(w.lastErrorLogAt) >= usageBillingOutboxErrorLogInterval {
			shouldLogError = true
			w.lastErrorLogAt = now
		}
	} else {
		recovered = w.health.ConsecutiveFailures > 0
		w.health.ConsecutiveFailures = 0
		w.health.LastSuccessAt = now
		if processed > 0 {
			w.health.ProcessedTotal += uint64(processed)
		}
	}
	w.mu.Unlock()

	if shouldLogError {
		slog.Error("usage billing outbox batch failed",
			"component", "service.usage_billing_outbox_worker",
			"owner", w.options.Owner,
			"consecutive_failures", consecutiveFailures,
			"error", safeError,
		)
	}
	if recovered {
		slog.Info("usage billing outbox worker recovered",
			"component", "service.usage_billing_outbox_worker",
			"owner", w.options.Owner,
		)
	}
}

func sanitizeUsageBillingOutboxWorkerError(err error) string {
	if err == nil {
		return ""
	}
	redacted := strings.TrimSpace(logredact.RedactText(err.Error()))
	if len(redacted) <= usageBillingOutboxLastErrorMaxBytes {
		return redacted
	}
	return redacted[:usageBillingOutboxLastErrorMaxBytes]
}

func (w *UsageBillingOutboxWorker) Health() UsageBillingOutboxWorkerHealth {
	if w == nil {
		return UsageBillingOutboxWorkerHealth{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.health
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
