package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWriteOpenAIContractErrorUsesCompleteEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	writeOpenAIContractError(c, http.StatusBadRequest, "invalid_request_error", "invalid_tools", "tools", "tools are invalid")

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	var payload struct {
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "tools are invalid", payload.Error["message"])
	require.Equal(t, "invalid_request_error", payload.Error["type"])
	require.Equal(t, "tools", payload.Error["param"])
	require.Equal(t, "invalid_tools", payload.Error["code"])
}

func TestWriteOpenAIContractErrorUsesNullForOmittedCodeAndParam(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	writeOpenAIContractError(c, http.StatusForbidden, "permission_error", "", "", "denied")

	var payload struct {
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Contains(t, payload.Error, "param")
	require.Contains(t, payload.Error, "code")
	require.Nil(t, payload.Error["param"])
	require.Nil(t, payload.Error["code"])
}
