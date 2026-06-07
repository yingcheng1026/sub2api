package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type hfcAbuseRiskRepository struct {
	db *sql.DB
}

func NewHFCAbuseRiskRepository(db *sql.DB) service.HFCAbuseRiskRepository {
	return &hfcAbuseRiskRepository{db: db}
}

func (r *hfcAbuseRiskRepository) CreateEvent(ctx context.Context, event *service.HFCAbuseRiskEvent) error {
	if event == nil {
		return nil
	}
	evidence, err := json.Marshal(event.Evidence)
	if err != nil {
		return fmt.Errorf("marshal hfc abuse risk evidence: %w", err)
	}
	err = r.db.QueryRowContext(ctx, `
INSERT INTO hfc_abuse_risk_events (
    source, severity, status, user_id, user_email, api_key_id, api_key_name,
    group_id, group_name, risk_score, signup_ip_prefix, device_fingerprint_hash,
    signup_user_agent_hash, payment_order_id, redeem_code_id, referral_user_id,
    content_moderation_log_id, summary, evidence
) VALUES (
    $1, $2, $3, $4, $5, $6, $7,
    $8, $9, $10, $11, $12,
    $13, $14, $15, $16,
    $17, $18, $19::jsonb
) RETURNING id, created_at, updated_at`,
		event.Source,
		event.Severity,
		event.Status,
		nullableInt64Ptr(event.UserID),
		event.UserEmail,
		nullableInt64Ptr(event.APIKeyID),
		event.APIKeyName,
		nullableInt64Ptr(event.GroupID),
		event.GroupName,
		event.RiskScore,
		event.SignupIPPrefix,
		event.DeviceFingerprintHash,
		event.SignupUserAgentHash,
		nullableInt64Ptr(event.PaymentOrderID),
		nullableInt64Ptr(event.RedeemCodeID),
		nullableInt64Ptr(event.ReferralUserID),
		nullableInt64Ptr(event.ContentModerationLogID),
		event.Summary,
		string(evidence),
	).Scan(&event.ID, &event.CreatedAt, &event.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert hfc abuse risk event: %w", err)
	}
	return nil
}

func (r *hfcAbuseRiskRepository) ListEvents(ctx context.Context, filter service.HFCAbuseRiskEventFilter) ([]service.HFCAbuseRiskEvent, *pagination.PaginationResult, error) {
	where, args := buildHFCAbuseRiskEventWhere(filter)
	whereSQL := "WHERE " + strings.Join(where, " AND ")
	params := defaultHFCAbuseRiskPagination(filter.Pagination)

	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM hfc_abuse_risk_events e "+whereSQL, args...).Scan(&total); err != nil {
		if isHFCAbuseRiskEventsTableMissing(err) {
			return []service.HFCAbuseRiskEvent{}, paginationResultFromTotal(0, params), nil
		}
		return nil, nil, fmt.Errorf("count hfc abuse risk events: %w", err)
	}

	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, params.Limit(), params.Offset())
	rows, err := r.db.QueryContext(ctx, hfcAbuseRiskEventSelectSQL+`
FROM hfc_abuse_risk_events e `+whereSQL+`
ORDER BY e.created_at DESC, e.id DESC
LIMIT $`+fmt.Sprint(len(queryArgs)-1)+` OFFSET $`+fmt.Sprint(len(queryArgs)),
		queryArgs...,
	)
	if err != nil {
		if isHFCAbuseRiskEventsTableMissing(err) {
			return []service.HFCAbuseRiskEvent{}, paginationResultFromTotal(0, params), nil
		}
		return nil, nil, fmt.Errorf("list hfc abuse risk events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.HFCAbuseRiskEvent, 0)
	for rows.Next() {
		item, err := scanHFCAbuseRiskEvent(rows)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate hfc abuse risk events: %w", err)
	}
	return items, paginationResultFromTotal(total, params), nil
}

func (r *hfcAbuseRiskRepository) GetEvent(ctx context.Context, id int64) (*service.HFCAbuseRiskEvent, error) {
	row := r.db.QueryRowContext(ctx, hfcAbuseRiskEventSelectSQL+`
FROM hfc_abuse_risk_events e
WHERE e.id = $1`, id)
	event, err := scanHFCAbuseRiskEvent(row)
	if err != nil {
		if err == sql.ErrNoRows || isHFCAbuseRiskEventsTableMissing(err) {
			return nil, service.ErrHFCAbuseRiskEventNotFound
		}
		return nil, err
	}
	return event, nil
}

func (r *hfcAbuseRiskRepository) UpdateReview(ctx context.Context, update service.HFCAbuseRiskReviewUpdate) error {
	if update.EventID <= 0 {
		return service.ErrHFCAbuseRiskEventNotFound
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE hfc_abuse_risk_events
SET status = $2,
    action_taken = $3,
    action_note = $4,
    reviewed_by = $5,
    reviewed_at = $6,
    updated_at = NOW()
WHERE id = $1`,
		update.EventID,
		update.Status,
		update.ActionTaken,
		update.ActionNote,
		nullableInt64Ptr(update.ReviewedBy),
		update.ReviewedAt,
	)
	if err != nil {
		if isHFCAbuseRiskEventsTableMissing(err) {
			return service.ErrHFCAbuseRiskEventNotFound
		}
		return fmt.Errorf("update hfc abuse risk event review: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return service.ErrHFCAbuseRiskEventNotFound
	}
	return nil
}

const hfcAbuseRiskEventSelectSQL = `
SELECT
    e.id, e.source, e.severity, e.status, e.user_id, e.user_email,
    e.api_key_id, e.api_key_name, e.group_id, e.group_name, e.risk_score,
    e.signup_ip_prefix, e.device_fingerprint_hash, e.signup_user_agent_hash,
    e.payment_order_id, e.redeem_code_id, e.referral_user_id,
    e.content_moderation_log_id, e.summary, e.evidence,
    e.action_taken, e.action_note, e.reviewed_by, e.reviewed_at,
    e.telegram_sent, e.telegram_sent_at, e.telegram_error,
    e.created_at, e.updated_at
`

type hfcAbuseRiskEventScanner interface {
	Scan(dest ...any) error
}

func scanHFCAbuseRiskEvent(scanner hfcAbuseRiskEventScanner) (*service.HFCAbuseRiskEvent, error) {
	var event service.HFCAbuseRiskEvent
	var userID, apiKeyID, groupID, paymentOrderID, redeemCodeID, referralUserID, moderationLogID, reviewedBy sql.NullInt64
	var reviewedAt, telegramSentAt sql.NullTime
	var evidenceRaw []byte
	if err := scanner.Scan(
		&event.ID,
		&event.Source,
		&event.Severity,
		&event.Status,
		&userID,
		&event.UserEmail,
		&apiKeyID,
		&event.APIKeyName,
		&groupID,
		&event.GroupName,
		&event.RiskScore,
		&event.SignupIPPrefix,
		&event.DeviceFingerprintHash,
		&event.SignupUserAgentHash,
		&paymentOrderID,
		&redeemCodeID,
		&referralUserID,
		&moderationLogID,
		&event.Summary,
		&evidenceRaw,
		&event.ActionTaken,
		&event.ActionNote,
		&reviewedBy,
		&reviewedAt,
		&event.TelegramSent,
		&telegramSentAt,
		&event.TelegramError,
		&event.CreatedAt,
		&event.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, err
		}
		return nil, fmt.Errorf("scan hfc abuse risk event: %w", err)
	}
	event.UserID = int64PtrFromNull(userID)
	event.APIKeyID = int64PtrFromNull(apiKeyID)
	event.GroupID = int64PtrFromNull(groupID)
	event.PaymentOrderID = int64PtrFromNull(paymentOrderID)
	event.RedeemCodeID = int64PtrFromNull(redeemCodeID)
	event.ReferralUserID = int64PtrFromNull(referralUserID)
	event.ContentModerationLogID = int64PtrFromNull(moderationLogID)
	event.ReviewedBy = int64PtrFromNull(reviewedBy)
	if reviewedAt.Valid {
		v := reviewedAt.Time
		event.ReviewedAt = &v
	}
	if telegramSentAt.Valid {
		v := telegramSentAt.Time
		event.TelegramSentAt = &v
	}
	event.Evidence = []service.HFCAbuseRiskEvidence{}
	_ = json.Unmarshal(evidenceRaw, &event.Evidence)
	return &event, nil
}

func buildHFCAbuseRiskEventWhere(filter service.HFCAbuseRiskEventFilter) ([]string, []any) {
	where := []string{"e.id IS NOT NULL"}
	args := make([]any, 0)
	add := func(expr string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	if filter.Source != "" {
		add("e.source = $%d", filter.Source)
	}
	if filter.Severity != "" {
		add("e.severity = $%d", filter.Severity)
	}
	if filter.Status != "" {
		add("e.status = $%d", filter.Status)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := "%" + search + "%"
		args = append(args, like, like, like, like, like, like)
		idx := len(args) - 5
		where = append(where, fmt.Sprintf("(e.user_email ILIKE $%d OR e.api_key_name ILIKE $%d OR e.group_name ILIKE $%d OR e.summary ILIKE $%d OR e.signup_ip_prefix ILIKE $%d OR e.device_fingerprint_hash ILIKE $%d)", idx, idx+1, idx+2, idx+3, idx+4, idx+5))
	}
	if filter.From != nil && !filter.From.IsZero() {
		add("e.created_at >= $%d", *filter.From)
	}
	if filter.To != nil && !filter.To.IsZero() {
		add("e.created_at <= $%d", *filter.To)
	}
	return where, args
}

func defaultHFCAbuseRiskPagination(params pagination.PaginationParams) pagination.PaginationParams {
	if params.Page <= 0 {
		params.Page = 1
	}
	if params.PageSize <= 0 {
		params.PageSize = 20
	}
	if params.PageSize > 100 {
		params.PageSize = 100
	}
	return params
}

func isHFCAbuseRiskEventsTableMissing(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), `relation "hfc_abuse_risk_events" does not exist`)
}

func nullableInt64Ptr(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func int64PtrFromNull(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	v := value.Int64
	return &v
}
