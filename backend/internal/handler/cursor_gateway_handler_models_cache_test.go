package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCursorModelsUsesFreshCacheAndStaleOnSidecarFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cursorModelsListCache.resetForTest()
	t.Cleanup(func() { cursorModelsListCache.resetForTest() })
	t.Setenv("CURSOR_MODELS_CACHE_TTL_SECONDS", "1")
	t.Setenv("CURSOR_MODELS_STALE_TTL_SECONDS", "60")
	t.Setenv("CURSOR_SIDECAR_URL", "")
	t.Setenv("CURSOR_SIDECAR_API_KEY", "")

	calls := 0
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/v1/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"cursor-live","object":"model"}]}`))
			return
		}
		http.Error(w, `{"error":"sidecar down"}`, http.StatusServiceUnavailable)
	}))
	defer sidecar.Close()

	h := &GatewayHandler{cfg: &config.Config{Cursor: config.CursorConfig{
		SidecarURL:            sidecar.URL,
		SidecarAPIKey:         "sidecar-key",
		RequestTimeoutSeconds: 3,
	}}}

	first := performCursorModelsRequest(h)
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "miss", first.Header().Get(cursorModelsCacheStatusHeader))
	require.Contains(t, first.Body.String(), "cursor-live")
	require.Equal(t, 1, calls)

	second := performCursorModelsRequest(h)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "hit", second.Header().Get(cursorModelsCacheStatusHeader))
	require.Contains(t, second.Body.String(), "cursor-live")
	require.Equal(t, 1, calls)

	time.Sleep(1100 * time.Millisecond)
	third := performCursorModelsRequest(h)
	require.Equal(t, http.StatusOK, third.Code)
	require.Equal(t, "stale", third.Header().Get(cursorModelsCacheStatusHeader))
	require.Contains(t, third.Body.String(), "cursor-live")
	require.Equal(t, 2, calls)
}

func performCursorModelsRequest(h *GatewayHandler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/cursor/v1/models", nil)
	h.CursorModels(c)
	return rec
}
