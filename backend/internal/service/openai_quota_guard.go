package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	OpenAIQuotaGuardThresholdPercent = 95.0
	OpenAIQuotaGuardReasonPrefix     = "hfc-quota-guard:"
	OpenAIQuotaGuardReasonCodex7d    = OpenAIQuotaGuardReasonPrefix + "openai-codex-7d>=95"
	OpenAIQuotaGuardReasonNoReset    = OpenAIQuotaGuardReasonCodex7d + ":no-reset-at"
	OpenAIQuotaGuardNoResetCooldown  = 6 * time.Hour
)

type OpenAIQuotaGuardDecision struct {
	Until       time.Time
	Reason      string
	UsedPercent float64
}

func (a *Account) OpenAIQuotaGuardDecision(now time.Time) *OpenAIQuotaGuardDecision {
	if a == nil || a.Platform != PlatformOpenAI || a.Type != AccountTypeOAuth {
		return nil
	}
	return openAIQuotaGuardDecisionFromExtra(a.Extra, now)
}

func (a *Account) IsOpenAIQuotaGuardedAt(now time.Time) bool {
	return a.OpenAIQuotaGuardDecision(now) != nil
}

func openAIQuotaGuardDecisionFromExtra(extra map[string]any, now time.Time) *OpenAIQuotaGuardDecision {
	if len(extra) == 0 {
		return nil
	}
	usedPercent := parseExtraFloat64(extra["codex_7d_used_percent"])
	if usedPercent < OpenAIQuotaGuardThresholdPercent {
		return nil
	}

	if resetAt, ok := parseOpenAIQuotaGuardTime(extra["codex_7d_reset_at"]); ok {
		if resetAt.After(now) {
			return &OpenAIQuotaGuardDecision{
				Until:       resetAt,
				Reason:      OpenAIQuotaGuardReasonCodex7d,
				UsedPercent: usedPercent,
			}
		}
		return nil
	}

	base := now
	if updatedAt, ok := parseOpenAIQuotaGuardTime(extra["codex_usage_updated_at"]); ok {
		base = updatedAt
	}
	until := base.Add(OpenAIQuotaGuardNoResetCooldown)
	if !until.After(now) {
		return nil
	}
	return &OpenAIQuotaGuardDecision{
		Until:       until,
		Reason:      OpenAIQuotaGuardReasonNoReset,
		UsedPercent: usedPercent,
	}
}

func parseOpenAIQuotaGuardTime(value any) (time.Time, bool) {
	switch v := value.(type) {
	case time.Time:
		if v.IsZero() {
			return time.Time{}, false
		}
		return v, true
	case *time.Time:
		if v == nil || v.IsZero() {
			return time.Time{}, false
		}
		return *v, true
	}

	raw := strings.TrimSpace(fmt.Sprint(value))
	if raw == "" || raw == "<nil>" {
		return time.Time{}, false
	}
	parsed, err := parseTime(raw)
	if err != nil || parsed.IsZero() {
		return time.Time{}, false
	}
	return parsed, true
}

func applyOpenAIQuotaGuardFromUpdates(ctx context.Context, repo AccountRepository, accountID int64, updates map[string]any, now time.Time) {
	if repo == nil || accountID <= 0 {
		return
	}
	decision := openAIQuotaGuardDecisionFromExtra(updates, now)
	if decision == nil {
		return
	}
	if err := repo.SetTempUnschedulable(ctx, accountID, decision.Until, decision.Reason); err != nil {
		slog.Warn("openai_quota_guard_set_temp_unschedulable_failed",
			"account_id", accountID,
			"used_percent", decision.UsedPercent,
			"until", decision.Until,
			"reason", decision.Reason,
			"error", err,
		)
		return
	}
	slog.Info("openai_quota_guard_temp_unschedulable_set",
		"account_id", accountID,
		"used_percent", decision.UsedPercent,
		"until", decision.Until,
		"reason", decision.Reason,
	)
}
