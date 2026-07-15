package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/dgraph-io/ristretto"
	"golang.org/x/sync/singleflight"
)

var (
	ErrAPIKeyNotFound              = infraerrors.NotFound("API_KEY_NOT_FOUND", "api key not found")
	ErrGroupNotAllowed             = infraerrors.Forbidden("GROUP_NOT_ALLOWED", "user is not allowed to bind this group")
	ErrAPIKeyGroupRequired         = infraerrors.BadRequest("API_KEY_GROUP_REQUIRED", "user-created api keys must select a group")
	ErrWalletUniversalKeyImmutable = infraerrors.BadRequest("WALLET_KEY_IMMUTABLE", "the system wallet key name and group cannot be changed")
	ErrWalletUniversalKeyReserved  = infraerrors.BadRequest("WALLET_KEY_NAME_RESERVED", "the system wallet key name is reserved")
	ErrWalletUniversalKeyDelete    = infraerrors.BadRequest("WALLET_KEY_DELETE_FORBIDDEN", "the system wallet key cannot be deleted; disable or rotate it instead")
	ErrActiveCreditsWalletRequired = infraerrors.Forbidden("ACTIVE_CREDITS_WALLET_REQUIRED", "an active permanent credits wallet is required")
	ErrAPIKeyExists                = infraerrors.Conflict("API_KEY_EXISTS", "api key already exists")
	ErrAPIKeyTooShort              = infraerrors.BadRequest("API_KEY_TOO_SHORT", "api key must be at least 16 characters")
	ErrAPIKeyInvalidChars          = infraerrors.BadRequest("API_KEY_INVALID_CHARS", "api key can only contain letters, numbers, underscores, and hyphens")
	ErrAPIKeyCustomKeyDisabled     = infraerrors.BadRequest("API_KEY_CUSTOM_KEY_DISABLED", "custom API keys are disabled; use a generated key")
	ErrAPIKeyRateLimited           = infraerrors.TooManyRequests("API_KEY_RATE_LIMITED", "too many failed attempts, please try again later")
	ErrAPIKeyCreateUnavailable     = infraerrors.ServiceUnavailable("API_KEY_CREATE_UNAVAILABLE", "API key creation is temporarily unavailable")
	ErrAPIKeyLimitReached          = infraerrors.Conflict("API_KEY_LIMIT_REACHED", "active API key limit reached; delete an unused key before creating another")
	ErrAPIKeyRevealVerification    = infraerrors.Forbidden("API_KEY_REVEAL_VERIFICATION_FAILED", "API key reveal verification failed")
	ErrAPIKeyRevealUnavailable     = infraerrors.ServiceUnavailable("API_KEY_REVEAL_UNAVAILABLE", "API key reveal verification is temporarily unavailable")
	ErrAPIKeyUpdateVerification    = infraerrors.Forbidden("API_KEY_UPDATE_VERIFICATION_FAILED", "API key update verification failed")
	ErrAPIKeyReactivationForbidden = infraerrors.Forbidden("API_KEY_REACTIVATION_FORBIDDEN", "disabled API keys require administrator reactivation")
	ErrInvalidIPPattern            = infraerrors.BadRequest("INVALID_IP_PATTERN", "invalid IP or CIDR pattern")
	// ErrAPIKeyExpired        = infraerrors.Forbidden("API_KEY_EXPIRED", "api key has expired")
	ErrAPIKeyExpired = infraerrors.Forbidden("API_KEY_EXPIRED", "api key 已过期")
	// ErrAPIKeyQuotaExhausted = infraerrors.TooManyRequests("API_KEY_QUOTA_EXHAUSTED", "api key quota exhausted")
	ErrAPIKeyQuotaExhausted = infraerrors.TooManyRequests("API_KEY_QUOTA_EXHAUSTED", "api key 额度已用完")

	// Rate limit errors
	ErrAPIKeyRateLimit5hExceeded = infraerrors.TooManyRequests("API_KEY_RATE_5H_EXCEEDED", "api key 5小时限额已用完")
	ErrAPIKeyRateLimit1dExceeded = infraerrors.TooManyRequests("API_KEY_RATE_1D_EXCEEDED", "api key 日限额已用完")
	ErrAPIKeyRateLimit7dExceeded = infraerrors.TooManyRequests("API_KEY_RATE_7D_EXCEEDED", "api key 7天限额已用完")
)

const (
	apiKeyMaxErrorsPerHour    = 20
	apiKeyMaxCreatesPerHour   = 20
	apiKeyMaxActivePerUser    = 50
	APIKeyStepUpPurposeReveal = "reveal"
	APIKeyStepUpPurposeCreate = "create"
	APIKeyStepUpPurposeUpdate = "update"
	apiKeyLastUsedMinTouch    = 30 * time.Second
	// DB 写失败后的短退避，避免请求路径持续同步重试造成写风暴与高延迟。
	apiKeyLastUsedFailBackoff = 5 * time.Second
)

// 钱包多 key 模式：每把 key 命名为 "钱包-" + group.Name（如 "钱包-gpt-5"）。
// 前端可按 prefix 识别钱包 key（HasPrefix），与 v3 单 group 老 key 区分。
// 注意：5/14 反转决策后激活流程不再走多 key 路径；EnsureWalletGroupKeys 实现保留作底层能力。
const WalletGroupKeyNamePrefix = "钱包-"

// WalletUniversalAPIKeyName 单 key 模式自动建的钱包通用 key 命名（5/14 反转决策回归 B1.4 形态）。
const WalletUniversalAPIKeyName = "钱包通用 key（自动路由）"

// IsWalletGroupKeyName 判断 key 名是否为钱包多 key 模式生成的命名格式。
func IsWalletGroupKeyName(name string) bool {
	return strings.HasPrefix(name, WalletGroupKeyNamePrefix)
}

// IsWalletUniversalKeyName 判断 key 名是否为单 key 模式自动建的钱包通用 key。
func IsWalletUniversalKeyName(name string) bool {
	return name == WalletUniversalAPIKeyName
}

// walletGroupKeyName 为指定 group 拼装钱包 key 命名。
func walletGroupKeyName(group *Group) string {
	if group == nil {
		return WalletGroupKeyNamePrefix
	}
	return WalletGroupKeyNamePrefix + group.Name
}

type APIKeyRepository interface {
	Create(ctx context.Context, key *APIKey) error
	GetByID(ctx context.Context, id int64) (*APIKey, error)
	// GetKeyAndOwnerID 仅获取 API Key 的 key 与所有者 ID，用于删除等轻量场景
	GetKeyAndOwnerID(ctx context.Context, id int64) (string, int64, error)
	GetByKey(ctx context.Context, key string) (*APIKey, error)
	// GetByKeyForAuth 认证专用查询，返回最小字段集
	GetByKeyForAuth(ctx context.Context, key string) (*APIKey, error)
	Update(ctx context.Context, key *APIKey) error
	Delete(ctx context.Context, id int64) error

	ListByUserID(ctx context.Context, userID int64, params pagination.PaginationParams, filters APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error)
	VerifyOwnership(ctx context.Context, userID int64, apiKeyIDs []int64) ([]int64, error)
	CountByUserID(ctx context.Context, userID int64) (int64, error)
	ExistsByKey(ctx context.Context, key string) (bool, error)
	ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]APIKey, *pagination.PaginationResult, error)
	SearchAPIKeys(ctx context.Context, userID int64, keyword string, limit int) ([]APIKey, error)
	ClearGroupIDByGroupID(ctx context.Context, groupID int64) (int64, error)
	// UpdateGroupIDByUserAndGroup 将用户下绑定 oldGroupID 的所有 Key 迁移到 newGroupID
	UpdateGroupIDByUserAndGroup(ctx context.Context, userID, oldGroupID, newGroupID int64) (int64, error)
	CountByGroupID(ctx context.Context, groupID int64) (int64, error)
	ListAuthCacheLocatorsByUserID(ctx context.Context, userID int64) ([]string, error)
	ListAuthCacheLocatorsByGroupID(ctx context.Context, groupID int64) ([]string, error)

	// Quota methods
	IncrementQuotaUsed(ctx context.Context, id int64, amount float64) (float64, error)
	UpdateLastUsed(ctx context.Context, id int64, usedAt time.Time) error

	// Rate limit methods
	IncrementRateLimitUsage(ctx context.Context, id int64, cost float64) error
	ResetRateLimitWindows(ctx context.Context, id int64) error
	GetRateLimitData(ctx context.Context, id int64) (*APIKeyRateLimitData, error)
}

// APIKeyProtector provides domain-separated encryption and keyed lookup for
// customer API keys. It is deliberately separate from TOTP/payment secrets so
// a compromise or rotation in one domain does not expose another.
type APIKeyProtector interface {
	EncryptAPIKey(plaintext, associatedData string) (string, error)
	DecryptAPIKey(ciphertext, associatedData string) (string, error)
	LookupLocator(plaintext string) string
}

// APIKeyPlaintextMigrator is implemented by the production repository. The
// server invokes it before opening the HTTP listener.
type APIKeyPlaintextMigrator interface {
	MigratePlaintextAPIKeysToEncrypted(ctx context.Context) (int, error)
}

type APIKeyAuthCacheLocatorProvider interface {
	APIKeyAuthCacheLocator(plaintext string) string
}

// APIKeyPurposeRepository is the production point lookup used for the unique
// live wallet-universal key. It intentionally includes disabled keys so an
// emergency user disable is preserved across wallet top-ups.
type APIKeyPurposeRepository interface {
	GetByUserIDAndPurpose(ctx context.Context, userID int64, purpose string) (*APIKey, error)
}

// APIKeyAuthCacheSecurityValidator verifies the small, permission-sensitive
// portion of an auth snapshot against the database. Positive cache entries are
// never trusted when this validator is unavailable or returns an error.
//
// This is intentionally separate from APIKeyRepository so test/dedicated
// repositories that do not support auth caching are not forced to implement a
// production-only fast path.
type APIKeyAuthCacheSecurityValidator interface {
	ValidateAuthCacheSnapshot(ctx context.Context, cacheLocator string, snapshot *APIKeyAuthSnapshot) (bool, error)
}

// APIKeyRateLimitData holds rate limit usage and window state for an API key.
type APIKeyRateLimitData struct {
	Usage5h       float64
	Usage1d       float64
	Usage7d       float64
	Window5hStart *time.Time
	Window1dStart *time.Time
	Window7dStart *time.Time
}

// EffectiveUsage5h returns the 5h window usage, or 0 if the window has expired.
func (d *APIKeyRateLimitData) EffectiveUsage5h() float64 {
	if IsWindowExpired(d.Window5hStart, RateLimitWindow5h) {
		return 0
	}
	return d.Usage5h
}

// EffectiveUsage1d returns the 1d window usage, or 0 if the window has expired.
func (d *APIKeyRateLimitData) EffectiveUsage1d() float64 {
	if IsWindowExpired(d.Window1dStart, RateLimitWindow1d) {
		return 0
	}
	return d.Usage1d
}

// EffectiveUsage7d returns the 7d window usage, or 0 if the window has expired.
func (d *APIKeyRateLimitData) EffectiveUsage7d() float64 {
	if IsWindowExpired(d.Window7dStart, RateLimitWindow7d) {
		return 0
	}
	return d.Usage7d
}

// APIKeyQuotaUsageState captures the latest quota fields after an atomic quota update.
// It is intentionally small so repositories can return it from a single SQL statement.
type APIKeyQuotaUsageState struct {
	QuotaUsed        float64
	Quota            float64
	AuthCacheLocator string
	Status           string
}

type WalletModelRouteInfo struct {
	Pattern                 string  `json:"pattern"`
	ExampleModel            string  `json:"example_model"`
	GroupID                 int64   `json:"group_id"`
	GroupName               string  `json:"group_name"`
	Platform                string  `json:"platform"`
	RateMultiplier          float64 `json:"rate_multiplier"`
	EffectiveRateMultiplier float64 `json:"effective_rate_multiplier"`
}

// APIKeyCache defines cache operations for API key service
type APIKeyCache interface {
	GetCreateAttemptCount(ctx context.Context, userID int64) (int, error)
	IncrementCreateAttemptCount(ctx context.Context, userID int64) error
	DeleteCreateAttemptCount(ctx context.Context, userID int64) error

	IncrementDailyUsage(ctx context.Context, apiKey string) error
	SetDailyUsageExpiry(ctx context.Context, apiKey string, ttl time.Duration) error

	GetAuthCache(ctx context.Context, key string) (*APIKeyAuthCacheEntry, error)
	SetAuthCache(ctx context.Context, key string, entry *APIKeyAuthCacheEntry, ttl time.Duration) error
	DeleteAuthCache(ctx context.Context, key string) error

	// Pub/Sub for L1 cache invalidation across instances
	PublishAuthCacheInvalidation(ctx context.Context, cacheKey string) error
	SubscribeAuthCacheInvalidation(ctx context.Context, handler func(cacheKey string)) error
}

// APIKeyStepUpAttemptCache is deliberately separate from the legacy custom-key
// limiter and scoped per protected action. Redis failure must fail closed.
type APIKeyStepUpAttemptCache interface {
	IncrementAPIKeyStepUpAttempt(ctx context.Context, purpose string, userID int64) (int, error)
	DeleteAPIKeyStepUpAttempts(ctx context.Context, purpose string, userID int64) error
}

type APIKeyCreateReservationCache interface {
	ReserveAPIKeyCreate(ctx context.Context, userID int64) (int, error)
}

// APIKeyAuthCacheInvalidator 提供认证缓存失效能力
type APIKeyAuthCacheInvalidator interface {
	InvalidateAuthCacheByKey(ctx context.Context, key string)
	InvalidateAuthCacheByUserID(ctx context.Context, userID int64)
	InvalidateAuthCacheByGroupID(ctx context.Context, groupID int64)
}

// CreateAPIKeyRequest 创建API Key请求
type CreateAPIKeyRequest struct {
	Name        string   `json:"name"`
	GroupID     *int64   `json:"group_id"`
	CustomKey   *string  `json:"custom_key"`   // 可选的自定义key
	IPWhitelist []string `json:"ip_whitelist"` // IP 白名单
	IPBlacklist []string `json:"ip_blacklist"` // IP 黑名单

	// Quota fields
	Quota         float64 `json:"quota"`           // Quota limit in USD (0 = unlimited)
	ExpiresInDays *int    `json:"expires_in_days"` // Days until expiry (nil = never expires)

	// Rate limit fields (0 = unlimited)
	RateLimit5h float64 `json:"rate_limit_5h"`
	RateLimit1d float64 `json:"rate_limit_1d"`
	RateLimit7d float64 `json:"rate_limit_7d"`
}

// UpdateAPIKeyRequest 更新API Key请求
type UpdateAPIKeyRequest struct {
	Name        *string   `json:"name"`
	GroupID     *int64    `json:"group_id"`
	Status      *string   `json:"status"`
	IPWhitelist *[]string `json:"ip_whitelist"` // 省略=不修改，显式 []=清空
	IPBlacklist *[]string `json:"ip_blacklist"` // 省略=不修改，显式 []=清空

	// Quota fields
	Quota           *float64   `json:"quota"`       // Quota limit in USD (nil = no change, 0 = unlimited)
	ExpiresAt       *time.Time `json:"expires_at"`  // Expiration time (nil = no change)
	ClearExpiration bool       `json:"-"`           // Clear expiration (internal use)
	ResetQuota      *bool      `json:"reset_quota"` // Reset quota_used to 0

	// Rate limit fields (nil = no change, 0 = unlimited)
	RateLimit5h         *float64 `json:"rate_limit_5h"`
	RateLimit1d         *float64 `json:"rate_limit_1d"`
	RateLimit7d         *float64 `json:"rate_limit_7d"`
	ResetRateLimitUsage *bool    `json:"reset_rate_limit_usage"` // Reset all usage counters to 0
	StepUpVerified      bool     `json:"-"`                      // Set only after fresh password/TOTP proof
}

// APIKeyUpdateRequiresStepUp identifies owner mutations that can expand the
// authority or usable lifetime of an existing key. Name changes and explicit
// deactivation remain available as containment actions without fresh proof.
func APIKeyUpdateRequiresStepUp(req UpdateAPIKeyRequest) bool {
	if req.GroupID != nil || req.IPWhitelist != nil || req.IPBlacklist != nil ||
		req.Quota != nil || req.ExpiresAt != nil || req.ClearExpiration ||
		req.RateLimit5h != nil || req.RateLimit1d != nil || req.RateLimit7d != nil {
		return true
	}
	if req.ResetQuota != nil && *req.ResetQuota {
		return true
	}
	if req.ResetRateLimitUsage != nil && *req.ResetRateLimitUsage {
		return true
	}
	return req.Status != nil && *req.Status == StatusAPIKeyActive
}

// APIKeyService API Key服务
// RateLimitCacheInvalidator invalidates rate limit cache entries on manual reset.
type RateLimitCacheInvalidator interface {
	InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error
}

type APIKeyService struct {
	apiKeyRepo                 APIKeyRepository
	authCacheSecurityValidator APIKeyAuthCacheSecurityValidator
	authCacheLocatorProvider   APIKeyAuthCacheLocatorProvider
	userRepo                   UserRepository
	groupRepo                  GroupRepository
	userSubRepo                UserSubscriptionRepository
	userGroupRateRepo          UserGroupRateRepository
	cache                      APIKeyCache
	rateLimitCacheInvalid      RateLimitCacheInvalidator // optional: invalidate Redis rate limit cache
	cfg                        *config.Config
	authCacheL1                *ristretto.Cache
	authCfg                    apiKeyAuthCacheConfig
	authGroup                  singleflight.Group
	lastUsedTouchL1            sync.Map // keyID -> nextAllowedAt(time.Time)
	lastUsedTouchSF            singleflight.Group
}

// NewAPIKeyService 创建API Key服务实例
func NewAPIKeyService(
	apiKeyRepo APIKeyRepository,
	userRepo UserRepository,
	groupRepo GroupRepository,
	userSubRepo UserSubscriptionRepository,
	userGroupRateRepo UserGroupRateRepository,
	cache APIKeyCache,
	cfg *config.Config,
) *APIKeyService {
	authCacheSecurityValidator, _ := apiKeyRepo.(APIKeyAuthCacheSecurityValidator)
	authCacheLocatorProvider, _ := apiKeyRepo.(APIKeyAuthCacheLocatorProvider)
	svc := &APIKeyService{
		apiKeyRepo:                 apiKeyRepo,
		authCacheSecurityValidator: authCacheSecurityValidator,
		authCacheLocatorProvider:   authCacheLocatorProvider,
		userRepo:                   userRepo,
		groupRepo:                  groupRepo,
		userSubRepo:                userSubRepo,
		userGroupRateRepo:          userGroupRateRepo,
		cache:                      cache,
		cfg:                        cfg,
	}
	svc.initAuthCache(cfg)
	return svc
}

// MigratePlaintextAPIKeysToEncrypted secures historical rows before startup.
func (s *APIKeyService) MigratePlaintextAPIKeysToEncrypted(ctx context.Context) (int, error) {
	if s == nil || s.apiKeyRepo == nil {
		return 0, fmt.Errorf("API key repository is unavailable")
	}
	migrator, ok := s.apiKeyRepo.(APIKeyPlaintextMigrator)
	if !ok {
		return 0, fmt.Errorf("API key plaintext migration is not configured")
	}
	return migrator.MigratePlaintextAPIKeysToEncrypted(ctx)
}

// SetRateLimitCacheInvalidator sets the optional rate limit cache invalidator.
// Called after construction (e.g. in wire) to avoid circular dependencies.
func (s *APIKeyService) SetRateLimitCacheInvalidator(inv RateLimitCacheInvalidator) {
	s.rateLimitCacheInvalid = inv
}

func (s *APIKeyService) compileAPIKeyIPRules(apiKey *APIKey) {
	if apiKey == nil {
		return
	}
	apiKey.CompiledIPWhitelist = ip.CompileIPRules(apiKey.IPWhitelist)
	apiKey.CompiledIPBlacklist = ip.CompileIPRules(apiKey.IPBlacklist)
}

// GenerateKey 生成随机API Key
func (s *APIKeyService) GenerateKey() (string, error) {
	// 生成32字节随机数据
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}

	// 转换为十六进制字符串并添加前缀
	prefix := s.cfg.Default.APIKeyPrefix
	if prefix == "" {
		prefix = "sk-"
	}

	key := prefix + hex.EncodeToString(bytes)
	return key, nil
}

// ValidateCustomKey 验证自定义API Key格式
func (s *APIKeyService) ValidateCustomKey(key string) error {
	// 检查长度
	if len(key) < 16 {
		return ErrAPIKeyTooShort
	}

	// 检查字符：只允许字母、数字、下划线、连字符
	for _, c := range key {
		if (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '_' || c == '-' {
			continue
		}
		return ErrAPIKeyInvalidChars
	}

	return nil
}

// checkAPIKeyRateLimit 检查用户创建自定义Key的错误次数是否超限
func (s *APIKeyService) checkAPIKeyRateLimit(ctx context.Context, userID int64) error {
	if s.cache == nil {
		return nil
	}

	count, err := s.cache.GetCreateAttemptCount(ctx, userID)
	if err != nil {
		// Redis 出错时不阻止用户操作
		return nil
	}

	if count >= apiKeyMaxErrorsPerHour {
		return ErrAPIKeyRateLimited
	}

	return nil
}

// incrementAPIKeyErrorCount 增加用户创建自定义Key的错误计数
func (s *APIKeyService) incrementAPIKeyErrorCount(ctx context.Context, userID int64) {
	if s.cache == nil {
		return
	}

	_ = s.cache.IncrementCreateAttemptCount(ctx, userID)
}

// canUserBindGroup 检查用户是否可以绑定指定分组
// 对于订阅类型分组：检查用户是否有有效订阅
// 对于标准类型分组：使用原有的 AllowedGroups 和 IsExclusive 逻辑
func (s *APIKeyService) canUserBindGroup(ctx context.Context, user *User, group *Group) bool {
	// 订阅类型分组：需要有效订阅
	if group.IsSubscriptionType() {
		_, err := s.userSubRepo.GetActiveByUserIDAndGroupID(ctx, user.ID, group.ID)
		return err == nil // 有有效订阅则允许
	}
	// 标准类型分组：使用原有逻辑
	return user.CanBindGroup(group.ID, group.IsExclusive)
}

func validateWalletUniversalKeyMutation(apiKey *APIKey, req UpdateAPIKeyRequest) error {
	if apiKey == nil {
		return nil
	}
	if apiKey.IsWalletUniversal() {
		if !apiKey.HasValidWalletUniversalShape() {
			return ErrWalletUniversalKeyImmutable
		}
		if req.GroupID != nil || (req.Name != nil && *req.Name != WalletUniversalAPIKeyName) {
			return ErrWalletUniversalKeyImmutable
		}
		return nil
	}
	if req.Name != nil && IsWalletUniversalKeyName(*req.Name) {
		return ErrWalletUniversalKeyReserved
	}
	return nil
}

// Create 创建API Key
func (s *APIKeyService) Create(ctx context.Context, userID int64, req CreateAPIKeyRequest) (*APIKey, error) {
	return s.create(ctx, userID, req, APIKeyPurposeStandard)
}

func (s *APIKeyService) create(ctx context.Context, userID int64, req CreateAPIKeyRequest, purpose string) (*APIKey, error) {
	if req.CustomKey != nil && strings.TrimSpace(*req.CustomKey) != "" {
		return nil, ErrAPIKeyCustomKeyDisabled
	}
	// 验证用户存在
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if purpose == APIKeyPurposeWalletUniversal {
		if req.GroupID != nil || !IsWalletUniversalKeyName(req.Name) {
			return nil, ErrWalletUniversalKeyImmutable
		}
	} else {
		purpose = APIKeyPurposeStandard
		if IsWalletUniversalKeyName(req.Name) {
			return nil, ErrWalletUniversalKeyReserved
		}
	}
	if req.GroupID == nil && purpose != APIKeyPurposeWalletUniversal {
		return nil, ErrAPIKeyGroupRequired
	}
	if purpose == APIKeyPurposeStandard {
		activeCount, err := s.apiKeyRepo.CountByUserID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("count active api keys: %w", err)
		}
		if activeCount >= apiKeyMaxActivePerUser {
			return nil, ErrAPIKeyLimitReached
		}
	}

	// 验证 IP 白名单格式
	if len(req.IPWhitelist) > 0 {
		if invalid := ip.ValidateIPPatterns(req.IPWhitelist); len(invalid) > 0 {
			return nil, fmt.Errorf("%w: %v", ErrInvalidIPPattern, invalid)
		}
	}

	// 验证 IP 黑名单格式
	if len(req.IPBlacklist) > 0 {
		if invalid := ip.ValidateIPPatterns(req.IPBlacklist); len(invalid) > 0 {
			return nil, fmt.Errorf("%w: %v", ErrInvalidIPPattern, invalid)
		}
	}

	// 验证分组权限（如果指定了分组）
	if req.GroupID != nil {
		group, err := s.groupRepo.GetByID(ctx, *req.GroupID)
		if err != nil {
			return nil, fmt.Errorf("get group: %w", err)
		}

		// 检查用户是否可以绑定该分组
		if !s.canUserBindGroup(ctx, user, group) {
			return nil, ErrGroupNotAllowed
		}
	}
	if purpose == APIKeyPurposeStandard && s.cache != nil {
		reservations, ok := s.cache.(APIKeyCreateReservationCache)
		if !ok {
			return nil, ErrAPIKeyCreateUnavailable
		}
		count, err := reservations.ReserveAPIKeyCreate(ctx, userID)
		if err != nil {
			return nil, ErrAPIKeyCreateUnavailable
		}
		if count > apiKeyMaxCreatesPerHour {
			return nil, ErrAPIKeyRateLimited
		}
	}

	// New credentials are always generated with CSPRNG entropy. Existing custom
	// keys remain valid, but the API no longer accepts user-chosen secrets.
	key, err := s.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	// 创建API Key记录
	apiKey := &APIKey{
		UserID:      userID,
		Key:         key,
		KeyHash:     HashAPIKey(key),
		KeyPrefix:   APIKeyPrefixForStorage(key),
		Name:        req.Name,
		Purpose:     purpose,
		GroupID:     req.GroupID,
		Status:      StatusActive,
		IPWhitelist: req.IPWhitelist,
		IPBlacklist: req.IPBlacklist,
		Quota:       req.Quota,
		QuotaUsed:   0,
		RateLimit5h: req.RateLimit5h,
		RateLimit1d: req.RateLimit1d,
		RateLimit7d: req.RateLimit7d,
	}

	// Set expiration time if specified
	if req.ExpiresInDays != nil && *req.ExpiresInDays > 0 {
		expiresAt := time.Now().AddDate(0, 0, *req.ExpiresInDays)
		apiKey.ExpiresAt = &expiresAt
	}

	if err := s.apiKeyRepo.Create(ctx, apiKey); err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}

	s.InvalidateAuthCacheByKey(ctx, apiKey.Key)
	s.compileAPIKeyIPRules(apiKey)

	return apiKey, nil
}

// List 获取用户的API Key列表
func (s *APIKeyService) List(ctx context.Context, userID int64, params pagination.PaginationParams, filters APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error) {
	keys, pagination, err := s.apiKeyRepo.ListByUserID(ctx, userID, params, filters)
	if err != nil {
		return nil, nil, fmt.Errorf("list api keys: %w", err)
	}
	return keys, pagination, nil
}

// EnsureWalletUniversalKey 为用户建/复用 1 把通用 key（group_id=NULL），靠 B1.1/B1.2
// 的 model_router 按调用模型自动路由到对应 group。
//
// 5/14 反转决策（参见 docs/plans/2026-05-14-wallet-single-key-reversal.md）：
// 钱包激活/topup 走单 key 路径，废弃 B2.2 多 key 改造。
//
// 幂等：按不可变 purpose 查找唯一 live key。disabled key 也会复用，避免
// wallet top-up 绕过用户的紧急撤权并偷偷创建一把新 key。
// 返回 (key, created, err)。
func (s *APIKeyService) EnsureWalletUniversalKey(ctx context.Context, userID int64) (*APIKey, bool, error) {
	if err := s.requireActiveCreditsWallet(ctx, userID); err != nil {
		return nil, false, err
	}
	purposeRepo, ok := s.apiKeyRepo.(APIKeyPurposeRepository)
	if !ok {
		return nil, false, infraerrors.InternalServer("API_KEY_PURPOSE_REPOSITORY_UNAVAILABLE", "wallet key purpose lookup is not configured")
	}
	key, err := purposeRepo.GetByUserIDAndPurpose(ctx, userID, APIKeyPurposeWalletUniversal)
	if err == nil {
		if !key.HasValidWalletUniversalShape() {
			return nil, false, ErrWalletUniversalKeyImmutable
		}
		return key, false, nil
	}
	if !errors.Is(err, ErrAPIKeyNotFound) {
		return nil, false, err
	}

	newKey, err := s.create(ctx, userID, CreateAPIKeyRequest{
		Name:    WalletUniversalAPIKeyName,
		GroupID: nil,
	}, APIKeyPurposeWalletUniversal)
	if err != nil {
		return nil, false, fmt.Errorf("create wallet universal key: %w", err)
	}
	return newKey, true, nil
}

func (s *APIKeyService) requireActiveCreditsWallet(ctx context.Context, userID int64) error {
	if s.userSubRepo == nil {
		return infraerrors.InternalServer("SUBSCRIPTION_REPOSITORY_UNAVAILABLE", "subscription repository is not configured")
	}
	wallet, err := s.userSubRepo.GetActiveCreditsWalletByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrSubscriptionNotFound) {
			return ErrActiveCreditsWalletRequired
		}
		return fmt.Errorf("get active credits wallet: %w", err)
	}
	if wallet == nil || !wallet.IsActive() || !wallet.IsUniversalWalletMode() {
		return ErrActiveCreditsWalletRequired
	}
	return nil
}

// EnsureWalletGroupKeys 钱包激活 / 充值时为用户按 groupIDs 建/复用 N 把分组 key。
//
// 命名：每把 key 命名为 "钱包-{group.Name}"。
// 幂等：若用户名下已有 (groupID, name="钱包-{Name}", status=active, not expired/exhausted)
//
//	的 key → 复用；否则新建。
//
// 返回：(allKeys, createdCount, err)。allKeys 顺序按入参 groupIDs。
func (s *APIKeyService) EnsureWalletGroupKeys(ctx context.Context, userID int64, groupIDs []int64) ([]APIKey, int, error) {
	if len(groupIDs) == 0 {
		return nil, 0, nil
	}

	// 查用户所有 active key 一次，避免每个 group 查一次
	existingKeys, _, err := s.List(ctx, userID, pagination.PaginationParams{
		Page:      1,
		PageSize:  500,
		SortBy:    "created_at",
		SortOrder: "desc",
	}, APIKeyListFilters{Status: StatusAPIKeyActive})
	if err != nil {
		return nil, 0, err
	}

	// 按 groupID 索引可复用的钱包 key
	reusable := make(map[int64]*APIKey, len(existingKeys))
	for i := range existingKeys {
		key := &existingKeys[i]
		if key.GroupID == nil || !IsWalletGroupKeyName(key.Name) {
			continue
		}
		if !key.IsActive() || key.IsExpired() || key.IsQuotaExhausted() {
			continue
		}
		reusable[*key.GroupID] = key
	}

	result := make([]APIKey, 0, len(groupIDs))
	created := 0
	for _, gid := range groupIDs {
		if exist, ok := reusable[gid]; ok {
			result = append(result, *exist)
			continue
		}
		group, err := s.groupRepo.GetByID(ctx, gid)
		if err != nil {
			return nil, created, fmt.Errorf("get group %d: %w", gid, err)
		}
		if group == nil {
			continue
		}
		newKey, err := s.Create(ctx, userID, CreateAPIKeyRequest{
			Name:    walletGroupKeyName(group),
			GroupID: &gid,
		})
		if err != nil {
			return nil, created, fmt.Errorf("create wallet group key for group %d: %w", gid, err)
		}
		created++
		result = append(result, *newKey)
	}
	return result, created, nil
}

func (s *APIKeyService) GetWalletModelRoutes(ctx context.Context, userID int64, modelRoutes []ModelRoute) ([]WalletModelRouteInfo, error) {
	if len(modelRoutes) == 0 {
		modelRoutes = DefaultModelRoutes()
	}
	if s.userRepo == nil {
		return nil, fmt.Errorf("wallet route user repository is unavailable")
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get wallet route user: %w", err)
	}
	if user == nil {
		return nil, ErrUserNotFound
	}

	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active groups: %w", err)
	}
	groupsByName := make(map[string]Group, len(groups))
	for _, group := range groups {
		if group.Status == StatusActive {
			groupsByName[group.Name] = group
		}
	}

	userRates, err := s.GetUserGroupRates(ctx, userID)
	if err != nil {
		return nil, err
	}

	out := make([]WalletModelRouteInfo, 0, len(modelRoutes))
	for _, route := range modelRoutes {
		group, ok := groupsByName[route.GroupName]
		if !ok || !CanUseWalletGroup(user, &group) {
			continue
		}
		effectiveRate := group.RateMultiplier
		if override, ok := userRates[group.ID]; ok {
			effectiveRate = override
		}
		out = append(out, WalletModelRouteInfo{
			Pattern:                 route.Pattern,
			ExampleModel:            route.ExampleModel,
			GroupID:                 group.ID,
			GroupName:               group.Name,
			Platform:                group.Platform,
			RateMultiplier:          group.RateMultiplier,
			EffectiveRateMultiplier: effectiveRate,
		})
	}
	return out, nil
}

func (s *APIKeyService) VerifyOwnership(ctx context.Context, userID int64, apiKeyIDs []int64) ([]int64, error) {
	if len(apiKeyIDs) == 0 {
		return []int64{}, nil
	}

	validIDs, err := s.apiKeyRepo.VerifyOwnership(ctx, userID, apiKeyIDs)
	if err != nil {
		return nil, fmt.Errorf("verify api key ownership: %w", err)
	}
	return validIDs, nil
}

// GetByID 根据ID获取API Key
func (s *APIKeyService) GetByID(ctx context.Context, id int64) (*APIKey, error) {
	apiKey, err := s.apiKeyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	s.compileAPIKeyIPRules(apiKey)
	return apiKey, nil
}

// RevealVerificationMode tells the handler which fresh proof is required.
// TOTP is preferred whenever the user enabled it; otherwise the current
// password is required.
func (s *APIKeyService) RevealVerificationMode(ctx context.Context, userID int64) (string, error) {
	if s == nil || s.userRepo == nil {
		return "", fmt.Errorf("user repository is unavailable")
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("get API key owner: %w", err)
	}
	if user.TotpEnabled {
		return "totp", nil
	}
	return "password", nil
}

func (s *APIKeyService) VerifyRevealPassword(ctx context.Context, userID int64, password string) error {
	return s.VerifyStepUpPassword(ctx, userID, password, APIKeyStepUpPurposeReveal)
}

func (s *APIKeyService) VerifyStepUpPassword(ctx context.Context, userID int64, password, purpose string) error {
	if purpose != APIKeyStepUpPurposeReveal && purpose != APIKeyStepUpPurposeCreate && purpose != APIKeyStepUpPurposeUpdate {
		return ErrAPIKeyRevealUnavailable
	}
	if s == nil || s.cache == nil {
		return ErrAPIKeyRevealUnavailable
	}
	stepUpCache, ok := s.cache.(APIKeyStepUpAttemptCache)
	if !ok {
		return ErrAPIKeyRevealUnavailable
	}
	count, err := stepUpCache.IncrementAPIKeyStepUpAttempt(ctx, purpose, userID)
	if err != nil {
		return ErrAPIKeyRevealUnavailable
	}
	if count > apiKeyMaxErrorsPerHour {
		return ErrAPIKeyRateLimited
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.userRepo == nil || strings.TrimSpace(password) == "" {
		return ErrAPIKeyRevealVerification
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil || user == nil || !user.CheckPassword(password) {
		return ErrAPIKeyRevealVerification
	}
	if err := stepUpCache.DeleteAPIKeyStepUpAttempts(ctx, purpose, userID); err != nil {
		return ErrAPIKeyRevealUnavailable
	}
	return nil
}

// Reveal returns one owned API key after the handler has completed fresh
// password/TOTP verification. Callers must never persist the returned value.
func (s *APIKeyService) Reveal(ctx context.Context, id, userID int64) (string, error) {
	apiKey, err := s.apiKeyRepo.GetByID(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get API key: %w", err)
	}
	if apiKey.UserID != userID {
		return "", ErrInsufficientPerms
	}
	if strings.TrimSpace(apiKey.Key) == "" {
		return "", ErrAPIKeyNotFound
	}
	return apiKey.Key, nil
}

// GetByKey 根据Key字符串获取API Key（用于认证）
func (s *APIKeyService) GetByKey(ctx context.Context, key string) (*APIKey, error) {
	cacheKey := s.authCacheKey(key)

	if entry, ok := s.getAuthCacheEntry(ctx, cacheKey); ok {
		if apiKey, used, err := s.applyAuthCacheEntry(key, entry); used {
			if err != nil {
				return nil, fmt.Errorf("get api key: %w", err)
			}
			valid, validationErr := s.validateCachedAuthSnapshot(ctx, cacheKey, entry.Snapshot)
			if validationErr != nil {
				return nil, fmt.Errorf("get api key: %w", validationErr)
			}
			if valid {
				s.compileAPIKeyIPRules(apiKey)
				return apiKey, nil
			}
			// The cached authorization state changed (or this repository cannot
			// safely validate it). Clear what we can, then reload from the DB.
			// Redis failures are deliberately ignored here: every future cache hit
			// is independently validated before it can authorize a request.
			s.deleteAuthCache(ctx, cacheKey)
		}
	}

	if s.authCfg.singleflight {
		value, err, _ := s.authGroup.Do(cacheKey, func() (any, error) {
			return s.loadAuthCacheEntry(ctx, key, cacheKey)
		})
		if err != nil {
			return nil, err
		}
		entry, _ := value.(*APIKeyAuthCacheEntry)
		if apiKey, used, err := s.applyAuthCacheEntry(key, entry); used {
			if err != nil {
				return nil, fmt.Errorf("get api key: %w", err)
			}
			s.compileAPIKeyIPRules(apiKey)
			return apiKey, nil
		}
	} else {
		entry, err := s.loadAuthCacheEntry(ctx, key, cacheKey)
		if err != nil {
			return nil, err
		}
		if apiKey, used, err := s.applyAuthCacheEntry(key, entry); used {
			if err != nil {
				return nil, fmt.Errorf("get api key: %w", err)
			}
			s.compileAPIKeyIPRules(apiKey)
			return apiKey, nil
		}
	}

	apiKey, err := s.apiKeyRepo.GetByKeyForAuth(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	apiKey.Key = key
	s.compileAPIKeyIPRules(apiKey)
	return apiKey, nil
}

// Update 更新API Key
func (s *APIKeyService) Update(ctx context.Context, id int64, userID int64, req UpdateAPIKeyRequest) (*APIKey, error) {
	apiKey, err := s.apiKeyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}

	// 验证所有权
	if apiKey.UserID != userID {
		return nil, ErrInsufficientPerms
	}
	if err := validateWalletUniversalKeyMutation(apiKey, req); err != nil {
		return nil, err
	}
	if req.Status != nil && apiKey.Status == StatusAPIKeyDisabled && *req.Status == StatusAPIKeyActive {
		return nil, ErrAPIKeyReactivationForbidden
	}
	if APIKeyUpdateRequiresStepUp(req) && !req.StepUpVerified {
		return nil, ErrAPIKeyUpdateVerification
	}

	// 验证 IP 白名单格式
	if req.IPWhitelist != nil && len(*req.IPWhitelist) > 0 {
		if invalid := ip.ValidateIPPatterns(*req.IPWhitelist); len(invalid) > 0 {
			return nil, fmt.Errorf("%w: %v", ErrInvalidIPPattern, invalid)
		}
	}

	// 验证 IP 黑名单格式
	if req.IPBlacklist != nil && len(*req.IPBlacklist) > 0 {
		if invalid := ip.ValidateIPPatterns(*req.IPBlacklist); len(invalid) > 0 {
			return nil, fmt.Errorf("%w: %v", ErrInvalidIPPattern, invalid)
		}
	}

	// 更新字段
	if req.Name != nil {
		apiKey.Name = *req.Name
	}

	if req.GroupID != nil {
		// 验证分组权限
		user, err := s.userRepo.GetByID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("get user: %w", err)
		}

		group, err := s.groupRepo.GetByID(ctx, *req.GroupID)
		if err != nil {
			return nil, fmt.Errorf("get group: %w", err)
		}

		if !s.canUserBindGroup(ctx, user, group) {
			return nil, ErrGroupNotAllowed
		}

		apiKey.GroupID = req.GroupID
	}

	if req.Status != nil {
		apiKey.Status = *req.Status
		// 如果状态改变，清除Redis缓存
		if s.cache != nil {
			_ = s.cache.DeleteCreateAttemptCount(ctx, apiKey.UserID)
		}
	}

	// Update quota fields
	if req.Quota != nil {
		apiKey.Quota = *req.Quota
		// If quota is increased and status was quota_exhausted, reactivate
		if apiKey.Status == StatusAPIKeyQuotaExhausted && *req.Quota > apiKey.QuotaUsed {
			apiKey.Status = StatusActive
		}
	}
	if req.ResetQuota != nil && *req.ResetQuota {
		apiKey.QuotaUsed = 0
		// If resetting quota and status was quota_exhausted, reactivate
		if apiKey.Status == StatusAPIKeyQuotaExhausted {
			apiKey.Status = StatusActive
		}
	}
	if req.ClearExpiration {
		apiKey.ExpiresAt = nil
		// If clearing expiry and status was expired, reactivate
		if apiKey.Status == StatusAPIKeyExpired {
			apiKey.Status = StatusActive
		}
	} else if req.ExpiresAt != nil {
		apiKey.ExpiresAt = req.ExpiresAt
		// If extending expiry and status was expired, reactivate
		if apiKey.Status == StatusAPIKeyExpired && time.Now().Before(*req.ExpiresAt) {
			apiKey.Status = StatusActive
		}
	}

	// Omitted policies retain their current security boundary. Only an explicit
	// JSON array updates the field; an explicit empty array clears it.
	if req.IPWhitelist != nil {
		apiKey.IPWhitelist = append([]string(nil), (*req.IPWhitelist)...)
	}
	if req.IPBlacklist != nil {
		apiKey.IPBlacklist = append([]string(nil), (*req.IPBlacklist)...)
	}

	// Update rate limit configuration
	if req.RateLimit5h != nil {
		apiKey.RateLimit5h = *req.RateLimit5h
	}
	if req.RateLimit1d != nil {
		apiKey.RateLimit1d = *req.RateLimit1d
	}
	if req.RateLimit7d != nil {
		apiKey.RateLimit7d = *req.RateLimit7d
	}
	resetRateLimit := req.ResetRateLimitUsage != nil && *req.ResetRateLimitUsage
	if resetRateLimit {
		apiKey.Usage5h = 0
		apiKey.Usage1d = 0
		apiKey.Usage7d = 0
		apiKey.Window5hStart = nil
		apiKey.Window1dStart = nil
		apiKey.Window7dStart = nil
	}

	if err := s.apiKeyRepo.Update(ctx, apiKey); err != nil {
		return nil, fmt.Errorf("update api key: %w", err)
	}

	s.InvalidateAuthCacheByKey(ctx, apiKey.Key)
	s.compileAPIKeyIPRules(apiKey)

	// Invalidate Redis rate limit cache so reset takes effect immediately
	if resetRateLimit && s.rateLimitCacheInvalid != nil {
		_ = s.rateLimitCacheInvalid.InvalidateAPIKeyRateLimit(ctx, apiKey.ID)
	}

	return apiKey, nil
}

// Delete 删除API Key
func (s *APIKeyService) Delete(ctx context.Context, id int64, userID int64) error {
	apiKey, err := s.apiKeyRepo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get api key: %w", err)
	}

	// 验证当前用户是否为该 API Key 的所有者
	if apiKey.UserID != userID {
		return ErrInsufficientPerms
	}
	if apiKey.IsWalletUniversal() {
		return ErrWalletUniversalKeyDelete
	}

	// 清除Redis缓存（使用 userID 而非 apiKey.UserID）
	if s.cache != nil {
		_ = s.cache.DeleteCreateAttemptCount(ctx, userID)
	}
	s.InvalidateAuthCacheByKey(ctx, apiKey.Key)

	if err := s.apiKeyRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	s.lastUsedTouchL1.Delete(id)

	return nil
}

// ValidateKey 验证API Key是否有效（用于认证中间件）
func (s *APIKeyService) ValidateKey(ctx context.Context, key string) (*APIKey, *User, error) {
	// 获取API Key
	apiKey, err := s.GetByKey(ctx, key)
	if err != nil {
		return nil, nil, err
	}

	// 检查API Key状态
	if !apiKey.IsActive() {
		return nil, nil, infraerrors.Unauthorized("API_KEY_INACTIVE", "api key is not active")
	}

	// 获取用户信息
	user, err := s.userRepo.GetByID(ctx, apiKey.UserID)
	if err != nil {
		return nil, nil, fmt.Errorf("get user: %w", err)
	}

	// 检查用户状态
	if !user.IsActive() {
		return nil, nil, ErrUserNotActive
	}

	return apiKey, user, nil
}

// TouchLastUsed 通过防抖更新 api_keys.last_used_at，减少高频写放大。
// 该操作为尽力而为，不应阻塞主请求链路。
func (s *APIKeyService) TouchLastUsed(ctx context.Context, keyID int64) error {
	if keyID <= 0 {
		return nil
	}

	now := time.Now()
	if v, ok := s.lastUsedTouchL1.Load(keyID); ok {
		if nextAllowedAt, ok := v.(time.Time); ok && now.Before(nextAllowedAt) {
			return nil
		}
	}

	_, err, _ := s.lastUsedTouchSF.Do(strconv.FormatInt(keyID, 10), func() (any, error) {
		latest := time.Now()
		if v, ok := s.lastUsedTouchL1.Load(keyID); ok {
			if nextAllowedAt, ok := v.(time.Time); ok && latest.Before(nextAllowedAt) {
				return nil, nil
			}
		}

		if err := s.apiKeyRepo.UpdateLastUsed(ctx, keyID, latest); err != nil {
			s.lastUsedTouchL1.Store(keyID, latest.Add(apiKeyLastUsedFailBackoff))
			return nil, fmt.Errorf("touch api key last used: %w", err)
		}
		s.lastUsedTouchL1.Store(keyID, latest.Add(apiKeyLastUsedMinTouch))
		return nil, nil
	})
	return err
}

// IncrementUsage 增加API Key使用次数（可选：用于统计）
func (s *APIKeyService) IncrementUsage(ctx context.Context, keyID int64) error {
	// 使用Redis计数器
	if s.cache != nil {
		cacheKey := fmt.Sprintf("apikey:usage:%d:%s", keyID, timezone.Now().Format("2006-01-02"))
		if err := s.cache.IncrementDailyUsage(ctx, cacheKey); err != nil {
			return fmt.Errorf("increment usage: %w", err)
		}
		// 设置24小时过期
		_ = s.cache.SetDailyUsageExpiry(ctx, cacheKey, 24*time.Hour)
	}
	return nil
}

// GetAvailableGroups 获取用户有权限绑定的分组列表
// 返回用户可以选择的分组：
// - 标准类型分组：公开的（非专属）或用户被明确允许的
// - 订阅类型分组：用户有有效订阅的
func (s *APIKeyService) GetAvailableGroups(ctx context.Context, userID int64) ([]Group, error) {
	// 获取用户信息
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}

	// 获取所有活跃分组
	allGroups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active groups: %w", err)
	}

	// 获取用户的所有有效订阅
	activeSubscriptions, err := s.userSubRepo.ListActiveByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list active subscriptions: %w", err)
	}

	// 构建订阅分组 ID 集合
	subscribedGroupIDs := make(map[int64]bool)
	for _, sub := range activeSubscriptions {
		if sub.GroupID != nil {
			subscribedGroupIDs[*sub.GroupID] = true
		}
	}

	// 过滤出用户有权限的分组
	availableGroups := make([]Group, 0)
	for _, group := range allGroups {
		if s.canUserBindGroupInternal(user, &group, subscribedGroupIDs) {
			availableGroups = append(availableGroups, group)
		}
	}

	return availableGroups, nil
}

// canUserBindGroupInternal 内部方法，检查用户是否可以绑定分组（使用预加载的订阅数据）
func (s *APIKeyService) canUserBindGroupInternal(user *User, group *Group, subscribedGroupIDs map[int64]bool) bool {
	// 订阅类型分组：需要有效订阅
	if group.IsSubscriptionType() {
		return subscribedGroupIDs[group.ID]
	}
	// 标准类型分组：使用原有逻辑
	return user.CanBindGroup(group.ID, group.IsExclusive)
}

func (s *APIKeyService) SearchAPIKeys(ctx context.Context, userID int64, keyword string, limit int) ([]APIKey, error) {
	keys, err := s.apiKeyRepo.SearchAPIKeys(ctx, userID, keyword, limit)
	if err != nil {
		return nil, fmt.Errorf("search api keys: %w", err)
	}
	return keys, nil
}

// GetUserGroupRates 获取用户的专属分组倍率配置
// 返回 map[groupID]rateMultiplier
func (s *APIKeyService) GetUserGroupRates(ctx context.Context, userID int64) (map[int64]float64, error) {
	if s.userGroupRateRepo == nil {
		return nil, nil
	}
	rates, err := s.userGroupRateRepo.GetByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user group rates: %w", err)
	}
	return rates, nil
}

// CheckAPIKeyQuotaAndExpiry checks if the API key is valid for use (not expired, quota not exhausted)
// Returns nil if valid, error if invalid
func (s *APIKeyService) CheckAPIKeyQuotaAndExpiry(apiKey *APIKey) error {
	// Check expiration
	if apiKey.IsExpired() {
		return ErrAPIKeyExpired
	}

	// Check quota
	if apiKey.IsQuotaExhausted() {
		return ErrAPIKeyQuotaExhausted
	}

	return nil
}

// UpdateQuotaUsed updates the quota_used field after a request
// Also checks if quota is exhausted and updates status accordingly
func (s *APIKeyService) UpdateQuotaUsed(ctx context.Context, apiKeyID int64, cost float64) error {
	if cost <= 0 {
		return nil
	}

	type quotaStateReader interface {
		IncrementQuotaUsedAndGetState(ctx context.Context, id int64, amount float64) (*APIKeyQuotaUsageState, error)
	}

	if repo, ok := s.apiKeyRepo.(quotaStateReader); ok {
		state, err := repo.IncrementQuotaUsedAndGetState(ctx, apiKeyID, cost)
		if err != nil {
			return fmt.Errorf("increment quota used: %w", err)
		}
		if state != nil && state.Status == StatusAPIKeyQuotaExhausted && strings.TrimSpace(state.AuthCacheLocator) != "" {
			if err := s.InvalidateAuthCacheByLocatorReliable(ctx, state.AuthCacheLocator); err != nil {
				return fmt.Errorf("invalidate exhausted API key authorization: %w", err)
			}
		}
		return nil
	}

	// Use repository to atomically increment quota_used
	newQuotaUsed, err := s.apiKeyRepo.IncrementQuotaUsed(ctx, apiKeyID, cost)
	if err != nil {
		return fmt.Errorf("increment quota used: %w", err)
	}

	// Check if quota is now exhausted and update status if needed
	apiKey, err := s.apiKeyRepo.GetByID(ctx, apiKeyID)
	if err != nil {
		return nil // Don't fail the request, just log
	}

	// If quota is set and now exhausted, update status
	if apiKey.Quota > 0 && newQuotaUsed >= apiKey.Quota {
		apiKey.Status = StatusAPIKeyQuotaExhausted
		if err := s.apiKeyRepo.Update(ctx, apiKey); err != nil {
			return nil // Don't fail the request
		}
		// Invalidate cache so next request sees the new status
		s.InvalidateAuthCacheByKey(ctx, apiKey.Key)
	}

	return nil
}

// GetRateLimitData returns rate limit usage and window state for an API key.
func (s *APIKeyService) GetRateLimitData(ctx context.Context, id int64) (*APIKeyRateLimitData, error) {
	return s.apiKeyRepo.GetRateLimitData(ctx, id)
}

// UpdateRateLimitUsage atomically increments rate limit usage counters in the DB.
func (s *APIKeyService) UpdateRateLimitUsage(ctx context.Context, apiKeyID int64, cost float64) error {
	if cost <= 0 {
		return nil
	}
	return s.apiKeyRepo.IncrementRateLimitUsage(ctx, apiKeyID, cost)
}
