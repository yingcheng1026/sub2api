//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayErrorProtocolForRequest(t *testing.T) {
	tests := []struct {
		path string
		want GatewayErrorProtocol
	}{
		{path: "/v1/messages", want: GatewayErrorProtocolAnthropic},
		{path: "/v1/messages/count_tokens", want: GatewayErrorProtocolAnthropic},
		{path: "/antigravity/v1/messages", want: GatewayErrorProtocolAnthropic},
		{path: "/v1/chat/completions", want: GatewayErrorProtocolOpenAI},
		{path: "/v1/responses", want: GatewayErrorProtocolOpenAI},
		{path: "/responses", want: GatewayErrorProtocolOpenAI},
		{path: "/backend-api/codex/responses", want: GatewayErrorProtocolOpenAI},
		{path: "/v1/models", want: GatewayErrorProtocolOpenAI},
		{path: "/v1/images/generations", want: GatewayErrorProtocolOpenAI},
		{path: "/v1beta/models", want: GatewayErrorProtocolGoogle},
		{path: "/antigravity/v1beta/models/gemini:generateContent", want: GatewayErrorProtocolGoogle},
		{path: "/v1/sub2api/billing", want: GatewayErrorProtocolManagement},
		{path: "/api/v1/admin/users", want: GatewayErrorProtocolManagement},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			require.Equal(t, tt.want, GatewayErrorProtocolForRequest(req))
		})
	}
}

func TestGatewayErrorProtocolForSharedModelsUsesClientSignal(t *testing.T) {
	openAIRequest := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	openAIRequest.Header.Set("Authorization", "Bearer placeholder")
	require.Equal(t, GatewayErrorProtocolOpenAI, GatewayErrorProtocolForRequest(openAIRequest))

	anthropicRequest := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	anthropicRequest.Header.Set("anthropic-version", "2023-06-01")
	require.Equal(t, GatewayErrorProtocolAnthropic, GatewayErrorProtocolForRequest(anthropicRequest))

	anthropicKeyRequest := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	anthropicKeyRequest.Header.Set("x-api-key", "placeholder")
	require.Equal(t, GatewayErrorProtocolAnthropic, GatewayErrorProtocolForRequest(anthropicKeyRequest))
}

func TestAPIKeyAuthMiddlewareUsesOfficialProtocolEnvelopes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
		return nil, service.ErrAPIKeyNotFound
	}}
	auth := gin.HandlerFunc(NewAPIKeyAuthMiddleware(service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg), nil, cfg))

	tests := []struct {
		name       string
		path       string
		headers    map[string]string
		wantStatus int
		assertBody func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name: "openai missing key", path: "/v1/responses", wantStatus: http.StatusUnauthorized,
			assertBody: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var payload map[string]any
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
				errorObject := payload["error"].(map[string]any)
				require.Equal(t, "invalid_request_error", errorObject["type"])
				require.Nil(t, errorObject["param"])
				require.Nil(t, errorObject["code"])
			},
		},
		{
			name: "anthropic missing key", path: "/v1/messages", wantStatus: http.StatusUnauthorized,
			assertBody: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var payload map[string]any
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
				require.Equal(t, "error", payload["type"])
				require.Equal(t, payload["request_id"], rec.Header().Get("request-id"))
				errorObject := payload["error"].(map[string]any)
				require.Equal(t, "authentication_error", errorObject["type"])
				require.NotContains(t, errorObject, "code")
			},
		},
		{
			name: "shared models anthropic signal", path: "/v1/models", headers: map[string]string{"anthropic-version": "2023-06-01"}, wantStatus: http.StatusUnauthorized,
			assertBody: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var payload map[string]any
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
				require.Equal(t, "error", payload["type"])
			},
		},
		{
			name: "management stays sub2api", path: "/v1/sub2api/billing", wantStatus: http.StatusUnauthorized,
			assertBody: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var payload ErrorResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
				require.Equal(t, "API_KEY_REQUIRED", payload.Code)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(RequestLogger(), auth)
			router.Any("/*path", func(c *gin.Context) { c.Status(http.StatusOK) })
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			for name, value := range tt.headers {
				req.Header.Set(name, value)
			}
			router.ServeHTTP(recorder, req)
			require.Equal(t, tt.wantStatus, recorder.Code)
			tt.assertBody(t, recorder)
		})
	}
}

func TestWriteGatewayErrorMatchesOfficialAuthenticationEnvelopes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("openai missing key", func(t *testing.T) {
		c, rec := errorContractContext(t, "/v1/responses", "rid-openai")
		WriteGatewayError(c, http.StatusUnauthorized, "API_KEY_REQUIRED", "Missing bearer authentication in header")

		require.Equal(t, http.StatusUnauthorized, rec.Code)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		errObject := payload["error"].(map[string]any)
		require.Equal(t, "invalid_request_error", errObject["type"])
		require.Contains(t, errObject, "param")
		require.Nil(t, errObject["param"])
		require.Contains(t, errObject, "code")
		require.Nil(t, errObject["code"])
		require.NotEmpty(t, rec.Header().Get("X-Request-ID"))
	})

	t.Run("openai invalid key", func(t *testing.T) {
		c, rec := errorContractContext(t, "/v1/chat/completions", "rid-openai-invalid")
		WriteGatewayError(c, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")

		var payload map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		errObject := payload["error"].(map[string]any)
		require.Equal(t, "invalid_request_error", errObject["type"])
		require.Equal(t, "invalid_api_key", errObject["code"])
		require.Nil(t, errObject["param"])
	})

	t.Run("anthropic missing key", func(t *testing.T) {
		c, rec := errorContractContext(t, "/v1/messages", "rid-anthropic")
		WriteGatewayError(c, http.StatusUnauthorized, "API_KEY_REQUIRED", "x-api-key header is required")

		var payload map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		require.Equal(t, "error", payload["type"])
		require.Equal(t, "req_ridanthropic", payload["request_id"])
		require.Equal(t, payload["request_id"], rec.Header().Get("request-id"))
		errObject := payload["error"].(map[string]any)
		require.Equal(t, "authentication_error", errObject["type"])
		require.NotContains(t, errObject, "code")
	})

	t.Run("gemini missing key", func(t *testing.T) {
		c, rec := errorContractContext(t, "/v1beta/models", "rid-gemini")
		WriteGatewayError(c, http.StatusUnauthorized, "API_KEY_REQUIRED", "API key is required")

		require.Equal(t, http.StatusForbidden, rec.Code)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		errObject := payload["error"].(map[string]any)
		require.Equal(t, float64(http.StatusForbidden), errObject["code"])
		require.Equal(t, "PERMISSION_DENIED", errObject["status"])
	})

	t.Run("gemini invalid key", func(t *testing.T) {
		c, rec := errorContractContext(t, "/v1beta/models", "rid-gemini-invalid")
		WriteGatewayError(c, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")

		require.Equal(t, http.StatusBadRequest, rec.Code)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		errObject := payload["error"].(map[string]any)
		require.Equal(t, float64(http.StatusBadRequest), errObject["code"])
		require.Equal(t, "INVALID_ARGUMENT", errObject["status"])
		details := errObject["details"].([]any)
		require.NotEmpty(t, details)
		require.Equal(t, "API_KEY_INVALID", details[0].(map[string]any)["reason"])
	})
}

func TestAnthropicErrorTypesFollowOfficialHTTPMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusBadRequest, "invalid_request_error"},
		{http.StatusUnauthorized, "authentication_error"},
		{http.StatusForbidden, "permission_error"},
		{http.StatusNotFound, "not_found_error"},
		{http.StatusRequestEntityTooLarge, "request_too_large"},
		{http.StatusTooManyRequests, "rate_limit_error"},
		{http.StatusServiceUnavailable, "api_error"},
		{529, "overloaded_error"},
	} {
		require.Equal(t, tc.want, anthropicErrorType(tc.status, ""), "status=%d", tc.status)
	}
}

func TestAnthropicStreamingErrorPayloadFollowsOfficialHTTPMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		input  string
		want   string
	}{
		{http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large"},
		{529, "api_error", "overloaded_error"},
		{http.StatusServiceUnavailable, "api_error", "api_error"},
	} {
		c, _ := errorContractContext(t, "/v1/messages", "stream-request-id")
		payload := AnthropicErrorPayloadForStatus(c, tc.status, tc.input, "message")
		errorObject := payload["error"].(gin.H)
		require.Equal(t, tc.want, errorObject["type"], "status=%d", tc.status)
		require.Equal(t, "req_streamrequestid", payload["request_id"])
	}
}

func TestAnthropicErrorWriterIncludesOfficialRequestID(t *testing.T) {
	c, rec := errorContractContext(t, "/v1/messages", "fixed-request-id")
	AnthropicErrorWriter(c, http.StatusForbidden, "Forbidden")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "req_fixedrequestid", payload["request_id"])
	require.Equal(t, payload["request_id"], rec.Header().Get("request-id"))
	errObject := payload["error"].(map[string]any)
	require.Equal(t, "permission_error", errObject["type"])
}

func TestProtocolErrorWriterUsesXAIContractForKnownGrokGroup(t *testing.T) {
	c, rec := errorContractContext(t, "/v1/chat/completions", "grok-policy")
	groupID := int64(7)
	c.Set(string(ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformGrok},
	})

	ProtocolErrorWriter(c, http.StatusForbidden, "group access denied")

	require.Equal(t, http.StatusForbidden, rec.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "permission-denied", payload["code"])
	require.Equal(t, "group access denied", payload["error"])
	require.NotContains(t, payload, "message")
}

func TestGatewayErrorProtocolDoesNotGuessGrokBeforeAuthentication(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	require.Equal(t, GatewayErrorProtocolOpenAI, GatewayErrorProtocolForRequest(req))
}

func errorContractContext(t *testing.T, path, requestID string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.RequestID, requestID))
	c.Request = req
	c.Header("X-Request-ID", requestID)
	return c, rec
}
