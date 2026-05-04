package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// CountTokens handles Anthropic count_tokens probes routed to an OpenAI group.
// OpenAI upstreams do not expose Anthropic's count_tokens endpoint, so this
// returns a local estimate without selecting an upstream account or recording usage.
func (h *OpenAIGatewayHandler) CountTokens(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.anthropicErrorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	if apiKey.Group != nil && !apiKey.Group.AllowMessagesDispatch {
		h.anthropicErrorResponse(c, http.StatusForbidden, "permission_error",
			"This group does not allow /v1/messages dispatch")
		return
	}

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.anthropicErrorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	parsedReq, err := service.ParseGatewayRequest(body, domain.PlatformAnthropic)
	if err != nil {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	if parsedReq.Model == "" {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}

	setOpsRequestContext(c, parsedReq.Model, parsedReq.Stream, body)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(parsedReq.Stream, false)))

	c.JSON(http.StatusOK, gin.H{
		"input_tokens": estimateAnthropicInputTokens(body),
	})
}

func estimateAnthropicInputTokens(body []byte) int {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return 1
	}

	total := 0
	for _, key := range []string{"system", "messages", "tools", "tool_choice"} {
		total += estimateTokensFromJSONValue(payload[key])
	}
	if total <= 0 {
		return 1
	}
	return total
}

func estimateTokensFromJSONValue(v any) int {
	switch value := v.(type) {
	case nil:
		return 0
	case string:
		return estimateTokensForCompatText(value)
	case []any:
		total := 0
		for _, item := range value {
			total += estimateTokensFromJSONValue(item)
		}
		return total
	case map[string]any:
		total := 0
		for key, item := range value {
			total += estimateTokensForCompatText(key)
			total += estimateTokensFromJSONValue(item)
		}
		return total
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return 0
		}
		return estimateTokensForCompatText(string(raw))
	}
}

func estimateTokensForCompatText(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	runes := []rune(s)
	if len(runes) == 0 {
		return 0
	}
	ascii := 0
	for _, r := range runes {
		if r <= 0x7f {
			ascii++
		}
	}
	asciiRatio := float64(ascii) / float64(len(runes))
	if asciiRatio >= 0.8 {
		return (len(runes) + 3) / 4
	}
	return len(runes)
}
