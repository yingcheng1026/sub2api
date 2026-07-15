package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBrowserSessionTokenPairUsesHttpOnlyRefreshCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	ctx.Request.Header.Set(browserSessionHeader, browserSessionHeaderValue)

	handler := &AuthHandler{cfg: &config.Config{JWT: config.JWTConfig{RefreshTokenExpireDays: 7}}}
	payload, err := handler.browserSessionTokenPairPayload(ctx, &service.TokenPair{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ExpiresIn:    3600,
	})
	require.NoError(t, err)
	require.Equal(t, "access-token", payload["access_token"])
	require.NotContains(t, payload, "refresh_token")
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))

	refreshCookie := findCookie(recorder.Result().Cookies(), browserRefreshCookieName)
	require.NotNil(t, refreshCookie)
	require.True(t, refreshCookie.HttpOnly)
	require.Equal(t, browserRefreshCookiePath, refreshCookie.Path)
	require.Equal(t, http.SameSiteStrictMode, refreshCookie.SameSite)
	require.Equal(t, 7*24*60*60, refreshCookie.MaxAge)

	csrfCookie := findCookie(recorder.Result().Cookies(), browserCSRFCookieName)
	require.NotNil(t, csrfCookie)
	require.False(t, csrfCookie.HttpOnly)
	require.Equal(t, "/", csrfCookie.Path)
	require.NotEmpty(t, csrfCookie.Value)
}

func TestNonBrowserTokenPairKeepsRefreshTokenInJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)

	payload, err := (&AuthHandler{}).browserSessionTokenPairPayload(ctx, &service.TokenPair{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ExpiresIn:    3600,
	})
	require.NoError(t, err)
	require.Equal(t, "refresh-token", payload["refresh_token"])
	require.Empty(t, recorder.Result().Cookies())
}

func TestBrowserSessionCSRFRejectsMissingOrMismatchedHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		header string
	}{
		{name: "missing"},
		{name: "mismatch", header: "wrong"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
			ctx.Request.Header.Set(browserSessionHeader, browserSessionHeaderValue)
			ctx.Request.Header.Set(browserCSRFHeader, tc.header)
			ctx.Request.AddCookie(&http.Cookie{Name: browserCSRFCookieName, Value: "expected"})

			err := validateBrowserSessionCSRF(ctx)
			require.Error(t, err)
		})
	}
}

func TestBrowserSessionCSRFAcceptsMatchingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	ctx.Request.Header.Set(browserSessionHeader, browserSessionHeaderValue)
	ctx.Request.Header.Set(browserCSRFHeader, "expected")
	ctx.Request.AddCookie(&http.Cookie{Name: browserCSRFCookieName, Value: "expected"})

	require.NoError(t, validateBrowserSessionCSRF(ctx))
}
