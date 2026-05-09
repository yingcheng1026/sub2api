package handler

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

const defaultChatBridgeRedirectPath = "/agent/inbox"

type chatBridgeService interface {
	CreateLoginCode(ctx context.Context, userID int64) (*service.ChatBridgeLoginCode, error)
	ExchangeLoginCode(ctx context.Context, code string) (*service.ChatBridgeExchangeResult, error)
}

type CreateChatBridgeCodeRequest struct {
	RedirectPath string `json:"redirect_path"`
}

type CreateChatBridgeCodeResponse struct {
	Code      string `json:"code"`
	ChatURL   string `json:"chat_url,omitempty"`
	ExpiresIn int    `json:"expires_in"`
}

type ExchangeChatBridgeCodeRequest struct {
	Code string `json:"code" binding:"required"`
}

type ExchangeChatBridgeCodeResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

func (h *AuthHandler) CreateChatBridgeCode(c *gin.Context) {
	if h == nil || h.chatBridgeSvc == nil {
		response.Error(c, http.StatusServiceUnavailable, "chat bridge is not configured")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Authorization is required")
		return
	}

	var req CreateChatBridgeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	code, err := h.chatBridgeSvc.CreateLoginCode(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	chatURL, err := buildChatBridgeURL(resolveChatBridgePublicBaseURL(), req.RedirectPath, code.Code)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	response.Success(c, CreateChatBridgeCodeResponse{
		Code:      code.Code,
		ChatURL:   chatURL,
		ExpiresIn: code.ExpiresIn,
	})
}

func (h *AuthHandler) ExchangeChatBridgeCode(c *gin.Context) {
	if h == nil || h.chatBridgeSvc == nil {
		response.Error(c, http.StatusServiceUnavailable, "chat bridge is not configured")
		return
	}

	var req ExchangeChatBridgeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	exchanged, err := h.chatBridgeSvc.ExchangeLoginCode(c.Request.Context(), req.Code)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, ExchangeChatBridgeCodeResponse{
		AccessToken: exchanged.AccessToken,
		ExpiresIn:   exchanged.ExpiresIn,
		TokenType:   exchanged.TokenType,
	})
}

func resolveChatBridgePublicBaseURL() string {
	for _, key := range []string{"HFC_CHAT_PUBLIC_URL", "HFC_CHAT_BASE_URL"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func buildChatBridgeURL(baseURL, redirectPath, code string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", nil
	}

	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", service.ErrIdentityRedirectInvalid
	}

	targetPath := sanitizeChatBridgeRedirectPath(redirectPath)
	relative, err := url.Parse(targetPath)
	if err != nil {
		return "", service.ErrIdentityRedirectInvalid
	}

	target := base.ResolveReference(relative)
	query := target.Query()
	query.Set("hfc_chat_code", code)
	target.RawQuery = query.Encode()
	target.Fragment = ""
	return target.String(), nil
}

func sanitizeChatBridgeRedirectPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultChatBridgeRedirectPath
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") {
		return defaultChatBridgeRedirectPath
	}
	parsed.Fragment = ""
	if parsed.Path == "" {
		parsed.Path = defaultChatBridgeRedirectPath
	}
	return parsed.String()
}
