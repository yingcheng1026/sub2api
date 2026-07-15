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

		// Group liveness and exclusive/reserved authorization are authentication
		// boundaries, not billing conveniences. They must run even in simple mode
		// so a revoked VIP key cannot regain access by changing run_mode.
		if apiKey.GroupID != nil && (apiKey.Group == nil || apiKey.Group.ID != *apiKey.GroupID || !apiKey.Group.Hydrated || apiKey.Group.Status != service.StatusActive) {
			AbortWithError(c, 403, "GROUP_NOT_ALLOWED", "该分组已停用或授权已撤销，请联系客服")
			return
		}
		if apiKey.GroupID != nil && apiKey.Group != nil && !apiKey.Group.IsSubscriptionType() {
			groupName := strings.TrimSpace(apiKey.Group.Name)
			if (groupName == service.WalletDefaultOpenAIGroupName || groupName == service.WalletDefaultVIPGroupName) &&
				!service.CanUseWalletGroup(apiKey.User, apiKey.Group) {
				AbortWithError(c, 403, "GROUP_NOT_ALLOWED", "钱包保留分组配置或授权不符合要求，请联系客服")
				return
			}
			if apiKey.Group.IsExclusive && !apiKey.User.CanBindGroup(apiKey.Group.ID, true) {
				AbortWithError(c, 403, "GROUP_NOT_ALLOWED", "该专属分组授权已撤销，请联系客服")
				return
			}
		}

		// ── 4. SimpleMode → early return ─────────────────────────────

		if cfg.RunMode == config.RunModeSimple {
			if apiKey.Group != nil {
				groupName := strings.TrimSpace(apiKey.Group.Name)
				if groupName == service.WalletDefaultOpenAIGroupName || groupName == service.WalletDefaultVIPGroupName {
					AbortWithError(c, 403, "SIMPLE_MODE_RESERVED_GROUP_DISABLED", "simple 模式禁止使用钱包与 VIP 保留分组")
					return
				}
			}
			if apiKey.IsWalletUniversal() {
				AbortWithError(c, 403, "SIMPLE_MODE_RESERVED_GROUP_DISABLED", "simple 模式禁止使用额度钱包 Key")
				return
			}
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

		// Probe the wallet, but do not select it yet. A fixed monthly key must
		// charge its monthly subscription first; the wallet is only a fallback
		// for the approved credits groups below.
		if subscriptionService != nil {
			foundWalletSub, walletErr := subscriptionService.GetActiveCreditsWalletSubscription(
				c.Request.Context(),
				apiKey.User.ID,
			)
			switch {
			case walletErr == nil && foundWalletSub != nil:
				walletSub = foundWalletSub
			case walletErr != nil && !errors.Is(walletErr, service.ErrSubscriptionNotFound):
				AbortWithError(c, 503, "BILLING_SERVICE_UNAVAILABLE", "订阅与钱包服务暂时不可用，请稍后重试")
				return
			default:
				legacyWallet, legacyErr := subscriptionService.GetActiveWalletSubscription(c.Request.Context(), apiKey.User.ID)
				if legacyErr == nil && legacyWallet != nil {
					walletSub = legacyWallet
				} else if legacyErr != nil && !errors.Is(legacyErr, service.ErrSubscriptionNotFound) {
					AbortWithError(c, 503, "BILLING_SERVICE_UNAVAILABLE", "订阅与钱包服务暂时不可用，请稍后重试")
					return
				}
			}
		}

		if (apiKey.GroupID == nil || apiKey.IsWalletUniversal()) && !apiKey.HasValidWalletUniversalShape() {
			AbortWithError(c, 403, "WALLET_KEY_INVALID", "该 Key 不是系统生成的钱包通用 Key，请使用钱包页面提供的 Key")
			return
		}

		// A NULL-group key is the wallet universal key, not an unscoped balance
		// key. Without an active permanent credits wallet it must fail closed;
		// otherwise an old universal key can fall through to user.balance with no
		// effective group and bypass the wallet routing policy.
		if apiKey.GroupID == nil && (walletSub == nil || !walletSub.IsUniversalWalletMode()) {
			AbortWithError(c, 403, "WALLET_NOT_ACTIVE", "额度钱包未开通或已失效，请联系客服")
			return
		}

		// group_id=NULL is reserved for permanent credits wallets. Route by the
		// requested model and then enforce the HFC group policy: GPT only through
		// openai-default; Claude/Fable only through an explicitly granted vip.
		if walletSub != nil && walletSub.IsUniversalWalletMode() && apiKey.HasValidWalletUniversalShape() {
			modelName := "gpt-wallet-default"
			if !usesWalletDefaultOpenAIRoute(c) {
				var extractErr error
				modelName, extractErr = extractModelFromRequest(c)
				if extractErr != nil {
					AbortWithError(c, 400, "model_unsupported", "该模型未启用，请联系客服")
					return
				}
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
			if !service.CanUseWalletGroup(apiKey.User, group) {
				AbortWithError(c, 403, "GROUP_NOT_ALLOWED", "该模型分组未授权；Claude/VIP 请先联系管理员开通")
				return
			}
			if isWalletModelsEndpoint(c) {
				visibility, visibilityErr := buildWalletModelVisibility(
					c.Request.Context(),
					apiKey,
					group,
					modelRouter,
					groupGetter,
				)
				if visibilityErr == nil {
					setWalletModelVisibility(c, visibility)
				} else if shouldFailClosedWalletModelVisibility(visibilityErr) {
					AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to resolve wallet model visibility")
					return
				}
			}
			routedGroup = group
			effectiveGroupID := group.ID
			apiKeyCopy := *apiKey
			apiKeyCopy.GroupID = &effectiveGroupID
			apiKeyCopy.Group = group
			apiKey = &apiKeyCopy
			subscription = walletSub
		}

		isSubscriptionType := apiKey.Group != nil && apiKey.Group.IsSubscriptionType()

		// Fixed-group keys use monthly entitlement first. This keeps monthly
		// quota and credits balance as two independent ledgers.
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
			} else if subErr != nil && !errors.Is(subErr, service.ErrSubscriptionNotFound) {
				AbortWithError(c, 503, "BILLING_SERVICE_UNAVAILABLE", "订阅与钱包服务暂时不可用，请稍后重试")
				return
			} else {
				covering, coverErr := subscriptionService.GetActiveSubscriptionCoveringGroup(
					c.Request.Context(),
					apiKey.User.ID,
					apiKey.Group.ID,
				)
				if coverErr == nil && covering != nil {
					subscription = covering
				} else if coverErr != nil && !errors.Is(coverErr, service.ErrSubscriptionNotFound) {
					AbortWithError(c, 503, "BILLING_SERVICE_UNAVAILABLE", "订阅与钱包服务暂时不可用，请稍后重试")
					return
				}
			}
		}

		// A fixed standard-group key may use the wallet only for the approved
		// credits groups. Finite legacy wallets are retained solely for an
		// explicitly granted vip; openai-default requires a permanent credits
		// wallet.
		if subscription == nil && walletSub != nil && apiKey.GroupID != nil && apiKey.Group != nil && !apiKey.Group.IsSubscriptionType() {
			allowed := service.CanUseWalletGroup(apiKey.User, apiKey.Group)
			if !allowed {
				AbortWithError(c, 403, "GROUP_NOT_ALLOWED", "该分组未授权或不属于额度钱包")
				return
			}
			if strings.TrimSpace(apiKey.Group.Name) == service.WalletDefaultVIPGroupName || walletSub.IsUniversalWalletMode() {
				if modelRouter == nil {
					AbortWithError(c, 500, "INTERNAL_ERROR", "Model router is not configured")
					return
				}
				modelName, extractErr := extractModelFromRequest(c)
				if extractErr != nil {
					AbortWithError(c, 400, "model_unsupported", "该模型未启用，请联系客服")
					return
				}
				expectedGroupID, routeErr := modelRouter.ResolveGroupID(c.Request.Context(), apiKey.User.ID, modelName)
				if routeErr != nil {
					if errors.Is(routeErr, service.ErrModelUnsupported) {
						AbortWithError(c, 400, "model_unsupported", "该模型未启用，请联系客服")
						return
					}
					AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to route model")
					return
				}
				if expectedGroupID != apiKey.Group.ID {
					AbortWithError(c, 403, "GROUP_NOT_ALLOWED", "当前 Key 分组与请求模型不匹配，请使用正确的钱包 Key")
					return
				}
				subscription = walletSub
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
			hasAny, hasAnyErr := subscriptionService.UserHasAnyActiveSubscription(c.Request.Context(), apiKey.User.ID)
			if hasAnyErr != nil {
				AbortWithError(c, 503, "BILLING_SERVICE_UNAVAILABLE", "订阅与钱包服务暂时不可用，请稍后重试")
				return
			}
			if hasAny {
				AbortWithError(c, 403, "GROUP_NOT_IN_SUBSCRIPTION",
					"该分组不在你的额度使用范围内，请到后台选择可用分组，或联系管理员或客服")
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
					_, effectiveGroup := service.EffectiveBillingContext(apiKey.Group, subscription)
					needsMaintenance, validateErr := subscriptionService.ValidateAndCheckLimits(subscription, effectiveGroup)
					if validateErr != nil {
						code := "SUBSCRIPTION_INVALID"
						status := 403
						if isSubscriptionUsageLimitError(validateErr) {
							code = "USAGE_LIMIT_EXCEEDED"
							status = 429
						}
						AbortWithError(c, status, code, validateErr.Error())
						return
					}

					// 边界窗口必须在请求继续前完成权威重读与 CAS 维护，
					// 防止延迟的重复 reset 清除已经结算的新窗口用量。
					if subscription != nil && needsMaintenance {
						maintained, maintenanceErr := subscriptionService.DoWindowMaintenance(c.Request.Context(), subscription.ID)
						if maintenanceErr != nil {
							AbortWithError(c, 503, "SUBSCRIPTION_MAINTENANCE_FAILED", "Subscription window maintenance is temporarily unavailable")
							return
						}
						subscription = maintained
						_, maintainedGroup := service.EffectiveBillingContext(apiKey.Group, subscription)
						stillNeedsMaintenance, validateErr := subscriptionService.ValidateAndCheckLimits(subscription, maintainedGroup)
						if validateErr != nil || stillNeedsMaintenance {
							AbortWithError(c, 503, "SUBSCRIPTION_MAINTENANCE_FAILED", "Subscription window maintenance did not converge")
							return
						}
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

func usesWalletDefaultOpenAIRoute(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	path := c.Request.URL.Path
	if path == "/v1/models" || path == "/v1/usage" {
		return true
	}
	return c.Request.Method == "GET" && (path == "/v1/responses" || path == "/responses" || strings.HasSuffix(path, "/responses"))
}

func isWalletModelsEndpoint(c *gin.Context) bool {
	return c != nil && c.Request != nil && c.Request.URL != nil &&
		c.Request.Method == "GET" && c.Request.URL.Path == "/v1/models"
}

func buildWalletModelVisibility(
	ctx context.Context,
	apiKey *service.APIKey,
	defaultGroup *service.Group,
	modelRouter service.ModelRouter,
	groupGetter apiKeyAuthGroupGetter,
) (WalletModelVisibility, error) {
	if apiKey == nil || apiKey.User == nil || !apiKey.HasValidWalletUniversalShape() || defaultGroup == nil {
		return WalletModelVisibility{}, errors.New("invalid universal wallet model visibility input")
	}
	routeProvider, ok := modelRouter.(service.ModelRouteProvider)
	if !ok {
		return WalletModelVisibility{}, errors.New("model router does not expose routes")
	}
	routes := routeProvider.Routes()
	if len(routes) == 0 {
		return WalletModelVisibility{}, errors.New("wallet model routes are empty")
	}
	if defaultGroup.Name != service.WalletDefaultOpenAIGroupName || !service.CanUseWalletGroup(apiKey.User, defaultGroup) {
		return WalletModelVisibility{}, errors.New("default wallet group violates policy")
	}

	visibility := WalletModelVisibility{
		APIKeyID: apiKey.ID,
		Groups:   []service.Group{*defaultGroup},
		Routes:   append([]service.ModelRoute(nil), routes...),
	}
	seenGroupIDs := map[int64]struct{}{defaultGroup.ID: {}}

	// openai-default is already the effective metadata route above. Additional
	// visibility is limited to the exact vip route and only when the user has an
	// explicit grant for the resolved group ID.
	probeModel, found := walletRouteProbeModel(routes, service.WalletDefaultVIPGroupName)
	if !found {
		return visibility, nil
	}
	vipGroupID, err := modelRouter.ResolveGroupID(ctx, apiKey.User.ID, probeModel)
	if err != nil {
		return visibility, nil
	}
	if !apiKey.User.CanBindGroup(vipGroupID, true) {
		return visibility, nil
	}
	vipGroup, err := groupGetter.GetByID(ctx, vipGroupID)
	if err != nil {
		return WalletModelVisibility{}, err
	}
	if vipGroup.Name != service.WalletDefaultVIPGroupName || !service.CanUseWalletGroup(apiKey.User, vipGroup) {
		return WalletModelVisibility{}, errors.New("vip wallet group violates policy")
	}
	if _, duplicate := seenGroupIDs[vipGroup.ID]; !duplicate {
		visibility.Groups = append(visibility.Groups, *vipGroup)
	}
	return visibility, nil
}

func shouldFailClosedWalletModelVisibility(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return !strings.Contains(msg, "model router does not expose routes") &&
		!strings.Contains(msg, "wallet model routes are empty")
}

func walletRouteProbeModel(routes []service.ModelRoute, groupName string) (string, bool) {
	for _, route := range routes {
		if strings.TrimSpace(route.GroupName) != groupName {
			continue
		}
		pattern := strings.TrimSpace(route.Pattern)
		probe := strings.TrimSpace(route.ExampleModel)
		if probe == "" {
			if strings.HasSuffix(pattern, "*") {
				probe = strings.TrimSuffix(pattern, "*") + "wallet-model-list-probe"
			} else {
				probe = pattern
			}
		}
		routedGroupName, ok := service.WalletModelRouteGroupName(routes, probe)
		if ok && routedGroupName == groupName {
			return probe, true
		}
	}
	return "", false
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

func isSubscriptionUsageLimitError(err error) bool {
	return errors.Is(err, service.ErrDailyLimitExceeded) ||
		errors.Is(err, service.ErrWeeklyLimitExceeded) ||
		errors.Is(err, service.ErrMonthlyLimitExceeded)
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
