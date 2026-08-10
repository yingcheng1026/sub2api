package middleware

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	"github.com/gin-gonic/gin"
)

// GatewayErrorProtocol identifies the client-visible error contract for an AI gateway request.
type GatewayErrorProtocol string

const (
	GatewayErrorProtocolManagement GatewayErrorProtocol = "management"
	GatewayErrorProtocolOpenAI     GatewayErrorProtocol = "openai"
	GatewayErrorProtocolAnthropic  GatewayErrorProtocol = "anthropic"
	GatewayErrorProtocolGoogle     GatewayErrorProtocol = "google"
)

// GatewayErrorProtocolForRequest selects the protocol before API key authentication.
// Shared model-list endpoints default to OpenAI's public list contract because the
// account/group is not available until authentication succeeds.
func GatewayErrorProtocolForRequest(r *http.Request) GatewayErrorProtocol {
	if r == nil || r.URL == nil {
		return GatewayErrorProtocolManagement
	}
	path := strings.TrimRight(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	if strings.HasPrefix(path, "/v1beta/") || path == "/v1beta" ||
		strings.HasPrefix(path, "/antigravity/v1beta/") || path == "/antigravity/v1beta" {
		return GatewayErrorProtocolGoogle
	}
	if path == "/v1/models" &&
		(strings.TrimSpace(r.Header.Get("anthropic-version")) != "" || strings.TrimSpace(r.Header.Get("x-api-key")) != "") {
		return GatewayErrorProtocolAnthropic
	}
	if path == "/v1/messages" || strings.HasPrefix(path, "/v1/messages/") ||
		path == "/antigravity/models" || strings.HasPrefix(path, "/antigravity/v1/") || path == "/antigravity/v1" {
		return GatewayErrorProtocolAnthropic
	}
	for _, root := range []string{
		"/v1/models", "/models", "/v1/chat/completions", "/chat/completions",
		"/v1/responses", "/responses", "/backend-api/codex", "/v1/embeddings", "/embeddings",
		"/v1/images", "/images", "/v1/videos", "/videos", "/v1/live", "/live", "/alpha/search",
	} {
		if path == root || strings.HasPrefix(path, root+"/") {
			return GatewayErrorProtocolOpenAI
		}
	}
	return GatewayErrorProtocolManagement
}

// WriteGatewayError writes an authentication or gateway-policy error using the
// official client contract selected from the request path.
func WriteGatewayError(c *gin.Context, status int, code, message string) {
	switch GatewayErrorProtocolForRequest(requestFromGin(c)) {
	case GatewayErrorProtocolOpenAI:
		writeOpenAIError(c, status, code, message)
	case GatewayErrorProtocolAnthropic:
		writeAnthropicError(c, status, code, message)
	case GatewayErrorProtocolGoogle:
		writeGoogleError(c, status, code, message)
	default:
		c.JSON(status, NewErrorResponse(code, message))
	}
}

// WriteOpenAIError writes a complete OpenAI-compatible error object. Callers
// may supply a specific type and code when the endpoint knows more than the
// HTTP status alone.
func WriteOpenAIError(c *gin.Context, status int, errType, code, message string) {
	if strings.TrimSpace(errType) == "" {
		errType, _ = openAIErrorClassification(status, code)
	}
	var officialCode any
	if strings.TrimSpace(code) != "" {
		officialCode = code
	}
	c.JSON(status, gin.H{"error": gin.H{
		"message": message,
		"type":    errType,
		"param":   nil,
		"code":    officialCode,
	}})
}

// WriteAnthropicError writes Anthropic's JSON error shape including request ID.
func WriteAnthropicError(c *gin.Context, status int, errType, message string) {
	errType = normalizeAnthropicErrorType(status, errType)
	requestID := anthropicRequestID(c)
	if requestID != "" {
		c.Header("request-id", requestID)
	}
	payload := AnthropicErrorPayload(c, errType, message)
	c.JSON(status, payload)
}

// AnthropicErrorPayload returns the official body shape for SSE error events.
func AnthropicErrorPayload(c *gin.Context, errType, message string) gin.H {
	requestID := anthropicRequestID(c)
	if requestID != "" && c != nil {
		c.Header("request-id", requestID)
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

func anthropicRequestID(c *gin.Context) string {
	requestID := gatewayRequestID(c)
	if requestID == "" || strings.HasPrefix(requestID, "req_") {
		return requestID
	}
	return "req_" + strings.ReplaceAll(requestID, "-", "")
}

// AnthropicErrorPayloadForStatus applies Anthropic's HTTP-to-error-type mapping
// before building a post-first-byte SSE error event.
func AnthropicErrorPayloadForStatus(c *gin.Context, status int, errType, message string) gin.H {
	return AnthropicErrorPayload(c, normalizeAnthropicErrorType(status, errType), message)
}

// WriteGoogleError exposes the canonical Google JSON error envelope.
func WriteGoogleError(c *gin.Context, status int, code, message string) {
	writeGoogleError(c, status, code, message)
}

func writeOpenAIError(c *gin.Context, status int, code, message string) {
	errType, officialCode := openAIErrorClassification(status, code)
	codeString, _ := officialCode.(string)
	WriteOpenAIError(c, status, errType, codeString, message)
}

func writeAnthropicError(c *gin.Context, status int, code, message string) {
	WriteAnthropicError(c, status, anthropicErrorType(status, code), message)
}

func writeGoogleError(c *gin.Context, status int, code, message string) {
	status, googleStatus := googleErrorClassification(status, code)
	errObject := gin.H{
		"code":    status,
		"message": message,
		"status":  googleStatus,
	}
	if code == "INVALID_API_KEY" {
		errObject["details"] = []gin.H{{
			"@type":  "type.googleapis.com/google.rpc.ErrorInfo",
			"reason": "API_KEY_INVALID",
			"domain": "googleapis.com",
		}}
	}
	c.JSON(status, gin.H{"error": errObject})
}

func openAIErrorClassification(status int, code string) (string, any) {
	switch code {
	case "INVALID_API_KEY", "API_KEY_DISABLED", "USER_INACTIVE", "USER_NOT_FOUND":
		return "invalid_request_error", "invalid_api_key"
	case "API_KEY_REQUIRED":
		return "invalid_request_error", nil
	case "API_KEY_QUOTA_EXHAUSTED", "INSUFFICIENT_BALANCE":
		return "insufficient_quota", "insufficient_quota"
	}
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error", nullableGatewayCode(code)
	case http.StatusUnauthorized:
		return "invalid_request_error", nullableGatewayCode(code)
	case http.StatusForbidden:
		return "permission_error", nullableGatewayCode(code)
	case http.StatusNotFound:
		return "not_found_error", nullableGatewayCode(code)
	case http.StatusTooManyRequests:
		return "rate_limit_error", nullableGatewayCode(code)
	default:
		if status >= 500 {
			return "server_error", nullableGatewayCode(code)
		}
		return "invalid_request_error", nullableGatewayCode(code)
	}
}

func anthropicErrorType(status int, code string) string {
	if code == "API_KEY_REQUIRED" || code == "INVALID_API_KEY" || code == "API_KEY_DISABLED" ||
		code == "USER_INACTIVE" || code == "USER_NOT_FOUND" {
		return "authentication_error"
	}
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

func normalizeAnthropicErrorType(status int, errType string) string {
	if status == http.StatusRequestEntityTooLarge {
		return "request_too_large"
	}
	if status == 529 {
		return "overloaded_error"
	}
	if strings.TrimSpace(errType) == "" {
		return anthropicErrorType(status, "")
	}
	return errType
}

func googleErrorClassification(status int, code string) (int, string) {
	switch code {
	case "API_KEY_REQUIRED":
		return http.StatusForbidden, "PERMISSION_DENIED"
	case "INVALID_API_KEY":
		return http.StatusBadRequest, "INVALID_ARGUMENT"
	default:
		return status, googleapi.HTTPStatusToGoogleStatus(status)
	}
}

func gatewayRequestID(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	requestID, _ := c.Request.Context().Value(ctxkey.RequestID).(string)
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = strings.TrimSpace(c.Writer.Header().Get("X-Request-ID"))
	}
	return requestID
}

func nullableGatewayCode(code string) any {
	if strings.TrimSpace(code) == "" {
		return nil
	}
	return strings.ToLower(strings.TrimSpace(code))
}

func requestFromGin(c *gin.Context) *http.Request {
	if c == nil {
		return nil
	}
	return c.Request
}
