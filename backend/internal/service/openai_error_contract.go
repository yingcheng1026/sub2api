package service

import "github.com/gin-gonic/gin"

// writeOpenAIContractError keeps local policy and request-validation errors
// structurally identical to the OpenAI-compatible gateway error envelope.
func writeOpenAIContractError(c *gin.Context, status int, errType, code, param, message string) {
	if c == nil {
		return
	}
	var codeValue any
	if code != "" {
		codeValue = code
	}
	var paramValue any
	if param != "" {
		paramValue = param
	}
	c.JSON(status, gin.H{"error": gin.H{
		"message": message,
		"type":    errType,
		"param":   paramValue,
		"code":    codeValue,
	}})
}

func writeOpenAIOrXAIContractError(c *gin.Context, account *Account, status int, errType, code, param, message string) {
	if account != nil && account.Platform == PlatformGrok {
		writeXAIContractError(c, status, message)
		return
	}
	writeOpenAIContractError(c, status, errType, code, param, message)
}
