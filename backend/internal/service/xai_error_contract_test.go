package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWriteXAIContractErrorUsesTopLevelEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	writeXAIContractError(c, http.StatusForbidden, " policy denied ")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "permission-denied", payload["code"])
	require.Equal(t, "policy denied", payload["error"])
	require.NotContains(t, payload, "message")
}
