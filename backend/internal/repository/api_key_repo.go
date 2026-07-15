package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/ent/userallowedgroup"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"

	entsql "entgo.io/ent/dialect/sql"
)

type apiKeyRepository struct {
	client                    *dbent.Client
	sql                       sqlExecutor
	keyProtector              service.APIKeyProtector
	validateStorageConstraint bool
}

var (
	_ service.APIKeyRepository               = (*apiKeyRepository)(nil)
	_ service.APIKeyPurposeRepository        = (*apiKeyRepository)(nil)
	_ service.APIKeyAuthCacheLocatorProvider = (*apiKeyRepository)(nil)
)

func (r *apiKeyRepository) APIKeyAuthCacheLocator(plaintext string) string {
	if r == nil || r.keyProtector == nil {
		return ""
	}
	return r.keyProtector.LookupLocator(plaintext)
}

func NewAPIKeyRepository(client *dbent.Client, sqlDB *sql.DB, keyProtector service.APIKeyProtector) service.APIKeyRepository {
	repo := newAPIKeyRepositoryWithSQL(client, sqlDB, keyProtector)
	repo.validateStorageConstraint = true
	return repo
}

func newAPIKeyRepositoryWithSQL(client *dbent.Client, sqlq sqlExecutor, keyProtector service.APIKeyProtector) *apiKeyRepository {
	return &apiKeyRepository{client: client, sql: sqlq, keyProtector: keyProtector}
}

func (r *apiKeyRepository) activeQuery() *dbent.APIKeyQuery {
	// 默认过滤已软删除记录，避免删除后仍被查询到。
	return r.client.APIKey.Query().Where(apikey.DeletedAtIsNil())
}

func (r *apiKeyRepository) Create(ctx context.Context, key *service.APIKey) error {
	if key == nil || strings.TrimSpace(key.Key) == "" {
		return fmt.Errorf("API key plaintext is required")
	}
	if r.keyProtector == nil {
		return fmt.Errorf("API key protector is not configured")
	}
	key.KeyHash = r.keyProtector.LookupLocator(key.Key)
	if key.KeyPrefix == "" {
		key.KeyPrefix = service.APIKeyPrefixForStorage(key.Key)
	}
	if strings.TrimSpace(key.Purpose) == "" {
		key.Purpose = service.APIKeyPurposeStandard
	}
	storedKey, err := r.encryptAPIKeyForStorage(key.Key, key.UserID, key.Purpose, key.KeyHash)
	if err != nil {
		return fmt.Errorf("encrypt API key for storage: %w", err)
	}

	client := clientFromContext(ctx, r.client)
	builder := client.APIKey.Create().
		SetUserID(key.UserID).
		SetKey(storedKey).
		SetKeyHash(key.KeyHash).
		SetKeyPrefix(key.KeyPrefix).
		SetName(key.Name).
		SetPurpose(key.Purpose).
		SetStatus(key.Status).
		SetNillableGroupID(key.GroupID).
		SetNillableLastUsedAt(key.LastUsedAt).
		SetQuota(key.Quota).
		SetQuotaUsed(key.QuotaUsed).
		SetNillableExpiresAt(key.ExpiresAt).
		SetRateLimit5h(key.RateLimit5h).
		SetRateLimit1d(key.RateLimit1d).
		SetRateLimit7d(key.RateLimit7d)

	if len(key.IPWhitelist) > 0 {
		builder.SetIPWhitelist(key.IPWhitelist)
	}
	if len(key.IPBlacklist) > 0 {
		builder.SetIPBlacklist(key.IPBlacklist)
	}

	created, err := builder.Save(ctx)
	if err == nil {
		key.ID = created.ID
		key.LastUsedAt = created.LastUsedAt
		key.CreatedAt = created.CreatedAt
		key.UpdatedAt = created.UpdatedAt
	}
	return translatePersistenceError(err, nil, service.ErrAPIKeyExists)
}

func (r *apiKeyRepository) GetByID(ctx context.Context, id int64) (*service.APIKey, error) {
	m, err := r.activeQuery().
		Where(apikey.IDEQ(id)).
		WithUser().
		WithGroup().
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrAPIKeyNotFound
		}
		return nil, err
	}
	return r.apiKeyEntityToService(m)
}

// GetKeyAndOwnerID 根据 API Key ID 获取其 key 与所有者（用户）ID。
// 相比 GetByID，此方法性能更优，因为：
//   - 使用 Select() 只查询必要字段，减少数据传输量
//   - 不加载完整的 API Key 实体及其关联数据（User、Group 等）
//   - 适用于删除等只需 key 与用户 ID 的场景
func (r *apiKeyRepository) GetKeyAndOwnerID(ctx context.Context, id int64) (string, int64, error) {
	m, err := r.activeQuery().
		Where(apikey.IDEQ(id)).
		Select(apikey.FieldKey, apikey.FieldKeyHash, apikey.FieldPurpose, apikey.FieldUserID).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return "", 0, service.ErrAPIKeyNotFound
		}
		return "", 0, err
	}
	key, err := r.decryptAPIKeyFromStorage(m.Key, m.UserID, m.Purpose, derefString(m.KeyHash))
	if err != nil {
		return "", 0, fmt.Errorf("decrypt API key %d: %w", id, err)
	}
	return key, m.UserID, nil
}

func (r *apiKeyRepository) GetByKey(ctx context.Context, key string) (*service.APIKey, error) {
	locator, err := r.lookupAPIKeyLocator(key)
	if err != nil {
		return nil, err
	}
	m, err := r.activeQuery().
		Where(apikey.KeyHashEQ(locator)).
		WithUser().
		WithGroup().
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrAPIKeyNotFound
		}
		return nil, err
	}
	return r.apiKeyEntityToService(m)
}

func (r *apiKeyRepository) GetByUserIDAndPurpose(ctx context.Context, userID int64, purpose string) (*service.APIKey, error) {
	client := clientFromContext(ctx, r.client)
	m, err := client.APIKey.Query().
		Where(
			apikey.UserIDEQ(userID),
			apikey.PurposeEQ(strings.TrimSpace(purpose)),
			apikey.DeletedAtIsNil(),
		).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrAPIKeyNotFound
		}
		return nil, err
	}
	return r.apiKeyEntityToService(m)
}

func (r *apiKeyRepository) GetByKeyForAuth(ctx context.Context, key string) (*service.APIKey, error) {
	locator, err := r.lookupAPIKeyLocator(key)
	if err != nil {
		return nil, err
	}
	m, err := r.activeQuery().
		Where(apikey.KeyHashEQ(locator)).
		Select(
			apikey.FieldID,
			apikey.FieldUserID,
			apikey.FieldKeyHash,
			apikey.FieldKeyPrefix,
			apikey.FieldGroupID,
			apikey.FieldName,
			apikey.FieldPurpose,
			apikey.FieldStatus,
			apikey.FieldIPWhitelist,
			apikey.FieldIPBlacklist,
			apikey.FieldQuota,
			apikey.FieldQuotaUsed,
			apikey.FieldExpiresAt,
			apikey.FieldRateLimit5h,
			apikey.FieldRateLimit1d,
			apikey.FieldRateLimit7d,
		).
		WithUser(func(q *dbent.UserQuery) {
			q.Select(
				user.FieldID,
				user.FieldEmail,
				user.FieldUsername,
				user.FieldStatus,
				user.FieldRole,
				user.FieldBalance,
				user.FieldConcurrency,
				user.FieldBalanceNotifyEnabled,
				user.FieldBalanceNotifyThresholdType,
				user.FieldBalanceNotifyThreshold,
				user.FieldBalanceNotifyExtraEmails,
				user.FieldTotalRecharged,
				user.FieldSignupSource,
				user.FieldLastLoginAt,
				user.FieldLastActiveAt,
				user.FieldRpmLimit,
				user.FieldTokenVersion,
			).WithUserAllowedGroups(func(q *dbent.UserAllowedGroupQuery) {
				q.Select(userallowedgroup.FieldUserID, userallowedgroup.FieldGroupID)
			})
		}).
		WithGroup(func(q *dbent.GroupQuery) {
			q.Select(
				group.FieldID,
				group.FieldName,
				group.FieldPlatform,
				group.FieldStatus,
				group.FieldIsExclusive,
				group.FieldSubscriptionType,
				group.FieldRateMultiplier,
				group.FieldDailyLimitUsd,
				group.FieldWeeklyLimitUsd,
				group.FieldMonthlyLimitUsd,
				group.FieldAllowImageGeneration,
				group.FieldImageRateIndependent,
				group.FieldImageRateMultiplier,
				group.FieldImagePrice1k,
				group.FieldImagePrice2k,
				group.FieldImagePrice4k,
				group.FieldClaudeCodeOnly,
				group.FieldFallbackGroupID,
				group.FieldFallbackGroupIDOnInvalidRequest,
				group.FieldModelRoutingEnabled,
				group.FieldModelRouting,
				group.FieldMcpXMLInject,
				group.FieldSupportedModelScopes,
				group.FieldAllowMessagesDispatch,
				group.FieldDefaultMappedModel,
				group.FieldMessagesDispatchModelConfig,
				group.FieldRpmLimit,
				group.FieldUpdatedAt,
			)
		}).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrAPIKeyNotFound
		}
		return nil, err
	}
	// The authentication query intentionally does not select the encrypted key
	// column. APIKeyService injects the presented plaintext only after the hash
	// lookup succeeds.
	return apiKeyEntityToService(m), nil
}

// ValidateAuthCacheSnapshot verifies only the fields that can change whether a
// cached API key is authorized. It deliberately reads PostgreSQL/SQLite on
// every positive L1/L2 hit: Redis deletion and pub/sub remain useful for fast
// convergence, but an outage in either can no longer preserve a revoked grant.
//
// Keep this query compact. The full auth query hydrates routing/pricing JSON;
// this check uses one indexed point read and compares allowed_groups as a set.
func (r *apiKeyRepository) ValidateAuthCacheSnapshot(ctx context.Context, cacheLocator string, snapshot *service.APIKeyAuthSnapshot) (bool, error) {
	cacheLocator = strings.ToLower(strings.TrimSpace(cacheLocator))
	if snapshot == nil || snapshot.APIKeyID <= 0 || snapshot.UserID <= 0 || cacheLocator == "" {
		return false, nil
	}
	if r.sql == nil {
		return false, fmt.Errorf("auth cache security validator database is unavailable")
	}

	const authStateQuery = `
		SELECT
			ak.id, ak.user_id, ak.key_hash, ak.group_id, ak.name, ak.purpose, ak.status,
			ak.ip_whitelist, ak.ip_blacklist, ak.quota, ak.quota_used,
			ak.expires_at, ak.rate_limit_5h, ak.rate_limit_1d, ak.rate_limit_7d,
			u.status, u.role, u.balance, u.concurrency, u.rpm_limit,
			g.id, g.name, g.platform, g.status, g.is_exclusive,
			g.subscription_type, g.rpm_limit, g.updated_at,
			ugr.rpm_override, uag.group_id
		FROM api_keys AS ak
		JOIN users AS u
			ON u.id = ak.user_id AND u.deleted_at IS NULL
		LEFT JOIN groups AS g
			ON g.id = ak.group_id AND g.deleted_at IS NULL
		LEFT JOIN user_allowed_groups AS uag
			ON uag.user_id = u.id
		LEFT JOIN user_group_rate_multipliers AS ugr
			ON ugr.user_id = u.id AND ugr.group_id = ak.group_id
		WHERE ak.id = $1 AND ak.deleted_at IS NULL
		ORDER BY uag.group_id`

	rows, err := r.sql.QueryContext(ctx, authStateQuery, snapshot.APIKeyID)
	if err != nil {
		return false, fmt.Errorf("query auth cache security state: %w", err)
	}

	var (
		apiKeyID        int64
		userID          int64
		storedKeyHash   sql.NullString
		groupID         sql.NullInt64
		apiKeyName      string
		apiKeyPurpose   string
		apiKeyStatus    string
		ipWhitelistJSON []byte
		ipBlacklistJSON []byte
		quota           float64
		quotaUsed       float64
		expiresAt       sql.NullTime
		rateLimit5h     float64
		rateLimit1d     float64
		rateLimit7d     float64
		userStatus      string
		userRole        string
		userBalance     float64
		userConcurrency int
		userRPMLimit    int
		groupRowID      sql.NullInt64
		groupName       sql.NullString
		groupPlatform   sql.NullString
		groupStatus     sql.NullString
		groupExclusive  sql.NullBool
		groupSubType    sql.NullString
		groupRPMLimit   sql.NullInt64
		groupUpdatedAt  sql.NullTime
		userGroupRPM    sql.NullInt64
		allowedGroups   []int64
	)
	scanRow := func() error {
		var allowedGroupID sql.NullInt64
		if err := rows.Scan(
			&apiKeyID,
			&userID,
			&storedKeyHash,
			&groupID,
			&apiKeyName,
			&apiKeyPurpose,
			&apiKeyStatus,
			&ipWhitelistJSON,
			&ipBlacklistJSON,
			&quota,
			&quotaUsed,
			&expiresAt,
			&rateLimit5h,
			&rateLimit1d,
			&rateLimit7d,
			&userStatus,
			&userRole,
			&userBalance,
			&userConcurrency,
			&userRPMLimit,
			&groupRowID,
			&groupName,
			&groupPlatform,
			&groupStatus,
			&groupExclusive,
			&groupSubType,
			&groupRPMLimit,
			&groupUpdatedAt,
			&userGroupRPM,
			&allowedGroupID,
		); err != nil {
			return err
		}
		if allowedGroupID.Valid {
			allowedGroups = append(allowedGroups, allowedGroupID.Int64)
		}
		return nil
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("read auth cache security state: %w", err)
		}
		if err := rows.Close(); err != nil {
			return false, fmt.Errorf("close auth cache security state rows: %w", err)
		}
		return false, nil
	}
	if err := scanRow(); err != nil {
		_ = rows.Close()
		return false, fmt.Errorf("scan auth cache security state: %w", err)
	}
	for rows.Next() {
		if err := scanRow(); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("scan auth cache security state: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, fmt.Errorf("read auth cache security state: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close auth cache security state rows: %w", err)
	}
	ipWhitelist, err := decodeAuthCacheStringList(ipWhitelistJSON)
	if err != nil {
		return false, fmt.Errorf("decode auth cache IP whitelist: %w", err)
	}
	ipBlacklist, err := decodeAuthCacheStringList(ipBlacklistJSON)
	if err != nil {
		return false, fmt.Errorf("decode auth cache IP blacklist: %w", err)
	}
	storedLocator := strings.ToLower(strings.TrimSpace(storedKeyHash.String))
	locatorMatches := storedKeyHash.Valid && storedLocator == cacheLocator

	if !locatorMatches ||
		apiKeyID != snapshot.APIKeyID ||
		userID != snapshot.UserID ||
		apiKeyName != snapshot.Name ||
		apiKeyPurpose != snapshot.Purpose ||
		apiKeyStatus != snapshot.Status ||
		!equalStringSets(ipWhitelist, snapshot.IPWhitelist) ||
		!equalStringSets(ipBlacklist, snapshot.IPBlacklist) ||
		quota != snapshot.Quota ||
		quotaAuthorizationClass(quota, quotaUsed) != quotaAuthorizationClass(snapshot.Quota, snapshot.QuotaUsed) ||
		!sameNullableTime(expiresAt, snapshot.ExpiresAt) ||
		rateLimit5h != snapshot.RateLimit5h ||
		rateLimit1d != snapshot.RateLimit1d ||
		rateLimit7d != snapshot.RateLimit7d ||
		userStatus != snapshot.User.Status ||
		userRole != snapshot.User.Role ||
		balanceAuthorizationClass(userBalance) != balanceAuthorizationClass(snapshot.User.Balance) ||
		userConcurrency != snapshot.User.Concurrency ||
		userRPMLimit != snapshot.User.RPMLimit ||
		!sameNullableInt(userGroupRPM, snapshot.User.UserGroupRPMOverride) ||
		!equalInt64Sets(allowedGroups, snapshot.User.AllowedGroups) ||
		!sameNullableInt64(groupID, snapshot.GroupID) {
		return false, nil
	}

	if snapshot.Group == nil {
		return !groupRowID.Valid, nil
	}
	if !groupRowID.Valid ||
		groupRowID.Int64 != snapshot.Group.ID ||
		!groupName.Valid || groupName.String != snapshot.Group.Name ||
		!groupPlatform.Valid || groupPlatform.String != snapshot.Group.Platform ||
		!groupStatus.Valid || groupStatus.String != snapshot.Group.Status ||
		!groupExclusive.Valid || groupExclusive.Bool != snapshot.Group.IsExclusive ||
		!groupSubType.Valid || groupSubType.String != snapshot.Group.SubscriptionType ||
		!groupRPMLimit.Valid || int(groupRPMLimit.Int64) != snapshot.Group.RPMLimit ||
		!groupUpdatedAt.Valid || !groupUpdatedAt.Time.Equal(snapshot.Group.UpdatedAt) {
		return false, nil
	}
	return true, nil
}

func sameNullableInt64(current sql.NullInt64, cached *int64) bool {
	if cached == nil {
		return !current.Valid
	}
	return current.Valid && current.Int64 == *cached
}

func sameNullableInt(current sql.NullInt64, cached *int) bool {
	if cached == nil {
		return !current.Valid
	}
	return current.Valid && int(current.Int64) == *cached
}

func equalInt64Sets(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[int64]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

func decodeAuthCacheStringList(raw []byte) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func equalStringSets(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

func sameNullableTime(current sql.NullTime, cached *time.Time) bool {
	if cached == nil {
		return !current.Valid
	}
	return current.Valid && current.Time.Equal(*cached)
}

func quotaAuthorizationClass(quota, quotaUsed float64) bool {
	return quota > 0 && quotaUsed >= quota
}

func balanceAuthorizationClass(balance float64) bool {
	return balance > 0
}

func (r *apiKeyRepository) Update(ctx context.Context, key *service.APIKey) error {
	// 使用原子操作：将软删除检查与更新合并到同一语句，避免竞态条件。
	// 之前的实现先检查 Exist 再 UpdateOneID，若在两步之间发生软删除，
	// 则会更新已删除的记录。
	// 这里选择 Update().Where()，确保只有未软删除记录能被更新。
	// 同时显式设置 updated_at，避免二次查询带来的并发可见性问题。
	client := clientFromContext(ctx, r.client)
	now := time.Now()
	builder := client.APIKey.Update().
		Where(apikey.IDEQ(key.ID), apikey.DeletedAtIsNil()).
		SetName(key.Name).
		SetStatus(key.Status).
		SetQuota(key.Quota).
		SetQuotaUsed(key.QuotaUsed).
		SetRateLimit5h(key.RateLimit5h).
		SetRateLimit1d(key.RateLimit1d).
		SetRateLimit7d(key.RateLimit7d).
		SetUsage5h(key.Usage5h).
		SetUsage1d(key.Usage1d).
		SetUsage7d(key.Usage7d).
		SetUpdatedAt(now)
	if key.GroupID != nil {
		builder.SetGroupID(*key.GroupID)
	} else {
		builder.ClearGroupID()
	}

	// Expiration time
	if key.ExpiresAt != nil {
		builder.SetExpiresAt(*key.ExpiresAt)
	} else {
		builder.ClearExpiresAt()
	}

	// Rate limit window start times
	if key.Window5hStart != nil {
		builder.SetWindow5hStart(*key.Window5hStart)
	} else {
		builder.ClearWindow5hStart()
	}
	if key.Window1dStart != nil {
		builder.SetWindow1dStart(*key.Window1dStart)
	} else {
		builder.ClearWindow1dStart()
	}
	if key.Window7dStart != nil {
		builder.SetWindow7dStart(*key.Window7dStart)
	} else {
		builder.ClearWindow7dStart()
	}

	// IP 限制字段
	if len(key.IPWhitelist) > 0 {
		builder.SetIPWhitelist(key.IPWhitelist)
	} else {
		builder.ClearIPWhitelist()
	}
	if len(key.IPBlacklist) > 0 {
		builder.SetIPBlacklist(key.IPBlacklist)
	} else {
		builder.ClearIPBlacklist()
	}

	affected, err := builder.Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		// 更新影响行数为 0，说明记录不存在或已被软删除。
		return service.ErrAPIKeyNotFound
	}

	// 使用同一时间戳回填，避免并发删除导致二次查询失败。
	key.UpdatedAt = now
	return nil
}

func (r *apiKeyRepository) Delete(ctx context.Context, id int64) error {
	// 存在唯一键约束 生成tombstone key 用来释放原key，长度远小于 128，满足 schema 限制
	tombstoneKey := fmt.Sprintf("__deleted__%d__%d", id, time.Now().UnixNano())
	// 显式软删除：避免依赖 Hook 行为，确保 deleted_at 一定被设置。
	affected, err := r.client.APIKey.Update().
		Where(apikey.IDEQ(id), apikey.DeletedAtIsNil()).
		SetKey(tombstoneKey).
		SetDeletedAt(time.Now()).
		Save(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return service.ErrAPIKeyNotFound
		}
		return err
	}
	if affected == 0 {
		exists, err := r.client.APIKey.Query().
			Where(apikey.IDEQ(id)).
			Exist(mixins.SkipSoftDelete(ctx))
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		return service.ErrAPIKeyNotFound
	}
	return nil
}

func (r *apiKeyRepository) ListByUserID(ctx context.Context, userID int64, params pagination.PaginationParams, filters service.APIKeyListFilters) ([]service.APIKey, *pagination.PaginationResult, error) {
	q := r.activeQuery().Where(apikey.UserIDEQ(userID))

	// Apply filters
	if filters.Search != "" {
		q = q.Where(apikey.Or(
			apikey.NameContainsFold(filters.Search),
			apikey.KeyPrefixContainsFold(filters.Search),
		))
	}
	if filters.Status != "" {
		q = q.Where(apikey.StatusEQ(filters.Status))
	}
	if filters.GroupID != nil {
		if *filters.GroupID == 0 {
			q = q.Where(apikey.GroupIDIsNil())
		} else {
			q = q.Where(apikey.GroupIDEQ(*filters.GroupID))
		}
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	keysQuery := q.
		WithGroup().
		Offset(params.Offset()).
		Limit(params.Limit())
	for _, order := range apiKeyListOrder(params) {
		keysQuery = keysQuery.Order(order)
	}

	keys, err := keysQuery.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	outKeys := make([]service.APIKey, 0, len(keys))
	for i := range keys {
		outKeys = append(outKeys, *apiKeyEntityToService(keys[i]))
	}

	return outKeys, paginationResultFromTotal(int64(total), params), nil
}

func (r *apiKeyRepository) VerifyOwnership(ctx context.Context, userID int64, apiKeyIDs []int64) ([]int64, error) {
	if len(apiKeyIDs) == 0 {
		return []int64{}, nil
	}

	ids, err := r.client.APIKey.Query().
		Where(apikey.UserIDEQ(userID), apikey.IDIn(apiKeyIDs...), apikey.DeletedAtIsNil()).
		IDs(ctx)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *apiKeyRepository) CountByUserID(ctx context.Context, userID int64) (int64, error) {
	count, err := r.activeQuery().Where(apikey.UserIDEQ(userID)).Count(ctx)
	return int64(count), err
}

func (r *apiKeyRepository) ExistsByKey(ctx context.Context, key string) (bool, error) {
	locator, err := r.lookupAPIKeyLocator(key)
	if err != nil {
		return false, err
	}
	count, err := r.client.APIKey.Query().
		Where(apikey.KeyHashEQ(locator)).
		Count(mixins.SkipSoftDelete(ctx))
	return count > 0, err
}

func (r *apiKeyRepository) ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]service.APIKey, *pagination.PaginationResult, error) {
	q := r.activeQuery().Where(apikey.GroupIDEQ(groupID))

	total, err := q.Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	keysQuery := q.
		WithUser().
		Offset(params.Offset()).
		Limit(params.Limit())
	for _, order := range apiKeyListOrder(params) {
		keysQuery = keysQuery.Order(order)
	}

	keys, err := keysQuery.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	outKeys := make([]service.APIKey, 0, len(keys))
	for i := range keys {
		outKeys = append(outKeys, *apiKeyEntityToService(keys[i]))
	}

	return outKeys, paginationResultFromTotal(int64(total), params), nil
}

func apiKeyListOrder(params pagination.PaginationParams) []func(*entsql.Selector) {
	sortBy := strings.ToLower(strings.TrimSpace(params.SortBy))
	sortOrder := params.NormalizedSortOrder(pagination.SortOrderDesc)

	var field string
	switch sortBy {
	case "name":
		field = apikey.FieldName
	case "status":
		field = apikey.FieldStatus
	case "expires_at":
		field = apikey.FieldExpiresAt
	case "last_used_at":
		field = apikey.FieldLastUsedAt
	case "created_at":
		field = apikey.FieldCreatedAt
	default:
		field = apikey.FieldID
	}

	if sortOrder == pagination.SortOrderAsc {
		return []func(*entsql.Selector){dbent.Asc(field), dbent.Asc(apikey.FieldID)}
	}
	return []func(*entsql.Selector){dbent.Desc(field), dbent.Desc(apikey.FieldID)}
}

// SearchAPIKeys searches API keys by user ID and/or keyword (name)
func (r *apiKeyRepository) SearchAPIKeys(ctx context.Context, userID int64, keyword string, limit int) ([]service.APIKey, error) {
	q := r.activeQuery()
	if userID > 0 {
		q = q.Where(apikey.UserIDEQ(userID))
	}

	if keyword != "" {
		q = q.Where(apikey.NameContainsFold(keyword))
	}

	keys, err := q.Limit(limit).Order(dbent.Desc(apikey.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}

	outKeys := make([]service.APIKey, 0, len(keys))
	for i := range keys {
		outKeys = append(outKeys, *apiKeyEntityToService(keys[i]))
	}
	return outKeys, nil
}

// ClearGroupIDByGroupID 将指定分组的所有 API Key 的 group_id 设为 nil
func (r *apiKeyRepository) ClearGroupIDByGroupID(ctx context.Context, groupID int64) (int64, error) {
	n, err := r.client.APIKey.Update().
		Where(apikey.GroupIDEQ(groupID), apikey.DeletedAtIsNil()).
		ClearGroupID().
		Save(ctx)
	return int64(n), err
}

// UpdateGroupIDByUserAndGroup 将用户下绑定 oldGroupID 的所有 Key 迁移到 newGroupID
func (r *apiKeyRepository) UpdateGroupIDByUserAndGroup(ctx context.Context, userID, oldGroupID, newGroupID int64) (int64, error) {
	client := clientFromContext(ctx, r.client)
	n, err := client.APIKey.Update().
		Where(apikey.UserIDEQ(userID), apikey.GroupIDEQ(oldGroupID), apikey.DeletedAtIsNil()).
		SetGroupID(newGroupID).
		Save(ctx)
	return int64(n), err
}

// CountByGroupID 获取分组的 API Key 数量
func (r *apiKeyRepository) CountByGroupID(ctx context.Context, groupID int64) (int64, error) {
	count, err := r.activeQuery().Where(apikey.GroupIDEQ(groupID)).Count(ctx)
	return int64(count), err
}

func (r *apiKeyRepository) ListAuthCacheLocatorsByUserID(ctx context.Context, userID int64) ([]string, error) {
	locators, err := r.activeQuery().
		Where(apikey.UserIDEQ(userID), apikey.KeyHashNotNil()).
		Select(apikey.FieldKeyHash).
		Strings(ctx)
	if err != nil {
		return nil, err
	}
	return locators, nil
}

func (r *apiKeyRepository) ListAuthCacheLocatorsByGroupID(ctx context.Context, groupID int64) ([]string, error) {
	locators, err := r.activeQuery().
		Where(apikey.GroupIDEQ(groupID), apikey.KeyHashNotNil()).
		Select(apikey.FieldKeyHash).
		Strings(ctx)
	if err != nil {
		return nil, err
	}
	return locators, nil
}

// IncrementQuotaUsed 使用 Ent 原子递增 quota_used 字段并返回新值
func (r *apiKeyRepository) IncrementQuotaUsed(ctx context.Context, id int64, amount float64) (float64, error) {
	updated, err := r.client.APIKey.UpdateOneID(id).
		Where(apikey.DeletedAtIsNil()).
		AddQuotaUsed(amount).
		Save(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return 0, service.ErrAPIKeyNotFound
		}
		return 0, err
	}
	return updated.QuotaUsed, nil
}

// IncrementQuotaUsedAndGetState atomically increments quota_used, conditionally marks the key
// as quota_exhausted, and returns the latest quota state in one round trip.
func (r *apiKeyRepository) IncrementQuotaUsedAndGetState(ctx context.Context, id int64, amount float64) (*service.APIKeyQuotaUsageState, error) {
	query := `
		UPDATE api_keys
		SET
			quota_used = quota_used + $1,
			status = CASE
				WHEN quota > 0 AND quota_used + $1 >= quota THEN $2
				ELSE status
			END,
			updated_at = NOW()
		WHERE id = $3 AND deleted_at IS NULL
		RETURNING quota_used, quota, key_hash, status
	`

	state := &service.APIKeyQuotaUsageState{}
	if err := scanSingleRow(ctx, r.sql, query, []any{amount, service.StatusAPIKeyQuotaExhausted, id}, &state.QuotaUsed, &state.Quota, &state.AuthCacheLocator, &state.Status); err != nil {
		if err == sql.ErrNoRows {
			return nil, service.ErrAPIKeyNotFound
		}
		return nil, err
	}
	return state, nil
}

func (r *apiKeyRepository) UpdateLastUsed(ctx context.Context, id int64, usedAt time.Time) error {
	affected, err := r.client.APIKey.Update().
		Where(apikey.IDEQ(id), apikey.DeletedAtIsNil()).
		SetLastUsedAt(usedAt).
		SetUpdatedAt(usedAt).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAPIKeyNotFound
	}
	return nil
}

// IncrementRateLimitUsage atomically increments all rate limit usage counters and initializes
// window start times via COALESCE if not already set.
func (r *apiKeyRepository) IncrementRateLimitUsage(ctx context.Context, id int64, cost float64) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE api_keys SET
			usage_5h = CASE WHEN window_5h_start IS NOT NULL AND window_5h_start + INTERVAL '5 hours' <= NOW() THEN $1 ELSE usage_5h + $1 END,
			usage_1d = CASE WHEN window_1d_start IS NOT NULL AND window_1d_start + INTERVAL '24 hours' <= NOW() THEN $1 ELSE usage_1d + $1 END,
			usage_7d = CASE WHEN window_7d_start IS NOT NULL AND window_7d_start + INTERVAL '7 days' <= NOW() THEN $1 ELSE usage_7d + $1 END,
			window_5h_start = CASE WHEN window_5h_start IS NULL OR window_5h_start + INTERVAL '5 hours' <= NOW() THEN NOW() ELSE window_5h_start END,
			window_1d_start = CASE WHEN window_1d_start IS NULL OR window_1d_start + INTERVAL '24 hours' <= NOW() THEN date_trunc('day', NOW()) ELSE window_1d_start END,
			window_7d_start = CASE WHEN window_7d_start IS NULL OR window_7d_start + INTERVAL '7 days' <= NOW() THEN date_trunc('day', NOW()) ELSE window_7d_start END,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL`,
		cost, id)
	return err
}

// ResetRateLimitWindows resets expired rate limit windows atomically.
func (r *apiKeyRepository) ResetRateLimitWindows(ctx context.Context, id int64) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE api_keys SET
			usage_5h = CASE WHEN window_5h_start IS NOT NULL AND window_5h_start + INTERVAL '5 hours' <= NOW() THEN 0 ELSE usage_5h END,
			window_5h_start = CASE WHEN window_5h_start IS NOT NULL AND window_5h_start + INTERVAL '5 hours' <= NOW() THEN NOW() ELSE window_5h_start END,
			usage_1d = CASE WHEN window_1d_start IS NOT NULL AND window_1d_start + INTERVAL '24 hours' <= NOW() THEN 0 ELSE usage_1d END,
			window_1d_start = CASE WHEN window_1d_start IS NOT NULL AND window_1d_start + INTERVAL '24 hours' <= NOW() THEN date_trunc('day', NOW()) ELSE window_1d_start END,
			usage_7d = CASE WHEN window_7d_start IS NOT NULL AND window_7d_start + INTERVAL '7 days' <= NOW() THEN 0 ELSE usage_7d END,
			window_7d_start = CASE WHEN window_7d_start IS NOT NULL AND window_7d_start + INTERVAL '7 days' <= NOW() THEN date_trunc('day', NOW()) ELSE window_7d_start END,
			updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`,
		id)
	return err
}

// GetRateLimitData returns the current rate limit usage and window start times for an API key.
func (r *apiKeyRepository) GetRateLimitData(ctx context.Context, id int64) (result *service.APIKeyRateLimitData, err error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT usage_5h, usage_1d, usage_7d, window_5h_start, window_1d_start, window_7d_start
		FROM api_keys
		WHERE id = $1 AND deleted_at IS NULL`,
		id)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if !rows.Next() {
		return nil, service.ErrAPIKeyNotFound
	}
	data := &service.APIKeyRateLimitData{}
	if err := rows.Scan(&data.Usage5h, &data.Usage1d, &data.Usage7d, &data.Window5hStart, &data.Window1dStart, &data.Window7dStart); err != nil {
		return nil, err
	}
	return data, rows.Err()
}

func apiKeyEntityToService(m *dbent.APIKey) *service.APIKey {
	if m == nil {
		return nil
	}
	out := &service.APIKey{
		ID:     m.ID,
		UserID: m.UserID,
		// Stored API-key ciphertext must never escape through generic entity
		// mappers (for example usage-log hydration). API-key repository read
		// paths explicitly decrypt it for authorized callers.
		Key:           "",
		KeyHash:       derefString(m.KeyHash),
		KeyPrefix:     m.KeyPrefix,
		Name:          m.Name,
		Purpose:       m.Purpose,
		Status:        m.Status,
		IPWhitelist:   m.IPWhitelist,
		IPBlacklist:   m.IPBlacklist,
		LastUsedAt:    m.LastUsedAt,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
		GroupID:       m.GroupID,
		Quota:         m.Quota,
		QuotaUsed:     m.QuotaUsed,
		ExpiresAt:     m.ExpiresAt,
		RateLimit5h:   m.RateLimit5h,
		RateLimit1d:   m.RateLimit1d,
		RateLimit7d:   m.RateLimit7d,
		Usage5h:       m.Usage5h,
		Usage1d:       m.Usage1d,
		Usage7d:       m.Usage7d,
		Window5hStart: m.Window5hStart,
		Window1dStart: m.Window1dStart,
		Window7dStart: m.Window7dStart,
	}
	if m.Edges.User != nil {
		out.User = userEntityToService(m.Edges.User)
	}
	if m.Edges.Group != nil {
		out.Group = groupEntityToService(m.Edges.Group)
	}
	return out
}

func userEntityToService(u *dbent.User) *service.User {
	if u == nil {
		return nil
	}
	out := &service.User{
		ID:                         u.ID,
		Email:                      u.Email,
		Username:                   u.Username,
		Notes:                      u.Notes,
		PasswordHash:               u.PasswordHash,
		Role:                       u.Role,
		Balance:                    u.Balance,
		Concurrency:                u.Concurrency,
		Status:                     u.Status,
		SignupSource:               u.SignupSource,
		LastLoginAt:                u.LastLoginAt,
		LastActiveAt:               u.LastActiveAt,
		TotpSecretEncrypted:        u.TotpSecretEncrypted,
		TotpEnabled:                u.TotpEnabled,
		TotpEnabledAt:              u.TotpEnabledAt,
		BalanceNotifyEnabled:       u.BalanceNotifyEnabled,
		BalanceNotifyThresholdType: u.BalanceNotifyThresholdType,
		BalanceNotifyThreshold:     u.BalanceNotifyThreshold,
		TotalRecharged:             u.TotalRecharged,
		RPMLimit:                   u.RpmLimit,
		TokenVersion:               u.TokenVersion,
		TokenVersionResolved:       true,
		CreatedAt:                  u.CreatedAt,
		UpdatedAt:                  u.UpdatedAt,
	}
	// Parse extra emails JSON (supports both old []string and new []NotifyEmailEntry format)
	if u.BalanceNotifyExtraEmails != "" && u.BalanceNotifyExtraEmails != "[]" {
		out.BalanceNotifyExtraEmails = service.ParseNotifyEmails(u.BalanceNotifyExtraEmails)
	}
	if len(u.Edges.UserAllowedGroups) > 0 {
		out.AllowedGroups = make([]int64, 0, len(u.Edges.UserAllowedGroups))
		for _, allowed := range u.Edges.UserAllowedGroups {
			if allowed != nil {
				out.AllowedGroups = append(out.AllowedGroups, allowed.GroupID)
			}
		}
	}
	return out
}

func groupEntityToService(g *dbent.Group) *service.Group {
	if g == nil {
		return nil
	}
	return &service.Group{
		ID:                              g.ID,
		Name:                            g.Name,
		Description:                     derefString(g.Description),
		Platform:                        g.Platform,
		RateMultiplier:                  g.RateMultiplier,
		IsExclusive:                     g.IsExclusive,
		Status:                          g.Status,
		Hydrated:                        true,
		SubscriptionType:                g.SubscriptionType,
		DailyLimitUSD:                   g.DailyLimitUsd,
		WeeklyLimitUSD:                  g.WeeklyLimitUsd,
		MonthlyLimitUSD:                 g.MonthlyLimitUsd,
		AllowImageGeneration:            g.AllowImageGeneration,
		ImageRateIndependent:            g.ImageRateIndependent,
		ImageRateMultiplier:             g.ImageRateMultiplier,
		ImagePrice1K:                    g.ImagePrice1k,
		ImagePrice2K:                    g.ImagePrice2k,
		ImagePrice4K:                    g.ImagePrice4k,
		DefaultValidityDays:             g.DefaultValidityDays,
		ClaudeCodeOnly:                  g.ClaudeCodeOnly,
		FallbackGroupID:                 g.FallbackGroupID,
		FallbackGroupIDOnInvalidRequest: g.FallbackGroupIDOnInvalidRequest,
		ModelRouting:                    g.ModelRouting,
		ModelRoutingEnabled:             g.ModelRoutingEnabled,
		MCPXMLInject:                    g.McpXMLInject,
		SupportedModelScopes:            g.SupportedModelScopes,
		SortOrder:                       g.SortOrder,
		AllowMessagesDispatch:           g.AllowMessagesDispatch,
		RequireOAuthOnly:                g.RequireOauthOnly,
		RequirePrivacySet:               g.RequirePrivacySet,
		DefaultMappedModel:              g.DefaultMappedModel,
		MessagesDispatchModelConfig:     g.MessagesDispatchModelConfig,
		RPMLimit:                        g.RpmLimit,
		CreatedAt:                       g.CreatedAt,
		UpdatedAt:                       g.UpdatedAt,
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
