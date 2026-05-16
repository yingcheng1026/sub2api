package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// NewAPIKeyAuthMiddleware 创建 API Key 认证中间件
func NewAPIKeyAuthMiddleware(apiKeyService *service.APIKeyService, subscriptionService *service.SubscriptionService, cfg *config.Config) APIKeyAuthMiddleware {
	return NewAPIKeyAuthMiddlewareWithRouter(apiKeyService, subscriptionService, nil, nil, cfg)
}

type apiKeyAuthGroupGetter interface {
	GetByID(ctx context.Context, id int64) (*service.Group, error)
}

// NewAPIKeyAuthMiddlewareWithRouter 创建支持钱包通用 Key 动态分组路由的认证中间件。
func NewAPIKeyAuthMiddlewareWithRouter(
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	modelRouter service.ModelRouter,
	groupGetter apiKeyAuthGroupGetter,
	cfg *config.Config,
) APIKeyAuthMiddleware {
	return APIKeyAuthMiddleware(apiKeyAuthWithSubscription(apiKeyService, subscriptionService, modelRouter, groupGetter, cfg))
}

// apiKeyAuthWithSubscription API Key认证中间件（支持订阅验证）
//
// 中间件职责分为两层：
//   - 鉴权（Authentication）：验证 Key 有效性、用户状态、IP 限制 —— 始终执行
//   - 计费执行（Billing Enforcement）：过期/配额/订阅/余额检查 —— skipBilling 时整块跳过
//
// /v1/usage 端点只需鉴权，不需要计费执行（允许过期/配额耗尽的 Key 查询自身用量）。
func apiKeyAuthWithSubscription(
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	modelRouter service.ModelRouter,
	groupGetter apiKeyAuthGroupGetter,
	cfg *config.Config,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		// ── 1. 提取 API Key ──────────────────────────────────────────

		queryKey := strings.TrimSpace(c.Query("key"))
		queryApiKey := strings.TrimSpace(c.Query("api_key"))
		if queryKey != "" || queryApiKey != "" {
			AbortWithError(c, 400, "api_key_in_query_deprecated", "API key in query parameter is deprecated. Please use Authorization header instead.")
			return
		}

		// 尝试从Authorization header中提取API key (Bearer scheme)
		authHeader := c.GetHeader("Authorization")
		var apiKeyString string

		if authHeader != "" {
			// 验证Bearer scheme
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				apiKeyString = strings.TrimSpace(parts[1])
			}
		}

		// 如果Authorization header中没有，尝试从x-api-key header中提取
		if apiKeyString == "" {
			apiKeyString = c.GetHeader("x-api-key")
		}

		// 如果x-api-key header中没有，尝试从x-goog-api-key header中提取（Gemini CLI兼容）
		if apiKeyString == "" {
			apiKeyString = c.GetHeader("x-goog-api-key")
		}

		// 如果所有header都没有API key
		if apiKeyString == "" {
			AbortWithError(c, 401, "API_KEY_REQUIRED", "API key is required in Authorization header (Bearer scheme), x-api-key header, or x-goog-api-key header")
			return
		}

		// ── 2. 验证 Key 存在 ─────────────────────────────────────────

		apiKey, err := apiKeyService.GetByKey(c.Request.Context(), apiKeyString)
		if err != nil {
			if errors.Is(err, service.ErrAPIKeyNotFound) {
				AbortWithError(c, 401, "INVALID_API_KEY", "Invalid API key")
				return
			}
			AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to validate API key")
			return
		}

		// ── 3. 基础鉴权（始终执行） ─────────────────────────────────

		// disabled / 未知状态 → 无条件拦截（expired 和 quota_exhausted 留给计费阶段）
		if !apiKey.IsActive() &&
			apiKey.Status != service.StatusAPIKeyExpired &&
			apiKey.Status != service.StatusAPIKeyQuotaExhausted {
			AbortWithError(c, 401, "API_KEY_DISABLED", "API key is disabled")
			return
		}

		// 检查 IP 限制（白名单/黑名单）
		// 注意：错误信息故意模糊，避免暴露具体的 IP 限制机制
		if len(apiKey.IPWhitelist) > 0 || len(apiKey.IPBlacklist) > 0 {
			clientIP := ip.GetTrustedClientIP(c)
			allowed, _ := ip.CheckIPRestrictionWithCompiledRules(clientIP, apiKey.CompiledIPWhitelist, apiKey.CompiledIPBlacklist)
			if !allowed {
				AbortWithError(c, 403, "ACCESS_DENIED", "Access denied")
				return
			}
		}

		// 检查关联的用户
		if apiKey.User == nil {
			AbortWithError(c, 401, "USER_NOT_FOUND", "User associated with API key not found")
			return
		}

		// 检查用户状态
		if !apiKey.User.IsActive() {
			AbortWithError(c, 401, "USER_INACTIVE", "User account is not active")
			return
		}

		// ── 4. SimpleMode → early return ─────────────────────────────

		if cfg.RunMode == config.RunModeSimple {
			c.Set(string(ContextKeyAPIKey), apiKey)
			c.Set(string(ContextKeyUser), AuthSubject{
				UserID:      apiKey.User.ID,
				Concurrency: apiKey.User.Concurrency,
			})
			c.Set(string(ContextKeyUserRole), apiKey.User.Role)
			setGroupContext(c, apiKey.Group)
			_ = apiKeyService.TouchLastUsed(c.Request.Context(), apiKey.ID)
			c.Next()
			return
		}

		// ── 5. 加载订阅（订阅模式时始终加载） ───────────────────────

		// skipBilling: /v1/usage 只需鉴权，跳过所有计费执行
		skipBilling := c.Request.URL.Path == "/v1/usage"

		var subscription *service.UserSubscription
		var walletSub *service.UserSubscription
		var routedGroup *service.Group
		isSubscriptionType := apiKey.Group != nil && apiKey.Group.IsSubscriptionType()

		// 钱包模式 (v4) 优先：钱包订阅独立于 api_key.group_id，只要用户持有
		// active 钱包订阅，所有 group（包括 standard 类型）都走钱包扣费。
		// 命中后跳过 (user, group) 老路径，由 BillingCacheService 走钱包预检。
		if subscriptionService != nil {
			foundWalletSub, walletErr := subscriptionService.GetActiveWalletSubscription(
				c.Request.Context(),
				apiKey.User.ID,
			)
			if walletErr == nil && foundWalletSub != nil {
				walletSub = foundWalletSub
				subscription = walletSub
			}
		}

		if walletSub != nil && apiKey.GroupID == nil {
			modelName, extractErr := extractModelFromRequest(c)
			if extractErr != nil {
				AbortWithError(c, 400, "model_unsupported", "该模型未启用，请联系客服")
				return
			}
			if modelRouter == nil || groupGetter == nil {
				AbortWithError(c, 500, "INTERNAL_ERROR", "Model router is not configured")
				return
			}
			groupID, routeErr := modelRouter.ResolveGroupID(c.Request.Context(), apiKey.User.ID, modelName)
			if routeErr != nil {
				if errors.Is(routeErr, service.ErrModelUnsupported) {
					AbortWithError(c, 400, "model_unsupported", "该模型未启用，请联系客服")
					return
				}
				AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to route model")
				return
			}
			group, groupErr := groupGetter.GetByID(c.Request.Context(), groupID)
			if groupErr != nil || !service.IsGroupContextValid(group) {
				AbortWithError(c, 400, "model_unsupported", "该模型未启用，请联系客服")
				return
			}
			routedGroup = group
			effectiveGroupID := group.ID
			apiKeyCopy := *apiKey
			apiKeyCopy.GroupID = &effectiveGroupID
			apiKeyCopy.Group = group
			apiKey = &apiKeyCopy
		}

		// 钱包未命中 → 月卡查找：exact match + plan_groups 间接覆盖。
		// 钱包命中时跳过：钱包订阅是用户级，覆盖一切 group。
		//
		// 月卡查找两步走（2026-05-16 方案 C，见 docs/plans/2026-05-16-wallet-v4-group-switch-billing-fix.md）：
		//   1. exact match by (user_id, key.group_id)：用户没切 group / 切到订阅主 group
		//   2. plan_groups 间接覆盖：用户在 admin 把 key 切到 plan 关联的 standard group
		//      （如月卡 paid-trial-v3-30d 关联 claude-Max pool / openai-default 等）
		if subscription == nil && apiKey.Group != nil && subscriptionService != nil {
			sub, subErr := subscriptionService.GetActiveSubscription(
				c.Request.Context(),
				apiKey.User.ID,
				apiKey.Group.ID,
			)
			if subErr == nil && sub != nil {
				subscription = sub
			} else if covering, coverErr := subscriptionService.GetActiveSubscriptionCoveringGroup(
				c.Request.Context(),
				apiKey.User.ID,
				apiKey.Group.ID,
			); coverErr == nil && covering != nil {
				subscription = covering
			}
		}

		// 都没匹配 → 决定 403 还是走老 balance 兼容路径。
		//   - 订阅类型 group：必须有 exact / 覆盖订阅，否则 SUBSCRIPTION_NOT_FOUND（保留原行为）
		//   - standard 类型 group：
		//     - 用户有任何 active 订阅但当前 group 不覆盖 → GROUP_NOT_IN_SUBSCRIPTION
		//       （2026-05-16 修复：原静默扣 user.balance 是 bug，月卡用户应被保护）
		//     - 用户没任何订阅 → 落到 §6 余额检查（保留纯余额用户兼容路径）
		if subscription == nil && !skipBilling && subscriptionService != nil {
			if isSubscriptionType {
				AbortWithError(c, 403, "SUBSCRIPTION_NOT_FOUND", "No active subscription found for this group")
				return
			}
			hasAny, _ := subscriptionService.UserHasAnyActiveSubscription(c.Request.Context(), apiKey.User.ID)
			if hasAny {
				AbortWithError(c, 403, "GROUP_NOT_IN_SUBSCRIPTION",
					"该分组不在你的套餐内，请到后台切回订阅分组，或联系客服（微信 aa402837 Donish）")
				return
			}
		}

		// ── 6. 计费执行（skipBilling 时整块跳过） ────────────────────

		if !skipBilling {
			// Key 状态检查
			switch apiKey.Status {
			case service.StatusAPIKeyQuotaExhausted:
				AbortWithError(c, 429, "API_KEY_QUOTA_EXHAUSTED", "API key 额度已用完")
				return
			case service.StatusAPIKeyExpired:
				AbortWithError(c, 403, "API_KEY_EXPIRED", "API key 已过期")
				return
			}

			// 运行时过期/配额检查（即使状态是 active，也要检查时间和用量）
			if apiKey.IsExpired() {
				AbortWithError(c, 403, "API_KEY_EXPIRED", "API key 已过期")
				return
			}
			if apiKey.IsQuotaExhausted() {
				AbortWithError(c, 429, "API_KEY_QUOTA_EXHAUSTED", "API key 额度已用完")
				return
			}

			// 订阅模式：验证订阅限额
			if subscription != nil {
				// 钱包模式 (v4) 跳过 group 维度 daily/weekly/monthly 限额检查 +
				// 窗口维护：钱包是用户级共享额度，不绑 group 限额。余额检查由
				// BillingCacheService.checkWalletEligibility 处理（→ 402）。
				// IsExpired 检查仍要做，避免过期钱包订阅继续扣款。
				if subscription.IsWalletMode() {
					if subscription.IsExpired() {
						AbortWithError(c, 403, "SUBSCRIPTION_EXPIRED", "Wallet subscription has expired")
						return
					}
				} else {
					needsMaintenance, validateErr := subscriptionService.ValidateAndCheckLimits(subscription, apiKey.Group)
					if validateErr != nil {
						code := "SUBSCRIPTION_INVALID"
						status := 403
						if errors.Is(validateErr, service.ErrDailyLimitExceeded) ||
							errors.Is(validateErr, service.ErrWeeklyLimitExceeded) ||
							errors.Is(validateErr, service.ErrMonthlyLimitExceeded) {
							code = "USAGE_LIMIT_EXCEEDED"
							status = 429
						}
						AbortWithError(c, status, code, validateErr.Error())
						return
					}

					// 窗口维护异步化（不阻塞请求）
					if needsMaintenance {
						maintenanceCopy := *subscription
						subscriptionService.DoWindowMaintenance(&maintenanceCopy)
					}
				}
			} else {
				// 非订阅模式 或 订阅模式但 subscriptionService 未注入：回退到余额检查
				if apiKey.User.Balance <= 0 {
					AbortWithError(c, 403, "INSUFFICIENT_BALANCE", "Insufficient account balance")
					return
				}
			}
		}

		// ── 7. 设置上下文 → Next ─────────────────────────────────────

		if subscription != nil {
			c.Set(string(ContextKeySubscription), subscription)
		}
		c.Set(string(ContextKeyAPIKey), apiKey)
		c.Set(string(ContextKeyUser), AuthSubject{
			UserID:      apiKey.User.ID,
			Concurrency: apiKey.User.Concurrency,
		})
		c.Set(string(ContextKeyUserRole), apiKey.User.Role)
		if routedGroup != nil {
			setGroupContext(c, routedGroup)
		} else {
			setGroupContext(c, apiKey.Group)
		}
		_ = apiKeyService.TouchLastUsed(c.Request.Context(), apiKey.ID)

		c.Next()
	}
}

func extractModelFromRequest(c *gin.Context) (string, error) {
	if c.Request == nil || c.Request.Body == nil {
		return "", service.ErrModelUnsupported
	}

	bodyBytes, err := io.ReadAll(c.Request.Body)
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", err
	}

	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return "", err
	}
	modelName := strings.TrimSpace(payload.Model)
	if modelName == "" {
		return "", service.ErrModelUnsupported
	}
	return modelName, nil
}

// GetAPIKeyFromContext 从上下文中获取API key
func GetAPIKeyFromContext(c *gin.Context) (*service.APIKey, bool) {
	value, exists := c.Get(string(ContextKeyAPIKey))
	if !exists {
		return nil, false
	}
	apiKey, ok := value.(*service.APIKey)
	return apiKey, ok
}

// GetSubscriptionFromContext 从上下文中获取订阅信息
func GetSubscriptionFromContext(c *gin.Context) (*service.UserSubscription, bool) {
	value, exists := c.Get(string(ContextKeySubscription))
	if !exists {
		return nil, false
	}
	subscription, ok := value.(*service.UserSubscription)
	return subscription, ok
}

func setGroupContext(c *gin.Context, group *service.Group) {
	if !service.IsGroupContextValid(group) {
		return
	}
	if existing, ok := c.Request.Context().Value(ctxkey.Group).(*service.Group); ok && existing != nil && existing.ID == group.ID && service.IsGroupContextValid(existing) {
		return
	}
	ctx := context.WithValue(c.Request.Context(), ctxkey.Group, group)
	c.Request = c.Request.WithContext(ctx)
}
