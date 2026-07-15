package routes

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

const (
	userUsageQueryRequestsPerMinute  = 120
	adminUsageQueryRequestsPerMinute = 120
)

func authenticatedUsageRateLimitKey(c *gin.Context) string {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		return ""
	}
	return "user-" + strconv.FormatInt(subject.UserID, 10)
}
