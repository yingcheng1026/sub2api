package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGatewayModelListPlatformForClientUsesAnthropicShapeForClaudeCode(t *testing.T) {
	require.Equal(t,
		service.PlatformAnthropic,
		gatewayModelListPlatformForClient(service.PlatformOpenAI, "claude-cli/2.1.126 (external, cli)"),
	)
	require.Equal(t,
		service.PlatformOpenAI,
		gatewayModelListPlatformForClient(service.PlatformOpenAI, "curl/8.7.1"),
	)
}

func TestShouldHideGatewayModelListForClaudeCode(t *testing.T) {
	require.True(t, shouldHideGatewayModelListForClient("claude-cli/2.1.126 (external, cli)"))
	require.False(t, shouldHideGatewayModelListForClient("curl/8.7.1"))
}

func TestBuildGatewayModelListFromIDsUsesOpenAIShape(t *testing.T) {
	data := buildGatewayModelListFromIDs([]string{"gpt-5.4"}, service.PlatformOpenAI)

	models, ok := data.([]openai.Model)
	require.True(t, ok)
	require.Len(t, models, 1)
	require.Equal(t, "gpt-5.4", models[0].ID)
	require.Equal(t, "model", models[0].Object)
	require.Equal(t, "openai", models[0].OwnedBy)
	require.NotZero(t, models[0].Created)
}

func TestBuildGatewayModelListFromIDsUsesClaudeShape(t *testing.T) {
	data := buildGatewayModelListFromIDs([]string{"claude-sonnet-4-6"}, service.PlatformAnthropic)

	models, ok := data.([]claude.Model)
	require.True(t, ok)
	require.Len(t, models, 1)
	require.Equal(t, "claude-sonnet-4-6", models[0].ID)
	require.Equal(t, "model", models[0].Type)
	require.Equal(t, "claude-sonnet-4-6", models[0].DisplayName)
	require.NotEmpty(t, models[0].CreatedAt)
}
