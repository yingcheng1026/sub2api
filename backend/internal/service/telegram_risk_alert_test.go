package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTelegramRiskNotifier_SendHFCAbuseRisk_PostsRedactedMessage(t *testing.T) {
	var got struct {
		Path string
		Body map[string]any
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Path = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got.Body))
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	repo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyHFCTelegramRiskAlertEnabled: "true",
		SettingKeyHFCTelegramBotToken:         "123456:test-token",
		SettingKeyHFCTelegramChatID:           "-100123",
		SettingKeyHFCTelegramMinSeverity:      "high",
	}}
	notifier := NewTelegramRiskNotifier(repo)
	notifier.apiBaseURL = server.URL

	err := notifier.SendHFCAbuseRisk(context.Background(), HFCAbuseRiskTelegramAlert{
		Source:                "signup_risk",
		Severity:              HFCAbuseRiskSeverityHigh,
		Summary:               "duplicate signup signals",
		UserID:                42,
		UserEmail:             "user@example.com",
		RiskScore:             80,
		SignupIPPrefix:        "203.0.113.0/24",
		DeviceFingerprintHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Evidence: []string{
			"hold_reason=duplicate_device_fingerprint",
			"raw secret should not be here",
		},
		Action:     "trial bonus held",
		OccurredAt: time.Date(2026, 5, 28, 1, 30, 0, 0, time.UTC),
	})

	require.NoError(t, err)
	require.Equal(t, "/bot123456:test-token/sendMessage", got.Path)
	require.Equal(t, "-100123", got.Body["chat_id"])
	text, _ := got.Body["text"].(string)
	require.Contains(t, text, "[HFC 风控告警] HIGH")
	require.Contains(t, text, "用户: #42 u***r@example.com")
	require.Contains(t, text, "注册 IP 段: 203.0.113.0/24")
	require.Contains(t, text, "设备指纹 hash: 01234567...89abcdef")
	require.NotContains(t, text, "user@example.com")
	require.NotContains(t, text, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
}

func TestTelegramRiskNotifier_SendHFCAbuseRisk_SkipsBelowMinSeverity(t *testing.T) {
	repo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyHFCTelegramRiskAlertEnabled: "true",
		SettingKeyHFCTelegramBotToken:         "123456:test-token",
		SettingKeyHFCTelegramChatID:           "-100123",
		SettingKeyHFCTelegramMinSeverity:      "critical",
	}}
	notifier := NewTelegramRiskNotifier(repo)

	err := notifier.SendHFCAbuseRisk(context.Background(), HFCAbuseRiskTelegramAlert{
		Severity: HFCAbuseRiskSeverityHigh,
		Summary:  "high only",
	})

	require.ErrorIs(t, err, errTelegramRiskAlertSkipped)
}

func TestTelegramRiskNotifier_SendHFCAbuseRisk_UsesEnvFallback(t *testing.T) {
	t.Setenv("HFC_TELEGRAM_RISK_ALERT_ENABLED", "true")
	t.Setenv("HFC_TELEGRAM_BOT_TOKEN", "123456:env-token")
	t.Setenv("HFC_TELEGRAM_CHAT_ID", "-100env")

	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	notifier := NewTelegramRiskNotifier(&contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyHFCTelegramRiskAlertEnabled: "false",
		SettingKeyHFCTelegramMinSeverity:      "high",
	}})
	notifier.apiBaseURL = server.URL

	err := notifier.SendHFCAbuseRisk(context.Background(), HFCAbuseRiskTelegramAlert{
		Severity: HFCAbuseRiskSeverityCritical,
		Summary:  "env configured",
	})

	require.NoError(t, err)
	require.True(t, called)
}

func TestTelegramRiskNotifier_SendHFCAbuseRisk_DisabledByDefault(t *testing.T) {
	notifier := NewTelegramRiskNotifier(&contentModerationTestSettingRepo{values: map[string]string{}})

	err := notifier.SendHFCAbuseRisk(context.Background(), HFCAbuseRiskTelegramAlert{
		Severity: HFCAbuseRiskSeverityCritical,
		Summary:  "disabled",
	})

	require.True(t, errors.Is(err, errTelegramRiskAlertSkipped))
}
