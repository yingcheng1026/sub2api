package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

type UserSubscriptionRepository interface {
	Create(ctx context.Context, sub *UserSubscription) error
	GetByID(ctx context.Context, id int64) (*UserSubscription, error)
	GetByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*UserSubscription, error)
	GetActiveByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*UserSubscription, error)
	// 钱包模式 (v4)：用户最多一条 active 钱包订阅（unique partial index 保证），
	// 与具体 group 解耦，gateway 中间件先 lookup 钱包再 fallback (user, group)。
	GetActiveWalletByUserID(ctx context.Context, userID int64) (*UserSubscription, error)
	// GetActiveByPlanCoveringGroup 查询用户有没有 active 月卡订阅，其 plan 通过
	// subscription_plan_groups 间接覆盖了 targetGroupID。
	//
	// 用例：用户在 admin 把 api_key.group_id 切到非订阅主 group 时，middleware
	// 调用本方法看 plan_groups 是否覆盖；覆盖则使用该订阅 quota 计费
	// （2026-05-16 方案 C，docs/plans/2026-05-16-wallet-v4-group-switch-billing-fix.md）。
	GetActiveByPlanCoveringGroup(ctx context.Context, userID, targetGroupID int64) (*UserSubscription, error)
	// HasAnyActiveSubscription 用户是否有任何 active 订阅（含钱包 / 月卡）。
	// middleware fallback 用：有订阅但当前 group 不覆盖 → 403 拒绝；
	// 无订阅 → 允许走老 user.balance 兼容路径。
	HasAnyActiveSubscription(ctx context.Context, userID int64) (bool, error)
	Update(ctx context.Context, sub *UserSubscription) error
	Delete(ctx context.Context, id int64) error

	ListByUserID(ctx context.Context, userID int64) ([]UserSubscription, error)
	ListActiveByUserID(ctx context.Context, userID int64) ([]UserSubscription, error)
	ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]UserSubscription, *pagination.PaginationResult, error)
	List(ctx context.Context, params pagination.PaginationParams, userID, groupID *int64, status, platform, sortBy, sortOrder string) ([]UserSubscription, *pagination.PaginationResult, error)

	ExistsByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (bool, error)
	ExtendExpiry(ctx context.Context, subscriptionID int64, newExpiresAt time.Time) error
	UpdateStatus(ctx context.Context, subscriptionID int64, status string) error
	UpdateNotes(ctx context.Context, subscriptionID int64, notes string) error

	ActivateWindows(ctx context.Context, id int64, start time.Time) error
	ResetDailyUsage(ctx context.Context, id int64, newWindowStart time.Time) error
	ResetWeeklyUsage(ctx context.Context, id int64, newWindowStart time.Time) error
	ResetMonthlyUsage(ctx context.Context, id int64, newWindowStart time.Time) error
	IncrementUsage(ctx context.Context, id int64, costUSD float64) error

	BatchUpdateExpiredStatus(ctx context.Context) (int64, error)
}
