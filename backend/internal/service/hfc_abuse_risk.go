package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const (
	HFCAbuseRiskSourceSignupRisk               = "signup_risk"
	HFCAbuseRiskSourceContentModerationAutoBan = "content_moderation_auto_ban"

	HFCAbuseRiskStatusOpen          = "open"
	HFCAbuseRiskStatusReviewing     = "reviewing"
	HFCAbuseRiskStatusResolved      = "resolved"
	HFCAbuseRiskStatusFalsePositive = "false_positive"

	HFCAbuseRiskActionMarkReviewing      = "mark_reviewing"
	HFCAbuseRiskActionResolveNoAction    = "resolve_no_action"
	HFCAbuseRiskActionMarkFalsePositive  = "mark_false_positive"
	HFCAbuseRiskActionDisableAPIKey      = "disable_api_key"
	HFCAbuseRiskActionDisableUserAPIKeys = "disable_user_api_keys"
	HFCAbuseRiskActionFreezeUser         = "freeze_user"

	hfcAbuseRiskMaxActionNoteRunes = 500
	hfcAbuseRiskActionPageSize     = 200
)

var (
	ErrHFCAbuseRiskEventNotFound = infraerrors.NotFound("HFC_ABUSE_RISK_EVENT_NOT_FOUND", "HFC abuse risk event not found")
	ErrHFCAbuseRiskInvalidAction = infraerrors.BadRequest("HFC_ABUSE_RISK_INVALID_ACTION", "invalid HFC abuse risk action")
	ErrHFCAbuseRiskMissingTarget = infraerrors.BadRequest("HFC_ABUSE_RISK_MISSING_TARGET", "risk event does not have the required target")
)

type HFCAbuseRiskEvidence struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
}

type HFCAbuseRiskEvent struct {
	ID                     int64                  `json:"id"`
	Source                 string                 `json:"source"`
	Severity               string                 `json:"severity"`
	Status                 string                 `json:"status"`
	UserID                 *int64                 `json:"user_id"`
	UserEmail              string                 `json:"user_email"`
	APIKeyID               *int64                 `json:"api_key_id"`
	APIKeyName             string                 `json:"api_key_name"`
	GroupID                *int64                 `json:"group_id"`
	GroupName              string                 `json:"group_name"`
	RiskScore              int                    `json:"risk_score"`
	SignupIPPrefix         string                 `json:"signup_ip_prefix"`
	DeviceFingerprintHash  string                 `json:"device_fingerprint_hash"`
	SignupUserAgentHash    string                 `json:"signup_user_agent_hash"`
	PaymentOrderID         *int64                 `json:"payment_order_id"`
	RedeemCodeID           *int64                 `json:"redeem_code_id"`
	ReferralUserID         *int64                 `json:"referral_user_id"`
	ContentModerationLogID *int64                 `json:"content_moderation_log_id"`
	Summary                string                 `json:"summary"`
	Evidence               []HFCAbuseRiskEvidence `json:"evidence"`
	ActionTaken            string                 `json:"action_taken"`
	ActionNote             string                 `json:"action_note"`
	ReviewedBy             *int64                 `json:"reviewed_by"`
	ReviewedAt             *time.Time             `json:"reviewed_at"`
	TelegramSent           bool                   `json:"telegram_sent"`
	TelegramSentAt         *time.Time             `json:"telegram_sent_at"`
	TelegramError          string                 `json:"telegram_error"`
	CreatedAt              time.Time              `json:"created_at"`
	UpdatedAt              time.Time              `json:"updated_at"`
}

type HFCAbuseRiskEventFilter struct {
	Pagination pagination.PaginationParams
	Source     string
	Severity   string
	Status     string
	Search     string
	From       *time.Time
	To         *time.Time
}

type HFCAbuseRiskActionInput struct {
	EventID     int64
	Action      string
	AdminUserID int64
	Note        string
}

type HFCAbuseRiskActionResult struct {
	Event           *HFCAbuseRiskEvent `json:"event"`
	DisabledAPIKeys int                `json:"disabled_api_keys"`
	FrozenUser      bool               `json:"frozen_user"`
}

type HFCAbuseRiskReviewUpdate struct {
	EventID       int64
	Status        string
	ActionTaken   string
	ActionNote    string
	ReviewedBy    *int64
	ReviewedAt    time.Time
	TelegramSent  *bool
	TelegramError *string
}

type HFCAbuseRiskRepository interface {
	CreateEvent(ctx context.Context, event *HFCAbuseRiskEvent) error
	ListEvents(ctx context.Context, filter HFCAbuseRiskEventFilter) ([]HFCAbuseRiskEvent, *pagination.PaginationResult, error)
	GetEvent(ctx context.Context, id int64) (*HFCAbuseRiskEvent, error)
	UpdateReview(ctx context.Context, update HFCAbuseRiskReviewUpdate) error
}

type HFCAbuseRiskRecorder interface {
	RecordEvent(ctx context.Context, event *HFCAbuseRiskEvent) (*HFCAbuseRiskEvent, error)
}

type HFCAbuseRiskService struct {
	repo                 HFCAbuseRiskRepository
	apiKeyRepo           APIKeyRepository
	userRepo             UserRepository
	authCacheInvalidator APIKeyAuthCacheInvalidator
}

func NewHFCAbuseRiskService(
	repo HFCAbuseRiskRepository,
	apiKeyRepo APIKeyRepository,
	userRepo UserRepository,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
) *HFCAbuseRiskService {
	return &HFCAbuseRiskService{
		repo:                 repo,
		apiKeyRepo:           apiKeyRepo,
		userRepo:             userRepo,
		authCacheInvalidator: authCacheInvalidator,
	}
}

func (s *HFCAbuseRiskService) RecordEvent(ctx context.Context, event *HFCAbuseRiskEvent) (*HFCAbuseRiskEvent, error) {
	if s == nil || s.repo == nil || event == nil {
		return event, nil
	}
	normalized := cloneHFCAbuseRiskEvent(event)
	normalized.Source = strings.TrimSpace(normalized.Source)
	if normalized.Source == "" {
		normalized.Source = "hfc_abuse_risk"
	}
	normalized.Severity = normalizeHFCAbuseRiskSeverity(normalized.Severity)
	normalized.Status = normalizeHFCAbuseRiskStatus(normalized.Status)
	normalized.UserEmail = strings.TrimSpace(normalized.UserEmail)
	normalized.APIKeyName = strings.TrimSpace(normalized.APIKeyName)
	normalized.GroupName = strings.TrimSpace(normalized.GroupName)
	normalized.SignupIPPrefix = strings.TrimSpace(normalized.SignupIPPrefix)
	normalized.DeviceFingerprintHash = strings.TrimSpace(normalized.DeviceFingerprintHash)
	normalized.SignupUserAgentHash = strings.TrimSpace(normalized.SignupUserAgentHash)
	normalized.Summary = trimRunes(strings.TrimSpace(normalized.Summary), 500)
	normalized.Evidence = sanitizeHFCAbuseRiskEvidence(normalized.Evidence)
	if normalized.Status == "" {
		normalized.Status = HFCAbuseRiskStatusOpen
	}
	if err := s.repo.CreateEvent(ctx, normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func (s *HFCAbuseRiskService) ListEvents(ctx context.Context, filter HFCAbuseRiskEventFilter) ([]HFCAbuseRiskEvent, *pagination.PaginationResult, error) {
	if s == nil || s.repo == nil {
		return []HFCAbuseRiskEvent{}, paginationResultOrDefault(filter.Pagination), nil
	}
	filter.Source = strings.TrimSpace(filter.Source)
	filter.Severity = normalizeHFCAbuseRiskSeverityAllowEmpty(filter.Severity)
	filter.Status = normalizeHFCAbuseRiskStatusAllowEmpty(filter.Status)
	filter.Search = strings.TrimSpace(filter.Search)
	return s.repo.ListEvents(ctx, filter)
}

func (s *HFCAbuseRiskService) GetEvent(ctx context.Context, id int64) (*HFCAbuseRiskEvent, error) {
	if id <= 0 {
		return nil, ErrHFCAbuseRiskEventNotFound
	}
	if s == nil || s.repo == nil {
		return nil, ErrHFCAbuseRiskEventNotFound
	}
	return s.repo.GetEvent(ctx, id)
}

func (s *HFCAbuseRiskService) ApplyAction(ctx context.Context, input HFCAbuseRiskActionInput) (*HFCAbuseRiskActionResult, error) {
	if s == nil || s.repo == nil || input.EventID <= 0 {
		return nil, ErrHFCAbuseRiskEventNotFound
	}
	action := strings.TrimSpace(input.Action)
	event, err := s.repo.GetEvent(ctx, input.EventID)
	if err != nil {
		return nil, err
	}

	result := &HFCAbuseRiskActionResult{}
	status := HFCAbuseRiskStatusResolved
	switch action {
	case HFCAbuseRiskActionMarkReviewing:
		status = HFCAbuseRiskStatusReviewing
	case HFCAbuseRiskActionResolveNoAction:
		status = HFCAbuseRiskStatusResolved
	case HFCAbuseRiskActionMarkFalsePositive:
		status = HFCAbuseRiskStatusFalsePositive
	case HFCAbuseRiskActionDisableAPIKey:
		disabled, err := s.disableEventAPIKey(ctx, event)
		if err != nil {
			return nil, err
		}
		result.DisabledAPIKeys = disabled
	case HFCAbuseRiskActionDisableUserAPIKeys:
		disabled, err := s.disableEventUserAPIKeys(ctx, event)
		if err != nil {
			return nil, err
		}
		result.DisabledAPIKeys = disabled
	case HFCAbuseRiskActionFreezeUser:
		frozen, err := s.freezeEventUser(ctx, event)
		if err != nil {
			return nil, err
		}
		result.FrozenUser = frozen
	default:
		return nil, ErrHFCAbuseRiskInvalidAction
	}

	reviewedAt := time.Now()
	var reviewer *int64
	if input.AdminUserID > 0 {
		reviewer = &input.AdminUserID
	}
	if err := s.repo.UpdateReview(ctx, HFCAbuseRiskReviewUpdate{
		EventID:     event.ID,
		Status:      status,
		ActionTaken: action,
		ActionNote:  trimRunes(strings.TrimSpace(input.Note), hfcAbuseRiskMaxActionNoteRunes),
		ReviewedBy:  reviewer,
		ReviewedAt:  reviewedAt,
	}); err != nil {
		return nil, err
	}
	updated, err := s.repo.GetEvent(ctx, event.ID)
	if err != nil {
		return nil, err
	}
	result.Event = updated
	return result, nil
}

func (s *HFCAbuseRiskService) disableEventAPIKey(ctx context.Context, event *HFCAbuseRiskEvent) (int, error) {
	if event == nil || event.APIKeyID == nil || *event.APIKeyID <= 0 {
		return 0, ErrHFCAbuseRiskMissingTarget
	}
	if s.apiKeyRepo == nil {
		return 0, infraerrors.InternalServer("HFC_ABUSE_RISK_API_KEY_REPO_MISSING", "API key repository is unavailable")
	}
	key, err := s.apiKeyRepo.GetByID(ctx, *event.APIKeyID)
	if err != nil {
		return 0, err
	}
	if event.UserID != nil && *event.UserID > 0 && key.UserID != *event.UserID {
		return 0, infraerrors.Forbidden("HFC_ABUSE_RISK_TARGET_MISMATCH", "API key does not belong to the event user")
	}
	return s.disableAPIKey(ctx, key)
}

func (s *HFCAbuseRiskService) disableEventUserAPIKeys(ctx context.Context, event *HFCAbuseRiskEvent) (int, error) {
	if event == nil || event.UserID == nil || *event.UserID <= 0 {
		return 0, ErrHFCAbuseRiskMissingTarget
	}
	if s.apiKeyRepo == nil {
		return 0, infraerrors.InternalServer("HFC_ABUSE_RISK_API_KEY_REPO_MISSING", "API key repository is unavailable")
	}
	disabled := 0
	for {
		keys, _, err := s.apiKeyRepo.ListByUserID(ctx, *event.UserID, pagination.PaginationParams{
			Page:     1,
			PageSize: hfcAbuseRiskActionPageSize,
		}, APIKeyListFilters{Status: StatusAPIKeyActive})
		if err != nil {
			return disabled, err
		}
		if len(keys) == 0 {
			return disabled, nil
		}
		for i := range keys {
			n, err := s.disableAPIKey(ctx, &keys[i])
			if err != nil {
				return disabled, err
			}
			disabled += n
		}
	}
}

func (s *HFCAbuseRiskService) disableAPIKey(ctx context.Context, key *APIKey) (int, error) {
	if key == nil || key.ID <= 0 {
		return 0, ErrHFCAbuseRiskMissingTarget
	}
	if key.Status == StatusAPIKeyDisabled {
		return 0, nil
	}
	key.Status = StatusAPIKeyDisabled
	if err := s.apiKeyRepo.Update(ctx, key); err != nil {
		return 0, err
	}
	if s.authCacheInvalidator != nil && key.Key != "" {
		s.authCacheInvalidator.InvalidateAuthCacheByKey(ctx, key.Key)
	}
	return 1, nil
}

func (s *HFCAbuseRiskService) freezeEventUser(ctx context.Context, event *HFCAbuseRiskEvent) (bool, error) {
	if event == nil || event.UserID == nil || *event.UserID <= 0 {
		return false, ErrHFCAbuseRiskMissingTarget
	}
	if s.userRepo == nil {
		return false, infraerrors.InternalServer("HFC_ABUSE_RISK_USER_REPO_MISSING", "user repository is unavailable")
	}
	user, err := s.userRepo.GetByID(ctx, *event.UserID)
	if err != nil {
		return false, err
	}
	if user.Role == RoleAdmin {
		return false, infraerrors.Forbidden("HFC_ABUSE_RISK_REFUSE_ADMIN_FREEZE", "admin users cannot be frozen from abuse review")
	}
	if user.Status == StatusDisabled {
		return false, nil
	}
	user.Status = StatusDisabled
	if err := s.userRepo.Update(ctx, user); err != nil {
		return false, err
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, user.ID)
	}
	return true, nil
}

func cloneHFCAbuseRiskEvent(in *HFCAbuseRiskEvent) *HFCAbuseRiskEvent {
	if in == nil {
		return nil
	}
	out := *in
	if in.Evidence != nil {
		out.Evidence = make([]HFCAbuseRiskEvidence, len(in.Evidence))
		copy(out.Evidence, in.Evidence)
	}
	return &out
}

func sanitizeHFCAbuseRiskEvidence(items []HFCAbuseRiskEvidence) []HFCAbuseRiskEvidence {
	out := make([]HFCAbuseRiskEvidence, 0, len(items))
	for _, item := range items {
		key := trimRunes(strings.TrimSpace(item.Key), 80)
		label := trimRunes(strings.TrimSpace(item.Label), 120)
		value := trimRunes(strings.TrimSpace(item.Value), 500)
		if key == "" && label == "" && value == "" {
			continue
		}
		out = append(out, HFCAbuseRiskEvidence{
			Key:   key,
			Label: label,
			Value: value,
		})
	}
	return out
}

func normalizeHFCAbuseRiskStatus(value string) string {
	if status := normalizeHFCAbuseRiskStatusAllowEmpty(value); status != "" {
		return status
	}
	return HFCAbuseRiskStatusOpen
}

func normalizeHFCAbuseRiskStatusAllowEmpty(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case HFCAbuseRiskStatusOpen, HFCAbuseRiskStatusReviewing, HFCAbuseRiskStatusResolved, HFCAbuseRiskStatusFalsePositive:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeHFCAbuseRiskSeverityAllowEmpty(value string) string {
	raw := strings.ToLower(strings.TrimSpace(value))
	switch raw {
	case HFCAbuseRiskSeverityLow, HFCAbuseRiskSeverityMedium, HFCAbuseRiskSeverityHigh, HFCAbuseRiskSeverityCritical:
		return raw
	default:
		return ""
	}
}

func paginationResultOrDefault(params pagination.PaginationParams) *pagination.PaginationResult {
	if params.Page <= 0 {
		params.Page = 1
	}
	if params.PageSize <= 0 {
		params.PageSize = 20
	}
	return &pagination.PaginationResult{
		Total:    0,
		Page:     params.Page,
		PageSize: params.Limit(),
		Pages:    1,
	}
}

func logHFCAbuseRiskRecordFailure(source string, userID int64, apiKeyID int64, err error) {
	if err == nil {
		return
	}
	slog.Warn("hfc_abuse_risk.record_event_failed",
		"source", source,
		"user_id", userID,
		"api_key_id", apiKeyID,
		"error", err)
}

func hfcRiskEvidence(key, label string, value any) HFCAbuseRiskEvidence {
	return HFCAbuseRiskEvidence{Key: key, Label: label, Value: fmt.Sprint(value)}
}
