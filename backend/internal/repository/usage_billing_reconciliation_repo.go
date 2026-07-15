package repository

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type usageBillingReconciliationAdmission struct {
	state                string
	userID               int64
	walletSubscriptionID sql.NullInt64
	walletReservedUSD    float64
	everDispatched       bool
}

type usageBillingReconciliationWallet struct {
	userID  int64
	balance float64
}

type usageBillingReconciliationOutbox struct {
	id            int64
	status        string
	lastErrorCode string
}

type usageBillingReconciliationAttempt struct {
	id             string
	state          string
	everDispatched bool
}

func (r *usageBillingOutboxRepository) ListUsageBillingReconciliationCases(
	ctx context.Context,
	limit int,
) ([]service.UsageBillingReconciliationCase, error) {
	if r == nil || r.db == nil {
		return nil, service.ErrUsageBillingReconciliationUnavailable
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT admission.request_id, admission.api_key_id, admission.state, admission.user_id,
			admission.subscription_id, admission.wallet_subscription_id,
			admission.wallet_reserved_usd::double precision,
			outbox.id, COALESCE(outbox.status, ''), COALESCE(outbox.last_error_code, ''),
			COUNT(attempt.attempt_id) FILTER (WHERE attempt.state = 'prepared'),
			COUNT(attempt.attempt_id) FILTER (WHERE attempt.state = 'dispatched'),
			COUNT(attempt.attempt_id) FILTER (WHERE attempt.state = 'failed'),
			COUNT(attempt.attempt_id) FILTER (WHERE attempt.state = 'finalized'),
			COUNT(attempt.attempt_id) FILTER (
				WHERE attempt.dispatched_at IS NOT NULL OR attempt.state IN ('dispatched', 'finalized')
			),
			admission.prepared_at, admission.dispatched_at, admission.orphaned_at,
			admission.reconcile_started_at
		FROM usage_billing_admissions AS admission
		LEFT JOIN usage_billing_outbox AS outbox
		  ON outbox.request_id = admission.request_id
		 AND outbox.api_key_id = admission.api_key_id
		LEFT JOIN usage_billing_admission_attempts AS attempt
		  ON attempt.request_id = admission.request_id
		 AND attempt.api_key_id = admission.api_key_id
		WHERE (
			admission.state = 'reconcile' AND outbox.status = 'dead_letter'
		) OR (
			admission.state IN ('orphaned', 'reconcile')
			AND admission.wallet_subscription_id IS NOT NULL
			AND outbox.id IS NULL
			AND EXISTS (
				SELECT 1 FROM usage_billing_admission_attempts present_attempt
				WHERE present_attempt.request_id = admission.request_id
				  AND present_attempt.api_key_id = admission.api_key_id
			)
			AND NOT EXISTS (
				SELECT 1 FROM usage_billing_admission_attempts finalized_attempt
				WHERE finalized_attempt.request_id = admission.request_id
				  AND finalized_attempt.api_key_id = admission.api_key_id
				  AND finalized_attempt.state = 'finalized'
			)
		)
		GROUP BY admission.request_id, admission.api_key_id, admission.state, admission.user_id,
			admission.subscription_id, admission.wallet_subscription_id, admission.wallet_reserved_usd,
			outbox.id, outbox.status, outbox.last_error_code, admission.prepared_at,
			admission.dispatched_at, admission.orphaned_at, admission.reconcile_started_at,
			admission.updated_at
		ORDER BY COALESCE(admission.orphaned_at, admission.reconcile_started_at, admission.updated_at),
			admission.request_id, admission.api_key_id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.UsageBillingReconciliationCase, 0, limit)
	for rows.Next() {
		item, err := scanUsageBillingReconciliationCase(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func scanUsageBillingReconciliationCase(scanner interface{ Scan(...any) error }) (service.UsageBillingReconciliationCase, error) {
	var (
		item           service.UsageBillingReconciliationCase
		subscriptionID sql.NullInt64
		walletID       sql.NullInt64
		outboxID       sql.NullInt64
		dispatchedAt   sql.NullTime
		orphanedAt     sql.NullTime
		reconcileAt    sql.NullTime
	)
	err := scanner.Scan(
		&item.RequestID, &item.APIKeyID, &item.State, &item.UserID,
		&subscriptionID, &walletID, &item.WalletReservedUSD,
		&outboxID, &item.OutboxStatus, &item.LastErrorCode,
		&item.PreparedAttempts, &item.DispatchedAttempts, &item.FailedAttempts, &item.FinalizedAttempts,
		&item.EverDispatchedAttempts,
		&item.PreparedAt, &dispatchedAt, &orphanedAt, &reconcileAt,
	)
	if err != nil {
		return service.UsageBillingReconciliationCase{}, err
	}
	item.SubscriptionID = usageBillingReconciliationInt64Ptr(subscriptionID)
	item.WalletSubscriptionID = usageBillingReconciliationInt64Ptr(walletID)
	item.OutboxID = usageBillingReconciliationInt64Ptr(outboxID)
	item.DispatchedAt = usageBillingReconciliationTimePtr(dispatchedAt)
	item.OrphanedAt = usageBillingReconciliationTimePtr(orphanedAt)
	item.ReconcileStartedAt = usageBillingReconciliationTimePtr(reconcileAt)
	item.AllowedActions = usageBillingReconciliationAllowedActions(item)
	return item, nil
}

func (r *usageBillingOutboxRepository) ResolveUsageBillingReconciliation(
	ctx context.Context,
	input service.UsageBillingReconciliationResolveInput,
) error {
	if r == nil || r.db == nil {
		return service.ErrUsageBillingReconciliationUnavailable
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	walletID, err := lookupUsageBillingReconciliationWallet(ctx, tx, input)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingReconciliationNotFound
	}
	if err != nil {
		return err
	}
	var wallet *usageBillingReconciliationWallet
	if walletID.Valid {
		wallet, err = lockUsageBillingReconciliationWallet(ctx, tx, walletID.Int64)
		if err != nil {
			return err
		}
	}
	admission, err := lockUsageBillingReconciliationAdmission(ctx, tx, input)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingReconciliationNotFound
	}
	if err != nil {
		return err
	}
	if !usageBillingReconciliationWalletIDsMatch(walletID, admission.walletSubscriptionID) ||
		(wallet != nil && wallet.userID != admission.userID) {
		return service.ErrUsageBillingReconciliationConflict
	}

	switch input.Action {
	case service.UsageBillingReconciliationActionRetryDeadLetter:
		err = resolveUsageBillingDeadLetterRetry(ctx, tx, input, admission)
	case service.UsageBillingReconciliationActionReleaseUndelivered:
		err = resolveUsageBillingUndeliveredRelease(ctx, tx, input, admission, wallet)
	case service.UsageBillingReconciliationActionSettleDelivered:
		err = resolveUsageBillingDeliveredSettlement(ctx, tx, input, admission, wallet)
	default:
		err = service.ErrUsageBillingReconciliationInvalid
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func resolveUsageBillingDeadLetterRetry(
	ctx context.Context,
	tx *sql.Tx,
	input service.UsageBillingReconciliationResolveInput,
	admission *usageBillingReconciliationAdmission,
) error {
	if admission.state != service.UsageBillingAdmissionStateReconcile {
		return service.ErrUsageBillingReconciliationConflict
	}
	outbox, err := lockUsageBillingReconciliationOutbox(ctx, tx, input)
	if err != nil {
		return usageBillingReconciliationConflictOnNoRows(err)
	}
	if outbox.status != service.UsageBillingOutboxStatusDeadLetter {
		return service.ErrUsageBillingReconciliationConflict
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE usage_billing_outbox
		SET status = 'retry', available_at = NOW(), locked_at = NULL, locked_by = NULL,
			lease_token = NULL, completed_at = NULL, dead_lettered_at = NULL,
			result_code = NULL, last_error_code = NULL, last_error = NULL, updated_at = NOW()
		WHERE id = $1 AND status = 'dead_letter'
	`, outbox.id)
	if err != nil {
		return err
	}
	if err := requireUsageBillingReconciliationRow(result); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE usage_billing_admissions
		SET state = 'outbox_pending', outbox_pending_at = COALESCE(outbox_pending_at, NOW()), updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND state = 'reconcile'
	`, input.RequestID, input.APIKeyID)
	if err != nil {
		return err
	}
	if err := requireUsageBillingReconciliationRow(result); err != nil {
		return err
	}
	return appendUsageBillingReconciliationAudit(
		ctx, tx, input, admission.state, service.UsageBillingAdmissionStateOutboxPending,
		nil, nil, outbox.lastErrorCode,
	)
}

func resolveUsageBillingUndeliveredRelease(
	ctx context.Context,
	tx *sql.Tx,
	input service.UsageBillingReconciliationResolveInput,
	admission *usageBillingReconciliationAdmission,
	wallet *usageBillingReconciliationWallet,
) error {
	if err := validateUsageBillingManualWalletResolution(admission, wallet); err != nil {
		return err
	}
	if admission.state != service.UsageBillingAdmissionStateOrphaned &&
		admission.state != service.UsageBillingAdmissionStateReconcile {
		return service.ErrUsageBillingReconciliationConflict
	}
	if err := requireNoUsageBillingReconciliationOutbox(ctx, tx, input); err != nil {
		return err
	}
	attempts, err := lockUsageBillingReconciliationAttempts(ctx, tx, input)
	if err != nil {
		return err
	}
	if len(attempts) == 0 || admission.everDispatched ||
		usageBillingReconciliationHasFinalizedAttempt(attempts) ||
		usageBillingReconciliationHasEverDispatchedAttempt(attempts) {
		return service.ErrUsageBillingReconciliationConflict
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE usage_billing_admission_attempts
		SET state = 'failed', failed_at = NOW(), updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND state IN ('prepared', 'dispatched')
	`, input.RequestID, input.APIKeyID); err != nil {
		return err
	}
	if err := promoteUsageBillingReconciliationState(ctx, tx, input, admission.state); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE usage_billing_admissions
		SET state = 'abandoned', abandoned_at = COALESCE(abandoned_at, NOW()),
			wallet_released_at = NOW(), updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND state = 'reconcile'
		  AND wallet_consumed_at IS NULL AND wallet_released_at IS NULL
	`, input.RequestID, input.APIKeyID)
	if err != nil {
		return err
	}
	if err := requireUsageBillingReconciliationRow(result); err != nil {
		return err
	}
	return appendUsageBillingReconciliationAudit(
		ctx, tx, input, admission.state, service.UsageBillingAdmissionStateAbandoned,
		nil, nil, "",
	)
}

func resolveUsageBillingDeliveredSettlement(
	ctx context.Context,
	tx *sql.Tx,
	input service.UsageBillingReconciliationResolveInput,
	admission *usageBillingReconciliationAdmission,
	wallet *usageBillingReconciliationWallet,
) error {
	if err := validateUsageBillingManualWalletResolution(admission, wallet); err != nil {
		return err
	}
	cost := input.Envelope.WalletCost()
	if (admission.state != service.UsageBillingAdmissionStateOrphaned &&
		admission.state != service.UsageBillingAdmissionStateReconcile) ||
		service.ValidateUsageBillingReconciliationWalletCosts(input.Envelope) != nil ||
		cost > admission.walletReservedUSD+1e-12 ||
		math.IsNaN(cost) || math.IsInf(cost, 0) {
		return service.ErrUsageBillingReconciliationConflict
	}
	if err := requireNoUsageBillingReconciliationOutbox(ctx, tx, input); err != nil {
		return err
	}
	attempts, err := lockUsageBillingReconciliationAttempts(ctx, tx, input)
	if err != nil {
		return err
	}
	if !usageBillingReconciliationAttemptCanSettle(attempts, input.AttemptID) {
		return service.ErrUsageBillingReconciliationConflict
	}
	if err := validateUsageBillingAdmissionAtReconciliation(ctx, tx, input.Envelope); err != nil {
		return usageBillingReconciliationEnvelopeConflict(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE usage_billing_admission_attempts
		SET state = 'failed', failed_at = NOW(), updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND attempt_id <> $3
		  AND state IN ('prepared', 'dispatched')
	`, input.RequestID, input.APIKeyID, input.AttemptID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE usage_billing_admission_attempts
		SET state = 'finalized', finalized_at = NOW(), updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND attempt_id = $3 AND state = 'dispatched'
	`, input.RequestID, input.APIKeyID, input.AttemptID)
	if err != nil {
		return err
	}
	if err := requireUsageBillingReconciliationRow(result); err != nil {
		return err
	}
	if err := insertUsageBillingReconciliationOutbox(ctx, tx, input.Envelope); err != nil {
		return err
	}
	if err := promoteUsageBillingReconciliationState(ctx, tx, input, admission.state); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE usage_billing_admissions
		SET state = 'outbox_pending', outbox_pending_at = COALESCE(outbox_pending_at, NOW()),
			updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND state = 'reconcile'
		  AND wallet_consumed_at IS NULL AND wallet_released_at IS NULL
	`, input.RequestID, input.APIKeyID)
	if err != nil {
		return err
	}
	if err := requireUsageBillingReconciliationRow(result); err != nil {
		return err
	}
	return appendUsageBillingReconciliationAudit(
		ctx, tx, input, admission.state, service.UsageBillingAdmissionStateOutboxPending,
		&input.AttemptID, &cost, "",
	)
}

func insertUsageBillingReconciliationOutbox(
	ctx context.Context,
	tx *sql.Tx,
	envelope service.UsageBillingEnvelope,
) error {
	raw, err := envelope.MarshalJSON()
	if err != nil || len(raw) > service.UsageBillingEnvelopeMaxBytes {
		return service.ErrUsageBillingReconciliationInvalid
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO usage_billing_outbox (
			request_id, api_key_id, request_fingerprint, envelope_version, envelope
		) VALUES ($1, $2, $3, $4, $5::jsonb)
		ON CONFLICT (request_id, api_key_id) DO NOTHING
	`, envelope.RequestID(), envelope.APIKeyID(), envelope.RequestFingerprint(), envelope.Version(), raw)
	if err != nil {
		return err
	}
	return requireUsageBillingReconciliationRow(result)
}

func lookupUsageBillingReconciliationWallet(
	ctx context.Context,
	tx *sql.Tx,
	input service.UsageBillingReconciliationResolveInput,
) (sql.NullInt64, error) {
	var walletID sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT wallet_subscription_id
		FROM usage_billing_admissions
		WHERE request_id = $1 AND api_key_id = $2
	`, input.RequestID, input.APIKeyID).Scan(&walletID)
	return walletID, err
}

func lockUsageBillingReconciliationWallet(
	ctx context.Context,
	tx *sql.Tx,
	walletID int64,
) (*usageBillingReconciliationWallet, error) {
	var wallet usageBillingReconciliationWallet
	var balance sql.NullFloat64
	err := tx.QueryRowContext(ctx, `
		SELECT user_id, wallet_balance_usd
		FROM user_subscriptions
		WHERE id = $1
		FOR UPDATE
	`, walletID).Scan(&wallet.userID, &balance)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !balance.Valid) {
		return nil, service.ErrUsageBillingReconciliationConflict
	}
	if err != nil {
		return nil, err
	}
	wallet.balance = balance.Float64
	return &wallet, nil
}

func lockUsageBillingReconciliationAdmission(
	ctx context.Context,
	tx *sql.Tx,
	input service.UsageBillingReconciliationResolveInput,
) (*usageBillingReconciliationAdmission, error) {
	var admission usageBillingReconciliationAdmission
	err := tx.QueryRowContext(ctx, `
		SELECT state, user_id, wallet_subscription_id, wallet_reserved_usd::double precision,
			dispatched_at IS NOT NULL AS ever_dispatched
		FROM usage_billing_admissions
		WHERE request_id = $1 AND api_key_id = $2
		FOR UPDATE
	`, input.RequestID, input.APIKeyID).Scan(
		&admission.state, &admission.userID, &admission.walletSubscriptionID,
		&admission.walletReservedUSD, &admission.everDispatched,
	)
	return &admission, err
}

func lockUsageBillingReconciliationOutbox(
	ctx context.Context,
	tx *sql.Tx,
	input service.UsageBillingReconciliationResolveInput,
) (*usageBillingReconciliationOutbox, error) {
	var outbox usageBillingReconciliationOutbox
	err := tx.QueryRowContext(ctx, `
		SELECT id, status, COALESCE(last_error_code, '')
		FROM usage_billing_outbox
		WHERE request_id = $1 AND api_key_id = $2
		FOR UPDATE
	`, input.RequestID, input.APIKeyID).Scan(&outbox.id, &outbox.status, &outbox.lastErrorCode)
	return &outbox, err
}

func requireNoUsageBillingReconciliationOutbox(
	ctx context.Context,
	tx *sql.Tx,
	input service.UsageBillingReconciliationResolveInput,
) error {
	_, err := lockUsageBillingReconciliationOutbox(ctx, tx, input)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return service.ErrUsageBillingReconciliationConflict
}

func lockUsageBillingReconciliationAttempts(
	ctx context.Context,
	tx *sql.Tx,
	input service.UsageBillingReconciliationResolveInput,
) ([]usageBillingReconciliationAttempt, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT attempt_id, state,
			(dispatched_at IS NOT NULL OR state IN ('dispatched', 'finalized')) AS ever_dispatched
		FROM usage_billing_admission_attempts
		WHERE request_id = $1 AND api_key_id = $2
		ORDER BY attempt_id
		FOR UPDATE
	`, input.RequestID, input.APIKeyID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	attempts := make([]usageBillingReconciliationAttempt, 0, 2)
	for rows.Next() {
		var attempt usageBillingReconciliationAttempt
		if err := rows.Scan(&attempt.id, &attempt.state, &attempt.everDispatched); err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

func promoteUsageBillingReconciliationState(
	ctx context.Context,
	tx *sql.Tx,
	input service.UsageBillingReconciliationResolveInput,
	state string,
) error {
	if state == service.UsageBillingAdmissionStateReconcile {
		return nil
	}
	if state != service.UsageBillingAdmissionStateOrphaned {
		return service.ErrUsageBillingReconciliationConflict
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE usage_billing_admissions
		SET state = 'reconcile', reconcile_started_at = COALESCE(reconcile_started_at, NOW()), updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2 AND state = 'orphaned'
	`, input.RequestID, input.APIKeyID)
	if err != nil {
		return err
	}
	return requireUsageBillingReconciliationRow(result)
}

func appendUsageBillingReconciliationAudit(
	ctx context.Context,
	tx *sql.Tx,
	input service.UsageBillingReconciliationResolveInput,
	fromState string,
	toState string,
	attemptID *string,
	actualCostUSD *float64,
	previousErrorCode string,
) error {
	var attempt any
	if attemptID != nil {
		attempt = *attemptID
	}
	var cost any
	if actualCostUSD != nil {
		cost = *actualCostUSD
	}
	var previousError any
	if strings.TrimSpace(previousErrorCode) != "" {
		previousError = strings.TrimSpace(previousErrorCode)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO usage_billing_reconciliation_audits (
			request_id, api_key_id, action, operator_id, evidence_ref,
			attempt_id, actual_cost_usd, from_state, to_state, previous_error_code
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, input.RequestID, input.APIKeyID, input.Action, input.OperatorID, input.EvidenceRef,
		attempt, cost, fromState, toState, previousError)
	return err
}

func validateUsageBillingManualWalletResolution(
	admission *usageBillingReconciliationAdmission,
	wallet *usageBillingReconciliationWallet,
) error {
	if admission == nil || wallet == nil || !admission.walletSubscriptionID.Valid ||
		admission.walletReservedUSD <= 0 || math.IsNaN(admission.walletReservedUSD) ||
		math.IsInf(admission.walletReservedUSD, 0) {
		return service.ErrUsageBillingReconciliationConflict
	}
	return nil
}

func usageBillingReconciliationAttemptCanSettle(
	attempts []usageBillingReconciliationAttempt,
	attemptID string,
) bool {
	selectedDispatched := false
	for _, attempt := range attempts {
		if attempt.state == "finalized" {
			return false
		}
		if attempt.id == attemptID && attempt.state == "dispatched" {
			selectedDispatched = true
		}
	}
	return selectedDispatched
}

func usageBillingReconciliationHasFinalizedAttempt(attempts []usageBillingReconciliationAttempt) bool {
	for _, attempt := range attempts {
		if attempt.state == "finalized" {
			return true
		}
	}
	return false
}

func usageBillingReconciliationHasEverDispatchedAttempt(attempts []usageBillingReconciliationAttempt) bool {
	for _, attempt := range attempts {
		if attempt.everDispatched {
			return true
		}
	}
	return false
}

func usageBillingReconciliationConflictOnNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingReconciliationConflict
	}
	return err
}

func usageBillingReconciliationEnvelopeConflict(err error) error {
	for _, conflict := range []error{
		service.ErrUsageBillingEnvelopeInvalid,
		service.ErrUsageBillingEnvelopeFingerprintMismatch,
		service.ErrUsageBillingRequestConflict,
		service.ErrUsageBillingCrossTenant,
		service.ErrUsageBillingAdmissionMissing,
		service.ErrUsageBillingAdmissionLeaseLost,
	} {
		if errors.Is(err, conflict) {
			return service.ErrUsageBillingReconciliationConflict
		}
	}
	return err
}

func usageBillingReconciliationAllowedActions(item service.UsageBillingReconciliationCase) []string {
	if item.State == service.UsageBillingAdmissionStateReconcile &&
		item.OutboxStatus == service.UsageBillingOutboxStatusDeadLetter {
		return []string{service.UsageBillingReconciliationActionRetryDeadLetter}
	}
	if item.WalletSubscriptionID == nil || item.OutboxID != nil || item.FinalizedAttempts != 0 ||
		item.PreparedAttempts+item.DispatchedAttempts+item.FailedAttempts == 0 {
		return []string{}
	}
	actions := make([]string, 0, 2)
	if item.EverDispatchedAttempts == 0 && item.DispatchedAt == nil {
		actions = append(actions, service.UsageBillingReconciliationActionReleaseUndelivered)
	}
	if item.DispatchedAttempts > 0 {
		actions = append(actions, service.UsageBillingReconciliationActionSettleDelivered)
	}
	return actions
}

func requireUsageBillingReconciliationRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return service.ErrUsageBillingReconciliationConflict
	}
	return nil
}

func usageBillingReconciliationWalletIDsMatch(left, right sql.NullInt64) bool {
	return left.Valid == right.Valid && (!left.Valid || left.Int64 == right.Int64)
}

func usageBillingReconciliationInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func usageBillingReconciliationTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}
