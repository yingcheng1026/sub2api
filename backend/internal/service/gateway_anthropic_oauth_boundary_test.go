package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const anthropicOAuthGatewayDisabledMessage = "disabled for shared gateway inference"

func TestGatewayService_AnthropicOAuthSharedGatewayBoundariesRejectBeforeUpstream(t *testing.T) {
	tests := []struct {
		name string
		call func(*GatewayService, *gin.Context, *Account) error
	}{
		{
			name: "messages",
			call: func(svc *GatewayService, c *gin.Context, account *Account) error {
				_, err := svc.Forward(context.Background(), c, account, &ParsedRequest{
					Body:   []byte(`{"model":"claude-sonnet-4","stream":false,"messages":[{"role":"user","content":"hi"}]}`),
					Model:  "claude-sonnet-4",
					Stream: false,
				})
				return err
			},
		},
		{
			name: "count_tokens",
			call: func(svc *GatewayService, c *gin.Context, account *Account) error {
				return svc.ForwardCountTokens(context.Background(), c, account, &ParsedRequest{
					Body:  []byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}]}`),
					Model: "claude-sonnet-4",
				})
			},
		},
		{
			name: "chat_completions",
			call: func(svc *GatewayService, c *gin.Context, account *Account) error {
				_, err := svc.ForwardAsChatCompletions(
					context.Background(),
					c,
					account,
					[]byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}]}`),
					&ParsedRequest{},
				)
				return err
			},
		},
		{
			name: "responses",
			call: func(svc *GatewayService, c *gin.Context, account *Account) error {
				_, err := svc.ForwardAsResponses(
					context.Background(),
					c,
					account,
					[]byte(`{"model":"claude-sonnet-4","input":"hi"}`),
					&ParsedRequest{},
				)
				return err
			},
		},
	}

	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		for _, tt := range tests {
			t.Run(accountType+"/"+tt.name, func(t *testing.T) {
				upstream := &anthropicHTTPUpstreamRecorder{err: errors.New("unexpected upstream call")}
				svc := newAnthropicOAuthBoundaryTestService(upstream)
				c, recorder := newAnthropicOAuthBoundaryTestContext(tt.name)
				account := newAnthropicOAuthBoundaryTestAccount(accountType)

				err := tt.call(svc, c, account)

				require.ErrorContains(t, err, anthropicOAuthGatewayDisabledMessage)
				require.Nil(t, upstream.lastReq, "policy rejection must happen before any upstream request")
				if tt.name == "count_tokens" {
					require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
				}
			})
		}
	}
}

func TestGatewayService_AnthropicOAuthRequestBuildersReject(t *testing.T) {
	account := newAnthropicOAuthBoundaryTestAccount(AccountTypeOAuth)
	svc := newAnthropicOAuthBoundaryTestService(nil)
	c, _ := newAnthropicOAuthBoundaryTestContext("builders")
	body := []byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}]}`)

	_, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body, "oauth-token", "oauth", "claude-sonnet-4", false,
	)
	require.ErrorContains(t, err, anthropicOAuthGatewayDisabledMessage)

	_, err = svc.buildCountTokensRequest(
		context.Background(), c, account, body, "oauth-token", "oauth", "claude-sonnet-4",
	)
	require.ErrorContains(t, err, anthropicOAuthGatewayDisabledMessage)
}

func TestGatewayService_AnthropicAPIKeyRemainsEligibleForGatewayInference(t *testing.T) {
	svc := newAnthropicOAuthBoundaryTestService(nil)
	c, _ := newAnthropicOAuthBoundaryTestContext("apikey")
	c.Request.Header.Set("anthropic-beta", "oauth-2025-04-20,claude-code-20250219,context-1m-2025-08-07")
	account := &Account{
		ID:          902,
		Name:        "anthropic-api-key",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "test-api-key"},
		Status:      StatusActive,
		Schedulable: true,
	}
	body := []byte(`{"model":"claude-sonnet-4","system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.92; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"Project prompt"}],"messages":[{"role":"user","content":"hi"}]}`)

	require.True(t, svc.isAccountSchedulableForSelection(account))
	req, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body, "test-api-key", "apikey", "claude-sonnet-4", false,
	)
	require.NoError(t, err)
	require.Equal(t, "test-api-key", getHeaderRaw(req.Header, "x-api-key"))
	require.Equal(t, "sub2api-gateway/1", getHeaderRaw(req.Header, "user-agent"))
	require.Empty(t, getHeaderRaw(req.Header, "x-app"))
	require.NotContains(t, getHeaderRaw(req.Header, "anthropic-beta"), "oauth-2025-04-20")
	require.NotContains(t, getHeaderRaw(req.Header, "anthropic-beta"), "claude-code-20250219")
	require.Contains(t, getHeaderRaw(req.Header, "anthropic-beta"), "context-1m-2025-08-07")
	upstreamBody, readErr := io.ReadAll(req.Body)
	require.NoError(t, readErr)
	require.NotContains(t, string(upstreamBody), "x-anthropic-billing-header")
	require.Contains(t, string(upstreamBody), "Project prompt")
}

func TestGatewayService_AnthropicOAuthIsNotSchedulableForGatewayInference(t *testing.T) {
	svc := newAnthropicOAuthBoundaryTestService(nil)
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		account := newAnthropicOAuthBoundaryTestAccount(accountType)
		require.False(t, svc.isAccountSchedulableForSelection(account))
		require.False(t, svc.isAccountSchedulableForModelSelection(context.Background(), account, "claude-sonnet-4"))
	}
}

func TestStripUntrustedAnthropicBillingAttribution(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "array_preserves_ordinary_blocks",
			body: `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"Project prompt"}],"messages":[]}`,
			want: `{"system":[{"type":"text","text":"Project prompt"}],"messages":[]}`,
		},
		{
			name: "string_removes_only_attribution",
			body: `{"system":" X-Anthropic-Billing-Header: forged","messages":[]}`,
			want: `{"messages":[]}`,
		},
		{
			name: "ordinary_system_unchanged",
			body: `{"system":"Project prompt","messages":[]}`,
			want: `{"system":"Project prompt","messages":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.JSONEq(t, tt.want, string(stripUntrustedAnthropicBillingAttribution([]byte(tt.body))))
		})
	}
}

func TestAccountTestService_AnthropicOAuthProbeRejectsBeforeUpstream(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run(accountType, func(t *testing.T) {
			upstream := &anthropicHTTPUpstreamRecorder{err: errors.New("unexpected upstream call")}
			svc := &AccountTestService{
				httpUpstream:        upstream,
				tlsFPProfileService: &TLSFingerprintProfileService{},
			}
			c, _ := newAnthropicOAuthBoundaryTestContext("account-test")

			err := svc.testClaudeAccountConnection(c, newAnthropicOAuthBoundaryTestAccount(accountType), "claude-sonnet-4")

			require.ErrorContains(t, err, anthropicOAuthGatewayDisabledMessage)
			require.Nil(t, upstream.lastReq, "account probe must not synthesize an official-client request")
		})
	}
}

func newAnthropicOAuthBoundaryTestService(upstream HTTPUpstream) *GatewayService {
	return &GatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
		},
		httpUpstream:        upstream,
		rateLimitService:    &RateLimitService{},
		deferredService:     &DeferredService{},
		tlsFPProfileService: &TLSFingerprintProfileService{},
	}
}

func newAnthropicOAuthBoundaryTestContext(path string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+path, nil)
	c.Request.Header.Set("User-Agent", "claude-cli/99.0.0 (forged, cli)")
	c.Request.Header.Set("X-App", "cli")
	return c, recorder
}

func newAnthropicOAuthBoundaryTestAccount(accountType string) *Account {
	return &Account{
		ID:          901,
		Name:        "anthropic-oauth-boundary",
		Platform:    PlatformAnthropic,
		Type:        accountType,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token"},
		Status:      StatusActive,
		Schedulable: true,
	}
}
