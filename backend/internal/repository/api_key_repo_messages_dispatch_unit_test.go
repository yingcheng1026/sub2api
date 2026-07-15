package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/userallowedgroup"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupEntityToService_PreservesMessagesDispatchModelConfig(t *testing.T) {
	group := &dbent.Group{
		ID:                    1,
		Name:                  "openai-dispatch",
		Platform:              service.PlatformOpenAI,
		Status:                service.StatusActive,
		SubscriptionType:      service.SubscriptionTypeStandard,
		RateMultiplier:        1,
		AllowMessagesDispatch: true,
		DefaultMappedModel:    "gpt-5.4",
		MessagesDispatchModelConfig: service.OpenAIMessagesDispatchModelConfig{
			OpusMappedModel:   "gpt-5.4-nano",
			SonnetMappedModel: "gpt-5.3-codex",
			HaikuMappedModel:  "gpt-5.4-mini",
			ExactModelMappings: map[string]string{
				"claude-sonnet-4.5": "gpt-5.4-nano",
			},
		},
	}

	got := groupEntityToService(group)
	require.NotNil(t, got)
	require.Equal(t, group.MessagesDispatchModelConfig, got.MessagesDispatchModelConfig)
}

func TestAPIKeyRepository_GetByKeyForAuth_PreservesMessagesDispatchModelConfig_SQLite(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "getbykey-auth-dispatch-unit@test.com")

	group, err := client.Group.Create().
		SetName("g-auth-dispatch-unit").
		SetPlatform(service.PlatformOpenAI).
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeStandard).
		SetRateMultiplier(1).
		SetAllowMessagesDispatch(true).
		SetDefaultMappedModel("gpt-5.4").
		SetMessagesDispatchModelConfig(service.OpenAIMessagesDispatchModelConfig{
			OpusMappedModel:   "gpt-5.4-nano",
			SonnetMappedModel: "gpt-5.3-codex",
			HaikuMappedModel:  "gpt-5.4-mini",
			ExactModelMappings: map[string]string{
				"claude-sonnet-4.5": "gpt-5.4-nano",
			},
		}).
		Save(ctx)
	require.NoError(t, err)

	key := &service.APIKey{
		UserID:  user.ID,
		Key:     "sk-getbykey-auth-dispatch-unit",
		Name:    "Dispatch Key Unit",
		GroupID: &group.ID,
		Status:  service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))

	got, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.Equal(t, key.Name, got.Name)
	require.NotNil(t, got.Group)
	require.Equal(t, group.MessagesDispatchModelConfig, got.Group.MessagesDispatchModelConfig)
}

func TestAPIKeyRepository_GetByKeyForAuth_PreservesExplicitAllowedGroupsAndExclusivity_SQLite(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "getbykey-auth-wallet-grant@test.com")

	group, err := client.Group.Create().
		SetName("vip").
		SetPlatform(service.PlatformAnthropic).
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeStandard).
		SetIsExclusive(true).
		SetRateMultiplier(1.7).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.UserAllowedGroup.Create().
		SetUserID(user.ID).
		SetGroupID(group.ID).
		Save(ctx)
	require.NoError(t, err)

	key := &service.APIKey{
		UserID:  user.ID,
		Key:     "sk-getbykey-auth-wallet-grant-unit",
		Name:    "VIP Wallet Key",
		GroupID: &group.ID,
		Status:  service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))

	got, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.NotNil(t, got.User)
	require.Contains(t, got.User.AllowedGroups, group.ID)
	require.NotNil(t, got.Group)
	require.True(t, got.Group.IsExclusive)
}

func TestAPIKeyRepository_ValidateAuthCacheSnapshot_DetectsPermissionStateChanges_SQLite(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "auth-cache-security-state@test.com")

	group, err := client.Group.Create().
		SetName("vip").
		SetPlatform(service.PlatformAnthropic).
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeStandard).
		SetIsExclusive(true).
		SetRateMultiplier(1.7).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.UserAllowedGroup.Create().
		SetUserID(user.ID).
		SetGroupID(group.ID).
		Save(ctx)
	require.NoError(t, err)

	key := &service.APIKey{
		UserID:  user.ID,
		Key:     "sk-auth-cache-security-state",
		Name:    "VIP key",
		GroupID: &group.ID,
		Status:  service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))

	loaded, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	snapshot := authSnapshotForRepositoryValidation(loaded)
	cacheLocator := repo.APIKeyAuthCacheLocator(key.Key)
	rpmOverride := 120
	_, err = repo.sql.ExecContext(ctx, `
		INSERT INTO user_group_rate_multipliers (user_id, group_id, rpm_override)
		VALUES ($1, $2, $3)`, user.ID, group.ID, rpmOverride)
	require.NoError(t, err)
	snapshot.User.UserGroupRPMOverride = &rpmOverride
	refreshSnapshot := func() {
		current, loadErr := repo.GetByKeyForAuth(ctx, key.Key)
		require.NoError(t, loadErr)
		snapshot = authSnapshotForRepositoryValidation(current)
		snapshot.User.UserGroupRPMOverride = &rpmOverride
		currentValid, validateErr := repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
		require.NoError(t, validateErr)
		require.True(t, currentValid)
	}

	valid, err := repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.True(t, valid)

	_, err = client.UserAllowedGroup.Delete().
		Where(userallowedgroup.UserIDEQ(user.ID), userallowedgroup.GroupIDEQ(group.ID)).
		Exec(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.False(t, valid, "revoking allowed_groups must invalidate a positive VIP snapshot")

	_, err = client.UserAllowedGroup.Create().SetUserID(user.ID).SetGroupID(group.ID).Save(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.True(t, valid)

	_, err = client.Group.UpdateOneID(group.ID).SetStatus(service.StatusDisabled).Save(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.False(t, valid, "disabling a group must invalidate its positive auth snapshot")
	_, err = client.Group.UpdateOneID(group.ID).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	refreshSnapshot()

	_, err = client.Group.UpdateOneID(group.ID).SetPlatform(service.PlatformOpenAI).Save(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.False(t, valid, "changing platform must invalidate the old routing permission snapshot")
	_, err = client.Group.UpdateOneID(group.ID).SetPlatform(service.PlatformAnthropic).Save(ctx)
	require.NoError(t, err)
	refreshSnapshot()

	_, err = client.Group.UpdateOneID(group.ID).SetIsExclusive(false).Save(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.False(t, valid, "changing exclusivity must invalidate the old membership policy snapshot")
	_, err = client.Group.UpdateOneID(group.ID).SetIsExclusive(true).Save(ctx)
	require.NoError(t, err)
	refreshSnapshot()

	_, err = client.Group.UpdateOneID(group.ID).SetRateMultiplier(2.5).Save(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.False(t, valid, "a group rate increase must invalidate the older lower-priced snapshot")
	_, err = client.Group.UpdateOneID(group.ID).SetRateMultiplier(1.7).Save(ctx)
	require.NoError(t, err)
	refreshSnapshot()

	_, err = client.Group.UpdateOneID(group.ID).SetDailyLimitUsd(10).Save(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.False(t, valid, "a group limit decrease must invalidate the older unlimited snapshot")
	_, err = client.Group.UpdateOneID(group.ID).ClearDailyLimitUsd().Save(ctx)
	require.NoError(t, err)
	refreshSnapshot()

	_, err = repo.sql.ExecContext(ctx, `
		UPDATE user_group_rate_multipliers
		SET rpm_override = $3, updated_at = CURRENT_TIMESTAMP
		WHERE user_id = $1 AND group_id = $2`, user.ID, group.ID, 60)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.False(t, valid, "a lower user-group RPM override must invalidate the old snapshot")
	_, err = repo.sql.ExecContext(ctx, `
		UPDATE user_group_rate_multipliers
		SET rpm_override = $3, updated_at = CURRENT_TIMESTAMP
		WHERE user_id = $1 AND group_id = $2`, user.ID, group.ID, rpmOverride)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.True(t, valid)

	valid, err = repo.ValidateAuthCacheSnapshot(ctx, repo.APIKeyAuthCacheLocator("a different raw key"), snapshot)
	require.NoError(t, err)
	require.False(t, valid, "the cache locator must still identify the same database key")

	_, err = client.APIKey.UpdateOneID(key.ID).SetQuota(1).SetQuotaUsed(1).Save(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.False(t, valid, "quota exhaustion must invalidate an older usable snapshot")
	_, err = client.APIKey.UpdateOneID(key.ID).SetQuota(0).SetQuotaUsed(0).Save(ctx)
	require.NoError(t, err)

	past := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	_, err = client.APIKey.UpdateOneID(key.ID).SetExpiresAt(past).Save(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.False(t, valid, "an expiration change must invalidate an unexpired snapshot")
	_, err = client.APIKey.UpdateOneID(key.ID).ClearExpiresAt().Save(ctx)
	require.NoError(t, err)

	_, err = client.APIKey.UpdateOneID(key.ID).SetIPWhitelist([]string{"192.0.2.10"}).Save(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.False(t, valid, "an IP allow-list change must invalidate the unrestricted snapshot")
	_, err = client.APIKey.UpdateOneID(key.ID).SetIPWhitelist([]string{}).Save(ctx)
	require.NoError(t, err)

	replacementKey := "sk-auth-cache-security-state-replaced"
	_, err = client.APIKey.UpdateOneID(key.ID).
		SetKey(replacementKey).
		SetKeyHash(repo.APIKeyAuthCacheLocator(replacementKey)).
		Save(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.False(t, valid, "a replaced key must not leave the old raw key authorized through cache")
	_, err = client.APIKey.UpdateOneID(key.ID).
		SetKey(key.Key).
		SetKeyHash(repo.APIKeyAuthCacheLocator(key.Key)).
		Save(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.True(t, valid)

	_, err = client.User.UpdateOneID(user.ID).SetStatus(service.StatusDisabled).Save(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.False(t, valid, "disabling the user must invalidate every cached API key")
}

func TestAPIKeyRepository_ValidateAuthCacheSnapshot_UniversalWalletComparesAllAllowedGroups_SQLite(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "auth-cache-universal-wallet@test.com")
	group, err := client.Group.Create().
		SetName("vip").
		SetPlatform(service.PlatformAnthropic).
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeStandard).
		SetIsExclusive(true).
		SetRateMultiplier(1).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.UserAllowedGroup.Create().SetUserID(user.ID).SetGroupID(group.ID).Save(ctx)
	require.NoError(t, err)

	key := &service.APIKey{
		UserID:  user.ID,
		Key:     "sk-auth-cache-universal-wallet",
		Name:    service.WalletUniversalAPIKeyName,
		Purpose: service.APIKeyPurposeWalletUniversal,
		Status:  service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))
	loaded, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.Nil(t, loaded.GroupID)
	snapshot := authSnapshotForRepositoryValidation(loaded)
	cacheLocator := repo.APIKeyAuthCacheLocator(key.Key)

	valid, err := repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.True(t, valid)
	_, err = client.UserAllowedGroup.Delete().
		Where(userallowedgroup.UserIDEQ(user.ID), userallowedgroup.GroupIDEQ(group.ID)).
		Exec(ctx)
	require.NoError(t, err)
	valid, err = repo.ValidateAuthCacheSnapshot(ctx, cacheLocator, snapshot)
	require.NoError(t, err)
	require.False(t, valid, "a NULL-group wallet key must not retain a revoked VIP grant")
}

func authSnapshotForRepositoryValidation(key *service.APIKey) *service.APIKeyAuthSnapshot {
	snapshot := &service.APIKeyAuthSnapshot{
		Version:     11,
		APIKeyID:    key.ID,
		UserID:      key.UserID,
		GroupID:     key.GroupID,
		Name:        key.Name,
		Purpose:     key.Purpose,
		Status:      key.Status,
		IPWhitelist: append([]string(nil), key.IPWhitelist...),
		IPBlacklist: append([]string(nil), key.IPBlacklist...),
		Quota:       key.Quota,
		QuotaUsed:   key.QuotaUsed,
		ExpiresAt:   key.ExpiresAt,
		RateLimit5h: key.RateLimit5h,
		RateLimit1d: key.RateLimit1d,
		RateLimit7d: key.RateLimit7d,
		User: service.APIKeyAuthUserSnapshot{
			ID:            key.User.ID,
			Status:        key.User.Status,
			Role:          key.User.Role,
			Balance:       key.User.Balance,
			Concurrency:   key.User.Concurrency,
			RPMLimit:      key.User.RPMLimit,
			AllowedGroups: append([]int64(nil), key.User.AllowedGroups...),
		},
	}
	if key.Group != nil {
		snapshot.Group = &service.APIKeyAuthGroupSnapshot{
			ID:               key.Group.ID,
			Name:             key.Group.Name,
			Platform:         key.Group.Platform,
			Status:           key.Group.Status,
			IsExclusive:      key.Group.IsExclusive,
			SubscriptionType: key.Group.SubscriptionType,
			RPMLimit:         key.Group.RPMLimit,
			UpdatedAt:        key.Group.UpdatedAt,
		}
	}
	return snapshot
}
