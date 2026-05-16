package handler

import (
	"net/http"
	"testing"

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
