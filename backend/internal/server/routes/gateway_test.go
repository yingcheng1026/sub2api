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
	gin.SetMode(gin.TestMode)
	router := gin.New()

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
				Group:   &service.Group{Platform: service.PlatformOpenAI, AllowMessagesDispatch: true},
			})
			c.Next()
		}),
		nil,
		nil,
		nil,
		nil,
		&config.Config{Gateway: config.GatewayConfig{MaxBodySize: 1 << 20}},
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

func TestGatewayRoutesOpenAIResponsesCompactPathIsRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter()

	for _, path := range []string{
		"/v1/responses/compact",
		"/openai/v1/responses/compact",
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
		"/openai/v1/images/generations",
		"/openai/v1/images/edits",
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

func TestGatewayRoutesPlatformAliasesAreRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter()

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/anthropic/v1/messages"},
		{http.MethodPost, "/anthropic/v1/messages/count_tokens"},
		{http.MethodGet, "/anthropic/v1/models"},
		{http.MethodGet, "/anthropic/v1/usage"},
		{http.MethodGet, "/openai/v1/models"},
		{http.MethodPost, "/openai/v1/chat/completions"},
		{http.MethodPost, "/openai/v1/responses"},
		{http.MethodPost, "/openai/v1/responses/*subpath"},
		{http.MethodGet, "/openai/v1/responses"},
		{http.MethodPost, "/openai/v1/images/generations"},
		{http.MethodPost, "/openai/v1/images/edits"},
	} {
		requireRouteRegistered(t, router, route.method, route.path)
	}
}

func TestGatewayRoutesOpenAIGroupCountTokensReturnsEstimate(t *testing.T) {
	router := newGatewayRoutesTestRouter()

	for _, path := range []string{
		"/v1/messages/count_tokens",
		"/anthropic/v1/messages/count_tokens",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{
			"model":"claude-sonnet-4-5-20250929",
			"messages":[{"role":"user","content":"hello from claude code"}]
		}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code, "path=%s should return a local estimate", path)
		require.Contains(t, w.Body.String(), "input_tokens")
	}
}
