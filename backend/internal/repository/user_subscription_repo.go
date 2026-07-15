package repository

import (
	"context"
	"errors"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplan"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplangroup"
	entuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type userSubscriptionRepository struct {
	client *dbent.Client
}

func NewUserSubscriptionRepository(client *dbent.Client) service.UserSubscriptionRepository {
	return &userSubscriptionRepository{client: client}
}

func (r *userSubscriptionRepository) Create(ctx context.Context, sub *service.UserSubscription) error {
	if sub == nil {
		return service.ErrSubscriptionNilInput
	}

	client := clientFromContext(ctx, r.client)
	builder := client.UserSubscription.Create().
		SetUserID(sub.UserID).
		SetNillableGroupID(sub.GroupID).
		SetExpiresAt(sub.ExpiresAt).
		SetNillableDailyWindowStart(sub.DailyWindowStart).
		SetNillableWeeklyWindowStart(sub.WeeklyWindowStart).
		SetNillableMonthlyWindowStart(sub.MonthlyWindowStart).
		SetDailyUsageUsd(sub.DailyUsageUSD).
		SetWeeklyUsageUsd(sub.WeeklyUsageUSD).
		SetMonthlyUsageUsd(sub.MonthlyUsageUSD).
		SetNillableWalletBalanceUsd(sub.WalletBalanceUSD).
		SetNillableWalletInitialUsd(sub.WalletInitialUSD).
		SetNillableAssignedBy(sub.AssignedBy)
	if len(sub.LockedRates) > 0 {
		builder.SetLockedRates(cloneLockedRates(sub.LockedRates))
	}

	if sub.StartsAt.IsZero() {
		builder.SetStartsAt(time.Now())
	} else {
		builder.SetStartsAt(sub.StartsAt)
	}
	if sub.Status != "" {
		builder.SetStatus(sub.Status)
	}
	if !sub.AssignedAt.IsZero() {
		builder.SetAssignedAt(sub.AssignedAt)
	}
	// Keep compatibility with historical behavior: always store notes as a string value.
	builder.SetNotes(sub.Notes)

	created, err := builder.Save(ctx)
	if err == nil {
		applyUserSubscriptionEntityToService(sub, created)
	}
	return translatePersistenceError(err, nil, service.ErrSubscriptionAlreadyExists)
}

func (r *userSubscriptionRepository) GetByID(ctx context.Context, id int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	m, err := client.UserSubscription.Query().
		Where(usersubscription.IDEQ(id)).
		WithUser().
		WithGroup().
		WithAssignedByUser().
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

func (r *userSubscriptionRepository) GetByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	m, err := client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID), usersubscription.GroupIDEQ(groupID)).
		WithGroup().
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

// LockUserForSubscriptionAssignment serializes all subscription entitlement
// mutations for one user. The caller must already be in a transaction; the
// service only invokes this method when an ent transaction is present.
func (r *userSubscriptionRepository) LockUserForSubscriptionAssignment(ctx context.Context, userID int64) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.User.Query().
		Where(entuser.IDEQ(userID)).
		ForUpdate().
		OnlyID(ctx)
	return err
}

func (r *userSubscriptionRepository) GetActiveByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	m, err := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.GroupIDEQ(groupID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
		).
		WithGroup().
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

// GetActiveWalletByUserID 返回最快到期的 active 钱包订阅（先到期先消费）。
// 多条月卡叠加场景下，计费侧优先消费最快到期的那张。
// 使用 First() 而非 Only()，兼容多条并存。
func (r *userSubscriptionRepository) GetActiveWalletByUserID(ctx context.Context, userID int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	m, err := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.GroupIDIsNil(),
			usersubscription.WalletBalanceUsdNotNil(),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
		).
		Order(dbent.Asc(usersubscription.FieldExpiresAt), dbent.Asc(usersubscription.FieldID)).
		WithGroup().
		First(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

func (r *userSubscriptionRepository) GetActiveCreditsWalletByUserID(ctx context.Context, userID int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	creditsThreshold := service.MaxExpiresAt.Add(-24 * time.Hour)
	m, err := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.GroupIDIsNil(),
			usersubscription.WalletBalanceUsdNotNil(),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGTE(creditsThreshold),
		).
		Order(dbent.Asc(usersubscription.FieldID)).
		WithGroup().
		First(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

// GetActiveByPlanCoveringGroup 查询用户 active 月卡订阅，其 plan 通过
// subscription_plan_groups 覆盖目标 group。兼容两种 plan 形态：
//  1. 老 v3 plan.group_id = 订阅主 group；
//  2. 新 M:N plan.group_id = NULL，由 plan_groups 中 subscription 类型 group 作为订阅锚点。
//
// 见 docs/plans/2026-05-16-wallet-v4-group-switch-billing-fix.md §4.1。
func (r *userSubscriptionRepository) GetActiveByPlanCoveringGroup(ctx context.Context, userID, targetGroupID int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	if snapshotted, err := r.getActiveSnapshotGrantCoveringGroup(ctx, client, userID, targetGroupID); err != nil {
		return nil, err
	} else if snapshotted != nil {
		return snapshotted, nil
	}

	primaryGroupIDs, err := r.coveringSubscriptionGroupIDs(ctx, client, targetGroupID)
	if err != nil {
		return nil, err
	}
	if len(primaryGroupIDs) == 0 {
		return nil, service.ErrSubscriptionNotFound
	}

	models, err := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
			usersubscription.WalletBalanceUsdIsNil(),
			usersubscription.GroupIDIn(primaryGroupIDs...),
		).
		WithGroup().
		Order(dbent.Desc(usersubscription.FieldExpiresAt)).
		All(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	for _, model := range models {
		hasGrant, grantErr := userSubscriptionHasAttachedSnapshotGrant(ctx, client, model.ID)
		if grantErr != nil {
			return nil, grantErr
		}
		if hasGrant {
			continue
		}
		if model.GroupID == nil {
			continue
		}
		covered, coverageErr := legacySubscriptionAnchorCoversGroupUnambiguously(ctx, client, *model.GroupID, targetGroupID)
		if coverageErr != nil {
			return nil, coverageErr
		}
		if !covered {
			continue
		}
		return userSubscriptionEntityToService(model), nil
	}
	return nil, service.ErrSubscriptionNotFound
}

// legacySubscriptionAnchorCoversGroupUnambiguously grants an unsnapshotted
// subscription only when every currently identifiable subscription plan using
// the same anchor covers the requested group. A missing plan or divergent
// shared-anchor coverage is ambiguous historical evidence and fails closed.
func legacySubscriptionAnchorCoversGroupUnambiguously(ctx context.Context, client *dbent.Client, anchorGroupID, targetGroupID int64) (bool, error) {
	rows, err := client.QueryContext(ctx, `
		WITH anchor_plans AS (
			SELECT plan.id
			FROM subscription_plans plan
			JOIN groups anchor_group
			  ON anchor_group.id = plan.group_id
			 AND anchor_group.subscription_type = $3
			 AND anchor_group.deleted_at IS NULL
			WHERE plan.plan_type = $4
			  AND plan.group_id = $1

			UNION

			SELECT plan.id
			FROM subscription_plans plan
			JOIN subscription_plan_groups anchor
			  ON anchor.plan_id = plan.id
			 AND anchor.group_id = $1
			JOIN groups anchor_group
			  ON anchor_group.id = anchor.group_id
			 AND anchor_group.subscription_type = $3
			 AND anchor_group.deleted_at IS NULL
			WHERE plan.plan_type = $4
			  AND plan.group_id IS NULL
		)
		SELECT EXISTS (SELECT 1 FROM anchor_plans)
		   AND NOT EXISTS (
			SELECT 1
			FROM anchor_plans plan
			WHERE NOT EXISTS (
				SELECT 1
				FROM subscription_plan_groups target
				WHERE target.plan_id = plan.id
				  AND target.group_id = $2
			)
		   )
	`, anchorGroupID, targetGroupID, service.SubscriptionTypeSubscription, service.PlanTypeSubscription)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return false, errors.New("legacy subscription anchor coverage query returned no row")
	}
	var covered bool
	if err := rows.Scan(&covered); err != nil {
		return false, err
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return covered, nil
}

func (r *userSubscriptionRepository) getActiveSnapshotGrantCoveringGroup(ctx context.Context, client *dbent.Client, userID, targetGroupID int64) (*service.UserSubscription, error) {
	var subscriptionID int64
	rows, err := client.QueryContext(ctx, `
		SELECT us.id
		FROM user_subscriptions us
		JOIN subscription_plan_fulfillment_snapshots grant_snapshot
		  ON grant_snapshot.user_subscription_id = us.id
		WHERE us.user_id = $1
		  AND us.deleted_at IS NULL
		  AND us.status = $2
		  AND us.expires_at > NOW()
		  AND us.wallet_balance_usd IS NULL
		  AND grant_snapshot.grant_starts_at <= NOW()
		  AND grant_snapshot.grant_expires_at > NOW()
		  AND grant_snapshot.snapshot->'covered_group_ids' @> jsonb_build_array($3::bigint)
		ORDER BY grant_snapshot.grant_expires_at DESC, grant_snapshot.payment_order_id DESC
		LIMIT 1
	`, userID, service.SubscriptionStatusActive, targetGroupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	if err := rows.Scan(&subscriptionID); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	m, err := client.UserSubscription.Query().
		Where(usersubscription.IDEQ(subscriptionID)).
		WithGroup().
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

func userSubscriptionHasAttachedSnapshotGrant(ctx context.Context, client *dbent.Client, subscriptionID int64) (bool, error) {
	var exists bool
	rows, err := client.QueryContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM subscription_plan_fulfillment_snapshots
			WHERE user_subscription_id = $1
			  AND grant_starts_at IS NOT NULL
			  AND grant_expires_at IS NOT NULL
		)
	`, subscriptionID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, nil
	}
	if err := rows.Scan(&exists); err != nil {
		return false, err
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return exists, nil
}

func (r *userSubscriptionRepository) coveringSubscriptionGroupIDs(ctx context.Context, client *dbent.Client, targetGroupID int64) ([]int64, error) {
	seen := make(map[int64]struct{})
	primaryGroupIDs := make([]int64, 0)

	coveringPrimaryGroupIDs, err := client.SubscriptionPlan.Query().
		Where(
			subscriptionplan.GroupIDNotNil(),
			subscriptionplan.HasPlanGroupsWith(
				subscriptionplangroup.GroupIDEQ(targetGroupID),
			),
		).
		Select(subscriptionplan.FieldGroupID).
		Ints(ctx)
	if err != nil {
		return nil, err
	}
	primaryGroupIDs = appendUniqueIntIDs(primaryGroupIDs, seen, coveringPrimaryGroupIDs)

	coveringPlanIDs, err := client.SubscriptionPlanGroup.Query().
		Where(subscriptionplangroup.GroupIDEQ(targetGroupID)).
		Select(subscriptionplangroup.FieldPlanID).
		Ints(ctx)
	if err != nil {
		return nil, err
	}
	if len(coveringPlanIDs) == 0 {
		return primaryGroupIDs, nil
	}

	planIDs := intIDsToInt64(coveringPlanIDs)
	anchorGroupIDs, err := client.SubscriptionPlanGroup.Query().
		Where(
			subscriptionplangroup.PlanIDIn(planIDs...),
			subscriptionplangroup.HasGroupWith(
				group.SubscriptionTypeEQ(service.SubscriptionTypeSubscription),
				group.DeletedAtIsNil(),
			),
		).
		Select(subscriptionplangroup.FieldGroupID).
		Ints(ctx)
	if err != nil {
		return nil, err
	}
	return appendUniqueIntIDs(primaryGroupIDs, seen, anchorGroupIDs), nil
}

func appendUniqueIntIDs(out []int64, seen map[int64]struct{}, values []int) []int64 {
	for _, value := range values {
		id := int64(value)
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func intIDsToInt64(values []int) []int64 {
	out := make([]int64, len(values))
	for i, value := range values {
		out[i] = int64(value)
	}
	return out
}

// HasAnyActiveSubscription 用户是否有任何 active 订阅（含钱包 / 月卡）。
// middleware fallback 决策用：有订阅但当前 group 不覆盖 → 403；无订阅 → 允许
// 走老 user.balance 兼容路径。
func (r *userSubscriptionRepository) HasAnyActiveSubscription(ctx context.Context, userID int64) (bool, error) {
	client := clientFromContext(ctx, r.client)
	return client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
		).
		Exist(ctx)
}

func (r *userSubscriptionRepository) Update(ctx context.Context, sub *service.UserSubscription) error {
	if sub == nil {
		return service.ErrSubscriptionNilInput
	}

	client := clientFromContext(ctx, r.client)
	builder := client.UserSubscription.UpdateOneID(sub.ID).
		SetUserID(sub.UserID).
		SetNillableGroupID(sub.GroupID).
		SetStartsAt(sub.StartsAt).
		SetExpiresAt(sub.ExpiresAt).
		SetStatus(sub.Status).
		SetNillableDailyWindowStart(sub.DailyWindowStart).
		SetNillableWeeklyWindowStart(sub.WeeklyWindowStart).
		SetNillableMonthlyWindowStart(sub.MonthlyWindowStart).
		SetDailyUsageUsd(sub.DailyUsageUSD).
		SetWeeklyUsageUsd(sub.WeeklyUsageUSD).
		SetMonthlyUsageUsd(sub.MonthlyUsageUSD).
		SetNillableWalletBalanceUsd(sub.WalletBalanceUSD).
		SetNillableWalletInitialUsd(sub.WalletInitialUSD).
		SetNillableAssignedBy(sub.AssignedBy).
		SetAssignedAt(sub.AssignedAt).
		SetNotes(sub.Notes)
	if len(sub.LockedRates) > 0 {
		builder.SetLockedRates(cloneLockedRates(sub.LockedRates))
	}

	updated, err := builder.Save(ctx)
	if err == nil {
		applyUserSubscriptionEntityToService(sub, updated)
		return nil
	}
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, service.ErrSubscriptionAlreadyExists)
}

func (r *userSubscriptionRepository) Delete(ctx context.Context, id int64) error {
	// Match GORM semantics: deleting a missing row is not an error.
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.Delete().Where(usersubscription.IDEQ(id)).Exec(ctx)
	return translateWalletSubscriptionDeleteError(err)
}

func translateWalletSubscriptionDeleteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pq.Error
	if errors.As(err, &pgErr) && pgErr != nil && pgErr.Code == "23514" &&
		pgErr.Constraint == "hfc_wallet_open_admission_revoke" {
		return service.ErrSubscriptionUsageBillingInFlight.WithCause(err)
	}
	return err
}

func (r *userSubscriptionRepository) ListByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	subs, err := client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID)).
		WithGroup().
		Order(dbent.Desc(usersubscription.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(subs), nil
}

func (r *userSubscriptionRepository) ListActiveByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	subs, err := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
		).
		WithGroup().
		Order(dbent.Desc(usersubscription.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(subs), nil
}

func (r *userSubscriptionRepository) ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	q := client.UserSubscription.Query().Where(usersubscription.GroupIDEQ(groupID))

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	subs, err := q.
		WithUser().
		WithGroup().
		Order(dbent.Desc(usersubscription.FieldCreatedAt)).
		Offset(params.Offset()).
		Limit(params.Limit()).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	return userSubscriptionEntitiesToService(subs), paginationResultFromTotal(int64(total), params), nil
}

func (r *userSubscriptionRepository) List(ctx context.Context, params pagination.PaginationParams, userID, groupID *int64, status, platform, sortBy, sortOrder string) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	q := client.UserSubscription.Query()
	if userID != nil {
		q = q.Where(usersubscription.UserIDEQ(*userID))
	}
	if groupID != nil {
		q = q.Where(usersubscription.GroupIDEQ(*groupID))
	}
	if platform != "" {
		q = q.Where(usersubscription.HasGroupWith(group.PlatformEQ(platform)))
	}

	// Status filtering with real-time expiration check
	now := time.Now()
	switch status {
	case service.SubscriptionStatusActive:
		// Active: status is active AND not yet expired
		q = q.Where(
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(now),
		)
	case service.SubscriptionStatusExpired:
		// Expired: status is expired OR (status is active but already expired)
		q = q.Where(
			usersubscription.Or(
				usersubscription.StatusEQ(service.SubscriptionStatusExpired),
				usersubscription.And(
					usersubscription.StatusEQ(service.SubscriptionStatusActive),
					usersubscription.ExpiresAtLTE(now),
				),
			),
		)
	case "":
		// No filter
	default:
		// Other status (e.g., revoked)
		q = q.Where(usersubscription.StatusEQ(status))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Apply sorting
	q = q.WithUser().WithGroup().WithAssignedByUser()

	// Determine sort field
	var field string
	switch sortBy {
	case "expires_at":
		field = usersubscription.FieldExpiresAt
	case "status":
		field = usersubscription.FieldStatus
	default:
		field = usersubscription.FieldCreatedAt
	}

	// Determine sort order (default: desc)
	if sortOrder == "asc" && sortBy != "" {
		q = q.Order(dbent.Asc(field))
	} else {
		q = q.Order(dbent.Desc(field))
	}

	subs, err := q.
		Offset(params.Offset()).
		Limit(params.Limit()).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	return userSubscriptionEntitiesToService(subs), paginationResultFromTotal(int64(total), params), nil
}

func (r *userSubscriptionRepository) ExistsByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (bool, error) {
	client := clientFromContext(ctx, r.client)
	return client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID), usersubscription.GroupIDEQ(groupID)).
		Exist(ctx)
}

func (r *userSubscriptionRepository) ExtendExpiry(ctx context.Context, subscriptionID int64, newExpiresAt time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(subscriptionID).
		SetExpiresAt(newExpiresAt).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) UpdateStatus(ctx context.Context, subscriptionID int64, status string) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(subscriptionID).
		SetStatus(status).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) UpdateNotes(ctx context.Context, subscriptionID int64, notes string) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(subscriptionID).
		SetNotes(notes).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) ActivateWindows(ctx context.Context, id int64, start time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).
		SetDailyWindowStart(start).
		SetWeeklyWindowStart(start).
		SetMonthlyWindowStart(start).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) AdvanceUsageWindow(ctx context.Context, id int64, advance service.SubscriptionUsageWindowAdvance) (bool, error) {
	client := clientFromContext(ctx, r.client)
	update := client.UserSubscription.Update().Where(
		usersubscription.IDEQ(id),
		usersubscription.DeletedAtIsNil(),
	)

	switch advance.Window {
	case service.SubscriptionUsageWindowDaily:
		update.Where(subscriptionWindowStartPredicate(advance.ExpectedStart, usersubscription.DailyWindowStartIsNil, usersubscription.DailyWindowStartEQ)).
			SetDailyWindowStart(advance.NewStart)
		if advance.ResetUsage {
			update.SetDailyUsageUsd(0)
		}
	case service.SubscriptionUsageWindowWeekly:
		update.Where(subscriptionWindowStartPredicate(advance.ExpectedStart, usersubscription.WeeklyWindowStartIsNil, usersubscription.WeeklyWindowStartEQ)).
			SetWeeklyWindowStart(advance.NewStart)
		if advance.ResetUsage {
			update.SetWeeklyUsageUsd(0)
		}
	case service.SubscriptionUsageWindowMonthly:
		update.Where(subscriptionWindowStartPredicate(advance.ExpectedStart, usersubscription.MonthlyWindowStartIsNil, usersubscription.MonthlyWindowStartEQ)).
			SetMonthlyWindowStart(advance.NewStart)
		if advance.ResetUsage {
			update.SetMonthlyUsageUsd(0)
		}
	default:
		return false, service.ErrInvalidInput
	}

	affected, err := update.Save(ctx)
	return affected == 1, err
}

func subscriptionWindowStartPredicate(
	expected *time.Time,
	isNil func() predicate.UserSubscription,
	equals func(time.Time) predicate.UserSubscription,
) predicate.UserSubscription {
	if expected == nil {
		return isNil()
	}
	return equals(*expected)
}

func (r *userSubscriptionRepository) ResetDailyUsage(ctx context.Context, id int64, newWindowStart time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).
		SetDailyUsageUsd(0).
		SetDailyWindowStart(newWindowStart).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) ResetWeeklyUsage(ctx context.Context, id int64, newWindowStart time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).
		SetWeeklyUsageUsd(0).
		SetWeeklyWindowStart(newWindowStart).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) ResetMonthlyUsage(ctx context.Context, id int64, newWindowStart time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).
		SetMonthlyUsageUsd(0).
		SetMonthlyWindowStart(newWindowStart).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

// IncrementUsage 原子性地累加订阅用量。
// 限额检查已在请求前由 BillingCacheService.CheckBillingEligibility 完成，
// 此处仅负责记录实际消费，确保消费数据的完整性。
func (r *userSubscriptionRepository) IncrementUsage(ctx context.Context, id int64, costUSD float64) error {
	const updateSQL = `
		UPDATE user_subscriptions us
		SET
			daily_usage_usd = us.daily_usage_usd + $1,
			weekly_usage_usd = us.weekly_usage_usd + $1,
			monthly_usage_usd = us.monthly_usage_usd + $1,
			updated_at = NOW()
		FROM groups g
		WHERE us.id = $2
			AND us.deleted_at IS NULL
			AND us.group_id = g.id
			AND g.deleted_at IS NULL
	`

	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(ctx, updateSQL, costUSD, id)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected > 0 {
		return nil
	}

	// affected == 0：订阅不存在或已删除
	return service.ErrSubscriptionNotFound
}

func (r *userSubscriptionRepository) BatchUpdateExpiredStatus(ctx context.Context) (int64, error) {
	client := clientFromContext(ctx, r.client)
	n, err := client.UserSubscription.Update().
		Where(
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtLTE(time.Now()),
		).
		SetStatus(service.SubscriptionStatusExpired).
		Save(ctx)
	return int64(n), err
}

// Extra repository helpers (currently used only by integration tests).

func (r *userSubscriptionRepository) ListExpired(ctx context.Context) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	subs, err := client.UserSubscription.Query().
		Where(
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtLTE(time.Now()),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(subs), nil
}

func (r *userSubscriptionRepository) CountByGroupID(ctx context.Context, groupID int64) (int64, error) {
	client := clientFromContext(ctx, r.client)
	count, err := client.UserSubscription.Query().Where(usersubscription.GroupIDEQ(groupID)).Count(ctx)
	return int64(count), err
}

func (r *userSubscriptionRepository) CountActiveByGroupID(ctx context.Context, groupID int64) (int64, error) {
	client := clientFromContext(ctx, r.client)
	count, err := client.UserSubscription.Query().
		Where(
			usersubscription.GroupIDEQ(groupID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
		).
		Count(ctx)
	return int64(count), err
}

func (r *userSubscriptionRepository) DeleteByGroupID(ctx context.Context, groupID int64) (int64, error) {
	client := clientFromContext(ctx, r.client)
	n, err := client.UserSubscription.Delete().Where(usersubscription.GroupIDEQ(groupID)).Exec(ctx)
	return int64(n), err
}

func userSubscriptionEntityToService(m *dbent.UserSubscription) *service.UserSubscription {
	if m == nil {
		return nil
	}
	out := &service.UserSubscription{
		ID:                 m.ID,
		UserID:             m.UserID,
		GroupID:            m.GroupID,
		StartsAt:           m.StartsAt,
		ExpiresAt:          m.ExpiresAt,
		Status:             m.Status,
		DailyWindowStart:   m.DailyWindowStart,
		WeeklyWindowStart:  m.WeeklyWindowStart,
		MonthlyWindowStart: m.MonthlyWindowStart,
		DailyUsageUSD:      m.DailyUsageUsd,
		WeeklyUsageUSD:     m.WeeklyUsageUsd,
		MonthlyUsageUSD:    m.MonthlyUsageUsd,
		WalletBalanceUSD:   m.WalletBalanceUsd,
		WalletInitialUSD:   m.WalletInitialUsd,
		LockedRates:        cloneLockedRates(m.LockedRates),
		AssignedBy:         m.AssignedBy,
		AssignedAt:         m.AssignedAt,
		Notes:              derefString(m.Notes),
		CreatedAt:          m.CreatedAt,
		UpdatedAt:          m.UpdatedAt,
	}
	if m.Edges.User != nil {
		out.User = userEntityToService(m.Edges.User)
	}
	if m.Edges.Group != nil {
		out.Group = groupEntityToService(m.Edges.Group)
	}
	if m.Edges.AssignedByUser != nil {
		out.AssignedByUser = userEntityToService(m.Edges.AssignedByUser)
	}
	return out
}

func userSubscriptionEntitiesToService(models []*dbent.UserSubscription) []service.UserSubscription {
	out := make([]service.UserSubscription, 0, len(models))
	for i := range models {
		if s := userSubscriptionEntityToService(models[i]); s != nil {
			out = append(out, *s)
		}
	}
	return out
}

func applyUserSubscriptionEntityToService(dst *service.UserSubscription, src *dbent.UserSubscription) {
	if dst == nil || src == nil {
		return
	}
	dst.ID = src.ID
	dst.CreatedAt = src.CreatedAt
	dst.UpdatedAt = src.UpdatedAt
}

func cloneLockedRates(in map[string]float64) map[string]float64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
