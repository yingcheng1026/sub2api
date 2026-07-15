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

func TestValidateConfiguredKiroSidecarURLRejectsSecretsAndQueries(t *testing.T) {
	_, err := validateConfiguredKiroSidecarURL("http://user:pass@127.0.0.1:8787")
	require.Error(t, err)

	_, err = validateConfiguredKiroSidecarURL("http://127.0.0.1:8787?token=secret")
	require.Error(t, err)

	got, err := validateConfiguredKiroSidecarURL("http://127.0.0.1:8787/")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8787", got)
}

func TestJoinKiroSidecarURLPreservesBasePath(t *testing.T) {
	got, err := joinKiroSidecarURL("http://sidecar.local/internal", "/v1/messages")
	require.NoError(t, err)
	require.Equal(t, "http://sidecar.local/internal/v1/messages", got)
}

func TestExtractKiroUsageSupportsCommonSchemas(t *testing.T) {
	anthropicUsage := extractKiroUsage([]byte(`{"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":3}}`))
	require.Equal(t, 10, anthropicUsage.InputTokens)
	require.Equal(t, 20, anthropicUsage.OutputTokens)
	require.Equal(t, 3, anthropicUsage.CacheReadInputTokens)

	openAIUsage := extractKiroUsage([]byte(`{"usage":{"prompt_tokens":11,"completion_tokens":21}}`))
	require.Equal(t, 11, openAIUsage.InputTokens)
	require.Equal(t, 21, openAIUsage.OutputTokens)

	geminiUsage := extractKiroUsage([]byte(`{"usage":{"promptTokenCount":12,"candidatesTokenCount":22}}`))
	require.Equal(t, 12, geminiUsage.InputTokens)
	require.Equal(t, 22, geminiUsage.OutputTokens)
}

func TestCopyKiroSidecarHeadersDropsSensitiveHopByHopHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Set("Authorization", "Bearer secret")
	src.Set("X-Kiro-API-Key", "kiro-secret")
	src.Set("Connection", "keep-alive")
	src.Set("Retry-After", "5")

	dst := http.Header{}
	copyKiroSidecarHeaders(dst, src)

	require.Equal(t, "application/json", dst.Get("Content-Type"))
	require.Equal(t, "5", dst.Get("Retry-After"))
	require.Empty(t, dst.Get("Authorization"))
	require.Empty(t, dst.Get("X-Kiro-API-Key"))
	require.Empty(t, dst.Get("Connection"))
}

func TestForwardKiroSidecarWalletStreamFailsClosedWithoutDeliveringBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: unmetered\n\n"))
	}))
	defer sidecar.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/kiro/v1/messages", nil)
	h := &GatewayHandler{cfg: &config.Config{}}
	h.cfg.Kiro.SidecarURL = sidecar.URL
	account := &service.Account{
		ID:          1,
		Platform:    service.PlatformKiro,
		Credentials: map[string]any{"api_key": "test-key"},
	}

	result, err := h.forwardKiroSidecar(c, account, kiroSidecarRequest{
		Method:                http.MethodPost,
		Path:                  "/v1/messages",
		UpstreamBody:          []byte(`{}`),
		RejectUnmeteredStream: true,
	})

	require.Nil(t, result)
	require.EqualError(t, err, "wallet billing for Kiro streaming is unavailable")
	require.Empty(t, w.Body.String())
}

func TestForwardKiroSidecarNonWalletStreamExtractsTerminalUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	streamBody := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":2}}}\n\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n"
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(streamBody))
	}))
	defer sidecar.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/kiro/v1/messages", nil)
	h := &GatewayHandler{cfg: &config.Config{}}
	h.cfg.Kiro.SidecarURL = sidecar.URL
	account := &service.Account{
		ID:          1,
		Platform:    service.PlatformKiro,
		Credentials: map[string]any{"api_key": "test-key"},
	}

	result, err := h.forwardKiroSidecar(c, account, kiroSidecarRequest{
		Method:       http.MethodPost,
		Path:         "/v1/messages",
		UpstreamBody: []byte(`{}`),
		RecordUsage:  true,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Equal(t, 2, result.Usage.CacheReadInputTokens)
	require.Equal(t, streamBody, w.Body.String())
}

func TestForwardKiroSidecarNonWalletStreamRejectsMissingUsageBeforeDelivery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\"}\n\n"))
	}))
	defer sidecar.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/kiro/v1/messages", nil)
	h := &GatewayHandler{cfg: &config.Config{}}
	h.cfg.Kiro.SidecarURL = sidecar.URL
	account := &service.Account{ID: 1, Platform: service.PlatformKiro, Credentials: map[string]any{"api_key": "test-key"}}

	result, err := h.forwardKiroSidecar(c, account, kiroSidecarRequest{
		Method: http.MethodPost, Path: "/v1/messages", UpstreamBody: []byte(`{}`), RecordUsage: true,
	})
	require.Nil(t, result)
	require.ErrorContains(t, err, "completed without usage")
	require.Empty(t, w.Body.String())
}

func TestForwardKiroSidecarRedactsNonFailoverUpstreamErrorBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const upstreamSecret = "sk-upstream-kiro-secret"
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"error":{"message":"internal endpoint http://10.0.0.7:8787 token ` + upstreamSecret + `"}}`))
	}))
	defer sidecar.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/kiro/v1/messages", nil)
	h := &GatewayHandler{cfg: &config.Config{}}
	h.cfg.Kiro.SidecarURL = sidecar.URL
	account := &service.Account{ID: 1, Platform: service.PlatformKiro, Credentials: map[string]any{"api_key": "test-key"}}

	result, err := h.forwardKiroSidecar(c, account, kiroSidecarRequest{
		Method: http.MethodPost, Path: "/v1/messages", UpstreamBody: []byte(`{}`),
	})

	require.Nil(t, result)
	require.EqualError(t, err, "kiro sidecar upstream error: 418")
	require.NotContains(t, w.Body.String(), upstreamSecret)
	require.NotContains(t, w.Body.String(), "10.0.0.7")
	require.Contains(t, w.Body.String(), "Upstream request failed")
}
