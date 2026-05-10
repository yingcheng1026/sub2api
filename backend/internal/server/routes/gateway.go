package routes

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterGatewayRoutes 注册 API 网关路由（Claude/OpenAI/Gemini 兼容）
func RegisterGatewayRoutes(
	r *gin.Engine,
	h *handler.Handlers,
	apiKeyAuth middleware.APIKeyAuthMiddleware,
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	opsService *service.OpsService,
	settingService *service.SettingService,
	cfg *config.Config,
) {
	bodyLimit := middleware.RequestBodyLimit(cfg.Gateway.MaxBodySize)
	clientRequestID := middleware.ClientRequestID()
	opsErrorLogger := handler.OpsErrorLoggerMiddleware(opsService)
	endpointNorm := handler.InboundEndpointMiddleware()

	// 未分组 Key 拦截中间件（按协议格式区分错误响应）
	requireGroupAnthropic := middleware.RequireGroupAssignment(settingService, middleware.AnthropicErrorWriter)
	requireGroupGoogle := middleware.RequireGroupAssignment(settingService, middleware.GoogleErrorWriter)

	anthropicMessagesHandler := func(c *gin.Context) {
		if rejectKiroAutoRoute(c, cfg) {
			return
		}
		if getGroupPlatform(c) == service.PlatformOpenAI {
			h.OpenAIGateway.Messages(c)
			return
		}
		h.Gateway.Messages(c)
	}
	anthropicCountTokensHandler := func(c *gin.Context) {
		if rejectKiroAutoRoute(c, cfg) {
			return
		}
		if getGroupPlatform(c) == service.PlatformOpenAI {
			c.JSON(http.StatusNotFound, gin.H{
				"type": "error",
				"error": gin.H{
					"type":    "not_found_error",
					"message": "Token counting is not supported for this platform",
				},
			})
			return
		}
		h.Gateway.CountTokens(c)
	}
	responsesHandler := func(c *gin.Context) {
		if rejectKiroAutoRoute(c, cfg) {
			return
		}
		if getGroupPlatform(c) == service.PlatformOpenAI {
			h.OpenAIGateway.Responses(c)
			return
		}
		h.Gateway.Responses(c)
	}
	responsesWebSocketHandler := func(c *gin.Context) {
		if rejectKiroAutoRoute(c, cfg) {
			return
		}
		h.OpenAIGateway.ResponsesWebSocket(c)
	}
	chatCompletionsHandler := func(c *gin.Context) {
		if rejectKiroAutoRoute(c, cfg) {
			return
		}
		if getGroupPlatform(c) == service.PlatformOpenAI {
			h.OpenAIGateway.ChatCompletions(c)
			return
		}
		h.Gateway.ChatCompletions(c)
	}
	imagesHandler := func(c *gin.Context) {
		if rejectKiroAutoRoute(c, cfg) {
			return
		}
		if getGroupPlatform(c) != service.PlatformOpenAI {
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "not_found_error",
					"message": "Images API is not supported for this platform",
				},
			})
			return
		}
		h.OpenAIGateway.Images(c)
	}
	modelsHandler := func(c *gin.Context) {
		if rejectKiroAutoRoute(c, cfg) {
			return
		}
		h.Gateway.Models(c)
	}
	usageHandler := func(c *gin.Context) {
		if rejectKiroAutoRoute(c, cfg) {
			return
		}
		h.Gateway.Usage(c)
	}

	// API网关（Claude API兼容）
	gateway := r.Group("/v1")
	gateway.Use(bodyLimit)
	gateway.Use(clientRequestID)
	gateway.Use(opsErrorLogger)
	gateway.Use(endpointNorm)
	gateway.Use(gin.HandlerFunc(apiKeyAuth))
	gateway.Use(requireGroupAnthropic)
	{
		// /v1/messages: auto-route based on group platform
		gateway.POST("/messages", anthropicMessagesHandler)
		gateway.POST("/messages/count_tokens", anthropicCountTokensHandler)
		gateway.GET("/models", modelsHandler)
		gateway.GET("/usage", usageHandler)
		// OpenAI Responses API: auto-route based on group platform
		gateway.POST("/responses", responsesHandler)
		gateway.POST("/responses/*subpath", responsesHandler)
		gateway.GET("/responses", responsesWebSocketHandler)
		// OpenAI Chat Completions API: auto-route based on group platform
		gateway.POST("/chat/completions", chatCompletionsHandler)
		gateway.POST("/images/generations", imagesHandler)
		gateway.POST("/images/edits", imagesHandler)
	}

	// Gemini 原生 API 兼容层（Gemini SDK/CLI 直连）
	gemini := r.Group("/v1beta")
	gemini.Use(bodyLimit)
	gemini.Use(clientRequestID)
	gemini.Use(opsErrorLogger)
	gemini.Use(endpointNorm)
	gemini.Use(middleware.APIKeyAuthWithSubscriptionGoogle(apiKeyService, subscriptionService, cfg))
	gemini.Use(requireGroupGoogle)
	{
		gemini.GET("/models", h.Gateway.GeminiV1BetaListModels)
		gemini.GET("/models/:model", h.Gateway.GeminiV1BetaGetModel)
		// Gin treats ":" as a param marker, but Gemini uses "{model}:{action}" in the same segment.
		gemini.POST("/models/*modelAction", h.Gateway.GeminiV1BetaModels)
	}

	r.POST("/responses", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, responsesHandler)
	r.POST("/responses/*subpath", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, responsesHandler)
	r.GET("/responses", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, responsesWebSocketHandler)
	codexDirect := r.Group("/backend-api/codex")
	codexDirect.Use(bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic)
	{
		codexDirect.POST("/responses", responsesHandler)
		codexDirect.POST("/responses/*subpath", responsesHandler)
		codexDirect.GET("/responses", responsesWebSocketHandler)
	}
	// OpenAI Chat Completions API（不带v1前缀的别名）— auto-route based on group platform
	r.POST("/chat/completions", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, chatCompletionsHandler)
	r.POST("/images/generations", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, imagesHandler)
	r.POST("/images/edits", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, imagesHandler)

	// Antigravity 模型列表
	r.GET("/antigravity/models", gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.Gateway.AntigravityModels)

	// Antigravity 专用路由（仅使用 antigravity 账户，不混合调度）
	antigravityV1 := r.Group("/antigravity/v1")
	antigravityV1.Use(bodyLimit)
	antigravityV1.Use(clientRequestID)
	antigravityV1.Use(opsErrorLogger)
	antigravityV1.Use(endpointNorm)
	antigravityV1.Use(middleware.ForcePlatform(service.PlatformAntigravity))
	antigravityV1.Use(gin.HandlerFunc(apiKeyAuth))
	antigravityV1.Use(requireGroupAnthropic)
	{
		antigravityV1.POST("/messages", h.Gateway.Messages)
		antigravityV1.POST("/messages/count_tokens", h.Gateway.CountTokens)
		antigravityV1.GET("/models", h.Gateway.AntigravityModels)
		antigravityV1.GET("/usage", h.Gateway.Usage)
	}

	if isKiroRouteEnabled(cfg) {
		kiroV1 := r.Group("/kiro/v1")
		kiroV1.Use(bodyLimit)
		kiroV1.Use(clientRequestID)
		kiroV1.Use(opsErrorLogger)
		kiroV1.Use(endpointNorm)
		kiroV1.Use(gin.HandlerFunc(apiKeyAuth))
		kiroV1.Use(requireGroupAnthropic)
		kiroV1.Use(requireKiroGroup())
		{
			kiroV1.GET("/models", kiroBridgeUnavailableHandler(cfg))
			kiroV1.POST("/messages", kiroBridgeUnavailableHandler(cfg))
			kiroV1.POST("/responses", kiroBridgeUnavailableHandler(cfg))
			kiroV1.POST("/chat/completions", kiroBridgeUnavailableHandler(cfg))
		}
	}

	antigravityV1Beta := r.Group("/antigravity/v1beta")
	antigravityV1Beta.Use(bodyLimit)
	antigravityV1Beta.Use(clientRequestID)
	antigravityV1Beta.Use(opsErrorLogger)
	antigravityV1Beta.Use(endpointNorm)
	antigravityV1Beta.Use(middleware.ForcePlatform(service.PlatformAntigravity))
	antigravityV1Beta.Use(middleware.APIKeyAuthWithSubscriptionGoogle(apiKeyService, subscriptionService, cfg))
	antigravityV1Beta.Use(requireGroupGoogle)
	{
		antigravityV1Beta.GET("/models", h.Gateway.GeminiV1BetaListModels)
		antigravityV1Beta.GET("/models/:model", h.Gateway.GeminiV1BetaGetModel)
		antigravityV1Beta.POST("/models/*modelAction", h.Gateway.GeminiV1BetaModels)
	}

}

// getGroupPlatform extracts the group platform from the API Key stored in context.
func getGroupPlatform(c *gin.Context) string {
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil {
		return ""
	}
	return apiKey.Group.Platform
}

func isKiroRouteEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.Kiro.Enabled && cfg.Kiro.RouteEnabled
}

func rejectKiroAutoRoute(c *gin.Context, cfg *config.Config) bool {
	if getGroupPlatform(c) != service.PlatformKiro {
		return false
	}
	if cfg != nil && cfg.Kiro.Enabled && cfg.Kiro.AutoRouteOnV1 {
		writeKiroRouteError(
			c,
			http.StatusNotImplemented,
			"api_error",
			"Kiro /v1 auto routing is not implemented in this build; use /kiro/v1 after the sidecar bridge lands",
		)
		return true
	}
	writeKiroRouteError(
		c,
		http.StatusNotFound,
		"not_found_error",
		"Kiro routing is disabled on the shared /v1 surface",
	)
	return true
}

func requireKiroGroup() gin.HandlerFunc {
	return func(c *gin.Context) {
		if getGroupPlatform(c) == service.PlatformKiro {
			c.Next()
			return
		}
		writeKiroRouteError(
			c,
			http.StatusForbidden,
			"authentication_error",
			"Kiro endpoint requires an API key assigned to a kiro group",
		)
		c.Abort()
	}
}

func kiroBridgeUnavailableHandler(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg == nil || cfg.Kiro.SidecarURL == "" {
			writeKiroRouteError(
				c,
				http.StatusServiceUnavailable,
				"api_error",
				"Kiro sidecar is not configured",
			)
			return
		}
		writeKiroRouteError(
			c,
			http.StatusNotImplemented,
			"api_error",
			"Kiro sidecar bridge is not implemented in this build",
		)
	}
}

func writeKiroRouteError(c *gin.Context, status int, errType string, message string) {
	c.JSON(status, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}
