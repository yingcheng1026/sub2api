package routes

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// RegisterInternalChatRoutes registers server-to-server endpoints for chat.handsfreeclub.com.
func RegisterInternalChatRoutes(
	r *gin.Engine,
	h *handler.Handlers,
	jwtAuth servermiddleware.JWTAuthMiddleware,
) {
	internal := r.Group("/internal/v1/chat")
	internal.Use(requireChatInternalToken())
	internal.Use(forwardChatUserAuthorization())
	internal.Use(gin.HandlerFunc(jwtAuth))
	{
		internal.GET("/default-api-key", h.APIKey.GetChatDefaultAPIKey)
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

func forwardChatUserAuthorization() gin.HandlerFunc {
	return func(c *gin.Context) {
		if auth := strings.TrimSpace(c.GetHeader("X-HFC-User-Authorization")); auth != "" {
			c.Request.Header.Set("Authorization", auth)
		}
		c.Next()
	}
}
