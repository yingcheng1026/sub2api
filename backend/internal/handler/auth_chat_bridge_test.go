package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type authChatBridgeServiceStub struct {
	createCode       *service.ChatBridgeLoginCode
	createUserID     int64
	exchangeCode     string
	exchangeResponse *service.ChatBridgeExchangeResult
}

func (s *authChatBridgeServiceStub) CreateLoginCode(_ context.Context, userID int64) (*service.ChatBridgeLoginCode, error) {
	s.createUserID = userID
	return s.createCode, nil
}

func (s *authChatBridgeServiceStub) ExchangeLoginCode(_ context.Context, code string) (*service.ChatBridgeExchangeResult, error) {
	s.exchangeCode = code
	return s.exchangeResponse, nil
}

func TestAuthHandlerCreateChatBridgeCodeReturnsChatURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("HFC_CHAT_PUBLIC_URL", "https://chat.handsfreeclub.com")

	stub := &authChatBridgeServiceStub{
		createCode: &service.ChatBridgeLoginCode{
			Code:      "code-123",
			ExpiresAt: time.Now().Add(time.Minute),
			ExpiresIn: 60,
		},
	}
	handler := &AuthHandler{chatBridgeSvc: stub}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/chat/bridge/code", strings.NewReader(`{"redirect_path":"/?hfc_model=gpt-5.4-mini"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})

	handler.CreateChatBridgeCode(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int64(42), stub.createUserID)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			Code      string `json:"code"`
			ChatURL   string `json:"chat_url"`
			ExpiresIn int    `json:"expires_in"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Equal(t, "code-123", resp.Data.Code)
	require.Equal(t, 60, resp.Data.ExpiresIn)

	chatURL, err := url.Parse(resp.Data.ChatURL)
	require.NoError(t, err)
	require.Equal(t, "https", chatURL.Scheme)
	require.Equal(t, "chat.handsfreeclub.com", chatURL.Host)
	require.Equal(t, "/", chatURL.Path)
	require.Equal(t, "code-123", chatURL.Query().Get("hfc_chat_code"))
	require.Equal(t, "gpt-5.4-mini", chatURL.Query().Get("hfc_model"))
}

func TestAuthHandlerExchangeChatBridgeCodeReturnsToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stub := &authChatBridgeServiceStub{
		exchangeResponse: &service.ChatBridgeExchangeResult{
			AccessToken: "user-jwt",
			ExpiresIn:   3600,
			TokenType:   "Bearer",
		},
	}
	handler := &AuthHandler{chatBridgeSvc: stub}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/chat/bridge/exchange", strings.NewReader(`{"code":" code-123 "}`))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.ExchangeChatBridgeCode(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, " code-123 ", stub.exchangeCode)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
			TokenType   string `json:"token_type"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Equal(t, "user-jwt", resp.Data.AccessToken)
	require.Equal(t, 3600, resp.Data.ExpiresIn)
	require.Equal(t, "Bearer", resp.Data.TokenType)
}

func TestSanitizeChatBridgeRedirectPathNormalizesNextChatUnsupportedPaths(t *testing.T) {
	require.Equal(t, "/?hfc_model=gpt-5.4-mini", sanitizeChatBridgeRedirectPath("/agent/inbox?hfc_model=gpt-5.4-mini"))
	require.Equal(t, "/?hfc_launch=image", sanitizeChatBridgeRedirectPath("/image?hfc_launch=image"))
	require.Equal(t, "/", sanitizeChatBridgeRedirectPath("https://evil.example.com/"))
}
