package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	telegramRiskAlertDefaultBaseURL = "https://api.telegram.org"
	telegramRiskAlertTimeout        = 5 * time.Second
	telegramRiskAlertMaxEvidence    = 6
	telegramRiskAlertMaxLineRunes   = 180

	HFCAbuseRiskSeverityLow      = "low"
	HFCAbuseRiskSeverityMedium   = "medium"
	HFCAbuseRiskSeverityHigh     = "high"
	HFCAbuseRiskSeverityCritical = "critical"
)

var errTelegramRiskAlertSkipped = errors.New("telegram risk alert skipped")

type TelegramRiskNotifier struct {
	settingRepo SettingRepository
	client      *http.Client
	apiBaseURL  string
	timeout     time.Duration
}

type hfcTelegramRiskAlertConfig struct {
	Enabled     bool
	BotToken    string
	ChatID      string
	MinSeverity string
}

type HFCAbuseRiskTelegramAlert struct {
	EventID               int64
	Source                string
	Severity              string
	Summary               string
	UserID                int64
	UserEmail             string
	APIKeyID              int64
	APIKeyName            string
	GroupID               int64
	GroupName             string
	RiskScore             int
	SignupIPPrefix        string
	DeviceFingerprintHash string
	Evidence              []string
	Action                string
	OccurredAt            time.Time
}

func NewTelegramRiskNotifier(settingRepo SettingRepository) *TelegramRiskNotifier {
	return &TelegramRiskNotifier{
		settingRepo: settingRepo,
		client: &http.Client{
			Timeout: telegramRiskAlertTimeout,
		},
		apiBaseURL: telegramRiskAlertDefaultBaseURL,
		timeout:    telegramRiskAlertTimeout,
	}
}

func DispatchHFCAbuseRiskTelegramAlert(settingRepo SettingRepository, alert HFCAbuseRiskTelegramAlert) {
	if settingRepo == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), telegramRiskAlertTimeout)
		defer cancel()

		if err := NewTelegramRiskNotifier(settingRepo).SendHFCAbuseRisk(ctx, alert); err != nil && !errors.Is(err, errTelegramRiskAlertSkipped) {
			slog.Warn("hfc_abuse_risk.telegram_alert_failed",
				"source", sanitizeTelegramLogField(alert.Source),
				"severity", normalizeHFCAbuseRiskSeverity(alert.Severity),
				"user_id", alert.UserID,
				"api_key_id", alert.APIKeyID,
				"error", err)
		}
	}()
}

func (n *TelegramRiskNotifier) SendHFCAbuseRisk(ctx context.Context, alert HFCAbuseRiskTelegramAlert) error {
	if n == nil || n.settingRepo == nil {
		return errTelegramRiskAlertSkipped
	}
	cfg, err := n.loadConfig(ctx)
	if err != nil {
		return fmt.Errorf("load telegram risk alert config: %w", err)
	}
	if !cfg.Enabled {
		return errTelegramRiskAlertSkipped
	}
	if cfg.BotToken == "" || cfg.ChatID == "" {
		return errors.New("telegram risk alert missing bot token or chat id")
	}
	if !hfcAbuseRiskSeverityAllowed(alert.Severity, cfg.MinSeverity) {
		return errTelegramRiskAlertSkipped
	}

	token := strings.TrimSpace(cfg.BotToken)
	if strings.Contains(token, "/") {
		return errors.New("telegram bot token contains invalid path separator")
	}

	payload := map[string]any{
		"chat_id":                  cfg.ChatID,
		"text":                     buildHFCAbuseRiskTelegramMessage(alert),
		"disable_web_page_preview": true,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal telegram payload: %w", err)
	}

	reqCtx := ctx
	if _, ok := ctx.Deadline(); !ok && n.timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, n.timeout)
		defer cancel()
	}
	url := strings.TrimRight(n.apiBaseURL, "/") + "/bot" + token + "/sendMessage"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return errors.New("build telegram request failed")
	}
	req.Header.Set("Content-Type", "application/json")

	client := n.client
	if client == nil {
		client = &http.Client{Timeout: telegramRiskAlertTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("send telegram request failed")
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var apiResp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(body, &apiResp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !apiResp.OK {
		desc := strings.TrimSpace(apiResp.Description)
		if desc == "" {
			desc = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("telegram send failed: status=%d description=%s", resp.StatusCode, sanitizeTelegramLine(desc))
	}
	return nil
}

func (n *TelegramRiskNotifier) loadConfig(ctx context.Context) (hfcTelegramRiskAlertConfig, error) {
	keys := []string{
		SettingKeyHFCTelegramRiskAlertEnabled,
		SettingKeyHFCTelegramBotToken,
		SettingKeyHFCTelegramChatID,
		SettingKeyHFCTelegramMinSeverity,
	}
	values, err := n.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return hfcTelegramRiskAlertConfig{}, err
	}

	enabled := parseBoolSetting(firstNonEmpty(
		os.Getenv("HFC_TELEGRAM_RISK_ALERT_ENABLED"),
		os.Getenv("HFC_TELEGRAM_ALERTS_ENABLED"),
		values[SettingKeyHFCTelegramRiskAlertEnabled],
	))
	return hfcTelegramRiskAlertConfig{
		Enabled: enabled,
		BotToken: firstNonEmpty(
			os.Getenv("HFC_TELEGRAM_BOT_TOKEN"),
			os.Getenv("TELEGRAM_BOT_TOKEN"),
			values[SettingKeyHFCTelegramBotToken],
		),
		ChatID: firstNonEmpty(
			os.Getenv("HFC_TELEGRAM_CHAT_ID"),
			os.Getenv("TELEGRAM_CHAT_ID"),
			values[SettingKeyHFCTelegramChatID],
		),
		MinSeverity: normalizeHFCAbuseRiskSeverity(firstNonEmpty(
			os.Getenv("HFC_TELEGRAM_MIN_SEVERITY"),
			values[SettingKeyHFCTelegramMinSeverity],
			HFCAbuseRiskSeverityHigh,
		)),
	}, nil
}

func buildHFCAbuseRiskTelegramMessage(alert HFCAbuseRiskTelegramAlert) string {
	severity := strings.ToUpper(normalizeHFCAbuseRiskSeverity(alert.Severity))
	occurredAt := alert.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}

	lines := []string{
		fmt.Sprintf("[HFC 风控告警] %s", severity),
		"来源: " + sanitizeTelegramLine(firstNonEmpty(alert.Source, "hfc_abuse_risk")),
		"摘要: " + sanitizeTelegramLine(alert.Summary),
	}
	if alert.EventID > 0 {
		lines = append(lines, "事件: #"+strconv.FormatInt(alert.EventID, 10))
	}
	if alert.UserID > 0 || strings.TrimSpace(alert.UserEmail) != "" {
		user := ""
		if alert.UserID > 0 {
			user = "#" + strconv.FormatInt(alert.UserID, 10)
		}
		if email := strings.TrimSpace(alert.UserEmail); email != "" {
			user = strings.TrimSpace(user + " " + MaskEmail(email))
		}
		lines = append(lines, "用户: "+sanitizeTelegramLine(user))
	}
	if alert.APIKeyID > 0 || strings.TrimSpace(alert.APIKeyName) != "" {
		apiKey := ""
		if alert.APIKeyID > 0 {
			apiKey = "#" + strconv.FormatInt(alert.APIKeyID, 10)
		}
		if name := strings.TrimSpace(alert.APIKeyName); name != "" {
			apiKey = strings.TrimSpace(apiKey + " " + name)
		}
		lines = append(lines, "API key: "+sanitizeTelegramLine(apiKey))
	}
	if alert.GroupID > 0 || strings.TrimSpace(alert.GroupName) != "" {
		group := ""
		if alert.GroupID > 0 {
			group = "#" + strconv.FormatInt(alert.GroupID, 10)
		}
		if name := strings.TrimSpace(alert.GroupName); name != "" {
			group = strings.TrimSpace(group + " " + name)
		}
		lines = append(lines, "分组: "+sanitizeTelegramLine(group))
	}
	if alert.RiskScore > 0 {
		lines = append(lines, "风险分: "+strconv.Itoa(alert.RiskScore))
	}
	if alert.SignupIPPrefix != "" {
		lines = append(lines, "注册 IP 段: "+sanitizeTelegramLine(alert.SignupIPPrefix))
	}
	if alert.DeviceFingerprintHash != "" {
		lines = append(lines, "设备指纹 hash: "+truncateMiddle(alert.DeviceFingerprintHash, 8, 8))
	}

	if evidence := normalizeTelegramEvidence(alert.Evidence); len(evidence) > 0 {
		lines = append(lines, "证据:")
		for _, item := range evidence {
			lines = append(lines, "- "+item)
		}
	}
	if alert.Action != "" {
		lines = append(lines, "建议/动作: "+sanitizeTelegramLine(alert.Action))
	}
	lines = append(lines, "时间: "+occurredAt.Format(time.RFC3339))

	return strings.Join(lines, "\n")
}

func normalizeTelegramEvidence(items []string) []string {
	out := make([]string, 0, minInt(len(items), telegramRiskAlertMaxEvidence))
	for _, item := range items {
		clean := sanitizeTelegramLine(item)
		if clean == "" {
			continue
		}
		out = append(out, clean)
		if len(out) >= telegramRiskAlertMaxEvidence {
			break
		}
	}
	return out
}

func sanitizeTelegramLine(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	fields := strings.Fields(value)
	if len(fields) > 0 {
		value = strings.Join(fields, " ")
	}
	runes := []rune(value)
	if len(runes) > telegramRiskAlertMaxLineRunes {
		return string(runes[:telegramRiskAlertMaxLineRunes]) + "..."
	}
	return value
}

func sanitizeTelegramLogField(value string) string {
	return sanitizeTelegramLine(value)
}

func truncateMiddle(value string, head, tail int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= head+tail+3 {
		return value
	}
	return string(runes[:head]) + "..." + string(runes[len(runes)-tail:])
}

func hfcAbuseRiskSeverityAllowed(actual, minimum string) bool {
	return hfcAbuseRiskSeverityRank(normalizeHFCAbuseRiskSeverity(actual)) >= hfcAbuseRiskSeverityRank(normalizeHFCAbuseRiskSeverity(minimum))
}

func normalizeHFCAbuseRiskSeverity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case HFCAbuseRiskSeverityLow, HFCAbuseRiskSeverityMedium, HFCAbuseRiskSeverityHigh, HFCAbuseRiskSeverityCritical:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return HFCAbuseRiskSeverityHigh
	}
}

func hfcAbuseRiskSeverityRank(value string) int {
	switch normalizeHFCAbuseRiskSeverity(value) {
	case HFCAbuseRiskSeverityLow:
		return 1
	case HFCAbuseRiskSeverityMedium:
		return 2
	case HFCAbuseRiskSeverityHigh:
		return 3
	case HFCAbuseRiskSeverityCritical:
		return 4
	default:
		return 3
	}
}

func parseBoolSetting(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "y", "on", "enabled":
		return true
	default:
		return false
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
