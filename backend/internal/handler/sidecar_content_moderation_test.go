//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSidecarGenerationContentModerationBlocksBeforeForward(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		platform string
		path     string
		body     []byte
		invoke   func(*GatewayHandler, *gin.Context, []byte)
	}{
		{
			name:     "kiro messages",
			platform: service.PlatformKiro,
			path:     "/kiro/v1/messages",
			body:     []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"blocked sidecar prompt"}]}`),
			invoke: func(h *GatewayHandler, c *gin.Context, body []byte) {
				h.handleKiroSidecar(c, kiroSidecarRequest{
					Method:       http.MethodPost,
					Path:         "/v1/messages",
					Model:        "claude-sonnet-4-6",
					RequestBody:  body,
					UpstreamBody: body,
					RecordUsage:  true,
				})
			},
		},
		{
			name:     "cursor responses",
			platform: service.PlatformCursor,
			path:     "/cursor/v1/responses",
			body:     []byte(`{"model":"gpt-5.5","input":[{"role":"user","content":[{"type":"input_text","text":"blocked sidecar prompt"}]}]}`),
			invoke: func(h *GatewayHandler, c *gin.Context, body []byte) {
				h.handleCursorSidecar(c, cursorSidecarRequest{
					Method:       http.MethodPost,
					Path:         "/v1/responses",
					Model:        "gpt-5.5",
					RequestBody:  body,
					UpstreamBody: body,
					RecordUsage:  true,
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sidecarCalls atomic.Int32
			sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				sidecarCalls.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer sidecar.Close()

			moderationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"results":[{"category_scores":{"sexual":0.9}}]}`))
			}))
			defer moderationServer.Close()

			moderationConfig := &service.ContentModerationConfig{
				Enabled:          true,
				Mode:             service.ContentModerationModePreBlock,
				BaseURL:          moderationServer.URL,
				Model:            "omni-moderation-latest",
				EncryptedAPIKeys: []string{"test:sk-test"},
				SampleRate:       100,
				AllGroups:        true,
				BlockMessage:     "sidecar moderation blocked",
			}
			rawConfig, err := json.Marshal(moderationConfig)
			require.NoError(t, err)
			moderationRepo := &contentModerationHandlerTestRepo{}
			moderationService := service.NewContentModerationServiceWithEncryptor(
				&contentModerationHandlerSettingRepo{values: map[string]string{
					service.SettingKeyRiskControlEnabled:      "true",
					service.SettingKeyContentModerationConfig: string(rawConfig),
				}},
				moderationRepo,
				nil,
				nil,
				nil,
				nil,
				nil,
				contentModerationHandlerTestEncryptor{},
			)

			group := &service.Group{ID: 77, Name: tt.platform, Platform: tt.platform}
			account := &service.Account{
				ID:          88,
				Name:        tt.platform + "-account",
				Platform:    tt.platform,
				Type:        service.AccountTypeAPIKey,
				Status:      service.StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: map[string]any{"api_key": "sidecar-account-key"},
			}
			h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
			defer cleanup()
			h.contentModerationService = moderationService
			h.cfg = &config.Config{}
			if tt.platform == service.PlatformKiro {
				h.cfg.Kiro.SidecarURL = sidecar.URL
			} else {
				h.cfg.Cursor.SidecarURL = sidecar.URL
			}

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, nil)
			apiKey := &service.APIKey{
				ID:      99,
				Name:    "sidecar-key",
				UserID:  100,
				GroupID: &group.ID,
				Group:   group,
				User:    &service.User{ID: 100, Concurrency: 1},
			}
			c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 1})

			tt.invoke(h, c, tt.body)

			require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
			require.Contains(t, recorder.Body.String(), "sidecar moderation blocked")
			require.Zero(t, sidecarCalls.Load())
			require.Len(t, moderationRepo.logs, 1)
			require.Equal(t, tt.platform, moderationRepo.logs[0].Provider)
		})
	}
}
