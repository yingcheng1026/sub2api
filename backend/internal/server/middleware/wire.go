package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/wire"
)

// JWTAuthMiddleware JWT 认证中间件类型
type JWTAuthMiddleware gin.HandlerFunc

// AdminAuthMiddleware 管理员认证中间件类型
type AdminAuthMiddleware gin.HandlerFunc

// APIKeyAuthMiddleware API Key 认证中间件类型
type APIKeyAuthMiddleware gin.HandlerFunc

func ProvideAPIKeyAuthMiddleware(
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	modelRouter service.ModelRouter,
	groupRepo service.GroupRepository,
	cfg *config.Config,
) APIKeyAuthMiddleware {
	return NewAPIKeyAuthMiddlewareWithRouter(apiKeyService, subscriptionService, modelRouter, groupRepo, cfg)
}

// ProviderSet 中间件层的依赖注入
var ProviderSet = wire.NewSet(
	NewJWTAuthMiddleware,
	NewAdminAuthMiddleware,
	ProvideAPIKeyAuthMiddleware,
)
