package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
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

func TestForwardCursorSidecarWalletStreamFailsClosedWithoutDeliveringBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: unmetered\n\n"))
	}))
	defer sidecar.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/cursor/v1/messages", nil)
	h := &GatewayHandler{cfg: &config.Config{}}
	h.cfg.Cursor.SidecarURL = sidecar.URL
	account := &service.Account{ID: 1, Platform: service.PlatformCursor}

	result, err := h.forwardCursorSidecar(c, account, cursorSidecarRequest{
		Method:                http.MethodPost,
		Path:                  "/v1/messages",
		UpstreamBody:          []byte(`{}`),
		RejectUnmeteredStream: true,
	})

	require.Nil(t, result)
	require.EqualError(t, err, "wallet billing for Cursor streaming is unavailable")
	require.Empty(t, w.Body.String())
}

func TestForwardCursorSidecarNonWalletStreamExtractsTerminalUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	streamBody := "{\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n" +
		"{\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":6}}}\n"
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(streamBody))
	}))
	defer sidecar.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/cursor/v1/messages", nil)
	h := &GatewayHandler{cfg: &config.Config{}}
	h.cfg.Cursor.SidecarURL = sidecar.URL
	account := &service.Account{ID: 1, Platform: service.PlatformCursor}

	result, err := h.forwardCursorSidecar(c, account, cursorSidecarRequest{
		Method:       http.MethodPost,
		Path:         "/v1/messages",
		UpstreamBody: []byte(`{}`),
		RecordUsage:  true,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 6, result.Usage.OutputTokens)
	require.Equal(t, streamBody, w.Body.String())
}

func TestForwardCursorSidecarNonWalletStreamRejectsMissingUsageBeforeDelivery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte("{\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n"))
	}))
	defer sidecar.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/cursor/v1/messages", nil)
	h := &GatewayHandler{cfg: &config.Config{}}
	h.cfg.Cursor.SidecarURL = sidecar.URL
	account := &service.Account{ID: 1, Platform: service.PlatformCursor}

	result, err := h.forwardCursorSidecar(c, account, cursorSidecarRequest{
		Method: http.MethodPost, Path: "/v1/messages", UpstreamBody: []byte(`{}`), RecordUsage: true,
	})
	require.Nil(t, result)
	require.ErrorContains(t, err, "completed without usage")
	require.Empty(t, w.Body.String())
}

func TestForwardCursorSidecarRedactsNonFailoverUpstreamErrorBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const upstreamSecret = "cursor-upstream-private-token"
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"error":{"message":"internal endpoint http://10.0.0.8:8788 token ` + upstreamSecret + `"}}`))
	}))
	defer sidecar.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/cursor/v1/messages", nil)
	h := &GatewayHandler{cfg: &config.Config{}}
	h.cfg.Cursor.SidecarURL = sidecar.URL
	account := &service.Account{ID: 1, Platform: service.PlatformCursor}

	result, err := h.forwardCursorSidecar(c, account, cursorSidecarRequest{
		Method: http.MethodPost, Path: "/v1/messages", UpstreamBody: []byte(`{}`),
	})

	require.Nil(t, result)
	require.EqualError(t, err, "cursor sidecar upstream error: 418")
	require.NotContains(t, w.Body.String(), upstreamSecret)
	require.NotContains(t, w.Body.String(), "10.0.0.8")
	require.Contains(t, w.Body.String(), "Upstream request failed")
}
