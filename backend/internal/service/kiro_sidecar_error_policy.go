package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const kiroTemporaryLimitCooldown = 30 * time.Minute

type kiroSidecarErrorAction int

const (
	kiroSidecarErrorNone kiroSidecarErrorAction = iota
	kiroSidecarErrorCredentialRejected
	kiroSidecarErrorTemporaryLimit
)

type kiroSidecarErrorDecision struct {
	action          kiroSidecarErrorAction
	effectiveStatus int
	message         string
}

// HandleKiroSidecarUpstreamError applies account health side effects for
// errors returned by the dedicated Kiro sidecar path before failover selects a
// new account.
func (s *GatewayService) HandleKiroSidecarUpstreamError(ctx context.Context, account *Account, statusCode int, headers http.Header, responseBody []byte) bool {
	if s == nil || s.rateLimitService == nil || account == nil || account.Platform != PlatformKiro {
		return false
	}
	return s.rateLimitService.HandleUpstreamError(ctx, account, statusCode, headers, responseBody)
}

func (s *RateLimitService) applyKiroSidecarErrorPolicy(ctx context.Context, account *Account, statusCode int, responseBody []byte) bool {
	if s == nil || s.accountRepo == nil || account == nil || account.Platform != PlatformKiro {
		return false
	}

	decision := classifyKiroSidecarError(statusCode, responseBody)
	switch decision.action {
	case kiroSidecarErrorCredentialRejected:
		s.handleAuthError(ctx, account, decision.message)
		return true
	case kiroSidecarErrorTemporaryLimit:
		until := time.Now().Add(kiroTemporaryLimitCooldown)
		reason := fmt.Sprintf("%s; cooldown=%s", decision.message, kiroTemporaryLimitCooldown)
		if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
			slog.Warn("kiro_temp_limit_set_temp_unschedulable_failed", "account_id", account.ID, "error", err)
			return false
		}
		slog.Warn("kiro_temp_limit_unscheduled", "account_id", account.ID, "until", until, "effective_status", decision.effectiveStatus)
		return true
	default:
		return false
	}
}

func (s *AccountTestService) applyKiroAccountTestErrorPolicy(ctx context.Context, account *Account, statusCode int, responseBody []byte) {
	if s == nil || s.accountRepo == nil || account == nil || account.Platform != PlatformKiro {
		return
	}

	decision := classifyKiroSidecarError(statusCode, responseBody)
	switch decision.action {
	case kiroSidecarErrorCredentialRejected:
		if err := s.accountRepo.SetError(ctx, account.ID, decision.message); err != nil {
			slog.Warn("kiro_account_test_set_error_failed", "account_id", account.ID, "error", err)
		}
	case kiroSidecarErrorTemporaryLimit:
		until := time.Now().Add(kiroTemporaryLimitCooldown)
		reason := fmt.Sprintf("%s; cooldown=%s", decision.message, kiroTemporaryLimitCooldown)
		if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
			slog.Warn("kiro_account_test_set_temp_unschedulable_failed", "account_id", account.ID, "error", err)
		}
	}
}

func classifyKiroSidecarError(statusCode int, responseBody []byte) kiroSidecarErrorDecision {
	text := normalizeKiroSidecarErrorText(responseBody)
	effectiveStatus := statusCode
	if wrappedStatus := extractKiroWrappedHTTPStatus(text); wrappedStatus > 0 {
		effectiveStatus = wrappedStatus
	}

	if isKiroCredentialRejected(effectiveStatus, text) {
		return kiroSidecarErrorDecision{
			action:          kiroSidecarErrorCredentialRejected,
			effectiveStatus: effectiveStatus,
			message:         buildKiroSidecarErrorMessage("Kiro API credential rejected", statusCode, effectiveStatus, responseBody),
		}
	}

	if isKiroTemporaryLimit(effectiveStatus, text) {
		return kiroSidecarErrorDecision{
			action:          kiroSidecarErrorTemporaryLimit,
			effectiveStatus: effectiveStatus,
			message:         buildKiroSidecarErrorMessage("Kiro temporary upstream limit", statusCode, effectiveStatus, responseBody),
		}
	}

	return kiroSidecarErrorDecision{action: kiroSidecarErrorNone, effectiveStatus: effectiveStatus}
}

func normalizeKiroSidecarErrorText(responseBody []byte) string {
	parts := []string{
		ExtractUpstreamErrorMessage(responseBody),
		string(responseBody),
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func extractKiroWrappedHTTPStatus(text string) int {
	searchFrom := 0
	for {
		idx := strings.Index(text[searchFrom:], "http ")
		if idx < 0 {
			return 0
		}
		start := searchFrom + idx + len("http ")
		end := start
		for end < len(text) && text[end] >= '0' && text[end] <= '9' {
			end++
		}
		if end-start == 3 {
			if status, err := strconv.Atoi(text[start:end]); err == nil {
				return status
			}
		}
		searchFrom = start
		if searchFrom >= len(text) {
			return 0
		}
	}
}

func isKiroCredentialRejected(effectiveStatus int, text string) bool {
	if effectiveStatus != http.StatusUnauthorized {
		return false
	}
	if strings.Contains(text, "bad credentials") {
		return true
	}
	if strings.Contains(text, "kiro auth") && containsAnyKiroErrorText(text, "unauthorized", "invalid credential", "invalid token", "invalid refresh") {
		return true
	}
	return false
}

func isKiroTemporaryLimit(effectiveStatus int, text string) bool {
	if effectiveStatus == http.StatusTooManyRequests {
		return true
	}
	return containsAnyKiroErrorText(text, "too many requests", "suspicious activity", "temporary limit", "temporary limits")
}

func containsAnyKiroErrorText(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func buildKiroSidecarErrorMessage(prefix string, sidecarStatus int, effectiveStatus int, responseBody []byte) string {
	msg := strings.TrimSpace(ExtractUpstreamErrorMessage(responseBody))
	if msg == "" {
		msg = truncateForLog(responseBody, 512)
	}
	if msg == "" {
		msg = "no upstream detail"
	}
	return fmt.Sprintf("%s (sidecar HTTP %d, upstream HTTP %d): %s", prefix, sidecarStatus, effectiveStatus, msg)
}
