package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
)

// writeAnthropicContractError keeps service-level Messages errors aligned with
// the Anthropic request-id header/body contract.
func writeAnthropicContractError(c *gin.Context, status int, errType, message string) {
	if c == nil {
		return
	}
	payload := anthropicContractErrorPayload(c, errType, message)
	if requestID, _ := payload["request_id"].(string); requestID != "" && !c.Writer.Written() {
		c.Header("request-id", requestID)
	}
	c.JSON(status, payload)
}

func anthropicContractErrorPayload(c *gin.Context, errType, message string) gin.H {
	requestID := ""
	if c != nil && c.Request != nil {
		requestID, _ = c.Request.Context().Value(ctxkey.RequestID).(string)
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" && c != nil {
		requestID = strings.TrimSpace(c.Writer.Header().Get("X-Request-ID"))
	}
	if requestID != "" && !strings.HasPrefix(requestID, "req_") {
		requestID = "req_" + strings.ReplaceAll(requestID, "-", "")
	}
	payload := gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	}
	if requestID != "" {
		payload["request_id"] = requestID
	}
	return payload
}

func prepareAnthropicContractStream(c *gin.Context) {
	if c == nil || c.Writer.Written() {
		return
	}
	payload := anthropicContractErrorPayload(c, "api_error", "")
	if requestID, _ := payload["request_id"].(string); requestID != "" {
		c.Header("request-id", requestID)
	}
}
