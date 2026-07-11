package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGenericGatewayRejectsGPT56BeforeAccountScheduling(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		run        func(*GatewayHandler, *gin.Context)
		wantStatus int
		wantType   string
	}{
		{
			name:       "responses exact tier",
			path:       "/v1/responses",
			body:       `{"model":"gpt-5.6-terra","input":"hello"}`,
			run:        func(h *GatewayHandler, c *gin.Context) { h.Responses(c) },
			wantStatus: http.StatusForbidden,
			wantType:   "model_not_available",
		},
		{
			name:       "messages exact tier",
			path:       "/v1/messages",
			body:       `{"model":"gpt-5.6-terra","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`,
			run:        func(h *GatewayHandler, c *gin.Context) { h.Messages(c) },
			wantStatus: http.StatusForbidden,
			wantType:   "model_not_available",
		},
		{
			name:       "responses bare family",
			path:       "/v1/responses",
			body:       `{"model":"gpt-5.6","input":"hello"}`,
			run:        func(h *GatewayHandler, c *gin.Context) { h.Responses(c) },
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
		},
		{
			name:       "chat completions exact tier",
			path:       "/v1/chat/completions",
			body:       `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hello"}]}`,
			run:        func(h *GatewayHandler, c *gin.Context) { h.ChatCompletions(c) },
			wantStatus: http.StatusForbidden,
			wantType:   "model_not_available",
		},
		{
			name:       "count tokens exact tier",
			path:       "/v1/messages/count_tokens",
			body:       `{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hello"}]}`,
			run:        func(h *GatewayHandler, c *gin.Context) { h.CountTokens(c) },
			wantStatus: http.StatusForbidden,
			wantType:   "model_not_available",
		},
		{
			name:       "count tokens bare family",
			path:       "/v1/messages/count_tokens",
			body:       `{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}]}`,
			run:        func(h *GatewayHandler, c *gin.Context) { h.CountTokens(c) },
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			groupID := int64(22)
			apiKey := &service.APIKey{
				ID: 607, UserID: 142, GroupID: &groupID, Status: service.StatusActive,
				User:  &service.User{ID: 142, Concurrency: 1},
				Group: &service.Group{ID: groupID, Platform: service.PlatformAnthropic},
			}
			c.Set(string(middleware.ContextKeyAPIKey), apiKey)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 1})

			tt.run(&GatewayHandler{}, c)

			require.Equal(t, tt.wantStatus, recorder.Code, recorder.Body.String())
			actualType := gjson.GetBytes(recorder.Body.Bytes(), "error.type").String()
			if actualType == "" {
				actualType = gjson.GetBytes(recorder.Body.Bytes(), "error.code").String()
			}
			require.Equal(t, tt.wantType, actualType)
			require.Contains(t, gjson.GetBytes(recorder.Body.Bytes(), "error.message").String(), "GPT-5.6")
		})
	}
}

func TestGenericGatewayGPT56MappedAccessError(t *testing.T) {
	tests := []struct {
		name       string
		mapping    service.ChannelMappingResult
		wantStatus int
		wantType   string
		wantReject bool
	}{
		{
			name:       "mapped exact tier",
			mapping:    service.ChannelMappingResult{Mapped: true, MappedModel: "gpt-5.6-terra"},
			wantStatus: http.StatusForbidden,
			wantType:   "model_not_available",
			wantReject: true,
		},
		{
			name:       "mapped bare family",
			mapping:    service.ChannelMappingResult{Mapped: true, MappedModel: "gpt-5.6"},
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
			wantReject: true,
		},
		{
			name:       "mapped non gpt56",
			mapping:    service.ChannelMappingResult{Mapped: true, MappedModel: "gpt-5.4"},
			wantReject: false,
		},
		{
			name:       "unmapped gpt56 target is ignored",
			mapping:    service.ChannelMappingResult{Mapped: false, MappedModel: "gpt-5.6-sol"},
			wantReject: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, errType, _, reject := genericGatewayGPT56MappedAccessError(tt.mapping)
			require.Equal(t, tt.wantReject, reject)
			require.Equal(t, tt.wantStatus, status)
			require.Equal(t, tt.wantType, errType)
		})
	}
}

func TestGenericGatewayGPT56AccountAccessError(t *testing.T) {
	account := &service.Account{
		Platform: service.PlatformAnthropic,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-sonnet-4-6": "gpt-5.6-sol",
				"claude-opus-*":     "gpt-5.6-terra",
				"claude-haiku-4-5":  "claude-haiku-4-5-20251001",
			},
		},
	}

	status, errType, _, reject := genericGatewayGPT56AccountAccessError(account, "claude-sonnet-4-6", service.ChannelMappingResult{})
	require.True(t, reject)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "model_not_available", errType)

	status, errType, _, reject = genericGatewayGPT56AccountAccessError(account, "claude-opus-4-6", service.ChannelMappingResult{})
	require.True(t, reject)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "model_not_available", errType)

	_, _, _, reject = genericGatewayGPT56AccountAccessError(account, "claude-haiku-4-5", service.ChannelMappingResult{})
	require.False(t, reject)

	_, _, _, reject = genericGatewayGPT56AccountAccessError(account, "claude-sonnet-4-6", service.ChannelMappingResult{Mapped: true, MappedModel: "claude-haiku-4-5"})
	require.False(t, reject)
}
