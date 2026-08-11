package service

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAntigravityCompatStreamErrorsUseInboundOpenAIProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("chat completions", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		writer := newAntigravityClientWriter(c.Writer, c.Writer, "test")
		newAntigravityChatStreamAdapter("gpt-test", false).WriteError(writer, "upstream failed")

		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(recorder.Body.String()), "data:"))
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &payload))
		errObject := payload["error"].(map[string]any)
		require.Equal(t, "server_error", errObject["type"])
		require.Contains(t, errObject, "param")
		require.Contains(t, errObject, "code")
	})

	t.Run("responses", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		writer := newAntigravityClientWriter(c.Writer, c.Writer, "test")
		newAntigravityResponsesStreamAdapter("gpt-test").WriteError(writer, "upstream failed")

		require.Contains(t, recorder.Body.String(), "event: response.failed\n")
		data := strings.SplitN(recorder.Body.String(), "data: ", 2)
		require.Len(t, data, 2)
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(data[1])), &payload))
		require.Equal(t, "response.failed", payload["type"])
		response := payload["response"].(map[string]any)
		require.Equal(t, "failed", response["status"])
		require.Equal(t, "server_error", response["error"].(map[string]any)["code"])
	})
}
