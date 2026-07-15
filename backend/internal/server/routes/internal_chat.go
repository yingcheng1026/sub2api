package routes

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// RegisterInternalChatRoutes keeps a fail-closed tombstone for the retired
// reusable-key export. Chat integrations must proxy bounded operations instead
// of taking custody of a customer's long-lived gateway credential.
func RegisterInternalChatRoutes(r *gin.Engine) {
	internal := r.Group("/internal/v1/chat")
	internal.Use(requireChatInternalToken())
	{
		internal.GET("/default-api-key", func(c *gin.Context) {
			c.Header("Cache-Control", "no-store, max-age=0")
			c.Header("Pragma", "no-cache")
			c.JSON(http.StatusGone, gin.H{"error": "reusable API-key export is retired"})
		})
	}
}

func requireChatInternalToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		expected := strings.TrimSpace(os.Getenv("HFC_CHAT_INTERNAL_TOKEN"))
		if expected == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "chat internal token is not configured"})
			c.Abort()
			return
		}
		got := strings.TrimSpace(c.GetHeader("X-HFC-Internal-Token"))
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid internal token"})
			c.Abort()
			return
		}
		c.Next()
	}
}
