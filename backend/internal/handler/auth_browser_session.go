package handler

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	browserSessionHeader      = "X-Sub2API-Browser-Session"
	browserSessionHeaderValue = "1"
	browserCSRFHeader         = "X-CSRF-Token"
	browserRefreshCookieName  = "sub2api_refresh"
	browserRefreshCookiePath  = "/api/v1/auth"
	browserCSRFCookieName     = "sub2api_csrf"
	defaultRefreshCookieDays  = 30
)

func isBrowserSessionRequest(c *gin.Context) bool {
	return c != nil && strings.TrimSpace(c.GetHeader(browserSessionHeader)) == browserSessionHeaderValue
}

func (h *AuthHandler) browserSessionTokenPairPayload(c *gin.Context, tokenPair *service.TokenPair) (gin.H, error) {
	if tokenPair == nil {
		return nil, errors.New("token pair is required")
	}
	payload := gin.H{
		"access_token":  tokenPair.AccessToken,
		"refresh_token": tokenPair.RefreshToken,
		"expires_in":    tokenPair.ExpiresIn,
		"token_type":    "Bearer",
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	if !isBrowserSessionRequest(c) {
		return payload, nil
	}
	if err := h.setBrowserSessionCookies(c, tokenPair.RefreshToken); err != nil {
		return nil, err
	}
	delete(payload, "refresh_token")
	return payload, nil
}

func (h *AuthHandler) setBrowserSessionCookies(c *gin.Context, refreshToken string) error {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return errors.New("refresh token is required")
	}
	csrfBytes := make([]byte, 32)
	if _, err := rand.Read(csrfBytes); err != nil {
		return err
	}
	csrfToken := base64.RawURLEncoding.EncodeToString(csrfBytes)
	secure := isRequestHTTPS(c)
	maxAge := defaultRefreshCookieDays * 24 * 60 * 60
	if h != nil && h.cfg != nil && h.cfg.JWT.RefreshTokenExpireDays > 0 {
		maxAge = h.cfg.JWT.RefreshTokenExpireDays * 24 * 60 * 60
	}

	http.SetCookie(c.Writer, &http.Cookie{
		Name:     browserRefreshCookieName,
		Value:    encodeCookieValue(refreshToken),
		Path:     browserRefreshCookiePath,
		MaxAge:   maxAge,
		Expires:  time.Now().Add(time.Duration(maxAge) * time.Second),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     browserCSRFCookieName,
		Value:    csrfToken,
		Path:     "/",
		MaxAge:   maxAge,
		Expires:  time.Now().Add(time.Duration(maxAge) * time.Second),
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func readBrowserRefreshToken(c *gin.Context) (string, error) {
	cookie, err := c.Request.Cookie(browserRefreshCookieName)
	if err != nil {
		return "", err
	}
	refreshToken, err := decodeCookieValue(cookie.Value)
	if err != nil || strings.TrimSpace(refreshToken) == "" {
		return "", errors.New("browser refresh cookie is invalid")
	}
	return refreshToken, nil
}

func validateBrowserSessionCSRF(c *gin.Context) error {
	cookie, err := c.Request.Cookie(browserCSRFCookieName)
	if err != nil {
		return infraerrors.Forbidden("CSRF_TOKEN_INVALID", "CSRF token is missing or invalid")
	}
	headerValue := strings.TrimSpace(c.GetHeader(browserCSRFHeader))
	cookieValue := strings.TrimSpace(cookie.Value)
	if headerValue == "" || cookieValue == "" || len(headerValue) != len(cookieValue) ||
		subtle.ConstantTimeCompare([]byte(headerValue), []byte(cookieValue)) != 1 {
		return infraerrors.Forbidden("CSRF_TOKEN_INVALID", "CSRF token is missing or invalid")
	}
	return nil
}

func clearBrowserSessionCookies(c *gin.Context) {
	secure := isRequestHTTPS(c)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     browserRefreshCookieName,
		Value:    "",
		Path:     browserRefreshCookiePath,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     browserCSRFCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}
