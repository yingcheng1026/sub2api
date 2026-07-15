package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const usageBillingOutboxSelectColumns = `
	id, request_id, api_key_id, request_fingerprint, envelope_version,
	envelope, status, attempt_count, max_attempts, available_at,
	locked_at, locked_by, lease_token, last_attempt_at, completed_at, dead_lettered_at,
	result_code, last_error_code, last_error, created_at, updated_at
`

const walletAdmissionInitialLease = 2 * time.Minute

type usageBillingOutboxRepository struct {
	db                    *sql.DB
	requireAdmission      bool
	useAdmissionIfPresent bool
}

func NewUsageBillingOutboxRepository(db *sql.DB) *usageBillingOutboxRepository {
	return &usageBillingOutboxRepository{db: db}
}

func (r *usageBillingOutboxRepository) Enqueue(ctx context.Context, envelope service.UsageBillingEnvelope) (*service.UsageBillingOutboxEvent, bool, error) {
	if r == nil || r.db == nil {
		return nil, false, errors.New("usage billing outbox repository db is nil")
	}
	if err := envelope.Validate(); err != nil {
		return nil, false, err
	}
	raw, err := envelope.MarshalJSON()
	if err != nil {
		return nil, false, err
	}
	if len(raw) > service.UsageBillingEnvelopeMaxBytes {
		return nil, false, service.ErrUsageBillingEnvelopeInvalid
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, false, markUsageBillingOutboxAdmissionStorageError(err)
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := selectUsageBillingOutboxByIdentity(ctx, tx, envelope.RequestID(), envelope.APIKeyID())
	if err == nil {
		if err := validateExistingUsageBillingOutbox(existing, envelope); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, markUsageBillingOutboxAdmissionStorageError(err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, markUsageBillingOutboxAdmissionStorageError(err)
	}
	useAdmission := r.requireAdmission || envelope.WalletCost() > 0 || envelope.AdmissionAttemptID() != ""
	if !useAdmission && r.useAdmissionIfPresent {
		useAdmission, err = usageBillingAdmissionExists(ctx, tx, envelope.RequestID(), envelope.APIKeyID())
		if err != nil {
			return nil, false, markUsageBillingOutboxAdmissionStorageError(err)
		}
	}
	if useAdmission {
		if err := validateUsageBillingAdmissionAtEnqueue(ctx, tx, envelope); err != nil {
			return nil, false, markUsageBillingOutboxAdmissionStorageError(err)
		}
	} else {
		if err := validateUsageBillingBindingsAtEnqueue(ctx, tx, envelope); err != nil {
			return nil, false, markUsageBillingOutboxAdmissionStorageError(err)
		}
	}

	row := tx.QueryRowContext(ctx, `
		INSERT INTO usage_billing_outbox (
			request_id, api_key_id, request_fingerprint, envelope_version, envelope
		)
		VALUES ($1, $2, $3, $4, $5::jsonb)
		ON CONFLICT (request_id, api_key_id) DO NOTHING
		RETURNING `+usageBillingOutboxSelectColumns,
		envelope.RequestID(), envelope.APIKeyID(), envelope.RequestFingerprint(), envelope.Version(), raw)
	event, err := scanUsageBillingOutboxEvent(row)
	if err == nil {
		if useAdmission {
			if err := finalizeUsageBillingAdmission(ctx, tx, envelope); err != nil {
				return nil, false, markUsageBillingOutboxAdmissionStorageError(err)
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, false, markUsageBillingOutboxAdmissionStorageError(err)
		}
		return event, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, markUsageBillingOutboxAdmissionStorageError(err)
	}

	event, err = selectUsageBillingOutboxByIdentity(ctx, tx, envelope.RequestID(), envelope.APIKeyID())
	if err != nil {
		return nil, false, markUsageBillingOutboxAdmissionStorageError(err)
	}
	if err := validateExistingUsageBillingOutbox(event, envelope); err != nil {
		return nil, false, err
	}
	if useAdmission {
		if err := finalizeUsageBillingAdmission(ctx, tx, envelope); err != nil {
			return nil, false, markUsageBillingOutboxAdmissionStorageError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, markUsageBillingOutboxAdmissionStorageError(err)
	}
	return event, false, nil
}

func usageBillingAdmissionExists(ctx context.Context, q usageBillingBindingQuerier, requestID string, apiKeyID int64) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM usage_billing_admissions
			WHERE request_id = $1 AND api_key_id = $2
		)
	`, strings.TrimSpace(requestID), apiKeyID).Scan(&exists)
	return exists, err
}

func markUsageBillingOutboxAdmissionStorageError(err error) error {
	if err == nil {
		return nil
	}
	// These errors describe a permanently invalid billing fact/binding and must
	// be surfaced to the caller. All other errors originate from the database
	// admission transaction and are safe to retry because Enqueue is idempotent
	// on (request_id, api_key_id).
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
		service.ErrWalletInsufficient,
		service.ErrWalletAdmissionBusy,
	} {
		if errors.Is(err, permanent) {
			return err
		}
	}
	return service.MarkUsageBillingOutboxAdmissionRetryable(err)
}

func (r *usageBillingOutboxRepository) Admit(ctx context.Context, admission service.UsageBillingAdmission) error {
	if r == nil || r.db == nil {
		return service.ErrUsageBillingOutboxUnavailable
	}
	if err := admission.Validate(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return markUsageBillingOutboxAdmissionStorageError(err)
	}
	defer func() { _ = tx.Rollback() }()

	// Lock the subscription before any shared binding reads. All wallet
	// admission and settlement paths use the order wallet -> admission, which
	// serializes multiple application instances without lock-upgrade deadlocks.
	wallet, err := lockUsageBillingAdmissionWallet(ctx, tx, admission)
	if err != nil {
		return err
	}
	validationEnvelope, err := usageBillingAdmissionValidationEnvelope(admission)
	if err != nil {
		return err
	}
	if err := validateUsageBillingBindingsAtAdmission(ctx, tx, validationEnvelope); err != nil {
		return markUsageBillingOutboxAdmissionStorageError(err)
	}

	existing, err := lockUsageBillingAdmission(ctx, tx, admission.RequestID(), admission.APIKeyID())
	switch {
	case err == nil && (existing.state == service.UsageBillingAdmissionStateOutboxPending || existing.state == service.UsageBillingAdmissionStateSettled):
		return service.ErrUsageBillingAdmissionFinalized
	case err == nil && existing.state != service.UsageBillingAdmissionStatePrepared && existing.state != service.UsageBillingAdmissionStateDispatched:
		return service.ErrUsageBillingAdmissionLeaseLost
	case err == nil && !existing.matchesStableBinding(admission):
		return service.ErrUsageBillingRequestConflict
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return markUsageBillingOutboxAdmissionStorageError(err)
	}

	walletSubscriptionID, reservedUSD, leaseExpiresAt, err := prepareUsageBillingWalletHold(
		ctx, tx, admission, wallet, existing,
	)
	if err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO usage_billing_admissions (
			request_id, api_key_id, binding_fingerprint, owner_token, request_payload_hash, auth_cache_locator,
			user_id, subscription_id, group_id, effective_billing_group_id,
			billing_type, billing_model, pricing_source, pricing_revision, pricing_hash,
			rate_multiplier, worst_case_cost_usd,
			alternate_billing_model, alternate_pricing_source, alternate_pricing_revision,
			alternate_pricing_hash, alternate_rate_multiplier, state,
			wallet_subscription_id, wallet_reserved_usd, wallet_lease_expires_at,
			wallet_consumed_at, wallet_released_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,
			NULLIF($18,''),NULLIF($19,''),NULLIF($20,''),NULLIF($21,''),NULLIF($22,0),'prepared',
			$23,$24,$25,NULL,NULL,NOW()
		)
		ON CONFLICT (request_id, api_key_id) DO UPDATE SET
			wallet_lease_expires_at = EXCLUDED.wallet_lease_expires_at,
			updated_at = NOW()
		WHERE usage_billing_admissions.state IN ('prepared','dispatched')
		  AND usage_billing_admissions.binding_fingerprint = EXCLUDED.binding_fingerprint
		  AND usage_billing_admissions.owner_token = EXCLUDED.owner_token
	`, admission.RequestID(), admission.APIKeyID(), admission.Fingerprint(), admission.OwnerToken(), admission.RequestPayloadHash(), admission.AuthCacheLocator(),
		admission.UserID(), admission.SubscriptionID(), admission.GroupID(),
		admission.EffectiveBillingGroupID(), admission.BillingType(),
		admission.BillingModel(), admission.PricingSource(), admission.PricingRevision(), admission.PricingHash(),
		admission.RateMultiplier(), admission.WorstCaseCostUSD(),
		admission.AlternateBillingModel(), admission.AlternatePricingSource(), admission.AlternatePricingRevision(),
		admission.AlternatePricingHash(), admission.AlternateRateMultiplier(),
		walletSubscriptionID, reservedUSD, leaseExpiresAt)
	if err != nil {
		return markUsageBillingOutboxAdmissionStorageError(err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return markUsageBillingOutboxAdmissionStorageError(err)
	}
	if updated != 1 {
		return service.ErrUsageBillingAdmissionLeaseLost
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO usage_billing_admission_attempts (
			request_id, api_key_id, attempt_id, owner_token, attempt_fingerprint,
			account_id, account_type, state, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,'prepared',NOW())
		ON CONFLICT (request_id, api_key_id, attempt_id) DO UPDATE SET
			updated_at = NOW()
		WHERE usage_billing_admission_attempts.owner_token = EXCLUDED.owner_token
		  AND usage_billing_admission_attempts.attempt_fingerprint = EXCLUDED.attempt_fingerprint
		  AND usage_billing_admission_attempts.state = 'prepared'
	`, admission.RequestID(), admission.APIKeyID(), admission.AttemptID(), admission.OwnerToken(),
		admission.AttemptFingerprint(), admission.AccountID(), admission.AccountType()); err != nil {
		return markUsageBillingOutboxAdmissionStorageError(err)
	}
	if err := tx.Commit(); err != nil {
		return markUsageBillingOutboxAdmissionStorageError(err)
	}
	return nil
}

type usageBillingAdmissionWallet struct {
	id      int64
	balance float64
}

func lockUsageBillingAdmissionWallet(ctx context.Context, tx *sql.Tx, admission service.UsageBillingAdmission) (*usageBillingAdmissionWallet, error) {
	subscriptionID := admission.SubscriptionID()
	if subscriptionID == nil {
		return nil, nil
	}
	var (
		userID    int64
		balance   sql.NullFloat64
		status    string
		expiresAt time.Time
	)
	err := tx.QueryRowContext(ctx, `
		SELECT user_id, wallet_balance_usd, status, expires_at
		FROM user_subscriptions
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE
	`, *subscriptionID).Scan(&userID, &balance, &status, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		// The authoritative binding validator below returns the public target
		// error. A missing row is not treated as a wallet here.
		return nil, nil
	}
	if err != nil {
		return nil, markUsageBillingOutboxAdmissionStorageError(err)
	}
	if !balance.Valid {
		return nil, nil
	}
	if userID != admission.UserID() {
		return nil, service.ErrUsageBillingCrossTenant
	}
	if strings.TrimSpace(status) != service.SubscriptionStatusActive || !expiresAt.After(time.Now()) {
		return nil, service.ErrWalletInsufficient
	}
	return &usageBillingAdmissionWallet{id: *subscriptionID, balance: balance.Float64}, nil
}

type usageBillingAdmissionState struct {
	bindingFingerprint       string
	ownerToken               string
	requestPayloadHash       string
	state                    string
	authCacheLocator         string
	userID                   int64
	subscriptionID           sql.NullInt64
	groupID                  int64
	effectiveBillingGroupID  int64
	billingType              int8
	billingModel             string
	pricingSource            string
	pricingRevision          string
	pricingHash              string
	rateMultiplier           float64
	worstCaseCostUSD         float64
	alternateBillingModel    string
	alternatePricingSource   string
	alternatePricingRevision string
	alternatePricingHash     string
	alternateRateMultiplier  float64
	walletSubscriptionID     sql.NullInt64
	walletReservedUSD        float64
	walletConsumedAt         sql.NullTime
	walletReleasedAt         sql.NullTime
}

func lockUsageBillingAdmission(ctx context.Context, tx *sql.Tx, requestID string, apiKeyID int64) (*usageBillingAdmissionState, error) {
	state := &usageBillingAdmissionState{}
	err := tx.QueryRowContext(ctx, `
		SELECT binding_fingerprint, owner_token, request_payload_hash,
			state, COALESCE(auth_cache_locator, ''), user_id, subscription_id,
			group_id, effective_billing_group_id, billing_type,
			billing_model, pricing_source, pricing_revision, pricing_hash,
			rate_multiplier, worst_case_cost_usd,
			COALESCE(alternate_billing_model, ''), COALESCE(alternate_pricing_source, ''),
			COALESCE(alternate_pricing_revision, ''), COALESCE(alternate_pricing_hash, ''),
			COALESCE(alternate_rate_multiplier, 0),
			wallet_subscription_id, wallet_reserved_usd,
			wallet_consumed_at, wallet_released_at
		FROM usage_billing_admissions
		WHERE request_id = $1 AND api_key_id = $2
		FOR UPDATE
	`, requestID, apiKeyID).Scan(
		&state.bindingFingerprint, &state.ownerToken, &state.requestPayloadHash,
		&state.state, &state.authCacheLocator, &state.userID, &state.subscriptionID,
		&state.groupID, &state.effectiveBillingGroupID, &state.billingType,
		&state.billingModel, &state.pricingSource, &state.pricingRevision, &state.pricingHash,
		&state.rateMultiplier, &state.worstCaseCostUSD,
		&state.alternateBillingModel, &state.alternatePricingSource, &state.alternatePricingRevision,
		&state.alternatePricingHash, &state.alternateRateMultiplier,
		&state.walletSubscriptionID, &state.walletReservedUSD,
		&state.walletConsumedAt, &state.walletReleasedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return state, err
}

func (s *usageBillingAdmissionState) matchesStableBinding(admission service.UsageBillingAdmission) bool {
	if s == nil {
		return false
	}
	return strings.EqualFold(s.bindingFingerprint, admission.Fingerprint()) &&
		strings.EqualFold(s.ownerToken, admission.OwnerToken()) &&
		strings.EqualFold(s.requestPayloadHash, admission.RequestPayloadHash()) &&
		strings.EqualFold(s.authCacheLocator, admission.AuthCacheLocator()) &&
		s.userID == admission.UserID() &&
		nullInt64Matches(s.subscriptionID, admission.SubscriptionID()) &&
		s.groupID == int64Value(admission.GroupID()) &&
		s.effectiveBillingGroupID == int64Value(admission.EffectiveBillingGroupID()) &&
		s.billingType == admission.BillingType() &&
		s.billingModel == admission.BillingModel() &&
		s.pricingSource == admission.PricingSource() &&
		s.pricingRevision == admission.PricingRevision() &&
		strings.EqualFold(s.pricingHash, admission.PricingHash()) &&
		math.Abs(s.rateMultiplier-admission.RateMultiplier()) <= 1e-12 &&
		s.alternateBillingModel == admission.AlternateBillingModel() &&
		s.alternatePricingSource == admission.AlternatePricingSource() &&
		s.alternatePricingRevision == admission.AlternatePricingRevision() &&
		strings.EqualFold(s.alternatePricingHash, admission.AlternatePricingHash()) &&
		math.Abs(s.alternateRateMultiplier-admission.AlternateRateMultiplier()) <= 1e-12 &&
		math.Abs(s.worstCaseCostUSD-admission.WorstCaseCostUSD()) <= 1e-12
}

func nullInt64Matches(stored sql.NullInt64, value *int64) bool {
	if value == nil {
		return !stored.Valid
	}
	return stored.Valid && stored.Int64 == *value
}

func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func prepareUsageBillingWalletHold(
	ctx context.Context,
	tx *sql.Tx,
	admission service.UsageBillingAdmission,
	wallet *usageBillingAdmissionWallet,
	existing *usageBillingAdmissionState,
) (*int64, float64, any, error) {
	if wallet == nil {
		if existing != nil && existing.walletSubscriptionID.Valid {
			return nil, 0, nil, service.ErrUsageBillingRequestConflict
		}
		return nil, 0, nil, nil
	}
	if existing != nil {
		if !existing.walletSubscriptionID.Valid || existing.walletSubscriptionID.Int64 != wallet.id ||
			existing.walletConsumedAt.Valid || existing.walletReleasedAt.Valid || existing.walletReservedUSD <= 0 {
			return nil, 0, nil, service.ErrUsageBillingRequestConflict
		}
	}

	var outstandingUSD float64
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(wallet_reserved_usd), 0)
		FROM usage_billing_admissions
		WHERE wallet_subscription_id = $1
		  AND wallet_consumed_at IS NULL
		  AND wallet_released_at IS NULL
		  AND NOT (request_id = $2 AND api_key_id = $3)
	`, wallet.id, admission.RequestID(), admission.APIKeyID()).Scan(&outstandingUSD)
	if err != nil {
		return nil, 0, nil, markUsageBillingOutboxAdmissionStorageError(err)
	}

	reservedUSD := admission.WorstCaseCostUSD()
	if existing != nil {
		reservedUSD = existing.walletReservedUSD
	}
	if reservedUSD <= 0 || wallet.balance-outstandingUSD+1e-12 < reservedUSD {
		return nil, 0, nil, service.ErrWalletInsufficient
	}
	walletID := wallet.id
	return &walletID, reservedUSD, time.Now().Add(walletAdmissionInitialLease), nil
}

func (r *usageBillingOutboxRepository) MarkDispatched(ctx context.Context, ref service.UsageBillingAdmissionAttemptRef) error {
	if r == nil || r.db == nil {
		return service.ErrUsageBillingOutboxUnavailable
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE usage_billing_admissions
		SET state = 'dispatched', dispatched_at = COALESCE(dispatched_at, NOW()), updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND owner_token = $3
		  AND state IN ('prepared','dispatched')
	`, strings.TrimSpace(ref.RequestID), ref.APIKeyID, strings.TrimSpace(ref.OwnerToken))
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return service.ErrUsageBillingAdmissionLeaseLost
	}
	attemptResult, err := tx.ExecContext(ctx, `
		UPDATE usage_billing_admission_attempts
		SET state = 'dispatched', dispatched_at = COALESCE(dispatched_at, NOW()), updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND attempt_id = $3
		  AND owner_token = $4 AND state = 'prepared'
	`, strings.TrimSpace(ref.RequestID), ref.APIKeyID, strings.TrimSpace(ref.AttemptID), strings.TrimSpace(ref.OwnerToken))
	if err != nil {
		return err
	}
	attemptUpdated, err := attemptResult.RowsAffected()
	if err != nil {
		return err
	}
	if attemptUpdated != 1 {
		return service.ErrUsageBillingAdmissionLeaseLost
	}
	return tx.Commit()
}

func (r *usageBillingOutboxRepository) MarkAttemptFailed(ctx context.Context, ref service.UsageBillingAdmissionAttemptRef) error {
	if r == nil || r.db == nil {
		return service.ErrUsageBillingOutboxUnavailable
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE usage_billing_admission_attempts
		SET state = 'failed', failed_at = COALESCE(failed_at, NOW()), updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND attempt_id = $3
		  AND owner_token = $4 AND state IN ('prepared','dispatched')
	`, strings.TrimSpace(ref.RequestID), ref.APIKeyID, strings.TrimSpace(ref.AttemptID), strings.TrimSpace(ref.OwnerToken))
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return service.ErrUsageBillingAdmissionLeaseLost
	}
	return nil
}

func (r *usageBillingOutboxRepository) MarkOrphaned(ctx context.Context, ref service.UsageBillingAdmissionAttemptRef) error {
	if r == nil || r.db == nil {
		return service.ErrUsageBillingOutboxUnavailable
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE usage_billing_admissions
		SET state = 'orphaned', orphaned_at = COALESCE(orphaned_at, NOW()), updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND owner_token = $3
		  AND state IN ('prepared','dispatched')
	`, strings.TrimSpace(ref.RequestID), ref.APIKeyID, strings.TrimSpace(ref.OwnerToken))
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return service.ErrUsageBillingAdmissionLeaseLost
	}
	return nil
}

func (r *usageBillingOutboxRepository) Heartbeat(ctx context.Context, ref service.UsageBillingAdmissionAttemptRef, lease time.Duration) error {
	if r == nil || r.db == nil {
		return service.ErrUsageBillingOutboxUnavailable
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE usage_billing_admissions
		SET wallet_lease_expires_at = CASE WHEN wallet_subscription_id IS NULL THEN wallet_lease_expires_at ELSE NOW() + ($3 * INTERVAL '1 millisecond') END,
			updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND owner_token = $4
		  AND state IN ('prepared','dispatched')
	`, strings.TrimSpace(ref.RequestID), ref.APIKeyID, lease.Milliseconds(), strings.TrimSpace(ref.OwnerToken))
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return service.ErrUsageBillingAdmissionLeaseLost
	}
	return nil
}

func (r *usageBillingOutboxRepository) Abandon(ctx context.Context, ref service.UsageBillingAdmissionAttemptRef) error {
	if r == nil || r.db == nil {
		return service.ErrUsageBillingOutboxUnavailable
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE usage_billing_admissions
		SET state = 'abandoned', abandoned_at = NOW(),
			wallet_released_at = CASE WHEN wallet_subscription_id IS NULL THEN wallet_released_at ELSE COALESCE(wallet_released_at, NOW()) END,
			updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND owner_token = $3
		  AND (
			  state = 'prepared'
			  OR (state = 'dispatched' AND NOT EXISTS (
				  SELECT 1 FROM usage_billing_admission_attempts attempt
				  WHERE attempt.request_id = usage_billing_admissions.request_id
					AND attempt.api_key_id = usage_billing_admissions.api_key_id
					AND attempt.state IN ('prepared','dispatched','finalized')
			  ))
		  )
	`, strings.TrimSpace(ref.RequestID), ref.APIKeyID, strings.TrimSpace(ref.OwnerToken))
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return service.ErrUsageBillingAdmissionLeaseLost
	}
	return nil
}

func (r *usageBillingOutboxRepository) WaitSettled(ctx context.Context, ref service.UsageBillingAdmissionAttemptRef) error {
	if r == nil || r.db == nil {
		return service.ErrUsageBillingOutboxUnavailable
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	var state string
	err := r.db.QueryRowContext(ctx, `
		SELECT state
		FROM usage_billing_admissions
		WHERE request_id = $1 AND api_key_id = $2 AND owner_token = $3
	`, strings.TrimSpace(ref.RequestID), ref.APIKeyID, strings.TrimSpace(ref.OwnerToken)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingAdmissionMissing
	}
	if err != nil {
		return err
	}
	if state == service.UsageBillingAdmissionStateSettled || state == service.UsageBillingAdmissionStateOutboxPending {
		return nil
	}
	if state == service.UsageBillingAdmissionStateOrphaned || state == service.UsageBillingAdmissionStateReconcile {
		return service.ErrUsageBillingAdmissionOrphaned
	}
	return service.ErrUsageBillingAdmissionLeaseLost
}

func (r *usageBillingOutboxRepository) ReconcileStaleAdmissions(
	ctx context.Context,
	preparedGrace time.Duration,
	dispatchedGrace time.Duration,
	limit int,
) (service.UsageBillingAdmissionReconcileResult, error) {
	var reconciled service.UsageBillingAdmissionReconcileResult
	if r == nil || r.db == nil {
		return reconciled, service.ErrUsageBillingOutboxUnavailable
	}
	if limit <= 0 {
		return reconciled, nil
	}
	if preparedGrace <= 0 || dispatchedGrace <= 0 {
		return reconciled, service.ErrUsageBillingAdmissionInvalid
	}
	if limit > 100 {
		limit = 100
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return reconciled, err
	}
	defer func() { _ = tx.Rollback() }()

	remaining := limit
	if reconciled.AbandonedPrepared, err = reconcilePreparedAdmissions(ctx, tx, preparedGrace, remaining); err != nil {
		return service.UsageBillingAdmissionReconcileResult{}, err
	}
	remaining -= reconciled.AbandonedPrepared
	if remaining > 0 {
		reconciled.AbandonedFailedDispatched, err = reconcileFailedDispatchedAdmissions(ctx, tx, preparedGrace, remaining)
		if err != nil {
			return service.UsageBillingAdmissionReconcileResult{}, err
		}
		remaining -= reconciled.AbandonedFailedDispatched
	}
	if remaining > 0 {
		reconciled.OrphanedDispatched, err = reconcileUnknownDispatchedAdmissions(ctx, tx, dispatchedGrace, remaining)
		if err != nil {
			return service.UsageBillingAdmissionReconcileResult{}, err
		}
		remaining -= reconciled.OrphanedDispatched
	}
	if remaining > 0 {
		reconciled.FlaggedDeadLetter, err = reconcileDeadLetterAdmissions(ctx, tx, remaining)
		if err != nil {
			return service.UsageBillingAdmissionReconcileResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return service.UsageBillingAdmissionReconcileResult{}, err
	}
	return reconciled, nil
}

func reconcilePreparedAdmissions(ctx context.Context, tx *sql.Tx, grace time.Duration, limit int) (int, error) {
	result, err := tx.ExecContext(ctx, `
		WITH candidates AS (
			SELECT admission.request_id, admission.api_key_id
			FROM usage_billing_admissions admission
			WHERE admission.state = 'prepared'
			  AND admission.updated_at <= NOW() - ($1 * INTERVAL '1 millisecond')
			  AND (admission.wallet_lease_expires_at IS NULL OR admission.wallet_lease_expires_at <= NOW())
			  AND NOT EXISTS (
				  SELECT 1 FROM usage_billing_outbox outbox
				  WHERE outbox.request_id = admission.request_id
					AND outbox.api_key_id = admission.api_key_id
			  )
			  AND NOT EXISTS (
				  SELECT 1 FROM usage_billing_admission_attempts attempt
				  WHERE attempt.request_id = admission.request_id
					AND attempt.api_key_id = admission.api_key_id
					AND attempt.state IN ('dispatched','finalized')
			  )
			ORDER BY admission.updated_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		), failed_attempts AS (
			UPDATE usage_billing_admission_attempts attempt
			SET state = 'failed', failed_at = COALESCE(failed_at, NOW()), updated_at = NOW()
			FROM candidates candidate
			WHERE attempt.request_id = candidate.request_id
			  AND attempt.api_key_id = candidate.api_key_id
			  AND attempt.state = 'prepared'
			RETURNING attempt.request_id
		)
		UPDATE usage_billing_admissions admission
		SET state = 'abandoned', abandoned_at = COALESCE(abandoned_at, NOW()),
			wallet_released_at = CASE WHEN wallet_subscription_id IS NULL THEN wallet_released_at ELSE COALESCE(wallet_released_at, NOW()) END,
			updated_at = NOW()
		FROM candidates candidate
		WHERE admission.request_id = candidate.request_id
		  AND admission.api_key_id = candidate.api_key_id
	`, grace.Milliseconds(), limit)
	if err != nil {
		return 0, err
	}
	return usageBillingReconcileRowsAffected(result)
}

func reconcileFailedDispatchedAdmissions(ctx context.Context, tx *sql.Tx, grace time.Duration, limit int) (int, error) {
	result, err := tx.ExecContext(ctx, `
		WITH candidates AS (
			SELECT admission.request_id, admission.api_key_id
			FROM usage_billing_admissions admission
			WHERE admission.state = 'dispatched'
			  AND admission.updated_at <= NOW() - ($1 * INTERVAL '1 millisecond')
			  AND NOT EXISTS (
				  SELECT 1 FROM usage_billing_outbox outbox
				  WHERE outbox.request_id = admission.request_id
					AND outbox.api_key_id = admission.api_key_id
			  )
			  AND EXISTS (
				  SELECT 1 FROM usage_billing_admission_attempts attempt
				  WHERE attempt.request_id = admission.request_id
					AND attempt.api_key_id = admission.api_key_id
					AND attempt.state = 'failed'
			  )
			  AND NOT EXISTS (
				  SELECT 1 FROM usage_billing_admission_attempts attempt
				  WHERE attempt.request_id = admission.request_id
					AND attempt.api_key_id = admission.api_key_id
					AND attempt.state IN ('prepared','dispatched','finalized')
			  )
			ORDER BY admission.updated_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE usage_billing_admissions admission
		SET state = 'abandoned', abandoned_at = COALESCE(abandoned_at, NOW()),
			wallet_released_at = CASE WHEN wallet_subscription_id IS NULL THEN wallet_released_at ELSE COALESCE(wallet_released_at, NOW()) END,
			updated_at = NOW()
		FROM candidates candidate
		WHERE admission.request_id = candidate.request_id
		  AND admission.api_key_id = candidate.api_key_id
	`, grace.Milliseconds(), limit)
	if err != nil {
		return 0, err
	}
	return usageBillingReconcileRowsAffected(result)
}

func reconcileUnknownDispatchedAdmissions(ctx context.Context, tx *sql.Tx, grace time.Duration, limit int) (int, error) {
	result, err := tx.ExecContext(ctx, `
		WITH candidates AS (
			SELECT admission.request_id, admission.api_key_id
			FROM usage_billing_admissions admission
			WHERE admission.state = 'dispatched'
			  AND admission.updated_at <= NOW() - ($1 * INTERVAL '1 millisecond')
			  AND NOT EXISTS (
				  SELECT 1 FROM usage_billing_outbox outbox
				  WHERE outbox.request_id = admission.request_id
					AND outbox.api_key_id = admission.api_key_id
			  )
			  AND (
				  EXISTS (
					  SELECT 1 FROM usage_billing_admission_attempts attempt
					  WHERE attempt.request_id = admission.request_id
						AND attempt.api_key_id = admission.api_key_id
						AND attempt.state IN ('prepared','dispatched','finalized')
				  )
				  OR NOT EXISTS (
					  SELECT 1 FROM usage_billing_admission_attempts attempt
					  WHERE attempt.request_id = admission.request_id
						AND attempt.api_key_id = admission.api_key_id
				  )
			  )
			ORDER BY admission.updated_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE usage_billing_admissions admission
		SET state = 'orphaned', orphaned_at = COALESCE(orphaned_at, NOW()), updated_at = NOW()
		FROM candidates candidate
		WHERE admission.request_id = candidate.request_id
		  AND admission.api_key_id = candidate.api_key_id
	`, grace.Milliseconds(), limit)
	if err != nil {
		return 0, err
	}
	return usageBillingReconcileRowsAffected(result)
}

func reconcileDeadLetterAdmissions(ctx context.Context, tx *sql.Tx, limit int) (int, error) {
	result, err := tx.ExecContext(ctx, `
		WITH candidates AS (
			SELECT admission.request_id, admission.api_key_id
			FROM usage_billing_admissions admission
			JOIN usage_billing_outbox outbox
			  ON outbox.request_id = admission.request_id
			 AND outbox.api_key_id = admission.api_key_id
			WHERE admission.state = 'outbox_pending'
			  AND outbox.status = 'dead_letter'
			ORDER BY outbox.dead_lettered_at, outbox.id
			FOR UPDATE OF admission SKIP LOCKED
			LIMIT $1
		)
		UPDATE usage_billing_admissions admission
		SET state = 'reconcile', reconcile_started_at = COALESCE(reconcile_started_at, NOW()), updated_at = NOW()
		FROM candidates candidate
		WHERE admission.request_id = candidate.request_id
		  AND admission.api_key_id = candidate.api_key_id
	`, limit)
	if err != nil {
		return 0, err
	}
	return usageBillingReconcileRowsAffected(result)
}

func usageBillingReconcileRowsAffected(result sql.Result) (int, error) {
	count, err := result.RowsAffected()
	return int(count), err
}

func usageBillingAdmissionValidationEnvelope(admission service.UsageBillingAdmission) (service.UsageBillingEnvelope, error) {
	return service.NewUsageBillingEnvelope(service.UsageBillingEnvelopeInput{
		RequestID: admission.RequestID(), RequestPayloadHash: admission.RequestPayloadHash(),
		AdmissionAttemptID: admission.AttemptID(), APIKeyID: admission.APIKeyID(),
		AuthCacheLocator: admission.AuthCacheLocator(), UserID: admission.UserID(), AccountID: admission.AccountID(),
		SubscriptionID: admission.SubscriptionID(), GroupID: admission.GroupID(),
		EffectiveBillingGroupID: admission.EffectiveBillingGroupID(), AccountType: admission.AccountType(),
		BillingModel: admission.BillingModel(), BillingType: admission.BillingType(),
		PricingSource: admission.PricingSource(), PricingRevision: admission.PricingRevision(),
		PricingHash: admission.PricingHash(), RateMultiplier: admission.RateMultiplier(), AccountRateMultiplier: 1,
	})
}

func validateUsageBillingAdmissionAtEnqueue(ctx context.Context, q usageBillingBindingQuerier, envelope service.UsageBillingEnvelope) error {
	return validateUsageBillingAdmissionForEnvelope(ctx, q, envelope, usageBillingAdmissionSettlementStateMatches)
}

func validateUsageBillingAdmissionAtReconciliation(
	ctx context.Context,
	q usageBillingBindingQuerier,
	envelope service.UsageBillingEnvelope,
) error {
	return validateUsageBillingAdmissionForEnvelope(ctx, q, envelope, usageBillingAdmissionReconciliationStateMatches)
}

func validateUsageBillingAdmissionForEnvelope(
	ctx context.Context,
	q usageBillingBindingQuerier,
	envelope service.UsageBillingEnvelope,
	stateMatches func(string, string) bool,
) error {
	var (
		fingerprint, ownerToken, requestPayloadHash, authLocator, state, attemptID, accountType, attemptState string
		billingModel, pricingSource, pricingRevision, pricingHash                                             string
		alternateBillingModel, alternatePricingSource, alternatePricingRevision, alternatePricingHash         string
		userID, accountID, groupID, effectiveGroupID                                                          int64
		subscriptionID, walletSubscriptionID                                                                  sql.NullInt64
		billingType                                                                                           int8
		rateMultiplier, worstCaseCostUSD, alternateRateMultiplier                                             float64
	)
	err := q.QueryRowContext(ctx, `
		SELECT a.binding_fingerprint, a.owner_token, a.request_payload_hash, COALESCE(a.auth_cache_locator, ''),
			a.user_id, a.subscription_id, a.group_id, a.effective_billing_group_id,
			a.billing_type, COALESCE(a.billing_model, ''), COALESCE(a.pricing_source, ''),
			COALESCE(a.pricing_revision, ''), COALESCE(a.pricing_hash, ''),
			a.rate_multiplier, a.worst_case_cost_usd,
			COALESCE(a.alternate_billing_model, ''), COALESCE(a.alternate_pricing_source, ''),
			COALESCE(a.alternate_pricing_revision, ''), COALESCE(a.alternate_pricing_hash, ''),
			COALESCE(a.alternate_rate_multiplier, 0), a.wallet_subscription_id, a.state,
			attempt.attempt_id, attempt.account_id, attempt.account_type, attempt.state
		FROM usage_billing_admissions a
		JOIN usage_billing_admission_attempts attempt
		  ON attempt.request_id = a.request_id
		 AND attempt.api_key_id = a.api_key_id
			 AND attempt.owner_token = a.owner_token
			 AND attempt.account_id = $3
			 AND attempt.account_type = $4
			 AND attempt.attempt_id = $5
		WHERE a.request_id = $1 AND a.api_key_id = $2
		FOR UPDATE
	`, envelope.RequestID(), envelope.APIKeyID(), envelope.AccountID(), envelope.AccountType(), envelope.AdmissionAttemptID()).Scan(
		&fingerprint, &ownerToken, &requestPayloadHash, &authLocator, &userID, &subscriptionID, &groupID,
		&effectiveGroupID, &billingType, &billingModel, &pricingSource, &pricingRevision,
		&pricingHash, &rateMultiplier, &worstCaseCostUSD,
		&alternateBillingModel, &alternatePricingSource, &alternatePricingRevision,
		&alternatePricingHash, &alternateRateMultiplier, &walletSubscriptionID, &state,
		&attemptID, &accountID, &accountType, &attemptState,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingAdmissionMissing
	}
	if err != nil {
		return err
	}
	if stateMatches == nil || !stateMatches(state, attemptState) {
		return service.ErrUsageBillingAdmissionLeaseLost
	}
	var subID *int64
	if subscriptionID.Valid {
		value := subscriptionID.Int64
		subID = &value
	}
	admission, err := service.NewUsageBillingAdmission(service.UsageBillingAdmissionInput{
		RequestID: envelope.RequestID(), APIKeyID: envelope.APIKeyID(), AuthCacheLocator: authLocator,
		UserID: userID, AccountID: accountID, SubscriptionID: subID,
		GroupID: &groupID, EffectiveBillingGroupID: &effectiveGroupID,
		AccountType: accountType, BillingType: billingType,
		OwnerToken: ownerToken, AttemptID: attemptID,
		BillingModel: billingModel, RequestPayloadHash: requestPayloadHash, PricingSource: pricingSource,
		PricingRevision: pricingRevision, PricingHash: pricingHash,
		RateMultiplier: rateMultiplier, WorstCaseCostUSD: worstCaseCostUSD,
		AlternateBillingModel: alternateBillingModel, AlternatePricingSource: alternatePricingSource,
		AlternatePricingRevision: alternatePricingRevision, AlternatePricingHash: alternatePricingHash,
		AlternateRateMultiplier: alternateRateMultiplier,
	})
	if err != nil {
		return err
	}
	if admission.Fingerprint() != fingerprint || !admission.MatchesEnvelope(envelope) ||
		envelope.PrimaryBillingCost() > admission.WorstCaseCostUSD()+1e-12 ||
		!usageBillingAdmissionCostFamilyMatches(walletSubscriptionID, billingType, subscriptionID, envelope) {
		return service.ErrUsageBillingRequestConflict
	}
	return nil
}

func usageBillingAdmissionSettlementStateMatches(admissionState, attemptState string) bool {
	return (admissionState == service.UsageBillingAdmissionStateDispatched &&
		attemptState == service.UsageBillingAttemptStateDispatched) ||
		(admissionState == service.UsageBillingAdmissionStateOutboxPending &&
			attemptState == service.UsageBillingAttemptStateFinalized)
}

func usageBillingAdmissionReconciliationStateMatches(admissionState, attemptState string) bool {
	return (admissionState == service.UsageBillingAdmissionStateOrphaned ||
		admissionState == service.UsageBillingAdmissionStateReconcile) &&
		attemptState == service.UsageBillingAttemptStateDispatched
}

func usageBillingAdmissionCostFamilyMatches(
	walletSubscriptionID sql.NullInt64,
	billingType int8,
	subscriptionID sql.NullInt64,
	envelope service.UsageBillingEnvelope,
) bool {
	switch {
	case walletSubscriptionID.Valid:
		return billingType == service.BillingTypeSubscription && subscriptionID.Valid &&
			walletSubscriptionID.Int64 == subscriptionID.Int64 &&
			envelope.BalanceCost() == 0 && envelope.SubscriptionCost() == 0
	case billingType == service.BillingTypeSubscription:
		return subscriptionID.Valid && envelope.BalanceCost() == 0 && envelope.WalletCost() == 0
	case billingType == service.BillingTypeBalance:
		return !subscriptionID.Valid && envelope.SubscriptionCost() == 0 && envelope.WalletCost() == 0
	default:
		return false
	}
}

func finalizeUsageBillingAdmission(ctx context.Context, q usageBillingBindingQuerier, envelope service.UsageBillingEnvelope) error {
	result, err := q.ExecContext(ctx, `
		UPDATE usage_billing_admissions
		SET state = 'outbox_pending', outbox_pending_at = COALESCE(outbox_pending_at, NOW()), updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND state IN ('dispatched', 'outbox_pending')
	`, envelope.RequestID(), envelope.APIKeyID())
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return service.ErrUsageBillingAdmissionMissing
	}
	attemptResult, err := q.ExecContext(ctx, `
		UPDATE usage_billing_admission_attempts
		SET state = 'finalized', finalized_at = COALESCE(finalized_at, NOW()), updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2
		  AND attempt_id = $3
		  AND state IN ('dispatched', 'finalized')
	`, envelope.RequestID(), envelope.APIKeyID(), envelope.AdmissionAttemptID())
	if err != nil {
		return err
	}
	attemptUpdated, err := attemptResult.RowsAffected()
	if err != nil {
		return err
	}
	if attemptUpdated != 1 {
		return service.ErrUsageBillingAdmissionMissing
	}
	return nil
}

func selectUsageBillingOutboxByIdentity(ctx context.Context, q usageBillingBindingQuerier, requestID string, apiKeyID int64) (*service.UsageBillingOutboxEvent, error) {
	row := q.QueryRowContext(ctx, `
		SELECT `+usageBillingOutboxSelectColumns+`
		FROM usage_billing_outbox
		WHERE request_id = $1 AND api_key_id = $2
	`, requestID, apiKeyID)
	return scanUsageBillingOutboxEvent(row)
}

func validateExistingUsageBillingOutbox(event *service.UsageBillingOutboxEvent, envelope service.UsageBillingEnvelope) error {
	if event == nil {
		return sql.ErrNoRows
	}
	if event.EnvelopeError != nil {
		return event.EnvelopeError
	}
	if event.Envelope.RequestFingerprint() != envelope.RequestFingerprint() {
		return service.ErrUsageBillingRequestConflict
	}
	return nil
}

func (r *usageBillingOutboxRepository) Claim(ctx context.Context, owner string, limit int, lease time.Duration) ([]service.UsageBillingOutboxEvent, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing outbox repository db is nil")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" || len(owner) > 128 {
		return nil, errors.New("usage billing outbox owner is invalid")
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	if lease <= 0 {
		lease = service.UsageBillingOutboxLeaseDuration
	}
	leaseSeconds := int64(lease / time.Second)
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}
	leaseToken, err := newUsageBillingLeaseToken()
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT id
			FROM usage_billing_outbox
			WHERE status IN ('pending', 'retry')
			  AND available_at <= NOW()
			  AND (
				status = 'retry'
				OR attempt_count < max_attempts
				OR (
					attempt_count >= max_attempts
					AND locked_at IS NOT NULL
					AND locked_at <= NOW() - ($3 * INTERVAL '1 second')
				)
			  )
			  AND (locked_at IS NULL OR locked_at <= NOW() - ($3 * INTERVAL '1 second'))
			ORDER BY available_at, id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE usage_billing_outbox AS outbox
		SET locked_at = NOW(),
			locked_by = $2,
			lease_token = $4,
			last_attempt_at = NOW(),
			attempt_count = LEAST(outbox.attempt_count + 1, outbox.max_attempts),
			updated_at = NOW()
		FROM candidates
		WHERE outbox.id = candidates.id
		RETURNING `+prefixedUsageBillingOutboxColumns("outbox"),
		limit, owner, leaseSeconds, leaseToken)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	events := make([]service.UsageBillingOutboxEvent, 0, limit)
	for rows.Next() {
		event, err := scanUsageBillingOutboxEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, *event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (r *usageBillingOutboxRepository) Complete(ctx context.Context, id int64, owner, leaseToken, resultCode string) error {
	resultCode = truncateUsageBillingOutboxText(resultCode, 64)
	return r.updateOwned(ctx, id, owner, leaseToken, `
		status = 'completed',
		completed_at = NOW(),
		result_code = $4,
		last_error_code = NULL,
		last_error = NULL,
		locked_at = NULL,
		locked_by = NULL,
		lease_token = NULL,
		updated_at = NOW()
	`, resultCode)
}

func (r *usageBillingOutboxRepository) Retry(ctx context.Context, id int64, owner, leaseToken string, availableAt time.Time, errorCode, errorMessage string) error {
	if availableAt.IsZero() {
		availableAt = time.Now().UTC()
	}
	return r.updateOwned(ctx, id, owner, leaseToken, `
		status = 'retry',
		available_at = $4,
		last_error_code = $5,
		last_error = $6,
		locked_at = NULL,
		locked_by = NULL,
		lease_token = NULL,
		updated_at = NOW()
	`, availableAt.UTC(), truncateUsageBillingOutboxText(errorCode, 64), truncateUsageBillingOutboxText(errorMessage, 1000))
}

func (r *usageBillingOutboxRepository) DeadLetter(ctx context.Context, id int64, owner, leaseToken, errorCode, errorMessage string) error {
	return r.updateOwned(ctx, id, owner, leaseToken, `
		status = 'dead_letter',
		dead_lettered_at = NOW(),
		last_error_code = $4,
		last_error = $5,
		locked_at = NULL,
		locked_by = NULL,
		lease_token = NULL,
		updated_at = NOW()
	`, truncateUsageBillingOutboxText(errorCode, 64), truncateUsageBillingOutboxText(errorMessage, 1000))
}

func (r *usageBillingOutboxRepository) ValidateBindings(ctx context.Context, envelope service.UsageBillingEnvelope) error {
	// Database ownership and mode are atomically validated and frozen by
	// Enqueue. Replay deliberately validates only the immutable envelope so a
	// later soft-delete or reassignment cannot erase an already accepted charge.
	return envelope.Validate()
}

func (r *usageBillingOutboxRepository) updateOwned(ctx context.Context, id int64, owner, leaseToken, assignments string, args ...any) error {
	if r == nil || r.db == nil {
		return errors.New("usage billing outbox repository db is nil")
	}
	owner = strings.TrimSpace(owner)
	leaseToken = strings.TrimSpace(leaseToken)
	if id <= 0 || owner == "" || leaseToken == "" {
		return service.ErrUsageBillingOutboxLeaseLost
	}
	query := `UPDATE usage_billing_outbox SET ` + assignments + `
		WHERE id = $1
		  AND locked_by = $2
		  AND lease_token = $3
		  AND status IN ('pending', 'retry')`
	queryArgs := make([]any, 0, len(args)+3)
	queryArgs = append(queryArgs, id, owner, leaseToken)
	queryArgs = append(queryArgs, args...)
	result, err := r.db.ExecContext(ctx, query, queryArgs...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrUsageBillingOutboxLeaseLost
	}
	return nil
}

type usageBillingOutboxScanner interface {
	Scan(dest ...any) error
}

func scanUsageBillingOutboxEvent(scanner usageBillingOutboxScanner) (*service.UsageBillingOutboxEvent, error) {
	var (
		event            service.UsageBillingOutboxEvent
		outerRequestID   string
		outerAPIKeyID    int64
		outerFingerprint string
		outerVersion     int16
		rawEnvelope      []byte
		lockedAt         sql.NullTime
		lockedBy         sql.NullString
		leaseToken       sql.NullString
		lastAttemptAt    sql.NullTime
		completedAt      sql.NullTime
		deadLetteredAt   sql.NullTime
		resultCode       sql.NullString
		lastErrorCode    sql.NullString
		lastError        sql.NullString
	)
	if err := scanner.Scan(
		&event.ID, &outerRequestID, &outerAPIKeyID, &outerFingerprint, &outerVersion,
		&rawEnvelope, &event.Status, &event.AttemptCount, &event.MaxAttempts, &event.AvailableAt,
		&lockedAt, &lockedBy, &leaseToken, &lastAttemptAt, &completedAt, &deadLetteredAt,
		&resultCode, &lastErrorCode, &lastError, &event.CreatedAt, &event.UpdatedAt,
	); err != nil {
		return nil, err
	}
	envelope, err := service.DecodeUsageBillingEnvelope(rawEnvelope)
	if err != nil {
		event.EnvelopeError = err
	} else if envelope.RequestID() != outerRequestID ||
		envelope.APIKeyID() != outerAPIKeyID ||
		!strings.EqualFold(envelope.RequestFingerprint(), outerFingerprint) ||
		envelope.Version() != outerVersion {
		event.EnvelopeError = service.ErrUsageBillingEnvelopeFingerprintMismatch
	} else {
		event.Envelope = envelope
	}
	event.LockedAt = nullTimePointer(lockedAt)
	event.LockedBy = lockedBy.String
	event.LeaseToken = leaseToken.String
	event.LastAttemptAt = nullTimePointer(lastAttemptAt)
	event.CompletedAt = nullTimePointer(completedAt)
	event.DeadLetteredAt = nullTimePointer(deadLetteredAt)
	event.ResultCode = resultCode.String
	event.LastErrorCode = lastErrorCode.String
	event.LastError = lastError.String
	return &event, nil
}

type usageBillingBindingQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func validateUsageBillingBindingsAtEnqueue(ctx context.Context, q usageBillingBindingQuerier, envelope service.UsageBillingEnvelope) error {
	return validateUsageBillingBindings(ctx, q, envelope, false)
}

func validateUsageBillingBindingsAtAdmission(ctx context.Context, q usageBillingBindingQuerier, envelope service.UsageBillingEnvelope) error {
	return validateUsageBillingBindings(ctx, q, envelope, true)
}

func validateUsageBillingBindings(ctx context.Context, q usageBillingBindingQuerier, envelope service.UsageBillingEnvelope, requireCurrentWalletGrant bool) error {
	groupID := envelope.GroupID()
	effectiveBillingGroupID := envelope.EffectiveBillingGroupID()
	if groupID == nil || effectiveBillingGroupID == nil {
		return service.ErrUsageBillingEnvelopeInvalid
	}

	var apiKeyUserID int64
	var apiKeyGroupID sql.NullInt64
	var apiKeyName, apiKeyPurpose string
	var apiKeyHash sql.NullString
	err := q.QueryRowContext(ctx, `
		SELECT user_id, group_id, name, purpose, key_hash FROM api_keys
		WHERE id = $1 AND deleted_at IS NULL
		FOR SHARE
	`, envelope.APIKeyID()).Scan(&apiKeyUserID, &apiKeyGroupID, &apiKeyName, &apiKeyPurpose, &apiKeyHash)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingOutboxTargetNotFound
	}
	if err != nil {
		return err
	}
	if apiKeyUserID != envelope.UserID() {
		return service.ErrUsageBillingCrossTenant
	}
	if locator := envelope.AuthCacheLocator(); locator != "" {
		expectedLocator := strings.TrimSpace(apiKeyHash.String)
		if !apiKeyHash.Valid || !validAPIKeyAuthCacheLocator(expectedLocator) || !strings.EqualFold(expectedLocator, locator) {
			return service.ErrUsageBillingCrossTenant
		}
	}
	universalWalletKey := !apiKeyGroupID.Valid
	if universalWalletKey {
		if !validUsageBillingUniversalWalletKeyShape(apiKeyName, apiKeyPurpose, apiKeyGroupID.Valid) || envelope.SubscriptionID() == nil {
			return service.ErrUsageBillingCrossTenant
		}
	} else if apiKeyPurpose == service.APIKeyPurposeWalletUniversal || apiKeyGroupID.Int64 != *groupID {
		return service.ErrUsageBillingCrossTenant
	}

	var (
		groupStatus, groupName, groupPlatform, groupSubscriptionType string
		groupExclusive                                               bool
		groupClaudeCodeOnly                                          bool
		groupFallbackID, groupInvalidRequestFallbackID               sql.NullInt64
	)
	err = q.QueryRowContext(ctx, `
		SELECT status, name, platform, is_exclusive, subscription_type,
			claude_code_only, fallback_group_id, fallback_group_id_on_invalid_request
		FROM groups
		WHERE id = $1 AND deleted_at IS NULL
		FOR SHARE
	`, *groupID).Scan(
		&groupStatus, &groupName, &groupPlatform, &groupExclusive, &groupSubscriptionType,
		&groupClaudeCodeOnly, &groupFallbackID, &groupInvalidRequestFallbackID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingOutboxTargetNotFound
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(groupStatus) != service.StatusActive {
		return service.ErrUsageBillingCrossTenant
	}
	billingGroup := service.Group{
		ID: *groupID, Name: groupName, Platform: groupPlatform, Status: groupStatus,
		Hydrated: true, IsExclusive: groupExclusive, SubscriptionType: groupSubscriptionType,
		ClaudeCodeOnly:                  groupClaudeCodeOnly,
		FallbackGroupID:                 nullInt64Pointer(groupFallbackID),
		FallbackGroupIDOnInvalidRequest: nullInt64Pointer(groupInvalidRequestFallbackID),
	}

	var accountType string
	err = q.QueryRowContext(ctx, `
		SELECT type FROM accounts
		WHERE id = $1 AND deleted_at IS NULL
		FOR SHARE
	`, envelope.AccountID()).Scan(&accountType)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingOutboxTargetNotFound
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(accountType) != envelope.AccountType() {
		return service.ErrUsageBillingCrossTenant
	}
	var accountGroupExists bool
	err = q.QueryRowContext(ctx, `
		SELECT TRUE
		FROM account_groups
		WHERE account_id = $1 AND group_id = $2
		FOR SHARE
	`, envelope.AccountID(), *groupID).Scan(&accountGroupExists)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingCrossTenant
	}
	if err != nil {
		return err
	}
	if !accountGroupExists {
		return service.ErrUsageBillingCrossTenant
	}

	subscriptionID := envelope.SubscriptionID()
	if subscriptionID == nil {
		if *effectiveBillingGroupID != *groupID {
			return service.ErrUsageBillingCrossTenant
		}
		return nil
	}
	var subscriptionUserID int64
	var subscriptionGroupID sql.NullInt64
	var walletBalance sql.NullFloat64
	err = q.QueryRowContext(ctx, `
		SELECT user_id, group_id, wallet_balance_usd
		FROM user_subscriptions
		WHERE id = $1 AND deleted_at IS NULL
		FOR SHARE
	`, *subscriptionID).Scan(&subscriptionUserID, &subscriptionGroupID, &walletBalance)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingOutboxTargetNotFound
	}
	if err != nil {
		return err
	}
	if subscriptionUserID != envelope.UserID() {
		return service.ErrUsageBillingCrossTenant
	}
	if universalWalletKey && !walletBalance.Valid {
		return service.ErrUsageBillingCrossTenant
	}
	if walletBalance.Valid {
		if subscriptionGroupID.Valid || envelope.SubscriptionCost() > 0 || *effectiveBillingGroupID != *groupID {
			return service.ErrUsageBillingCrossTenant
		}
		if err := validateUsageBillingWalletGroupAuthorization(
			ctx, q, envelope, billingGroup, requireCurrentWalletGrant,
		); err != nil {
			return err
		}
		return nil
	}
	if envelope.WalletCost() > 0 || !subscriptionGroupID.Valid || subscriptionGroupID.Int64 != *effectiveBillingGroupID {
		return service.ErrUsageBillingCrossTenant
	}
	if *effectiveBillingGroupID != *groupID {
		if err := lockActiveUsageBillingGroup(ctx, q, *effectiveBillingGroupID); err != nil {
			return err
		}
		covered, err := lockUsageBillingPlanCoverage(ctx, q, *subscriptionID, *effectiveBillingGroupID, *groupID)
		if err != nil {
			return err
		}
		if !covered {
			return service.ErrUsageBillingCrossTenant
		}
	}
	return nil
}

func validAPIKeyAuthCacheLocator(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validUsageBillingUniversalWalletKeyShape(name, purpose string, groupIDValid bool) bool {
	return !groupIDValid && name == service.WalletUniversalAPIKeyName && purpose == service.APIKeyPurposeWalletUniversal
}

func usageBillingUniversalWalletGroupAuthorized(group service.Group, vipGranted bool) bool {
	user := &service.User{ID: 1}
	if vipGranted {
		user.AllowedGroups = []int64{group.ID}
	}
	return service.CanUseWalletGroup(user, &group)
}

func validateUsageBillingWalletGroupAuthorization(
	ctx context.Context,
	q usageBillingBindingQuerier,
	envelope service.UsageBillingEnvelope,
	group service.Group,
	requireCurrentGrant bool,
) error {
	vipGranted := false
	if group.Name == service.WalletDefaultVIPGroupName {
		grantFrozen := false
		if !requireCurrentGrant && envelope.AdmissionAttemptID() != "" {
			err := q.QueryRowContext(ctx, `
				SELECT EXISTS (
					SELECT 1
					FROM usage_billing_admissions admission
					JOIN usage_billing_admission_attempts attempt
					  ON attempt.request_id = admission.request_id
					 AND attempt.api_key_id = admission.api_key_id
					WHERE admission.request_id = $1 AND admission.api_key_id = $2
					  AND attempt.attempt_id = $3
				)
			`, envelope.RequestID(), envelope.APIKeyID(), envelope.AdmissionAttemptID()).Scan(&grantFrozen)
			if err != nil {
				return err
			}
		}
		if grantFrozen {
			vipGranted = true
		} else {
			err := q.QueryRowContext(ctx, `
				SELECT TRUE
				FROM user_allowed_groups
				WHERE user_id = $1 AND group_id = $2
				FOR SHARE
			`, envelope.UserID(), group.ID).Scan(&vipGranted)
			if errors.Is(err, sql.ErrNoRows) {
				vipGranted = false
			} else if err != nil {
				return err
			}
		}
	}
	if !usageBillingUniversalWalletGroupAuthorized(group, vipGranted) {
		return service.ErrUsageBillingCrossTenant
	}
	return nil
}

func nullInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func lockActiveUsageBillingGroup(ctx context.Context, q usageBillingBindingQuerier, groupID int64) error {
	var status string
	err := q.QueryRowContext(ctx, `
		SELECT status FROM groups
		WHERE id = $1 AND deleted_at IS NULL
		FOR SHARE
	`, groupID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingOutboxTargetNotFound
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != service.StatusActive {
		return service.ErrUsageBillingCrossTenant
	}
	return nil
}

func lockUsageBillingPlanCoverage(ctx context.Context, q usageBillingBindingQuerier, subscriptionID, anchorGroupID, routedGroupID int64) (bool, error) {
	var covered bool
	err := q.QueryRowContext(ctx, `
		WITH attached_snapshot AS (
			SELECT snapshot.snapshot, snapshot.grant_starts_at, snapshot.grant_expires_at
			FROM subscription_plan_fulfillment_snapshots snapshot
			WHERE snapshot.user_subscription_id = $1
			  AND snapshot.grant_starts_at IS NOT NULL
			  AND snapshot.grant_expires_at IS NOT NULL
		),
		anchor_plans AS (
			SELECT plan.id
			FROM subscription_plans plan
			JOIN groups anchor_group
			  ON anchor_group.id = plan.group_id
			 AND anchor_group.subscription_type = $4
			 AND anchor_group.deleted_at IS NULL
			WHERE plan.plan_type = $5
			  AND plan.group_id = $2

			UNION

			SELECT plan.id
			FROM subscription_plans plan
			JOIN subscription_plan_groups anchor
			  ON anchor.plan_id = plan.id
			 AND anchor.group_id = $2
			JOIN groups anchor_group
			  ON anchor_group.id = anchor.group_id
			 AND anchor_group.subscription_type = $4
			 AND anchor_group.deleted_at IS NULL
			WHERE plan.plan_type = $5
			  AND plan.group_id IS NULL
		)
		SELECT CASE
			WHEN EXISTS (
				SELECT 1
				FROM attached_snapshot snapshot
				WHERE snapshot.grant_starts_at <= NOW()
				  AND snapshot.grant_expires_at > NOW()
				  AND snapshot.snapshot->'covered_group_ids' @> jsonb_build_array($3::bigint)
			) THEN TRUE
			WHEN EXISTS (SELECT 1 FROM attached_snapshot) THEN FALSE
			ELSE EXISTS (SELECT 1 FROM anchor_plans)
			 AND NOT EXISTS (
				SELECT 1
				FROM anchor_plans plan
				WHERE NOT EXISTS (
					SELECT 1
					FROM subscription_plan_groups target
					WHERE target.plan_id = plan.id
					  AND target.group_id = $3
				)
			 )
		END
	`, subscriptionID, anchorGroupID, routedGroupID, service.SubscriptionTypeSubscription, service.PlanTypeSubscription).Scan(&covered)
	return covered, err
}

func newUsageBillingLeaseToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate usage billing lease token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}

func prefixedUsageBillingOutboxColumns(prefix string) string {
	columns := strings.Split(strings.TrimSpace(usageBillingOutboxSelectColumns), ",")
	for i := range columns {
		columns[i] = prefix + "." + strings.TrimSpace(columns[i])
	}
	return strings.Join(columns, ", ")
}

func nullTimePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	copyValue := value.Time
	return &copyValue
}

func truncateUsageBillingOutboxText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

var _ service.UsageBillingOutboxRepository = (*usageBillingOutboxRepository)(nil)
var _ service.UsageBillingBindingValidator = (*usageBillingOutboxRepository)(nil)
