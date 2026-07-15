package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestUsageQueryRoutesFailCloseWhenRedisUnavailable(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = rdb.Close() })

	auth := func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 42})
		c.Next()
	}

	userRouter := gin.New()
	RegisterUserRoutes(
		userRouter.Group("/api/v1"),
		&handler.Handlers{Usage: &handler.UsageHandler{}},
		servermiddleware.JWTAuthMiddleware(auth),
		nil,
		rdb,
	)
	userRequest := httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)
	userRecorder := httptest.NewRecorder()
	userRouter.ServeHTTP(userRecorder, userRequest)
	require.Equal(t, http.StatusTooManyRequests, userRecorder.Code)

	adminRouter := gin.New()
	RegisterAdminRoutes(
		adminRouter.Group("/api/v1"),
		&handler.Handlers{Admin: &handler.AdminHandlers{Usage: &adminhandler.UsageHandler{}}},
		servermiddleware.AdminAuthMiddleware(auth),
		rdb,
	)
	adminRequest := httptest.NewRequest(http.MethodGet, "/api/v1/admin/usage", nil)
	adminRecorder := httptest.NewRecorder()
	adminRouter.ServeHTTP(adminRecorder, adminRequest)
	require.Equal(t, http.StatusTooManyRequests, adminRecorder.Code)
}
