package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestModels_HidesGatewayModelsForClaudeCodeOpenAIDispatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.126 (external, cli)")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{
			ID:                    3,
			Platform:              service.PlatformOpenAI,
			AllowMessagesDispatch: true,
		},
	})

	(&GatewayHandler{}).Models(c)

	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Object string            `json:"object"`
		Data   []json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "list", body.Object)
	require.Empty(t, body.Data)
}

func TestHideGatewayModelsForClaudeCodeModelPickerOnlyMatchesClaudeCodeOpenAIDispatch(t *testing.T) {
	apiKey := &service.APIKey{
		Group: &service.Group{
			Platform:              service.PlatformOpenAI,
			AllowMessagesDispatch: true,
		},
	}

	require.True(t, hideGatewayModelsForClaudeCodeModelPicker(apiKey, "claude-cli/2.1.126 (external, cli)"))
	require.False(t, hideGatewayModelsForClaudeCodeModelPicker(apiKey, "curl/8.7.1"))

	apiKey.Group.AllowMessagesDispatch = false
	require.False(t, hideGatewayModelsForClaudeCodeModelPicker(apiKey, "claude-cli/2.1.126 (external, cli)"))

	apiKey.Group.AllowMessagesDispatch = true
	apiKey.Group.Platform = service.PlatformAnthropic
	require.False(t, hideGatewayModelsForClaudeCodeModelPicker(apiKey, "claude-cli/2.1.126 (external, cli)"))
}
