package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGatewayAnthropicEndpointsRejectOversizedSanitizationSlowPath(t *testing.T) {
	body := make([]byte, 0, 16<<20+256)
	body = append(body, `{"model":"claude-test","max_tokens":8,"thinking":"","messages":[],"padding":"`...)
	body = append(body, bytes.Repeat([]byte{'x'}, 16<<20)...)
	body = append(body, `"}`...)

	tests := []struct {
		name string
		path string
		run  func(*GatewayHandler, *gin.Context)
	}{
		{name: "messages", path: "/v1/messages", run: func(h *GatewayHandler, c *gin.Context) { h.Messages(c) }},
		{name: "count tokens", path: "/v1/messages/count_tokens", run: func(h *GatewayHandler, c *gin.Context) { h.CountTokens(c) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(body))
			groupID := int64(22)
			apiKey := &service.APIKey{
				ID:      607,
				UserID:  142,
				GroupID: &groupID,
				Status:  service.StatusActive,
			}
			c.Set(string(middleware.ContextKeyAPIKey), apiKey)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID})

			tt.run(&GatewayHandler{}, c)

			require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
			require.Equal(t, "invalid_request_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
			require.Contains(t, gjson.GetBytes(recorder.Body.Bytes(), "error.message").String(), "compatibility sanitization limit")
		})
	}
}
