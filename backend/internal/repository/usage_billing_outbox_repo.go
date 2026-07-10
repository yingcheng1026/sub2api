package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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

type usageBillingOutboxRepository struct {
	db *sql.DB
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
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := selectUsageBillingOutboxByIdentity(ctx, tx, envelope.RequestID(), envelope.APIKeyID())
	if err == nil {
		if err := validateExistingUsageBillingOutbox(existing, envelope); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	if err := validateUsageBillingBindingsAtEnqueue(ctx, tx, envelope); err != nil {
		return nil, false, err
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
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return event, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}

	event, err = selectUsageBillingOutboxByIdentity(ctx, tx, envelope.RequestID(), envelope.APIKeyID())
	if err != nil {
		return nil, false, err
	}
	if err := validateExistingUsageBillingOutbox(event, envelope); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return event, false, nil
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
				attempt_count < max_attempts
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
}

func validateUsageBillingBindingsAtEnqueue(ctx context.Context, q usageBillingBindingQuerier, envelope service.UsageBillingEnvelope) error {
	groupID := envelope.GroupID()
	effectiveBillingGroupID := envelope.EffectiveBillingGroupID()
	if groupID == nil || effectiveBillingGroupID == nil {
		return service.ErrUsageBillingEnvelopeInvalid
	}

	var apiKeyUserID int64
	var apiKeyGroupID sql.NullInt64
	var apiKeyName string
	var apiKeyHash sql.NullString
	var apiKeyValue string
	err := q.QueryRowContext(ctx, `
		SELECT user_id, group_id, name, key_hash, key FROM api_keys
		WHERE id = $1 AND deleted_at IS NULL
		FOR SHARE
	`, envelope.APIKeyID()).Scan(&apiKeyUserID, &apiKeyGroupID, &apiKeyName, &apiKeyHash, &apiKeyValue)
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
		if !apiKeyHash.Valid || expectedLocator == "" {
			expectedLocator = service.APIKeyAuthCacheLocator(apiKeyValue)
		}
		if !strings.EqualFold(expectedLocator, locator) {
			return service.ErrUsageBillingCrossTenant
		}
	}
	universalWalletKey := !apiKeyGroupID.Valid
	if universalWalletKey {
		if !service.IsWalletUniversalKeyName(apiKeyName) || envelope.SubscriptionID() == nil {
			return service.ErrUsageBillingCrossTenant
		}
	} else if apiKeyGroupID.Int64 != *groupID {
		return service.ErrUsageBillingCrossTenant
	}

	var groupStatus string
	err = q.QueryRowContext(ctx, `
		SELECT status FROM groups
		WHERE id = $1 AND deleted_at IS NULL
		FOR SHARE
	`, *groupID).Scan(&groupStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingOutboxTargetNotFound
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(groupStatus) != service.StatusActive {
		return service.ErrUsageBillingCrossTenant
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
		return nil
	}
	if envelope.WalletCost() > 0 || !subscriptionGroupID.Valid || subscriptionGroupID.Int64 != *effectiveBillingGroupID {
		return service.ErrUsageBillingCrossTenant
	}
	if *effectiveBillingGroupID != *groupID {
		if err := lockActiveUsageBillingGroup(ctx, q, *effectiveBillingGroupID); err != nil {
			return err
		}
		covered, err := lockUsageBillingPlanCoverage(ctx, q, *effectiveBillingGroupID, *groupID)
		if err != nil {
			return err
		}
		if !covered {
			return service.ErrUsageBillingCrossTenant
		}
	}
	return nil
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

func lockUsageBillingPlanCoverage(ctx context.Context, q usageBillingBindingQuerier, anchorGroupID, routedGroupID int64) (bool, error) {
	var planID int64
	err := q.QueryRowContext(ctx, `
		SELECT sp.id
		FROM subscription_plans sp
		JOIN subscription_plan_groups target
			ON target.plan_id = sp.id AND target.group_id = $2
		WHERE sp.group_id = $1
		LIMIT 1
		FOR SHARE OF sp, target
	`, anchorGroupID, routedGroupID).Scan(&planID)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	err = q.QueryRowContext(ctx, `
		SELECT sp.id
		FROM subscription_plans sp
		JOIN subscription_plan_groups anchor
			ON anchor.plan_id = sp.id AND anchor.group_id = $1
		JOIN groups anchor_group
			ON anchor_group.id = anchor.group_id
			AND anchor_group.subscription_type = $3
			AND anchor_group.deleted_at IS NULL
		JOIN subscription_plan_groups target
			ON target.plan_id = sp.id AND target.group_id = $2
		WHERE sp.group_id IS NULL
		LIMIT 1
		FOR SHARE OF sp, anchor, anchor_group, target
	`, anchorGroupID, routedGroupID, service.SubscriptionTypeSubscription).Scan(&planID)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
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
