package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWriteGoogleContractErrorClassifiesAuthenticationFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		code       string
		wantStatus int
		wantGoogle string
		wantReason string
	}{
		{name: "missing key", code: "API_KEY_REQUIRED", wantStatus: http.StatusForbidden, wantGoogle: "PERMISSION_DENIED"},
		{name: "invalid key", code: "INVALID_API_KEY", wantStatus: http.StatusBadRequest, wantGoogle: "INVALID_ARGUMENT", wantReason: "API_KEY_INVALID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			writeGoogleContractError(c, http.StatusUnauthorized, tt.code, "auth failed")

			require.Equal(t, tt.wantStatus, recorder.Code)
			var payload map[string]any
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
			errObject := payload["error"].(map[string]any)
			require.Equal(t, float64(tt.wantStatus), errObject["code"])
			require.Equal(t, tt.wantGoogle, errObject["status"])
			if tt.wantReason != "" {
				details := errObject["details"].([]any)
				require.Equal(t, tt.wantReason, details[0].(map[string]any)["reason"])
			}
		})
	}
}
