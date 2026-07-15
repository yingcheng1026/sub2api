package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplan"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplangroup"
	"github.com/Wei-Shaw/sub2api/ent/userallowedgroup"
	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/dgraph-io/ristretto"
	"golang.org/x/sync/singleflight"
)

// MaxExpiresAt is the maximum allowed expiration date (year 2099)
// This prevents time.Time JSON serialization errors (RFC 3339 requires year <= 9999)
var MaxExpiresAt = time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)

// MaxValidityDays is the maximum allowed validity days for subscriptions (100 years)
const MaxValidityDays = 36500

var (
	ErrSubscriptionNotFound             = infraerrors.NotFound("SUBSCRIPTION_NOT_FOUND", "subscription not found")
	ErrSubscriptionExpired              = infraerrors.Forbidden("SUBSCRIPTION_EXPIRED", "subscription has expired")
	ErrSubscriptionSuspended            = infraerrors.Forbidden("SUBSCRIPTION_SUSPENDED", "subscription is suspended")
	ErrSubscriptionAlreadyExists        = infraerrors.Conflict("SUBSCRIPTION_ALREADY_EXISTS", "subscription already exists for this user and group")
	ErrSubscriptionAssignConflict       = infraerrors.Conflict("SUBSCRIPTION_ASSIGN_CONFLICT", "subscription exists but request conflicts with existing assignment semantics")
	ErrSubscriptionUsageBillingInFlight = infraerrors.Conflict(
		"SUBSCRIPTION_USAGE_BILLING_IN_FLIGHT",
		"wallet has in-flight usage billing; reconcile it before revoking the subscription",
	)
	ErrGroupNotSubscriptionType = infraerrors.BadRequest("GROUP_NOT_SUBSCRIPTION_TYPE", "group is not a subscription type")
	ErrInvalidInput             = infraerrors.BadRequest("INVALID_INPUT", "at least one of resetDaily, resetWeekly, or resetMonthly must be true")
	ErrDailyLimitExceeded       = infraerrors.TooManyRequests("DAILY_LIMIT_EXCEEDED", "daily usage limit exceeded")
	ErrWeeklyLimitExceeded      = infraerrors.TooManyRequests("WEEKLY_LIMIT_EXCEEDED", "weekly usage limit exceeded")
	ErrMonthlyLimitExceeded     = infraerrors.TooManyRequests("MONTHLY_LIMIT_EXCEEDED", "monthly usage limit exceeded")
	ErrSubscriptionNilInput     = infraerrors.BadRequest("SUBSCRIPTION_NIL_INPUT", "subscription input cannot be nil")
	ErrAdjustWouldExpire        = infraerrors.BadRequest("ADJUST_WOULD_EXPIRE", "adjustment would result in expired subscription (remaining days must be > 0)")
)

// SubscriptionService 订阅服务
type SubscriptionService struct {
	groupRepo           GroupRepository
	userSubRepo         UserSubscriptionRepository
	billingCacheService *BillingCacheService
	entClient           *dbent.Client
	walletKeyService    WalletGroupKeyService
	walletTopupService  WalletTopupService

	// L1 缓存：加速中间件热路径的订阅查询
	subCacheL1     *ristretto.Cache
	subCacheGroup  singleflight.Group
	subCacheTTL    time.Duration
	subCacheJitter int // 抖动百分比

	maintenanceQueue *SubscriptionMaintenanceQueue
}

// subscriptionAssignmentLocker is implemented by the PostgreSQL repository.
// It serializes entitlement mutations for one user inside the caller's
// transaction, so concurrent paid orders cannot both extend from the same
// stale expiration timestamp. In-memory test repositories need not implement
// it because they do not provide database transaction semantics.
type subscriptionAssignmentLocker interface {
	LockUserForSubscriptionAssignment(ctx context.Context, userID int64) error
}

// WalletGroupKeyService 钱包 key 服务接口。
//
// 5/14 反转决策（参见 docs/plans/2026-05-14-wallet-single-key-reversal.md）：
// 激活流程走 EnsureWalletUniversalKey 单 key 路径，建 1 把 group_id=NULL 通用 key，
// 靠 model_router (B1.1/B1.2) 按调用模型自动路由到对应 group。
// EnsureWalletGroupKeys 多 key 路径保留作底层能力，激活不再调用（不删，将来要切回快速）。
type WalletGroupKeyService interface {
	EnsureWalletGroupKeys(ctx context.Context, userID int64, groupIDs []int64) ([]APIKey, int, error)
	EnsureWalletUniversalKey(ctx context.Context, userID int64) (*APIKey, bool, error)
}

// WalletTopupService 钱包叠加充值接口（B2.4）：把 deltaUSD 同时累加到现有钱包订阅的
// wallet_balance_usd 和 wallet_initial_usd，并写一条 reason='topup' 流水。
//
// SubscriptionService 在「用户已有 active 钱包 + 本次是额度卡 (plan_type='credits')」
// 场景下调用，避免为额度卡新建独立 wallet 行。WalletService 实现此接口。
type WalletTopupService interface {
	Activate(ctx context.Context, subscriptionID int64, initialUSD float64, operatorID *int64, notes string) (WalletLedgerEntry, error)
	Topup(ctx context.Context, subscriptionID int64, deltaUSD float64, operatorID *int64, notes string) (WalletLedgerEntry, error)
}

// WalletPaymentSourceTopupService records the immutable payment order that
// created a credits activation/top-up. Payment fulfillment must use these
// methods so a later refund can reverse exactly that purchase delta.
type WalletPaymentSourceTopupService interface {
	ActivateFromPayment(ctx context.Context, subscriptionID int64, initialUSD float64, paymentOrderID int64, notes string) (WalletLedgerEntry, error)
	TopupFromPayment(ctx context.Context, subscriptionID int64, deltaUSD float64, paymentOrderID int64, notes string) (WalletLedgerEntry, error)
}

// NewSubscriptionService 创建订阅服务
func NewSubscriptionService(groupRepo GroupRepository, userSubRepo UserSubscriptionRepository, billingCacheService *BillingCacheService, entClient *dbent.Client, cfg *config.Config) *SubscriptionService {
	svc := &SubscriptionService{
		groupRepo:           groupRepo,
		userSubRepo:         userSubRepo,
		billingCacheService: billingCacheService,
		entClient:           entClient,
	}
	svc.initSubCache(cfg)
	svc.initMaintenanceQueue(cfg)
	return svc
}

func (s *SubscriptionService) SetWalletGroupKeyService(keyService WalletGroupKeyService) {
	s.walletKeyService = keyService
}

// SetWalletTopupService 注入钱包叠加服务。未注入时，已有 active 钱包 + 再来一张额度卡
// 直接返回 ErrSubscriptionAssignConflict（B2.4 之前的旧行为）。
func (s *SubscriptionService) SetWalletTopupService(topup WalletTopupService) {
	s.walletTopupService = topup
}

func (s *SubscriptionService) initMaintenanceQueue(cfg *config.Config) {
	if cfg == nil {
		return
	}
	mc := cfg.SubscriptionMaintenance
	if mc.WorkerCount <= 0 || mc.QueueSize <= 0 {
		return
	}
	s.maintenanceQueue = NewSubscriptionMaintenanceQueue(mc.WorkerCount, mc.QueueSize)
}

// Stop stops the maintenance worker pool.
func (s *SubscriptionService) Stop() {
	if s == nil {
		return
	}
	if s.maintenanceQueue != nil {
		s.maintenanceQueue.Stop()
	}
}

// initSubCache 初始化订阅 L1 缓存
func (s *SubscriptionService) initSubCache(cfg *config.Config) {
	if cfg == nil {
		return
	}
	sc := cfg.SubscriptionCache
	if sc.L1Size <= 0 || sc.L1TTLSeconds <= 0 {
		return
	}
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: int64(sc.L1Size) * 10,
		MaxCost:     int64(sc.L1Size),
		BufferItems: 64,
	})
	if err != nil {
		log.Printf("Warning: failed to init subscription L1 cache: %v", err)
		return
	}
	s.subCacheL1 = cache
	s.subCacheTTL = time.Duration(sc.L1TTLSeconds) * time.Second
	s.subCacheJitter = sc.JitterPercent
}

// subCacheKey 生成订阅缓存 key（热路径，避免 fmt.Sprintf 开销）
func subCacheKey(userID, groupID int64) string {
	return "sub:" + strconv.FormatInt(userID, 10) + ":" + strconv.FormatInt(groupID, 10)
}

// jitteredTTL 为 TTL 添加抖动，避免集中过期
func (s *SubscriptionService) jitteredTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 || s.subCacheJitter <= 0 {
		return ttl
	}
	pct := s.subCacheJitter
	if pct > 100 {
		pct = 100
	}
	delta := float64(pct) / 100
	factor := 1 - delta + rand.Float64()*(2*delta)
	if factor <= 0 {
		return ttl
	}
	return time.Duration(float64(ttl) * factor)
}

// InvalidateSubCache 失效指定用户+分组的订阅 L1 缓存
func (s *SubscriptionService) InvalidateSubCache(userID, groupID int64) {
	if s.subCacheL1 == nil {
		return
	}
	s.subCacheL1.Del(subCacheKey(userID, groupID))
	s.subCacheL1.Wait()
}

// AssignSubscriptionInput 分配订阅输入
//
// 三种模式：
//   - Plan 模式：PlanID > 0, WalletInitialUSD == nil，由 plan 读取钱包额度/有效期
//   - Group 模式（v3）：GroupID > 0, WalletInitialUSD == nil
//   - 钱包模式 (v4)：WalletInitialUSD != nil, GroupID 忽略；用户级，与 group 解耦
type AssignSubscriptionInput struct {
	UserID       int64
	GroupID      int64
	ValidityDays int
	AssignedBy   int64
	Notes        string

	// WalletInitialUSD 非 nil → 走钱包路径：创建一条 group_id=NULL 的钱包订阅，
	// 初始余额=该值（同时写入 wallet_initial_usd 和 wallet_balance_usd）。
	// 月卡允许一个用户多条 active wallet 订阅并存，按 expires_at 先到期先消费。
	WalletInitialUSD *float64

	// PlanID 钱包模式下用于查 subscription_plan_groups 决定建哪些 group 的 key。
	// 为 nil 时跳过自动建 key（仅创建钱包订阅，admin 手动开通场景）。
	PlanID *int64

	// PlanType 钱包模式 plan 形态：subscription (月卡) / credits (额度卡)。
	// 空串视为 subscription。credits 走永久 expires_at（截断到 MaxExpiresAt 2099）。
	// 来源：payment_fulfillment.doWalletSub 读 SubscriptionPlan.PlanType 透传。
	PlanType string

	// PaymentOrderID is set only by payment fulfillment. It is persisted on the
	// activation/topup ledger row as the immutable source for refund reversal.
	PaymentOrderID *int64
}

// IsCreditsAssign 当前 input 是否为额度卡（永久有效）分配。
func (i *AssignSubscriptionInput) IsCreditsAssign() bool {
	return i != nil && i.PlanType == PlanTypeCredits
}

// IsWalletAssign 判断当前 input 是否为钱包模式分配。
func (i *AssignSubscriptionInput) IsWalletAssign() bool {
	return i != nil && i.WalletInitialUSD != nil
}

// RunAssignmentTransaction executes an admin assignment workflow in one Ent
// transaction. The caller may include the idempotency claim/result and
// auxiliary writes (for example affiliate ledger entries) in execute so that
// a commit is all-or-nothing across every financial side effect.
func (s *SubscriptionService) RunAssignmentTransaction(ctx context.Context, execute func(context.Context) error) error {
	if execute == nil {
		return infraerrors.InternalServer("ASSIGNMENT_TRANSACTION_EXECUTOR_NIL", "assignment transaction executor is nil")
	}
	if dbent.TxFromContext(ctx) != nil {
		return execute(ctx)
	}
	if s == nil || s.entClient == nil {
		return infraerrors.ServiceUnavailable("ASSIGNMENT_TRANSACTION_UNAVAILABLE", "assignment transaction is unavailable")
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin assignment transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := execute(txCtx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit assignment transaction: %w", err)
	}
	return nil
}

// AssignSubscription 分配订阅给用户（不允许重复分配）
func (s *SubscriptionService) AssignSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, error) {
	normalized, err := s.normalizePlanWalletAssignInput(ctx, input)
	if err != nil {
		return nil, err
	}
	if normalized.IsWalletAssign() {
		sub, assignErr := s.assignWalletSubscriptionAtomic(ctx, normalized)
		return assignmentResultWithPlanType(sub, normalized), assignErr
	}
	if s.entClient != nil && dbent.TxFromContext(ctx) == nil {
		sub, assignErr := s.assignGroupSubscriptionAtomic(ctx, normalized)
		return assignmentResultWithPlanType(sub, normalized), assignErr
	}
	sub, _, err := s.assignSubscriptionWithReuse(ctx, normalized)
	if err != nil {
		return nil, err
	}
	return assignmentResultWithPlanType(sub, normalized), nil
}

func assignmentResultWithPlanType(sub *UserSubscription, input *AssignSubscriptionInput) *UserSubscription {
	if sub == nil || input == nil || input.PlanID == nil {
		return sub
	}
	result := *sub
	result.AssignmentPlanType = input.PlanType
	return &result
}

func (s *SubscriptionService) assignGroupSubscriptionAtomic(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, error) {
	sub, _, err := s.assignGroupSubscriptionWithReuseAtomic(ctx, input)
	return sub, err
}

func (s *SubscriptionService) assignGroupSubscriptionWithReuseAtomic(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, bool, error) {
	if s.entClient == nil || dbent.TxFromContext(ctx) != nil {
		return s.assignSubscriptionWithReuse(ctx, input)
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin subscription assignment transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	sub, reused, err := s.assignSubscriptionWithReuse(txCtx, input)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit subscription assignment transaction: %w", err)
	}
	return sub, reused, nil
}

func (s *SubscriptionService) assignWalletSubscriptionAtomic(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, error) {
	if s.entClient == nil || dbent.TxFromContext(ctx) != nil {
		sub, _, err := s.assignWalletSubscriptionWithReuse(ctx, input)
		return sub, err
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin wallet assignment transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	sub, _, err := s.assignWalletSubscriptionWithReuse(txCtx, input)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit wallet assignment transaction: %w", err)
	}
	return sub, nil
}

func (s *SubscriptionService) normalizePlanWalletAssignInput(ctx context.Context, input *AssignSubscriptionInput) (*AssignSubscriptionInput, error) {
	if input == nil {
		return nil, ErrSubscriptionNilInput
	}
	if input.PlanID == nil || input.WalletInitialUSD != nil {
		return input, nil
	}

	client := s.entClient
	if tx := dbent.TxFromContext(ctx); tx != nil {
		client = tx.Client()
	}
	if client == nil {
		return nil, infraerrors.BadRequest("SUBSCRIPTION_PLAN_UNAVAILABLE", "subscription plan lookup is unavailable")
	}

	plan, err := client.SubscriptionPlan.Query().
		Where(subscriptionplan.IDEQ(*input.PlanID)).
		Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, infraerrors.BadRequest("SUBSCRIPTION_PLAN_NOT_FOUND", "subscription plan not found")
	}
	if err != nil {
		return nil, err
	}
	planType, err := validatePlanType(plan.PlanType)
	if err != nil {
		return nil, err
	}
	if planType != PlanTypeCredits {
		primaryGroupID, err := s.resolveSubscriptionPlanPrimaryGroupID(ctx, client, plan)
		if err != nil {
			return nil, err
		}
		return &AssignSubscriptionInput{
			UserID:       input.UserID,
			GroupID:      primaryGroupID,
			ValidityDays: plan.ValidityDays,
			AssignedBy:   input.AssignedBy,
			Notes:        input.Notes,
			PlanID:       input.PlanID,
			PlanType:     planType,
		}, nil
	}
	if plan.WalletQuotaUsd == nil || *plan.WalletQuotaUsd <= 0 {
		return nil, infraerrors.BadRequest("SUBSCRIPTION_PLAN_NOT_WALLET", "credits plan is not a wallet plan")
	}

	walletInitial := *plan.WalletQuotaUsd
	return &AssignSubscriptionInput{
		UserID:           input.UserID,
		GroupID:          input.GroupID,
		ValidityDays:     plan.ValidityDays,
		AssignedBy:       input.AssignedBy,
		Notes:            input.Notes,
		WalletInitialUSD: &walletInitial,
		PlanID:           input.PlanID,
		PlanType:         planType,
	}, nil
}

func (s *SubscriptionService) resolveSubscriptionPlanPrimaryGroupID(ctx context.Context, client *dbent.Client, plan *dbent.SubscriptionPlan) (int64, error) {
	if plan == nil {
		return 0, infraerrors.BadRequest("SUBSCRIPTION_PLAN_NOT_FOUND", "subscription plan not found")
	}
	if plan.GroupID != nil && *plan.GroupID > 0 {
		return *plan.GroupID, nil
	}
	if client == nil {
		return 0, infraerrors.BadRequest("SUBSCRIPTION_PLAN_GROUP_REQUIRED", "subscription plan group lookup is unavailable")
	}
	groupIDs, err := client.SubscriptionPlanGroup.Query().
		Where(
			subscriptionplangroup.PlanIDEQ(plan.ID),
			subscriptionplangroup.HasGroupWith(
				group.SubscriptionTypeEQ(SubscriptionTypeSubscription),
				group.StatusEQ(StatusActive),
				group.DeletedAtIsNil(),
			),
		).
		Select(subscriptionplangroup.FieldGroupID).
		Ints(ctx)
	if err != nil {
		return 0, err
	}
	if len(groupIDs) != 1 {
		return 0, infraerrors.BadRequest("SUBSCRIPTION_PLAN_GROUP_REQUIRED", "monthly subscription plans must have exactly one active subscription group")
	}
	return int64(groupIDs[0]), nil
}

// AssignOrExtendSubscription 分配或续期订阅（用于兑换码等场景）
// 如果用户已有同分组的订阅：
//   - 未过期：从当前过期时间累加天数
//   - 已过期：从当前时间开始计算新的过期时间，并激活订阅
//
// 如果没有订阅：创建新订阅
func (s *SubscriptionService) AssignOrExtendSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, bool, error) {
	if input == nil {
		return nil, false, ErrSubscriptionNilInput
	}
	if s.entClient == nil || dbent.TxFromContext(ctx) != nil {
		return s.assignOrExtendSubscriptionInTransaction(ctx, input)
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin subscription assignment transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	sub, reused, err := s.assignOrExtendSubscriptionInTransaction(txCtx, input)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit subscription assignment transaction: %w", err)
	}
	s.invalidateSubscriptionCaches(ctx, input.UserID, input.GroupID)
	return sub, reused, nil
}

func (s *SubscriptionService) assignOrExtendSubscriptionInTransaction(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, bool, error) {
	group, err := s.groupRepo.GetByID(ctx, input.GroupID)
	if err != nil {
		return nil, false, fmt.Errorf("group not found: %w", err)
	}
	if !group.IsSubscriptionType() {
		return nil, false, ErrGroupNotSubscriptionType
	}
	if dbent.TxFromContext(ctx) != nil {
		if locker, ok := s.userSubRepo.(subscriptionAssignmentLocker); ok {
			if err := locker.LockUserForSubscriptionAssignment(ctx, input.UserID); err != nil {
				return nil, false, fmt.Errorf("lock subscription assignment: %w", err)
			}
		}
	}

	existingSub, err := s.userSubRepo.GetByUserIDAndGroupID(ctx, input.UserID, input.GroupID)
	if errors.Is(err, ErrSubscriptionNotFound) {
		existingSub = nil
	} else if err != nil {
		return nil, false, err
	}

	validityDays := normalizeAssignValidityDays(input.ValidityDays)
	if existingSub == nil {
		sub, err := s.createSubscription(ctx, input)
		return sub, false, err
	}

	now := time.Now()
	newExpiresAt := now.AddDate(0, 0, validityDays)
	if existingSub.ExpiresAt.After(now) {
		newExpiresAt = existingSub.ExpiresAt.AddDate(0, 0, validityDays)
	}
	if newExpiresAt.After(MaxExpiresAt) {
		newExpiresAt = MaxExpiresAt
	}
	if err := s.userSubRepo.ExtendExpiry(ctx, existingSub.ID, newExpiresAt); err != nil {
		return nil, false, fmt.Errorf("extend subscription: %w", err)
	}
	if existingSub.Status != SubscriptionStatusActive {
		if err := s.userSubRepo.UpdateStatus(ctx, existingSub.ID, SubscriptionStatusActive); err != nil {
			return nil, false, fmt.Errorf("update subscription status: %w", err)
		}
	}
	if input.Notes != "" {
		newNotes := existingSub.Notes
		if newNotes != "" {
			newNotes += "\n"
		}
		newNotes += input.Notes
		if err := s.userSubRepo.UpdateNotes(ctx, existingSub.ID, newNotes); err != nil {
			return nil, false, fmt.Errorf("update subscription notes: %w", err)
		}
	}
	if err := s.ensureSubscriptionGroupAccess(ctx, input.UserID, input.GroupID); err != nil {
		return nil, false, err
	}
	sub, err := s.userSubRepo.GetByID(ctx, existingSub.ID)
	return sub, true, err
}

func (s *SubscriptionService) invalidateSubscriptionCaches(ctx context.Context, userID, groupID int64) {
	s.InvalidateSubCache(userID, groupID)
	if s.billingCacheService == nil {
		return
	}
	invalidateBillingCacheAfterCommit(ctx, "subscription mutation", func(cacheCtx context.Context) error {
		return s.billingCacheService.InvalidateSubscription(cacheCtx, userID, groupID)
	})
}

// InvalidateSubscriptionCachesAfterCommit reloads the authoritative
// subscription after an outer transaction has committed, then evicts both the
// process-local and shared billing caches. Re-reading by ID also makes an
// idempotent replay repair a cache invalidation that may have been interrupted
// after the original commit.
func (s *SubscriptionService) InvalidateSubscriptionCachesAfterCommit(ctx context.Context, subscriptionID int64) error {
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return fmt.Errorf("reload subscription for post-commit cache invalidation: %w", err)
	}
	if sub.GroupID == nil {
		return nil
	}
	s.invalidateSubscriptionCaches(ctx, sub.UserID, *sub.GroupID)
	return nil
}

// createSubscription 创建新订阅（内部方法）
func (s *SubscriptionService) createSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, error) {
	validityDays := input.ValidityDays
	if validityDays <= 0 {
		validityDays = 30
	}
	if validityDays > MaxValidityDays {
		validityDays = MaxValidityDays
	}

	now := time.Now()
	expiresAt := now.AddDate(0, 0, validityDays)
	if expiresAt.After(MaxExpiresAt) {
		expiresAt = MaxExpiresAt
	}

	groupID := input.GroupID
	sub := &UserSubscription{
		UserID:     input.UserID,
		GroupID:    &groupID,
		StartsAt:   now,
		ExpiresAt:  expiresAt,
		Status:     SubscriptionStatusActive,
		AssignedAt: now,
		Notes:      input.Notes,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	// 只有当 AssignedBy > 0 时才设置（0 表示系统分配，如兑换码）
	if input.AssignedBy > 0 {
		sub.AssignedBy = &input.AssignedBy
	}

	if err := s.userSubRepo.Create(ctx, sub); err != nil {
		return nil, err
	}
	if err := s.ensureSubscriptionGroupAccess(ctx, input.UserID, input.GroupID); err != nil {
		return nil, err
	}

	// 重新获取完整订阅信息（包含关联）
	return s.userSubRepo.GetByID(ctx, sub.ID)
}

func (s *SubscriptionService) ensureSubscriptionGroupAccess(ctx context.Context, userID, groupID int64) error {
	if userID <= 0 || groupID <= 0 || s.entClient == nil {
		return nil
	}
	client := s.entClient
	if tx := dbent.TxFromContext(ctx); tx != nil {
		client = tx.Client()
	}
	if err := client.UserAllowedGroup.Create().
		SetUserID(userID).
		SetGroupID(groupID).
		OnConflictColumns(userallowedgroup.FieldUserID, userallowedgroup.FieldGroupID).
		DoNothing().
		Exec(ctx); err != nil {
		return fmt.Errorf("sync subscription allowed group: %w", err)
	}
	return nil
}

// assignWalletSubscriptionWithReuse 钱包模式分配（v4）。
//
// 三条分支：
//  1. 用户没有 active 钱包，或本次是月卡（PlanType='subscription'）→ 新建独立行。
//     月卡叠月卡：允许多条 active wallet 并存，各自独立到期时间和余额，
//     计费时按 expires_at 升序选行（先到期先消费）。
//  2. 已有 active 钱包 + 本次是额度卡 (PlanType='credits') →
//     调用 walletTopupService.Topup，把 quota 合入现有钱包（balance+initial 双 +delta，
//     ledger 写 reason='topup'）；不新建 user_subscriptions 行。设计 §2.3。
//  3. 已有 active 钱包 + 本次是额度卡 + 未注入 topup 服务 →
//     返回 ErrSubscriptionAssignConflict（conflict_reason=wallet_topup_unsupported）。
//
// 不复用 group 路径的 ExistsByUserIDAndGroupID 是因为钱包订阅 group_id=NULL，
// 复合查询用 group_id=0 拿不到；改用 GetActiveWalletByUserID。
func (s *SubscriptionService) assignWalletSubscriptionWithReuse(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, bool, error) {
	if input.WalletInitialUSD == nil || *input.WalletInitialUSD <= 0 {
		return nil, false, fmt.Errorf("wallet_initial_usd must be > 0")
	}

	if !input.IsCreditsAssign() {
		return nil, false, infraerrors.BadRequest("MONTHLY_PLAN_WALLET_DISABLED", "monthly subscription plans must use group subscriptions")
	}

	// 额度卡：查找现有 active 钱包并叠加。
	existing, err := s.userSubRepo.GetActiveCreditsWalletByUserID(ctx, input.UserID)
	if err != nil && !errors.Is(err, ErrSubscriptionNotFound) {
		return nil, false, err
	}
	if existing != nil {
		return s.topupExistingWallet(ctx, input, existing)
	}

	// 额度卡但还没有任何 active 钱包 → 新建。
	sub, err := s.createWalletSubscription(ctx, input)
	if err != nil {
		return nil, false, err
	}
	if err := s.activateWalletSubscription(ctx, input, sub); err != nil {
		return nil, false, err
	}
	if err := s.ensureWalletGroupKeys(ctx, input, sub); err != nil {
		return nil, false, err
	}
	creditDelta := *input.WalletInitialUSD
	sub.WalletCreditDeltaUSD = &creditDelta

	// 钱包订阅与 group 解耦，不需要按 (user, group) 失效订阅缓存；
	// 中间件下次请求会直接 GetActiveWalletByUserID 命中。
	return sub, false, nil
}

func (s *SubscriptionService) activateWalletSubscription(ctx context.Context, input *AssignSubscriptionInput, sub *UserSubscription) error {
	if s.walletTopupService == nil {
		return infraerrors.InternalServer("WALLET_LIFECYCLE_UNAVAILABLE", "wallet lifecycle service is not configured")
	}
	if input == nil || input.WalletInitialUSD == nil || sub == nil {
		return ErrSubscriptionNilInput
	}
	var operatorID *int64
	if input.AssignedBy > 0 {
		op := input.AssignedBy
		operatorID = &op
	}
	var err error
	if input.PaymentOrderID != nil {
		sourced, ok := s.walletTopupService.(WalletPaymentSourceTopupService)
		if !ok {
			return infraerrors.InternalServer("WALLET_PAYMENT_SOURCE_UNAVAILABLE", "wallet lifecycle does not support immutable payment sources")
		}
		_, err = sourced.ActivateFromPayment(ctx, sub.ID, *input.WalletInitialUSD, *input.PaymentOrderID, strings.TrimSpace(input.Notes))
	} else {
		_, err = s.walletTopupService.Activate(ctx, sub.ID, *input.WalletInitialUSD, operatorID, strings.TrimSpace(input.Notes))
	}
	if err != nil {
		return fmt.Errorf("activate wallet ledger: %w", err)
	}
	return nil
}

// topupExistingWallet 用户已有 active 钱包 + credits → 走 B2.4 叠加路径或拒绝。
//
// 常规入口只会让 credits 调到这里；非 credits 若误入则返回 conflict 作为兜底。
// 未注入 walletTopupService 时返回 wallet_topup_unsupported，避免额度卡静默丢失。
//
// 返回 reused=true，sub.ID 不变（叠加在 existing 行上），但 WalletBalanceUSD /
// WalletInitialUSD 字段已更新到叠加后的值；同时 ensureWalletGroupKeys 也会跑一遍
// （幂等：缺哪把补哪把），保证额度卡新关联的 group 也能拿到 key。
func (s *SubscriptionService) topupExistingWallet(ctx context.Context, input *AssignSubscriptionInput, existing *UserSubscription) (*UserSubscription, bool, error) {
	if !input.IsCreditsAssign() {
		return nil, false, ErrSubscriptionAssignConflict.WithMetadata(map[string]string{
			"conflict_reason": "wallet_already_active",
		})
	}
	if s.walletTopupService == nil {
		return nil, false, ErrSubscriptionAssignConflict.WithMetadata(map[string]string{
			"conflict_reason": "wallet_topup_unsupported",
		})
	}

	delta := *input.WalletInitialUSD
	notes := fmt.Sprintf("credits topup: %s", strings.TrimSpace(input.Notes))
	var operator *int64
	if input.AssignedBy > 0 {
		op := input.AssignedBy
		operator = &op
	}
	var entry WalletLedgerEntry
	var err error
	if input.PaymentOrderID != nil {
		sourced, ok := s.walletTopupService.(WalletPaymentSourceTopupService)
		if !ok {
			return nil, false, infraerrors.InternalServer("WALLET_PAYMENT_SOURCE_UNAVAILABLE", "wallet lifecycle does not support immutable payment sources")
		}
		entry, err = sourced.TopupFromPayment(ctx, existing.ID, delta, *input.PaymentOrderID, notes)
	} else {
		entry, err = s.walletTopupService.Topup(ctx, existing.ID, delta, operator, notes)
	}
	if err != nil {
		return nil, false, fmt.Errorf("topup wallet: %w", err)
	}

	// 同步内存字段（避免再读一次 DB）。balance_after 来自 ledger 返回，权威。
	newBalance := entry.BalanceAfter
	newInitial := *existing.WalletInitialUSD + delta
	existing.WalletBalanceUSD = &newBalance
	existing.WalletInitialUSD = &newInitial
	existing.WalletCreditDeltaUSD = &delta

	// 额度卡新关联的 group 可能没建 key —— 跑一遍 ensureWalletGroupKeys 补缺。
	// 已有 key 复用，不重复建。
	if err := s.ensureWalletGroupKeys(ctx, input, existing); err != nil {
		return nil, false, err
	}

	return existing, true, nil
}

// ensureWalletGroupKeys 钱包激活/topup 时为用户建 1 把通用 key（group_id=NULL），
// 靠 model_router (B1.1/B1.2) 按调用模型自动路由。
//
// 5/14 反转决策（参见 docs/plans/2026-05-14-wallet-single-key-reversal.md）：
// 撤销 B2.2 多 key 改造，回到 B1.4 单 key 形态。多 key 路径 (EnsureWalletGroupKeys)
// 保留作底层能力，激活不再调用，函数名保留 ensureWalletGroupKeys 减少调用方改动。
//
// 失败策略：建 key 失败返回错误，钱包订阅在外层事务中应一并回滚。
func (s *SubscriptionService) ensureWalletGroupKeys(ctx context.Context, input *AssignSubscriptionInput, sub *UserSubscription) error {
	if s.walletKeyService == nil || sub == nil || input == nil {
		return nil
	}
	key, created, err := s.walletKeyService.EnsureWalletUniversalKey(ctx, input.UserID)
	if err != nil {
		return fmt.Errorf("ensure wallet universal api key: %w", err)
	}
	sub.WalletUniversalKey = key
	sub.WalletUniversalKeyCreated = created
	return nil
}

// lookupPlanGroupIDs 查询 plan 关联的 group ID 列表（subscription_plan_groups 表）。
//
// 5/14 反转决策后单 key 路径不再调用本方法；保留作多 key 路径（B2.2 EnsureWalletGroupKeys）
// 的底层能力，将来若切回多 key 形态直接复用，避免重写。
//
//nolint:unused // 反转决策保留底层能力，见 docs/plans/2026-05-14-wallet-single-key-reversal.md
func (s *SubscriptionService) lookupPlanGroupIDs(ctx context.Context, planID int64) ([]int64, error) {
	if s.entClient == nil {
		return nil, nil
	}
	rows, err := s.entClient.SubscriptionPlanGroup.Query().
		Where(subscriptionplangroup.PlanIDEQ(planID)).
		Select(subscriptionplangroup.FieldGroupID).
		Ints(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(rows))
	for _, v := range rows {
		out = append(out, int64(v))
	}
	return out, nil
}

// createWalletSubscription 创建新钱包订阅（内部方法）。group_id=NULL，
// wallet_initial_usd 和 wallet_balance_usd 都设为 input 给定的初始值。
//
// 月卡 (plan_type='subscription'，含空串默认)：expires_at = now + validity_days。
// 额度卡 (plan_type='credits')：expires_at = MaxExpiresAt (2099-12-31)，validity_days 忽略。
// 见 docs/plans/2026-05-13-wallet-multikey-credits-design.md §2.2。
func (s *SubscriptionService) createWalletSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, error) {
	if !input.IsCreditsAssign() {
		return nil, infraerrors.BadRequest("MONTHLY_PLAN_WALLET_DISABLED", "monthly subscription plans must use group subscriptions")
	}

	now := time.Now()
	expiresAt := MaxExpiresAt

	initial := *input.WalletInitialUSD
	balance := initial
	lockedRates, err := s.buildMonthlyLockedRatesForPlan(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("build monthly locked rates: %w", err)
	}
	sub := &UserSubscription{
		UserID:           input.UserID,
		GroupID:          nil,
		StartsAt:         now,
		ExpiresAt:        expiresAt,
		Status:           SubscriptionStatusActive,
		WalletInitialUSD: &initial,
		WalletBalanceUSD: &balance,
		LockedRates:      lockedRates,
		AssignedAt:       now,
		Notes:            input.Notes,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if input.AssignedBy > 0 {
		sub.AssignedBy = &input.AssignedBy
	}

	if err := s.userSubRepo.Create(ctx, sub); err != nil {
		return nil, err
	}
	return s.userSubRepo.GetByID(ctx, sub.ID)
}

// BulkAssignSubscriptionInput 批量分配订阅输入
type BulkAssignSubscriptionInput struct {
	UserIDs      []int64
	GroupID      int64
	ValidityDays int
	AssignedBy   int64
	Notes        string
}

// BulkAssignResult 批量分配结果
type BulkAssignResult struct {
	SuccessCount  int
	CreatedCount  int
	ReusedCount   int
	FailedCount   int
	Subscriptions []UserSubscription
	Errors        []string
	Statuses      map[int64]string
}

// BulkAssignSubscription 批量分配订阅
func (s *SubscriptionService) BulkAssignSubscription(ctx context.Context, input *BulkAssignSubscriptionInput) (*BulkAssignResult, error) {
	result := &BulkAssignResult{
		Subscriptions: make([]UserSubscription, 0),
		Errors:        make([]string, 0),
		Statuses:      make(map[int64]string),
	}

	for _, userID := range input.UserIDs {
		sub, reused, err := s.assignGroupSubscriptionWithReuseAtomic(ctx, &AssignSubscriptionInput{
			UserID:       userID,
			GroupID:      input.GroupID,
			ValidityDays: input.ValidityDays,
			AssignedBy:   input.AssignedBy,
			Notes:        input.Notes,
		})
		if err != nil {
			result.FailedCount++
			result.Errors = append(result.Errors, fmt.Sprintf("user %d: %v", userID, err))
			result.Statuses[userID] = "failed"
		} else {
			result.SuccessCount++
			result.Subscriptions = append(result.Subscriptions, *sub)
			if reused {
				result.ReusedCount++
				result.Statuses[userID] = "reused"
			} else {
				result.CreatedCount++
				result.Statuses[userID] = "created"
			}
		}
	}

	return result, nil
}

func (s *SubscriptionService) assignSubscriptionWithReuse(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, bool, error) {
	// 钱包模式 (v4)：跳过 group 校验，按 user 维度幂等
	if input.IsWalletAssign() {
		return s.assignWalletSubscriptionWithReuse(ctx, input)
	}

	// 检查分组是否存在且为订阅类型
	group, err := s.groupRepo.GetByID(ctx, input.GroupID)
	if err != nil {
		return nil, false, fmt.Errorf("group not found: %w", err)
	}
	if !group.IsSubscriptionType() {
		return nil, false, ErrGroupNotSubscriptionType
	}

	// 检查是否已存在订阅；若已存在，则按幂等成功返回现有订阅
	exists, err := s.userSubRepo.ExistsByUserIDAndGroupID(ctx, input.UserID, input.GroupID)
	if err != nil {
		return nil, false, err
	}
	if exists {
		sub, getErr := s.userSubRepo.GetByUserIDAndGroupID(ctx, input.UserID, input.GroupID)
		if getErr != nil {
			return nil, false, getErr
		}
		if conflictReason, conflict := detectAssignSemanticConflict(sub, input); conflict {
			return nil, false, ErrSubscriptionAssignConflict.WithMetadata(map[string]string{
				"conflict_reason": conflictReason,
			})
		}
		if err := s.ensureSubscriptionGroupAccess(ctx, input.UserID, input.GroupID); err != nil {
			return nil, false, err
		}
		return sub, true, nil
	}

	sub, err := s.createSubscription(ctx, input)
	if err != nil {
		return nil, false, err
	}

	// 完成缓存失效后再返回，避免刚分配成功却立即读到旧订阅状态。
	s.invalidateSubscriptionCaches(ctx, input.UserID, input.GroupID)

	return sub, false, nil
}

func detectAssignSemanticConflict(existing *UserSubscription, input *AssignSubscriptionInput) (string, bool) {
	if existing == nil || input == nil {
		return "", false
	}

	normalizedDays := normalizeAssignValidityDays(input.ValidityDays)
	if !existing.StartsAt.IsZero() {
		expectedExpiresAt := existing.StartsAt.AddDate(0, 0, normalizedDays)
		if expectedExpiresAt.After(MaxExpiresAt) {
			expectedExpiresAt = MaxExpiresAt
		}
		if !existing.ExpiresAt.Equal(expectedExpiresAt) {
			return "validity_days_mismatch", true
		}
	}

	existingNotes := strings.TrimSpace(existing.Notes)
	inputNotes := strings.TrimSpace(input.Notes)
	if existingNotes != inputNotes {
		return "notes_mismatch", true
	}

	return "", false
}

func normalizeAssignValidityDays(days int) int {
	if days <= 0 {
		days = 30
	}
	if days > MaxValidityDays {
		days = MaxValidityDays
	}
	return days
}

// RevokeSubscription 撤销订阅
func (s *SubscriptionService) RevokeSubscription(ctx context.Context, subscriptionID int64) error {
	// 先获取订阅信息用于失效缓存
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return err
	}

	if err := s.userSubRepo.Delete(ctx, subscriptionID); err != nil {
		return err
	}

	// 失效订阅缓存（钱包模式 sub.GroupID == nil，跳过 group 维度缓存）
	if sub.GroupID != nil {
		s.invalidateSubscriptionCaches(ctx, sub.UserID, *sub.GroupID)
	}

	return nil
}

// ExtendSubscription 调整订阅时长（正数延长，负数缩短）
func (s *SubscriptionService) ExtendSubscription(ctx context.Context, subscriptionID int64, days int) (*UserSubscription, error) {
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, ErrSubscriptionNotFound
	}

	// 限制调整天数范围
	if days > MaxValidityDays {
		days = MaxValidityDays
	}
	if days < -MaxValidityDays {
		days = -MaxValidityDays
	}

	now := time.Now()
	isExpired := !sub.ExpiresAt.After(now)

	// 如果订阅已过期，不允许负向调整
	if isExpired && days < 0 {
		return nil, infraerrors.BadRequest("CANNOT_SHORTEN_EXPIRED", "cannot shorten an expired subscription")
	}

	// 计算新的过期时间
	var newExpiresAt time.Time
	if isExpired {
		// 已过期：从当前时间开始增加天数
		newExpiresAt = now.AddDate(0, 0, days)
	} else {
		// 未过期：从原过期时间增加/减少天数
		newExpiresAt = sub.ExpiresAt.AddDate(0, 0, days)
	}

	if newExpiresAt.After(MaxExpiresAt) {
		newExpiresAt = MaxExpiresAt
	}

	// 检查新的过期时间必须大于当前时间
	if !newExpiresAt.After(now) {
		return nil, ErrAdjustWouldExpire
	}

	if err := s.userSubRepo.ExtendExpiry(ctx, subscriptionID, newExpiresAt); err != nil {
		return nil, err
	}

	// 如果订阅已过期，恢复为active状态
	if sub.Status == SubscriptionStatusExpired {
		if err := s.userSubRepo.UpdateStatus(ctx, subscriptionID, SubscriptionStatusActive); err != nil {
			return nil, err
		}
	}

	// An outer Ent transaction has not committed yet, so invalidating here can
	// race with a concurrent reader that refills the old positive cache. The
	// transactional admin path performs a second authoritative read and
	// invalidates after commit. Direct/autocommit callers can evict immediately.
	if sub.GroupID != nil && dbent.TxFromContext(ctx) == nil {
		s.invalidateSubscriptionCaches(ctx, sub.UserID, *sub.GroupID)
	}

	return s.userSubRepo.GetByID(ctx, subscriptionID)
}

// GetByID 根据ID获取订阅
func (s *SubscriptionService) GetByID(ctx context.Context, id int64) (*UserSubscription, error) {
	return s.userSubRepo.GetByID(ctx, id)
}

// GetActiveSubscription 获取用户对特定分组的有效订阅
// 使用 L1 缓存 + singleflight 加速中间件热路径。
// 返回缓存对象的浅拷贝，调用方可安全修改字段而不会污染缓存或触发 data race。
func (s *SubscriptionService) GetActiveSubscription(ctx context.Context, userID, groupID int64) (*UserSubscription, error) {
	key := subCacheKey(userID, groupID)

	// L1 缓存命中：返回浅拷贝
	if s.subCacheL1 != nil {
		if v, ok := s.subCacheL1.Get(key); ok {
			if sub, ok := v.(*UserSubscription); ok {
				cp := *sub
				return &cp, nil
			}
		}
	}

	// singleflight 防止并发击穿
	value, err, _ := s.subCacheGroup.Do(key, func() (any, error) {
		sub, err := s.userSubRepo.GetActiveByUserIDAndGroupID(ctx, userID, groupID)
		if err != nil {
			return nil, err // 直接透传 repo 已翻译的错误（NotFound → ErrSubscriptionNotFound，其他错误原样返回）
		}
		// 写入 L1 缓存
		if s.subCacheL1 != nil {
			_ = s.subCacheL1.SetWithTTL(key, sub, 1, s.jitteredTTL(s.subCacheTTL))
		}
		return sub, nil
	})
	if err != nil {
		return nil, err
	}
	// singleflight 返回的也是缓存指针，需要浅拷贝
	sub, ok := value.(*UserSubscription)
	if !ok || sub == nil {
		return nil, ErrSubscriptionNotFound
	}
	cp := *sub
	return &cp, nil
}

// GetActiveWalletSubscription 查找用户当前 active 钱包订阅 (v4)。
// 钱包订阅独立于 api_key.group_id：用户只要持有一条 wallet_balance_usd != NULL
// 的 active 订阅，所有 group 请求都走钱包扣费。schema 上有 partial unique
// index 保证最多一条，repo 直接 Only() 即可。
//
// 不命中 L1 缓存：钱包订阅频率低（一用户最多一条）且扣款会在事务里 FOR UPDATE
// 重新查，缓存收益有限。返回 ErrSubscriptionNotFound 表示用户没钱包订阅，
// 调用方应回退到 (user, group) 老路径或主余额检查。
func (s *SubscriptionService) GetActiveWalletSubscription(ctx context.Context, userID int64) (*UserSubscription, error) {
	return s.userSubRepo.GetActiveWalletByUserID(ctx, userID)
}

func (s *SubscriptionService) GetActiveCreditsWalletSubscription(ctx context.Context, userID int64) (*UserSubscription, error) {
	return s.userSubRepo.GetActiveCreditsWalletByUserID(ctx, userID)
}

// GetActiveSubscriptionCoveringGroup 查用户 active 月卡订阅，其 plan 通过
// subscription_plan_groups 间接覆盖了 groupID。返回 ErrSubscriptionNotFound
// 表示不覆盖，middleware 应继续 fallback。
//
// 用例：用户在 admin 把 api_key.group_id 切到非订阅主 group 后，middleware
// 不能再静默扣 user.balance，得先看 plan_groups 是否覆盖（2026-05-16 方案 C）。
func (s *SubscriptionService) GetActiveSubscriptionCoveringGroup(ctx context.Context, userID, groupID int64) (*UserSubscription, error) {
	return s.userSubRepo.GetActiveByPlanCoveringGroup(ctx, userID, groupID)
}

// UserHasAnyActiveSubscription 用户是否有任何 active 订阅（含钱包 / 月卡）。
// middleware fallback 决策用：有订阅但当前 group 不覆盖 → 403；无订阅 → 允许
// 走老 user.balance 兼容路径（纯余额用户）。
func (s *SubscriptionService) UserHasAnyActiveSubscription(ctx context.Context, userID int64) (bool, error) {
	return s.userSubRepo.HasAnyActiveSubscription(ctx, userID)
}

// ListUserSubscriptions 获取用户的所有订阅
func (s *SubscriptionService) ListUserSubscriptions(ctx context.Context, userID int64) ([]UserSubscription, error) {
	subs, err := s.userSubRepo.ListByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	normalizeExpiredWindows(subs)
	normalizeSubscriptionStatus(subs)
	return subs, nil
}

// ListActiveUserSubscriptions 获取用户的所有有效订阅
func (s *SubscriptionService) ListActiveUserSubscriptions(ctx context.Context, userID int64) ([]UserSubscription, error) {
	subs, err := s.userSubRepo.ListActiveByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	normalizeExpiredWindows(subs)
	return subs, nil
}

// ListGroupSubscriptions 获取分组的所有订阅
func (s *SubscriptionService) ListGroupSubscriptions(ctx context.Context, groupID int64, page, pageSize int) ([]UserSubscription, *pagination.PaginationResult, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}
	subs, pag, err := s.userSubRepo.ListByGroupID(ctx, groupID, params)
	if err != nil {
		return nil, nil, err
	}
	normalizeExpiredWindows(subs)
	normalizeSubscriptionStatus(subs)
	return subs, pag, nil
}

// List 获取所有订阅（分页，支持筛选和排序）
func (s *SubscriptionService) List(ctx context.Context, page, pageSize int, userID, groupID *int64, status, platform, sortBy, sortOrder string) ([]UserSubscription, *pagination.PaginationResult, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}
	subs, pag, err := s.userSubRepo.List(ctx, params, userID, groupID, status, platform, sortBy, sortOrder)
	if err != nil {
		return nil, nil, err
	}
	normalizeExpiredWindows(subs)
	normalizeSubscriptionStatus(subs)
	return subs, pag, nil
}

// normalizeExpiredWindows 将已过期窗口的数据清零（仅影响返回数据，不影响数据库）
// 这确保前端显示正确的当前窗口状态，而不是过期窗口的历史数据
func normalizeExpiredWindows(subs []UserSubscription) {
	for i := range subs {
		sub := &subs[i]
		// 日窗口过期：清零展示数据
		if sub.NeedsDailyReset() {
			sub.DailyWindowStart = nil
			sub.DailyUsageUSD = 0
		}
		// 周窗口过期：清零展示数据
		if sub.NeedsWeeklyReset() {
			sub.WeeklyWindowStart = nil
			sub.WeeklyUsageUSD = 0
		}
		// 月窗口过期：清零展示数据
		if sub.NeedsMonthlyReset() {
			sub.MonthlyWindowStart = nil
			sub.MonthlyUsageUSD = 0
		}
	}
}

// normalizeSubscriptionStatus 根据实际过期时间修正状态（仅影响返回数据，不影响数据库）
// 这确保前端显示正确的状态，即使定时任务尚未更新数据库
func normalizeSubscriptionStatus(subs []UserSubscription) {
	now := time.Now()
	for i := range subs {
		sub := &subs[i]
		if sub.Status == SubscriptionStatusActive && !sub.ExpiresAt.After(now) {
			sub.Status = SubscriptionStatusExpired
		}
	}
}

// startOfDay 返回给定时间所在日期的零点（保持原时区）
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// CheckAndActivateWindow 检查并激活窗口（首次使用时）
func (s *SubscriptionService) CheckAndActivateWindow(ctx context.Context, sub *UserSubscription) error {
	if sub.DailyWindowStart != nil && sub.WeeklyWindowStart != nil && sub.MonthlyWindowStart != nil {
		return nil
	}

	now := time.Now()
	windowStart := startOfDay(now)
	monthlyWindowStart := sub.StartsAt
	if monthlyWindowStart.IsZero() {
		monthlyWindowStart = now
	}
	if sub.DailyWindowStart == nil {
		advanced, err := s.userSubRepo.AdvanceUsageWindow(ctx, sub.ID, SubscriptionUsageWindowAdvance{
			Window:     SubscriptionUsageWindowDaily,
			NewStart:   windowStart,
			ResetUsage: false,
		})
		if err != nil {
			return err
		}
		if advanced {
			sub.DailyWindowStart = &windowStart
		}
	}
	if sub.WeeklyWindowStart == nil {
		advanced, err := s.userSubRepo.AdvanceUsageWindow(ctx, sub.ID, SubscriptionUsageWindowAdvance{
			Window:     SubscriptionUsageWindowWeekly,
			NewStart:   windowStart,
			ResetUsage: false,
		})
		if err != nil {
			return err
		}
		if advanced {
			sub.WeeklyWindowStart = &windowStart
		}
	}
	if sub.MonthlyWindowStart == nil {
		advanced, err := s.userSubRepo.AdvanceUsageWindow(ctx, sub.ID, SubscriptionUsageWindowAdvance{
			Window:     SubscriptionUsageWindowMonthly,
			NewStart:   monthlyWindowStart,
			ResetUsage: false,
		})
		if err != nil {
			return err
		}
		if advanced {
			sub.MonthlyWindowStart = &monthlyWindowStart
		}
	}
	return nil
}

// AdminResetQuota manually resets the daily, weekly, and/or monthly usage windows.
func (s *SubscriptionService) AdminResetQuota(ctx context.Context, subscriptionID int64, resetDaily, resetWeekly, resetMonthly bool) (*UserSubscription, error) {
	if !resetDaily && !resetWeekly && !resetMonthly {
		return nil, ErrInvalidInput
	}
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	windowStart := startOfDay(now)
	if resetDaily {
		if err := s.userSubRepo.ResetDailyUsage(ctx, sub.ID, windowStart); err != nil {
			return nil, err
		}
	}
	if resetWeekly {
		if err := s.userSubRepo.ResetWeeklyUsage(ctx, sub.ID, windowStart); err != nil {
			return nil, err
		}
	}
	if resetMonthly {
		if err := s.userSubRepo.ResetMonthlyUsage(ctx, sub.ID, now); err != nil {
			return nil, err
		}
	}
	// Invalidate L1 ristretto cache. Ristretto's Del() is asynchronous by design,
	// so call Wait() immediately after to flush pending operations and guarantee
	// the deleted key is not returned on the very next Get() call.
	// 钱包模式 sub.GroupID == nil，跳过 group 维度缓存
	if sub.GroupID != nil {
		s.InvalidateSubCache(sub.UserID, *sub.GroupID)
		if s.subCacheL1 != nil {
			s.subCacheL1.Wait()
		}
		if s.billingCacheService != nil {
			_ = s.billingCacheService.InvalidateSubscription(ctx, sub.UserID, *sub.GroupID)
		}
	}
	// Return the refreshed subscription from DB
	return s.userSubRepo.GetByID(ctx, subscriptionID)
}

// CheckAndResetWindows 检查并重置过期的窗口
func (s *SubscriptionService) CheckAndResetWindows(ctx context.Context, sub *UserSubscription) error {
	now := time.Now()
	windowStart := startOfDay(now)
	needsInvalidateCache := false

	// 日窗口重置（24小时）
	if sub.NeedsDailyReset() {
		advanced, err := s.userSubRepo.AdvanceUsageWindow(ctx, sub.ID, SubscriptionUsageWindowAdvance{
			Window:        SubscriptionUsageWindowDaily,
			ExpectedStart: sub.DailyWindowStart,
			NewStart:      windowStart,
			ResetUsage:    true,
		})
		if err != nil {
			return err
		}
		if advanced {
			sub.DailyWindowStart = &windowStart
			sub.DailyUsageUSD = 0
			needsInvalidateCache = true
		}
	}

	// 周窗口重置（7天）
	if sub.NeedsWeeklyReset() {
		advanced, err := s.userSubRepo.AdvanceUsageWindow(ctx, sub.ID, SubscriptionUsageWindowAdvance{
			Window:        SubscriptionUsageWindowWeekly,
			ExpectedStart: sub.WeeklyWindowStart,
			NewStart:      windowStart,
			ResetUsage:    true,
		})
		if err != nil {
			return err
		}
		if advanced {
			sub.WeeklyWindowStart = &windowStart
			sub.WeeklyUsageUSD = 0
			needsInvalidateCache = true
		}
	}

	// 月窗口重置（30天）
	if sub.NeedsMonthlyReset() {
		advanced, err := s.userSubRepo.AdvanceUsageWindow(ctx, sub.ID, SubscriptionUsageWindowAdvance{
			Window:        SubscriptionUsageWindowMonthly,
			ExpectedStart: sub.MonthlyWindowStart,
			NewStart:      now,
			ResetUsage:    true,
		})
		if err != nil {
			return err
		}
		if advanced {
			sub.MonthlyWindowStart = &now
			sub.MonthlyUsageUSD = 0
			needsInvalidateCache = true
		}
	}

	// 如果有窗口被重置，失效缓存以保持一致性（钱包模式 sub.GroupID == nil，跳过 group 维度缓存）
	if needsInvalidateCache && sub.GroupID != nil {
		s.InvalidateSubCache(sub.UserID, *sub.GroupID)
		if s.billingCacheService != nil {
			_ = s.billingCacheService.InvalidateSubscription(ctx, sub.UserID, *sub.GroupID)
		}
	}

	return nil
}

// CheckUsageLimits 检查使用限额（返回错误如果超限）
// 用于中间件的快速预检查，additionalCost 通常为 0
func (s *SubscriptionService) CheckUsageLimits(ctx context.Context, sub *UserSubscription, group *Group, additionalCost float64) error {
	if !sub.CheckDailyLimit(group, additionalCost) {
		return ErrDailyLimitExceeded
	}
	if !sub.CheckWeeklyLimit(group, additionalCost) {
		return ErrWeeklyLimitExceeded
	}
	if !sub.CheckMonthlyLimit(group, additionalCost) {
		return ErrMonthlyLimitExceeded
	}
	return nil
}

// ValidateAndCheckLimits 合并验证+限额检查（中间件热路径专用）
// 仅做内存检查，不触发 DB 写入。窗口重置的 DB 写入由 DoWindowMaintenance
// 在请求继续前同步完成。
// 返回 needsMaintenance 表示是否需要执行窗口维护。
func (s *SubscriptionService) ValidateAndCheckLimits(sub *UserSubscription, group *Group) (needsMaintenance bool, err error) {
	// 1. 验证订阅状态
	if sub.Status == SubscriptionStatusExpired {
		return false, ErrSubscriptionExpired
	}
	if sub.Status == SubscriptionStatusSuspended {
		return false, ErrSubscriptionSuspended
	}
	if sub.IsExpired() {
		return false, ErrSubscriptionExpired
	}

	// 2. 使用副本计算新窗口额度，不修改可能来自 L1 cache 的订阅对象。
	//    请求只有在同步、原子的窗口维护完成后才会继续。
	effective := *sub
	if effective.NeedsDailyReset() {
		effective.DailyUsageUSD = 0
		needsMaintenance = true
	}
	if effective.NeedsWeeklyReset() {
		effective.WeeklyUsageUSD = 0
		needsMaintenance = true
	}
	if effective.NeedsMonthlyReset() {
		effective.MonthlyUsageUSD = 0
		needsMaintenance = true
	}
	if !effective.IsWindowActivated() {
		needsMaintenance = true
	}

	// 3. 检查用量限额
	if !effective.CheckDailyLimit(group, 0) {
		return needsMaintenance, ErrDailyLimitExceeded
	}
	if !effective.CheckWeeklyLimit(group, 0) {
		return needsMaintenance, ErrWeeklyLimitExceeded
	}
	if !effective.CheckMonthlyLimit(group, 0) {
		return needsMaintenance, ErrMonthlyLimitExceeded
	}

	return needsMaintenance, nil
}

// DoWindowMaintenance completes authoritative, CAS-protected window
// maintenance before the request proceeds. A configured bounded queue still
// limits DB fan-out, but callers wait for the result so usage settlement cannot
// overtake a delayed reset.
func (s *SubscriptionService) DoWindowMaintenance(ctx context.Context, subscriptionID int64) (*UserSubscription, error) {
	if s == nil || s.userSubRepo == nil {
		return nil, infraerrors.ServiceUnavailable("SUBSCRIPTION_MAINTENANCE_UNAVAILABLE", "subscription window maintenance is unavailable")
	}
	type result struct {
		sub *UserSubscription
		err error
	}
	run := func() result {
		maintenanceCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sub, err := s.doWindowMaintenance(maintenanceCtx, subscriptionID)
		return result{sub: sub, err: err}
	}
	if s.maintenanceQueue == nil {
		res := run()
		return res.sub, res.err
	}

	results := make(chan result, 1)
	queuedRun := func() {
		deferredResult := result{}
		defer func() {
			if recovered := recover(); recovered != nil {
				deferredResult.err = fmt.Errorf("subscription window maintenance panic: %v", recovered)
			}
			results <- deferredResult
		}()
		deferredResult = run()
	}
	if err := s.maintenanceQueue.TryEnqueue(queuedRun); err != nil {
		return nil, fmt.Errorf("enqueue subscription window maintenance: %w", err)
	}
	select {
	case res := <-results:
		return res.sub, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *SubscriptionService) doWindowMaintenance(ctx context.Context, subscriptionID int64) (*UserSubscription, error) {
	current, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	if !current.IsWindowActivated() {
		if err := s.CheckAndActivateWindow(ctx, current); err != nil {
			return nil, err
		}
	}
	if err := s.CheckAndResetWindows(ctx, current); err != nil {
		return nil, err
	}

	latest, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	if latest.GroupID != nil {
		s.InvalidateSubCache(latest.UserID, *latest.GroupID)
	}
	return latest, nil
}

// RecordUsage 记录使用量到订阅
func (s *SubscriptionService) RecordUsage(ctx context.Context, subscriptionID int64, costUSD float64) error {
	return s.userSubRepo.IncrementUsage(ctx, subscriptionID, costUSD)
}

// SubscriptionProgress 订阅进度
type SubscriptionProgress struct {
	ID            int64                `json:"id"`
	GroupName     string               `json:"group_name"`
	ExpiresAt     time.Time            `json:"expires_at"`
	ExpiresInDays int                  `json:"expires_in_days"`
	Daily         *UsageWindowProgress `json:"daily,omitempty"`
	Weekly        *UsageWindowProgress `json:"weekly,omitempty"`
	Monthly       *UsageWindowProgress `json:"monthly,omitempty"`
}

// UsageWindowProgress 使用窗口进度
type UsageWindowProgress struct {
	LimitUSD        float64   `json:"limit_usd"`
	UsedUSD         float64   `json:"used_usd"`
	RemainingUSD    float64   `json:"remaining_usd"`
	Percentage      float64   `json:"percentage"`
	WindowStart     time.Time `json:"window_start"`
	ResetsAt        time.Time `json:"resets_at"`
	ResetsInSeconds int64     `json:"resets_in_seconds"`
}

// GetSubscriptionProgress 获取订阅使用进度
func (s *SubscriptionService) GetSubscriptionProgress(ctx context.Context, subscriptionID int64) (*SubscriptionProgress, error) {
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, ErrSubscriptionNotFound
	}

	// 钱包模式 (v4) 没有单 group 概念，进度需另算（暂不支持，A2 阶段补全）
	if sub.GroupID == nil {
		return nil, infraerrors.BadRequest("WALLET_MODE_NOT_SUPPORTED", "wallet-mode subscription progress not yet implemented")
	}

	group := sub.Group
	if group == nil {
		group, err = s.groupRepo.GetByID(ctx, *sub.GroupID)
		if err != nil {
			return nil, err
		}
	}

	return s.calculateProgress(sub, group), nil
}

// calculateProgress 根据已加载的订阅和分组数据计算使用进度（纯内存计算，无 DB 查询）
func (s *SubscriptionService) calculateProgress(sub *UserSubscription, group *Group) *SubscriptionProgress {
	progress := &SubscriptionProgress{
		ID:            sub.ID,
		GroupName:     group.Name,
		ExpiresAt:     sub.ExpiresAt,
		ExpiresInDays: sub.DaysRemaining(),
	}

	// 日进度
	if group.HasDailyLimit() && sub.DailyWindowStart != nil {
		limit := *group.DailyLimitUSD
		resetsAt := sub.DailyWindowStart.Add(24 * time.Hour)
		progress.Daily = &UsageWindowProgress{
			LimitUSD:        limit,
			UsedUSD:         sub.DailyUsageUSD,
			RemainingUSD:    limit - sub.DailyUsageUSD,
			Percentage:      (sub.DailyUsageUSD / limit) * 100,
			WindowStart:     *sub.DailyWindowStart,
			ResetsAt:        resetsAt,
			ResetsInSeconds: int64(time.Until(resetsAt).Seconds()),
		}
		if progress.Daily.RemainingUSD < 0 {
			progress.Daily.RemainingUSD = 0
		}
		if progress.Daily.Percentage > 100 {
			progress.Daily.Percentage = 100
		}
		if progress.Daily.ResetsInSeconds < 0 {
			progress.Daily.ResetsInSeconds = 0
		}
	}

	// 周进度
	if group.HasWeeklyLimit() && sub.WeeklyWindowStart != nil {
		limit := *group.WeeklyLimitUSD
		resetsAt := sub.WeeklyWindowStart.Add(7 * 24 * time.Hour)
		progress.Weekly = &UsageWindowProgress{
			LimitUSD:        limit,
			UsedUSD:         sub.WeeklyUsageUSD,
			RemainingUSD:    limit - sub.WeeklyUsageUSD,
			Percentage:      (sub.WeeklyUsageUSD / limit) * 100,
			WindowStart:     *sub.WeeklyWindowStart,
			ResetsAt:        resetsAt,
			ResetsInSeconds: int64(time.Until(resetsAt).Seconds()),
		}
		if progress.Weekly.RemainingUSD < 0 {
			progress.Weekly.RemainingUSD = 0
		}
		if progress.Weekly.Percentage > 100 {
			progress.Weekly.Percentage = 100
		}
		if progress.Weekly.ResetsInSeconds < 0 {
			progress.Weekly.ResetsInSeconds = 0
		}
	}

	// 月进度
	if group.HasMonthlyLimit() && sub.MonthlyWindowStart != nil {
		limit := *group.MonthlyLimitUSD
		resetsAt := sub.MonthlyWindowStart.Add(30 * 24 * time.Hour)
		progress.Monthly = &UsageWindowProgress{
			LimitUSD:        limit,
			UsedUSD:         sub.MonthlyUsageUSD,
			RemainingUSD:    limit - sub.MonthlyUsageUSD,
			Percentage:      (sub.MonthlyUsageUSD / limit) * 100,
			WindowStart:     *sub.MonthlyWindowStart,
			ResetsAt:        resetsAt,
			ResetsInSeconds: int64(time.Until(resetsAt).Seconds()),
		}
		if progress.Monthly.RemainingUSD < 0 {
			progress.Monthly.RemainingUSD = 0
		}
		if progress.Monthly.Percentage > 100 {
			progress.Monthly.Percentage = 100
		}
		if progress.Monthly.ResetsInSeconds < 0 {
			progress.Monthly.ResetsInSeconds = 0
		}
	}

	return progress
}

// GetUserSubscriptionsWithProgress 获取用户所有订阅及进度
func (s *SubscriptionService) GetUserSubscriptionsWithProgress(ctx context.Context, userID int64) ([]SubscriptionProgress, error) {
	// ListActiveByUserID 已使用 .WithGroup() eager-load Group 关联，1 次查询获取所有数据
	subs, err := s.userSubRepo.ListActiveByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	progresses := make([]SubscriptionProgress, 0, len(subs))
	for i := range subs {
		sub := &subs[i]
		group := sub.Group
		if group == nil {
			continue
		}
		progresses = append(progresses, *s.calculateProgress(sub, group))
	}

	return progresses, nil
}

// ValidateSubscription 验证订阅是否有效
func (s *SubscriptionService) ValidateSubscription(ctx context.Context, sub *UserSubscription) error {
	if sub.Status == SubscriptionStatusExpired {
		return ErrSubscriptionExpired
	}
	if sub.Status == SubscriptionStatusSuspended {
		return ErrSubscriptionSuspended
	}
	if sub.IsExpired() {
		// 更新状态
		_ = s.userSubRepo.UpdateStatus(ctx, sub.ID, SubscriptionStatusExpired)
		return ErrSubscriptionExpired
	}
	return nil
}
