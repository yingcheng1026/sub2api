package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWriteAnthropicContractErrorIncludesMatchingRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.RequestID, "service-error-id"))
	c.Request = req
	c.Header("X-Request-ID", "service-error-id")

	writeAnthropicContractError(c, http.StatusBadGateway, "api_error", "Upstream request failed")

	require.Equal(t, http.StatusBadGateway, recorder.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "error", payload["type"])
	require.Equal(t, "req_serviceerrorid", payload["request_id"])
	require.Equal(t, payload["request_id"], recorder.Header().Get("request-id"))
	errObject := payload["error"].(map[string]any)
	require.Equal(t, "api_error", errObject["type"])
	require.Equal(t, "Upstream request failed", errObject["message"])
}

func TestAnthropicStreamErrorUsesPreparedHeaderRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.RequestID, "stream-error-id"))
	c.Request = req

	prepareAnthropicContractStream(c)
	sse := buildAnthropicStreamErrorSSE(c, "api_error", "stream failed")

	require.Equal(t, "req_streamerrorid", recorder.Header().Get("request-id"))
	require.Contains(t, sse, `"request_id":"req_streamerrorid"`)
	require.Contains(t, sse, `"type":"api_error"`)
}
