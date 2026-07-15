package setup

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestSetupRoutesRejectOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("DATA_DIR", t.TempDir())
	router := gin.New()
	RegisterRoutes(router)

	body := `{"padding":"` + strings.Repeat("x", setupRequestBodyMaxBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/setup/install", strings.NewReader(body))
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s, want 413", recorder.Code, recorder.Body.String())
	}
}

func TestSetupRoutesRejectCrossOriginPlainTextAndReboundHost(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("DATA_DIR", t.TempDir())
	router := gin.New()
	RegisterRoutes(router)
	body := `{"admin":{"email":"attacker@example.com","password":"attacker-password"}}`

	tests := []struct {
		name        string
		host        string
		contentType string
		origin      string
		want        int
	}{
		{name: "cross origin", host: "127.0.0.1:8080", contentType: "application/json", origin: "https://evil.example", want: http.StatusForbidden},
		{name: "simple content type", host: "127.0.0.1:8080", contentType: "text/plain", origin: "http://127.0.0.1:8080", want: http.StatusUnsupportedMediaType},
		{name: "dns rebinding host", host: "evil.example:8080", contentType: "application/json", origin: "http://evil.example:8080", want: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/setup/install", strings.NewReader(body))
			req.Host = tt.host
			req.Header.Set("Content-Type", tt.contentType)
			req.Header.Set("Origin", tt.origin)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code != tt.want {
				t.Fatalf("status=%d body=%s, want %d", recorder.Code, recorder.Body.String(), tt.want)
			}
		})
	}
}

func TestSetupRateLimiterRejectsBurst(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(newSetupRateLimiter(2, time.Minute).middleware())
	router.POST("/setup", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	for i, want := range []int{http.StatusNoContent, http.StatusNoContent, http.StatusTooManyRequests} {
		req := httptest.NewRequest(http.MethodPost, "/setup", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		if recorder.Code != want {
			t.Fatalf("request %d status=%d, want %d", i+1, recorder.Code, want)
		}
	}
}
