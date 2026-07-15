package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSProtocolResolver_XAIProvidersAreHTTPOnly(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true

	tests := []struct {
		name        string
		credentials map[string]any
		accountType string
	}{
		{
			name:        "xai oauth",
			accountType: AccountTypeOAuth,
			credentials: map[string]any{"oauth_provider": "xai"},
		},
		{
			name:        "xai api key",
			accountType: AccountTypeAPIKey,
			credentials: map[string]any{"openai_compatible_provider": "xai"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				Platform:    PlatformOpenAI,
				Type:        tt.accountType,
				Credentials: tt.credentials,
				Extra: map[string]any{
					"responses_websockets_v2_enabled": true,
				},
			}

			decision := NewOpenAIWSProtocolResolver(cfg).Resolve(account)
			require.Equal(t, OpenAIUpstreamTransportHTTPSSE, decision.Transport)
			require.Equal(t, "xai_http_only", decision.Reason)
		})
	}
}

func TestBuildOpenAIWSHeaders_XAIProviderStripsCodexMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "customer-client/1.0")
	c.Request.Header.Set("originator", "codex_cli_rs")
	c.Request.Header.Set("session_id", "customer-session")
	c.Request.Header.Set("conversation_id", "customer-conversation")

	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: true}},
	}
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"oauth_provider":     "xai",
			"chatgpt_account_id": "must-not-leak",
		},
	}

	headers, _ := svc.buildOpenAIWSHeaders(
		c,
		account,
		"test-token",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		true,
		"turn-state",
		"turn-metadata",
		"prompt-cache-key",
	)

	require.Equal(t, "customer-client/1.0", headers.Get("User-Agent"))
	require.Empty(t, headers.Get("originator"))
	require.Empty(t, headers.Get("session_id"))
	require.Empty(t, headers.Get("conversation_id"))
	require.Empty(t, headers.Get("chatgpt-account-id"))
	require.Empty(t, headers.Get(openAIWSTurnStateHeader))
	require.Empty(t, headers.Get(openAIWSTurnMetadataHeader))
}
