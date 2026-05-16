package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestValidateConfiguredCursorSidecarURLRejectsSecretsAndQueries(t *testing.T) {
	_, err := validateConfiguredCursorSidecarURL("http://user:pass@127.0.0.1:8788")
	require.Error(t, err)

	_, err = validateConfiguredCursorSidecarURL("http://127.0.0.1:8788?token=secret")
	require.Error(t, err)

	got, err := validateConfiguredCursorSidecarURL("http://127.0.0.1:8788/")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8788", got)
}

func TestJoinCursorSidecarURLPreservesBasePath(t *testing.T) {
	got, err := joinCursorSidecarURL("http://sidecar.local/internal", "/v1/messages")
	require.NoError(t, err)
	require.Equal(t, "http://sidecar.local/internal/v1/messages", got)
}

func TestExtractCursorUsageSupportsCommonSchemas(t *testing.T) {
	anthropicUsage := extractCursorUsage([]byte(`{"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":3}}`))
	require.Equal(t, 10, anthropicUsage.InputTokens)
	require.Equal(t, 20, anthropicUsage.OutputTokens)
	require.Equal(t, 3, anthropicUsage.CacheReadInputTokens)

	openAIUsage := extractCursorUsage([]byte(`{"usage":{"prompt_tokens":11,"completion_tokens":21}}`))
	require.Equal(t, 11, openAIUsage.InputTokens)
	require.Equal(t, 21, openAIUsage.OutputTokens)

	geminiUsage := extractCursorUsage([]byte(`{"usage":{"promptTokenCount":12,"candidatesTokenCount":22}}`))
	require.Equal(t, 12, geminiUsage.InputTokens)
	require.Equal(t, 22, geminiUsage.OutputTokens)
}

func TestCopyCursorSidecarHeadersDropsSensitiveHopByHopHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Set("Authorization", "Bearer secret")
	src.Set("X-Cursor-Sidecar-Key", "cursor-secret")
	src.Set("Connection", "keep-alive")
	src.Set("Retry-After", "5")

	dst := http.Header{}
	copyCursorSidecarHeaders(dst, src)

	require.Equal(t, "application/json", dst.Get("Content-Type"))
	require.Equal(t, "5", dst.Get("Retry-After"))
	require.Empty(t, dst.Get("Authorization"))
	require.Empty(t, dst.Get("X-Cursor-Sidecar-Key"))
	require.Empty(t, dst.Get("Connection"))
}

func TestApplyCursorSidecarHeadersAddsInternalAndBearerAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/cursor/v1/responses", nil)

	h := &GatewayHandler{cfg: &config.Config{}}
	h.cfg.Cursor.SidecarAPIKey = "sidecar-test-key"

	req := httptest.NewRequest(http.MethodPost, "http://sidecar.local/v1/responses", nil)
	h.applyCursorSidecarHeaders(c, req, nil)

	require.Equal(t, "sidecar-test-key", req.Header.Get("X-Cursor-Sidecar-Key"))
	require.Equal(t, "Bearer sidecar-test-key", req.Header.Get("Authorization"))
	require.Equal(t, "sidecar-test-key", req.Header.Get("x-api-key"))
}
