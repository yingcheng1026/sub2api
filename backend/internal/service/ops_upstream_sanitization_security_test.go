package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeUpstreamErrorMessageRedactsCredentialShapes(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		secret string
	}{
		{name: "query", input: "https://upstream.test/fail?access_token=query-secret", secret: "query-secret"},
		{name: "authorization", input: "Authorization: Bearer bearer-private-value", secret: "bearer-private-value"},
		{name: "password", input: "password=hunter2-private", secret: "hunter2-private"},
		{name: "free text token", input: "internal endpoint rejected token cursor-private-token", secret: "cursor-private-token"},
		{name: "openai shape", input: "upstream rejected sk-proj-privatevalue123", secret: "sk-proj-privatevalue123"},
		{name: "github shape", input: "provider returned ghp_abcdefghijklmnopqrstuvwxyz1234567890", secret: "ghp_abcdefghijklmnopqrstuvwxyz1234567890"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeUpstreamErrorMessage(tt.input)
			require.NotContains(t, got, tt.secret)
			require.Contains(t, got, "***")
		})
	}

	require.Contains(t, sanitizeUpstreamErrorMessage("max_tokens 128 exceeded"), "128")
}

func TestSanitizeErrorBodyForStorageRedactsNonJSONCredentials(t *testing.T) {
	got, _ := sanitizeErrorBodyForStorage("upstream password: body-private-value", 2048)
	require.NotContains(t, got, "body-private-value")
	require.Contains(t, got, "***")
}
