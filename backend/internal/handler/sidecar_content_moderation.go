package handler

import (
	"strings"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func sidecarContentModerationProtocol(sidecarPath string) string {
	switch strings.TrimSpace(sidecarPath) {
	case "/v1/messages":
		return service.ContentModerationProtocolAnthropicMessages
	case "/v1/responses":
		return service.ContentModerationProtocolOpenAIResponses
	case "/v1/chat/completions":
		return service.ContentModerationProtocolOpenAIChat
	default:
		// The moderation extractor's default branch safely inspects the common
		// input/messages/contents shapes. Keeping the check active here also
		// prevents a future billable sidecar route from silently bypassing policy.
		return "sidecar_generic"
	}
}

func (h *GatewayHandler) blockSidecarContentModeration(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	sidecarPath string,
	model string,
	body []byte,
) bool {
	decision := h.checkContentModeration(
		c,
		reqLog,
		apiKey,
		subject,
		sidecarContentModerationProtocol(sidecarPath),
		model,
		body,
	)
	if decision == nil || !decision.Blocked {
		return false
	}
	h.handleStreamingAwareError(
		c,
		contentModerationStatus(decision),
		contentModerationErrorCode(decision),
		decision.Message,
		false,
	)
	return true
}
