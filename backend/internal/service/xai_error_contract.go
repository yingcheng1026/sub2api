package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func writeXAIContractError(c *gin.Context, status int, message string) {
	if c == nil {
		return
	}
	code := "api-error"
	switch status {
	case http.StatusBadRequest:
		code = "invalid-request"
	case http.StatusUnauthorized:
		code = "unauthenticated"
	case http.StatusForbidden:
		code = "permission-denied"
	case http.StatusNotFound:
		code = "not-found"
	case http.StatusTooManyRequests:
		code = "rate-limit-exceeded"
	}
	c.JSON(status, gin.H{"code": code, "error": strings.TrimSpace(message)})
}
