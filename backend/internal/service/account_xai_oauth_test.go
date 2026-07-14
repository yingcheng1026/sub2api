package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type xaiAccountTestUpstream struct {
	request  *http.Request
	response *http.Response
}

func (u *xaiAccountTestUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected Do call")
}

func (u *xaiAccountTestUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.request = req
	return u.response, nil
}

func TestOpenAIXAIOAuthUsesPlatformAPI(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"oauth_provider": "xai",
			"access_token":   "secret",
		},
	}

	require.True(t, account.IsOpenAIXAIOAuth())
	require.False(t, account.IsOpenAICodexOAuth())
	require.True(t, account.IsOpenAIPlatformAPIAccount())
	require.Equal(t, "https://api.x.ai", account.GetOpenAIBaseURL())
	require.True(t, account.IsPrivacySet())
}

func TestOpenAIOAuthWithoutProviderRemainsCodexCompatible(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.True(t, account.IsOpenAICodexOAuth())
	require.False(t, account.IsOpenAIXAIOAuth())
	require.False(t, account.IsOpenAIPlatformAPIAccount())
	require.Equal(t, "https://api.openai.com", account.GetOpenAIBaseURL())
}

func TestOpenAICodexOAuthDoesNotMatchOtherExplicitPlatforms(t *testing.T) {
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	require.False(t, account.IsOpenAICodexOAuth())
}

func TestOpenAIXAIProviderDetectionIncludesAPIKeyAccounts(t *testing.T) {
	oauthAccount := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"oauth_provider": "xai",
		},
	}
	apiKeyAccount := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"openai_compatible_provider": "xai",
		},
	}
	plainOpenAI := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	require.True(t, oauthAccount.IsOpenAIXAIProvider())
	require.True(t, apiKeyAccount.IsOpenAIXAIProvider())
	require.False(t, plainOpenAI.IsOpenAIXAIProvider())
}

func TestOpenAIXAIProviderDetectionCoversLegacyAPIKeyAccounts(t *testing.T) {
	officialBaseURL := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.x.ai/v1",
		},
	}
	legacyGrokMapping := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://legacy-openai-compatible.example",
			"model_mapping": map[string]any{
				"grok-4.5":                  "grok-4.5",
				"customer-grok-build-alias": "grok-build-0.1",
			},
		},
	}
	mixedMapping := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"grok-4.5": "grok-4.5",
				"gpt-5.4":  "gpt-5.4",
			},
		},
	}

	require.True(t, officialBaseURL.IsOpenAIXAIProvider())
	require.True(t, legacyGrokMapping.IsOpenAIXAIProvider())
	require.False(t, mixedMapping.IsOpenAIXAIProvider())
}

func TestOpenAIXAIOAuthWithoutMappingIsRestrictedToOfficialTextModels(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"oauth_provider": "xai",
		},
	}

	for _, model := range openai.XAIOAuthTextModelIDs() {
		require.True(t, account.IsModelSupported(model), model)
	}
	require.False(t, account.IsModelSupported("gpt-5.4"))
	require.False(t, account.IsModelSupported("grok-composer-2.5-fast"))
	require.False(t, account.IsModelSupported("grok-imagine-image"))
}

func TestAccountTestService_XAIOAuthDefaultsToGrokAndOmitsCodexInstructions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/91/test", nil)

	upstream := &xaiAccountTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n")),
	}}
	svc := &AccountTestService{
		cfg: &config.Config{Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          91,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"oauth_provider": "xai",
			"access_token":   "test-token",
		},
	}

	err := svc.testOpenAIAccountConnection(c, account, "", "", "")
	require.NoError(t, err)
	require.NotNil(t, upstream.request)
	require.Equal(t, "https://api.x.ai/v1/responses", upstream.request.URL.String())
	body, readErr := io.ReadAll(upstream.request.Body)
	require.NoError(t, readErr)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Equal(t, openai.XAIDefaultTestModel, payload["model"])
	require.NotContains(t, payload, "instructions")
	require.NotContains(t, payload, "store")
	require.Contains(t, recorder.Body.String(), "test_complete")
}
