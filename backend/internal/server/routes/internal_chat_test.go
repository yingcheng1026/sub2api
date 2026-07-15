//go:build unit

package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestInternalChatDefaultAPIKeyExportIsRetired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("HFC_CHAT_INTERNAL_TOKEN", "service-token")

	router := gin.New()
	RegisterInternalChatRoutes(router)

	denied := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodGet, "/internal/v1/chat/default-api-key", nil)
	badReq.Header.Set("X-HFC-Internal-Token", "wrong")
	router.ServeHTTP(denied, badReq)
	require.Equal(t, http.StatusUnauthorized, denied.Code)

	retired := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/chat/default-api-key", nil)
	request.Header.Set("X-HFC-Internal-Token", "service-token")
	request.Header.Set("X-HFC-User-Authorization", "Bearer replayable-user-jwt")
	router.ServeHTTP(retired, request)

	require.Equal(t, http.StatusGone, retired.Code)
	require.Contains(t, retired.Body.String(), "reusable API-key export is retired")
	require.NotContains(t, retired.Body.String(), "api_key")
	require.Equal(t, "no-store, max-age=0", retired.Header().Get("Cache-Control"))
}
