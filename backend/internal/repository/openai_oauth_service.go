package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/imroc/req/v3"
)

// NewOpenAIOAuthClient creates a new OpenAI OAuth client
func NewOpenAIOAuthClient() service.OpenAIOAuthClient {
	return &openaiOAuthService{
		tokenURL:      openai.TokenURL,
		providerAware: true,
	}
}

type openaiOAuthService struct {
	tokenURL      string
	providerAware bool
}

func (s *openaiOAuthService) ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string) (*openai.TokenResponse, error) {
	client, err := createOpenAIReqClient(proxyURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_CLIENT_INIT_FAILED", "create HTTP client: %v", err)
	}

	if redirectURI == "" {
		redirectURI = openai.DefaultRedirectURI
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID = openai.ClientID
	}
	providerCfg := openai.OAuthProviderConfigByClientID(clientID)

	formData := url.Values{}
	formData.Set("grant_type", "authorization_code")
	formData.Set("client_id", clientID)
	formData.Set("code", code)
	formData.Set("redirect_uri", redirectURI)
	formData.Set("code_verifier", codeVerifier)

	var tokenResp openai.TokenResponse

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("User-Agent", "codex-cli/0.91.0").
		SetFormDataFromValues(formData).
		SetSuccessResult(&tokenResp).
		Post(s.resolveTokenURL(providerCfg.TokenURL))

	if err != nil {
		if shouldReturnOpenAINoProxyHint(ctx, proxyURL, err) {
			return nil, newOpenAINoProxyHintError(err)
		}
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_REQUEST_FAILED", "request failed: %v", err)
	}

	if !resp.IsSuccessState() {
		if code := safeOAuthErrorCode(resp.String()); code != "" {
			return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_TOKEN_EXCHANGE_FAILED", "token exchange failed: status %d, error: %s", resp.StatusCode, code)
		}
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_TOKEN_EXCHANGE_FAILED", "token exchange failed: status %d", resp.StatusCode)
	}

	return &tokenResp, nil
}

func (s *openaiOAuthService) RefreshToken(ctx context.Context, refreshToken, proxyURL string) (*openai.TokenResponse, error) {
	return s.RefreshTokenWithClientID(ctx, refreshToken, proxyURL, "")
}

func (s *openaiOAuthService) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL string, clientID string) (*openai.TokenResponse, error) {
	// 调用方应始终传入正确的 client_id；为兼容旧数据，未指定时默认使用 OpenAI ClientID
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID = openai.ClientID
	}
	return s.refreshTokenWithClientID(ctx, refreshToken, proxyURL, clientID)
}

func (s *openaiOAuthService) refreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL, clientID string) (*openai.TokenResponse, error) {
	client, err := createOpenAIReqClient(proxyURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_CLIENT_INIT_FAILED", "create HTTP client: %v", err)
	}

	formData := url.Values{}
	formData.Set("grant_type", "refresh_token")
	formData.Set("refresh_token", refreshToken)
	formData.Set("client_id", clientID)
	providerCfg := openai.OAuthProviderConfigByClientID(clientID)
	if providerCfg.RefreshScopes != "" {
		formData.Set("scope", providerCfg.RefreshScopes)
	}

	var tokenResp openai.TokenResponse

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("User-Agent", "codex-cli/0.91.0").
		SetFormDataFromValues(formData).
		SetSuccessResult(&tokenResp).
		Post(s.resolveTokenURL(providerCfg.TokenURL))

	if err != nil {
		if shouldReturnOpenAINoProxyHint(ctx, proxyURL, err) {
			return nil, newOpenAINoProxyHintError(err)
		}
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_REQUEST_FAILED", "request failed: %v", err)
	}

	if !resp.IsSuccessState() {
		if code := safeOAuthErrorCode(resp.String()); code != "" {
			return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_TOKEN_REFRESH_FAILED", "token refresh failed: status %d, error: %s", resp.StatusCode, code)
		}
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_TOKEN_REFRESH_FAILED", "token refresh failed: status %d", resp.StatusCode)
	}

	return &tokenResp, nil
}

func safeOAuthErrorCode(rawBody string) string {
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(rawBody), &payload); err != nil {
		return ""
	}
	code := strings.TrimSpace(payload.Error)
	if code == "" || len(code) > 64 {
		return ""
	}
	for _, r := range code {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return ""
	}
	return code
}

func (s *openaiOAuthService) resolveTokenURL(providerDefault string) string {
	if s.providerAware {
		return providerDefault
	}
	if override := strings.TrimSpace(s.tokenURL); override != "" {
		return override
	}
	return providerDefault
}

func createOpenAIReqClient(proxyURL string) (*req.Client, error) {
	return getSharedReqClient(reqClientOptions{
		ProxyURL: proxyURL,
		Timeout:  120 * time.Second,
	})
}

func shouldReturnOpenAINoProxyHint(ctx context.Context, proxyURL string, err error) bool {
	if strings.TrimSpace(proxyURL) != "" || err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	return !errors.Is(err, context.Canceled)
}

func newOpenAINoProxyHintError(cause error) error {
	return infraerrors.New(
		http.StatusBadGateway,
		"OPENAI_OAUTH_PROXY_REQUIRED",
		"OpenAI OAuth request failed: no proxy is configured and this server could not reach OpenAI directly. Select a proxy that can access OpenAI, then retry; if the authorization code has expired, regenerate the authorization URL.",
	).WithCause(cause)
}
