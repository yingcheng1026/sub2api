package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRequireGroupAssignmentFailsClosedForUnroutedWalletPurpose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{
			ID:      1,
			Purpose: service.APIKeyPurposeWalletUniversal,
			Name:    service.WalletUniversalAPIKeyName,
		})
		c.Next()
	})
	router.Use(RequireGroupAssignment(nil, GoogleErrorWriter))
	router.GET("/v1beta/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1beta/test", nil))

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "routing was not resolved")
}

func TestRequireGroupAssignmentAllowsAlreadyRoutedWalletPurpose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(3)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{
			ID:      2,
			Purpose: service.APIKeyPurposeWalletUniversal,
			Name:    service.WalletUniversalAPIKeyName,
			GroupID: &groupID,
		})
		c.Next()
	})
	router.Use(RequireGroupAssignment(nil, GoogleErrorWriter))
	router.GET("/v1beta/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1beta/test", nil))

	require.Equal(t, http.StatusNoContent, recorder.Code)
}
