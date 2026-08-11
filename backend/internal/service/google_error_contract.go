package service

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	"github.com/gin-gonic/gin"
)

func writeGoogleContractError(c *gin.Context, status int, code, message string) {
	if c == nil {
		return
	}
	status, payload := googleContractErrorPayload(status, code, message)
	c.JSON(status, payload)
}

func googleContractErrorPayload(status int, code, message string) (int, gin.H) {
	status, googleStatus := googleContractClassification(status, code)
	errObject := gin.H{
		"code":    status,
		"message": message,
		"status":  googleStatus,
	}
	if code == "INVALID_API_KEY" {
		errObject["details"] = []gin.H{{
			"@type":  "type.googleapis.com/google.rpc.ErrorInfo",
			"reason": "API_KEY_INVALID",
			"domain": "googleapis.com",
		}}
	}
	return status, gin.H{"error": errObject}
}

func googleContractClassification(status int, code string) (int, string) {
	switch code {
	case "API_KEY_REQUIRED":
		return http.StatusForbidden, "PERMISSION_DENIED"
	case "INVALID_API_KEY":
		return http.StatusBadRequest, "INVALID_ARGUMENT"
	default:
		return status, googleapi.HTTPStatusToGoogleStatus(status)
	}
}
