package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const defaultDurableUsageBillingAdmissionTimeout = 250 * time.Millisecond

type durableUsageBillingOutboxOptions struct {
	AdmissionTimeout time.Duration
}

type durableUsageBillingOutboxRepository struct {
	inner            service.UsageBillingOutboxRepository
	bindingValidator service.UsageBillingBindingValidator
	spool            *usageBillingOutboxSpool
	admissionTimeout time.Duration
	drainMu          sync.Mutex
}

func NewDurableUsageBillingOutboxRepository(db *sql.DB) (*durableUsageBillingOutboxRepository, error) {
	inner := newDurableUsageBillingOutboxInner(db)
	return newDurableUsageBillingOutboxRepository(
		inner,
		inner,
		defaultUsageBillingOutboxSpoolDir(),
		durableUsageBillingOutboxOptions{},
	)
}

func newDurableUsageBillingOutboxInner(db *sql.DB) *usageBillingOutboxRepository {
	inner := NewUsageBillingOutboxRepository(db)
	// Rollout safety: producers already wired for pre-dispatch admission consume
	// their hold atomically. Legacy producers continue durable postpaid billing
	// instead of failing after a successful upstream response. Flip
	// requireAdmission only after every producer and reconciliation path passes.
	inner.useAdmissionIfPresent = true
	inner.requireAdmission = false
	return inner
}

func newDurableUsageBillingOutboxRepository(
	inner service.UsageBillingOutboxRepository,
	bindingValidator service.UsageBillingBindingValidator,
	spoolDir string,
	options durableUsageBillingOutboxOptions,
) (*durableUsageBillingOutboxRepository, error) {
	if inner == nil || bindingValidator == nil {
		return nil, errors.New("usage billing durable outbox inner repository is nil")
	}
	spool, err := newUsageBillingOutboxSpool(spoolDir)
	if err != nil {
		return nil, fmt.Errorf("initialize usage billing durable spool: %w", err)
	}
	if options.AdmissionTimeout <= 0 {
		options.AdmissionTimeout = defaultDurableUsageBillingAdmissionTimeout
	}
	return &durableUsageBillingOutboxRepository{
		inner: inner, bindingValidator: bindingValidator, spool: spool,
		admissionTimeout: options.AdmissionTimeout,
	}, nil
}

func (r *durableUsageBillingOutboxRepository) Enqueue(ctx context.Context, envelope service.UsageBillingEnvelope) (*service.UsageBillingOutboxEvent, bool, error) {
	if r == nil || r.inner == nil || r.spool == nil {
		return nil, false, service.ErrUsageBillingOutboxUnavailable
	}
	if err := envelope.Validate(); err != nil {
		return nil, false, err
	}
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	dbCtx, cancel := context.WithTimeout(baseCtx, r.admissionTimeout)
	event, inserted, err := r.inner.Enqueue(dbCtx, envelope)
	cancel()
	if err == nil {
		return event, inserted, nil
	}
	if !errors.Is(err, service.ErrUsageBillingOutboxAdmissionRetryable) {
		return nil, false, err
	}
	if spoolErr := r.spool.Put(envelope); spoolErr != nil {
		return nil, false, fmt.Errorf("%w: database: %v; durable spool: %w", service.ErrUsageBillingOutboxAdmissionRetryable, err, spoolErr)
	}
	return &service.UsageBillingOutboxEvent{
		Envelope: envelope, Status: service.UsageBillingOutboxStatusPending,
		MaxAttempts: service.UsageBillingOutboxDefaultMaxAttempts,
		CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, true, nil
}

func (r *durableUsageBillingOutboxRepository) Claim(ctx context.Context, owner string, limit int, lease time.Duration) ([]service.UsageBillingOutboxEvent, error) {
	if r == nil || r.inner == nil {
		return nil, service.ErrUsageBillingOutboxUnavailable
	}
	if err := r.drainSpool(ctx, limit); err != nil {
		return nil, err
	}
	return r.inner.Claim(ctx, owner, limit, lease)
}

func (r *durableUsageBillingOutboxRepository) drainSpool(ctx context.Context, limit int) error {
	if r == nil || r.spool == nil {
		return service.ErrUsageBillingOutboxUnavailable
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	r.drainMu.Lock()
	defer r.drainMu.Unlock()
	entries, err := r.spool.List(limit)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		envelope, err := r.spool.Read(entry)
		if err != nil {
			if shouldQuarantineUsageBillingSpoolError(err) {
				if quarantineErr := r.spool.Quarantine(entry); quarantineErr != nil {
					return fmt.Errorf("quarantine unreadable usage billing spool entry %s: %w", entry, quarantineErr)
				}
				slog.Error("quarantined unreadable usage billing spool entry", "entry", entry, "error", err)
				continue
			}
			return fmt.Errorf("read usage billing spool entry %s: %w", entry, err)
		}
		if _, _, err := r.inner.Enqueue(ctx, envelope); err != nil {
			if shouldQuarantineUsageBillingSpoolError(err) {
				if quarantineErr := r.spool.Quarantine(entry); quarantineErr != nil {
					return fmt.Errorf("quarantine rejected usage billing spool entry %s: %w", entry, quarantineErr)
				}
				slog.Error("quarantined rejected usage billing spool entry", "entry", entry, "error", err)
				continue
			}
			return fmt.Errorf("import usage billing spool entry %s: %w", entry, err)
		}
		if err := r.spool.Remove(entry); err != nil {
			return fmt.Errorf("remove imported usage billing spool entry %s: %w", entry, err)
		}
	}
	return nil
}

func (r *durableUsageBillingOutboxRepository) Complete(ctx context.Context, id int64, owner, leaseToken, resultCode string) error {
	return r.inner.Complete(ctx, id, owner, leaseToken, resultCode)
}

func (r *durableUsageBillingOutboxRepository) Retry(ctx context.Context, id int64, owner, leaseToken string, availableAt time.Time, errorCode, errorMessage string) error {
	return r.inner.Retry(ctx, id, owner, leaseToken, availableAt, errorCode, errorMessage)
}

func (r *durableUsageBillingOutboxRepository) DeadLetter(ctx context.Context, id int64, owner, leaseToken, errorCode, errorMessage string) error {
	return r.inner.DeadLetter(ctx, id, owner, leaseToken, errorCode, errorMessage)
}

func (r *durableUsageBillingOutboxRepository) ValidateBindings(ctx context.Context, envelope service.UsageBillingEnvelope) error {
	return r.bindingValidator.ValidateBindings(ctx, envelope)
}

func (r *durableUsageBillingOutboxRepository) Admit(ctx context.Context, admission service.UsageBillingAdmission) error {
	intentRepo, ok := r.inner.(service.UsageBillingAdmissionRepository)
	if !ok {
		return service.ErrUsageBillingOutboxUnavailable
	}
	return intentRepo.Admit(ctx, admission)
}

func (r *durableUsageBillingOutboxRepository) MarkDispatched(ctx context.Context, ref service.UsageBillingAdmissionAttemptRef) error {
	intentRepo, ok := r.inner.(service.UsageBillingAdmissionRepository)
	if !ok {
		return service.ErrUsageBillingOutboxUnavailable
	}
	return intentRepo.MarkDispatched(ctx, ref)
}

func (r *durableUsageBillingOutboxRepository) MarkAttemptFailed(ctx context.Context, ref service.UsageBillingAdmissionAttemptRef) error {
	intentRepo, ok := r.inner.(service.UsageBillingAdmissionRepository)
	if !ok {
		return service.ErrUsageBillingOutboxUnavailable
	}
	return intentRepo.MarkAttemptFailed(ctx, ref)
}

func (r *durableUsageBillingOutboxRepository) MarkOrphaned(ctx context.Context, ref service.UsageBillingAdmissionAttemptRef) error {
	intentRepo, ok := r.inner.(service.UsageBillingAdmissionRepository)
	if !ok {
		return service.ErrUsageBillingOutboxUnavailable
	}
	return intentRepo.MarkOrphaned(ctx, ref)
}

func (r *durableUsageBillingOutboxRepository) Heartbeat(ctx context.Context, ref service.UsageBillingAdmissionAttemptRef, lease time.Duration) error {
	intentRepo, ok := r.inner.(service.UsageBillingAdmissionRepository)
	if !ok {
		return service.ErrUsageBillingOutboxUnavailable
	}
	return intentRepo.Heartbeat(ctx, ref, lease)
}

func (r *durableUsageBillingOutboxRepository) Abandon(ctx context.Context, ref service.UsageBillingAdmissionAttemptRef) error {
	intentRepo, ok := r.inner.(service.UsageBillingAdmissionRepository)
	if !ok {
		return service.ErrUsageBillingOutboxUnavailable
	}
	return intentRepo.Abandon(ctx, ref)
}

func (r *durableUsageBillingOutboxRepository) WaitSettled(ctx context.Context, ref service.UsageBillingAdmissionAttemptRef) error {
	intentRepo, ok := r.inner.(service.UsageBillingAdmissionRepository)
	if !ok {
		return service.ErrUsageBillingOutboxUnavailable
	}
	return intentRepo.WaitSettled(ctx, ref)
}

func (r *durableUsageBillingOutboxRepository) ReconcileStaleAdmissions(
	ctx context.Context,
	preparedGrace time.Duration,
	dispatchedGrace time.Duration,
	limit int,
) (service.UsageBillingAdmissionReconcileResult, error) {
	reconciler, ok := r.inner.(service.UsageBillingAdmissionReconciler)
	if !ok {
		return service.UsageBillingAdmissionReconcileResult{}, service.ErrUsageBillingOutboxUnavailable
	}
	return reconciler.ReconcileStaleAdmissions(ctx, preparedGrace, dispatchedGrace, limit)
}

func (r *durableUsageBillingOutboxRepository) ListUsageBillingReconciliationCases(
	ctx context.Context,
	limit int,
) ([]service.UsageBillingReconciliationCase, error) {
	repo, ok := r.inner.(service.UsageBillingReconciliationRepository)
	if !ok {
		return nil, service.ErrUsageBillingReconciliationUnavailable
	}
	return repo.ListUsageBillingReconciliationCases(ctx, limit)
}

func (r *durableUsageBillingOutboxRepository) ResolveUsageBillingReconciliation(
	ctx context.Context,
	input service.UsageBillingReconciliationResolveInput,
) error {
	repo, ok := r.inner.(service.UsageBillingReconciliationRepository)
	if !ok {
		return service.ErrUsageBillingReconciliationUnavailable
	}
	r.drainMu.Lock()
	defer r.drainMu.Unlock()
	stored, found, err := r.spool.GetByIdentity(input.RequestID, input.APIKeyID)
	if err != nil {
		return err
	}
	if found {
		switch input.Action {
		case service.UsageBillingReconciliationActionSettleDelivered:
			if input.Envelope.Validate() != nil ||
				input.Envelope.RequestFingerprint() != stored.RequestFingerprint() {
				return service.ErrUsageBillingReconciliationConflict
			}
			input.Envelope = stored
		case service.UsageBillingReconciliationActionReleaseUndelivered,
			service.UsageBillingReconciliationActionRetryDeadLetter:
			return service.ErrUsageBillingReconciliationConflict
		}
	}
	if err := repo.ResolveUsageBillingReconciliation(ctx, input); err != nil {
		return err
	}
	if found && input.Action == service.UsageBillingReconciliationActionSettleDelivered {
		name := usageBillingSpoolFileNameForIdentity(input.RequestID, input.APIKeyID)
		if err := r.spool.Remove(name); err != nil {
			slog.Error("remove reconciled usage billing spool entry failed", "entry", name, "error", err)
		}
	}
	return nil
}

type usageBillingOutboxSpool struct {
	dir string
	mu  sync.Mutex
}

func newUsageBillingOutboxSpool(dir string) (*usageBillingOutboxSpool, error) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "." || dir == "" {
		return nil, errors.New("usage billing spool directory is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := syncDirectory(dir); err != nil {
		return nil, err
	}
	return &usageBillingOutboxSpool{dir: dir}, nil
}

func (s *usageBillingOutboxSpool) Put(envelope service.UsageBillingEnvelope) error {
	if s == nil {
		return errors.New("usage billing spool is nil")
	}
	if err := envelope.Validate(); err != nil {
		return err
	}
	raw, err := envelope.MarshalJSON()
	if err != nil {
		return err
	}
	if len(raw) > service.UsageBillingEnvelopeMaxBytes {
		return service.ErrUsageBillingEnvelopeInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name := usageBillingSpoolFileName(envelope)
	finalPath := filepath.Join(s.dir, name)
	if existing, err := os.ReadFile(finalPath); err == nil {
		stored, decodeErr := service.DecodeUsageBillingEnvelope(existing)
		if decodeErr != nil {
			return decodeErr
		}
		if stored.RequestFingerprint() != envelope.RequestFingerprint() {
			return service.ErrUsageBillingRequestConflict
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	temp, err := os.CreateTemp(s.dir, ".pending-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := func() { _ = os.Remove(tempPath) }
	defer cleanup()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Link(tempPath, finalPath); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		existing, readErr := os.ReadFile(finalPath)
		if readErr != nil {
			return readErr
		}
		stored, decodeErr := service.DecodeUsageBillingEnvelope(existing)
		if decodeErr != nil || stored.RequestFingerprint() != envelope.RequestFingerprint() {
			return service.ErrUsageBillingRequestConflict
		}
	}
	if err := syncDirectory(s.dir); err != nil {
		return err
	}
	return nil
}

func (s *usageBillingOutboxSpool) List(limit int) ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}
	return files, nil
}

func (s *usageBillingOutboxSpool) Read(name string) (service.UsageBillingEnvelope, error) {
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
		return service.UsageBillingEnvelope{}, errors.New("invalid usage billing spool file name")
	}
	file, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		return service.UsageBillingEnvelope{}, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, service.UsageBillingEnvelopeMaxBytes+1))
	if err != nil {
		return service.UsageBillingEnvelope{}, err
	}
	return service.DecodeUsageBillingEnvelope(raw)
}

func (s *usageBillingOutboxSpool) GetByIdentity(
	requestID string,
	apiKeyID int64,
) (service.UsageBillingEnvelope, bool, error) {
	name := usageBillingSpoolFileNameForIdentity(requestID, apiKeyID)
	envelope, err := s.Read(name)
	if errors.Is(err, os.ErrNotExist) {
		return service.UsageBillingEnvelope{}, false, nil
	}
	if err != nil {
		return service.UsageBillingEnvelope{}, false, err
	}
	return envelope, true, nil
}

func (s *usageBillingOutboxSpool) Quarantine(name string) error {
	if s == nil || filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
		return errors.New("invalid usage billing spool quarantine entry")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	quarantineDir := filepath.Join(s.dir, "quarantine")
	if err := os.MkdirAll(quarantineDir, 0o700); err != nil {
		return err
	}
	base := strings.TrimSuffix(name, ".json")
	target := filepath.Join(quarantineDir, fmt.Sprintf("%s.%d.json", base, time.Now().UTC().UnixNano()))
	if err := os.Rename(filepath.Join(s.dir, name), target); err != nil {
		return err
	}
	if err := syncDirectory(quarantineDir); err != nil {
		return err
	}
	return syncDirectory(s.dir)
}

func (s *usageBillingOutboxSpool) Remove(name string) error {
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
		return errors.New("invalid usage billing spool file name")
	}
	if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(s.dir)
}

func usageBillingSpoolFileName(envelope service.UsageBillingEnvelope) string {
	return usageBillingSpoolFileNameForIdentity(envelope.RequestID(), envelope.APIKeyID())
}

func usageBillingSpoolFileNameForIdentity(requestID string, apiKeyID int64) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(requestID) + "\x00" + strconv.FormatInt(apiKeyID, 10)))
	return hex.EncodeToString(sum[:]) + ".json"
}

func shouldQuarantineUsageBillingSpoolError(err error) bool {
	for _, permanent := range []error{
		service.ErrUsageBillingEnvelopeInvalid,
		service.ErrUsageBillingEnvelopeVersion,
		service.ErrUsageBillingEnvelopeFingerprintMismatch,
		service.ErrUsageBillingRequestConflict,
		service.ErrUsageBillingCrossTenant,
		service.ErrUsageBillingOutboxTargetNotFound,
		service.ErrUsageBillingAdmissionInvalid,
		service.ErrUsageBillingAdmissionMissing,
		service.ErrUsageBillingAdmissionFinalized,
		service.ErrUsageBillingAdmissionLeaseLost,
	} {
		if errors.Is(err, permanent) {
			return true
		}
	}
	return false
}

func syncDirectory(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := file.Sync(); err != nil {
		// Some development filesystems do not support syncing directories. The
		// production Linux volume does; refuse startup there instead of claiming
		// durability that the filesystem cannot provide.
		slog.Error("usage billing spool directory sync failed", "directory", dir, "error", err)
		return err
	}
	return nil
}

func defaultUsageBillingOutboxSpoolDir() string {
	if dataDir := strings.TrimSpace(os.Getenv("DATA_DIR")); dataDir != "" {
		return filepath.Join(dataDir, "usage-billing-outbox-spool")
	}
	if info, err := os.Stat("/app/data"); err == nil && info.IsDir() {
		return filepath.Join("/app/data", "usage-billing-outbox-spool")
	}
	return filepath.Join("data", "usage-billing-outbox-spool")
}

var _ service.UsageBillingOutboxRepository = (*durableUsageBillingOutboxRepository)(nil)
var _ service.UsageBillingBindingValidator = (*durableUsageBillingOutboxRepository)(nil)
var _ service.UsageBillingAdmissionRepository = (*durableUsageBillingOutboxRepository)(nil)
var _ service.UsageBillingReconciliationRepository = (*durableUsageBillingOutboxRepository)(nil)
