package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newGatewayRoutesTestRouter() *gin.Engine {
	return newGatewayRoutesTestRouterWith(&config.Config{Gateway: config.GatewayConfig{MaxBodySize: 1 << 20}}, service.PlatformOpenAI)
}

func newGatewayRoutesTestRouterWith(cfg *config.Config, platform string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if cfg == nil {
		cfg = &config.Config{Gateway: config.GatewayConfig{MaxBodySize: 1 << 20}}
	}

	RegisterGatewayRoutes(
		router,
		&handler.Handlers{
			Gateway:       &handler.GatewayHandler{},
			OpenAIGateway: &handler.OpenAIGatewayHandler{},
		},
		servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
			groupID := int64(1)
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
				GroupID: &groupID,
				Group:   &service.Group{ID: groupID, Platform: platform, AllowMessagesDispatch: true},
			})
			c.Next()
		}),
		nil,
		nil,
		nil,
		nil,
		cfg,
	)

	return router
}

func requireRouteRegistered(t *testing.T, router *gin.Engine, method, path string) {
	t.Helper()
	for _, route := range router.Routes() {
		if route.Method == method && route.Path == path {
			return
		}
	}
	t.Fatalf("route %s %s is not registered", method, path)
}

func requireRouteNotRegistered(t *testing.T, router *gin.Engine, method, path string) {
	t.Helper()
	for _, route := range router.Routes() {
		if route.Method == method && route.Path == path {
			t.Fatalf("route %s %s should not be registered", method, path)
		}
	}
}

func TestGatewayRoutesOpenAIResponsesCompactPathIsRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter()

	for _, path := range []string{
		"/v1/responses/compact",
		"/responses/compact",
		"/backend-api/codex/responses",
		"/backend-api/codex/responses/compact",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-5"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should hit OpenAI responses handler", path)
	}
}

func TestGatewayRoutesOpenAIImagesPathsAreRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter()

	for _, path := range []string{
		"/v1/images/generations",
		"/v1/images/edits",
		"/images/generations",
		"/images/edits",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-image-2","prompt":"draw a cat"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should hit OpenAI images handler", path)
	}
}
func TestGatewayRoutesKiroDedicatedRoutesAreDisabledByDefault(t *testing.T) {
	router := newGatewayRoutesTestRouter()

	requireRouteNotRegistered(t, router, http.MethodGet, "/kiro/v1/models")
	requireRouteNotRegistered(t, router, http.MethodPost, "/kiro/v1/messages")
}

func TestGatewayRoutesKiroDedicatedRoutesRequireKiroGroup(t *testing.T) {
	cfg := &config.Config{
		Gateway: config.GatewayConfig{MaxBodySize: 1 << 20},
		Kiro: config.KiroConfig{
			Enabled:               true,
			RouteEnabled:          true,
			MaxConcurrency:        1,
			RequestTimeoutSeconds: 90,
		},
	}
	router := newGatewayRoutesTestRouterWith(cfg, service.PlatformOpenAI)

	requireRouteRegistered(t, router, http.MethodGet, "/kiro/v1/models")

	req := httptest.NewRequest(http.MethodGet, "/kiro/v1/models", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "kiro group")
}

func TestGatewayRoutesKiroDedicatedRoutesReportMissingSidecar(t *testing.T) {
	cfg := &config.Config{
		Gateway: config.GatewayConfig{MaxBodySize: 1 << 20},
		Kiro: config.KiroConfig{
			Enabled:               true,
			RouteEnabled:          true,
			MaxConcurrency:        1,
			RequestTimeoutSeconds: 90,
		},
	}
	router := newGatewayRoutesTestRouterWith(cfg, service.PlatformKiro)

	req := httptest.NewRequest(http.MethodGet, "/kiro/v1/models", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "Kiro sidecar is not configured")
}

func TestGatewayRoutesKiroGroupDoesNotFallThroughSharedV1(t *testing.T) {
	router := newGatewayRoutesTestRouterWith(
		&config.Config{Gateway: config.GatewayConfig{MaxBodySize: 1 << 20}},
		service.PlatformKiro,
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"kiro"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "shared /v1")
}

func TestGatewayRoutesKiroGroupDoesNotFallThroughSharedResponsesWebSocket(t *testing.T) {
	router := newGatewayRoutesTestRouterWith(
		&config.Config{Gateway: config.GatewayConfig{MaxBodySize: 1 << 20}},
		service.PlatformKiro,
	)

	req := httptest.NewRequest(http.MethodGet, "/responses", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "shared /v1")
}
