//go:build unit

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResolveSMTPTestPasswordRequiresReentryWhenCredentialIdentityChanges(t *testing.T) {
	saved := &service.SMTPConfig{
		Host: "smtp.example.com", Port: 587, Username: "mailer", Password: "saved-secret", UseTLS: true,
	}
	for _, tc := range []struct {
		name   string
		config service.SMTPConfig
	}{
		{name: "host", config: service.SMTPConfig{Host: "attacker.example.com", Port: 587, Username: "mailer", UseTLS: true}},
		{name: "port", config: service.SMTPConfig{Host: "smtp.example.com", Port: 2525, Username: "mailer", UseTLS: true}},
		{name: "username", config: service.SMTPConfig{Host: "smtp.example.com", Port: 587, Username: "other", UseTLS: true}},
		{name: "tls downgrade", config: service.SMTPConfig{Host: "smtp.example.com", Port: 587, Username: "mailer", UseTLS: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			password, err := resolveSMTPTestPassword("", &tc.config, saved)
			require.Error(t, err)
			require.Empty(t, password)
		})
	}
}

func TestResolveSMTPTestPasswordPreservesSameIdentityAndExplicitSecret(t *testing.T) {
	saved := &service.SMTPConfig{
		Host: "smtp.example.com", Port: 587, Username: "mailer", Password: "saved-secret", UseTLS: true,
	}
	same := &service.SMTPConfig{Host: "SMTP.EXAMPLE.COM", Port: 587, Username: "mailer", UseTLS: true}

	password, err := resolveSMTPTestPassword("", same, saved)
	require.NoError(t, err)
	require.Equal(t, "saved-secret", password)

	changed := &service.SMTPConfig{Host: "custom.example.com", Port: 465, Username: "custom", UseTLS: true}
	password, err = resolveSMTPTestPassword("new-secret", changed, saved)
	require.NoError(t, err)
	require.Equal(t, "new-secret", password)
}

func TestSMTPTestHandlersRejectSavedPasswordReuseForChangedDestination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{values: map[string]string{
		service.SettingKeySMTPHost:     "smtp.example.com",
		service.SettingKeySMTPPort:     "587",
		service.SettingKeySMTPUsername: "mailer",
		service.SettingKeySMTPPassword: "saved-secret",
		service.SettingKeySMTPUseTLS:   "true",
	}}
	handler := NewSettingHandler(nil, service.NewEmailService(repo, nil), nil, nil, nil, nil)

	for _, tc := range []struct {
		name string
		path string
		body map[string]any
		call func(*gin.Context)
	}{
		{
			name: "connection test",
			path: "/api/v1/admin/settings/test-smtp",
			body: map[string]any{"smtp_host": "attacker.example.com", "smtp_port": 587, "smtp_username": "mailer", "smtp_use_tls": true},
			call: handler.TestSMTPConnection,
		},
		{
			name: "test email",
			path: "/api/v1/admin/settings/send-test-email",
			body: map[string]any{"email": "admin@example.com", "smtp_host": "attacker.example.com", "smtp_port": 587, "smtp_username": "mailer", "smtp_use_tls": true},
			call: handler.SendTestEmail,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(tc.body)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(payload))
			ctx.Request.Header.Set("Content-Type", "application/json")

			tc.call(ctx)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "smtp_password must be provided")
		})
	}
}
