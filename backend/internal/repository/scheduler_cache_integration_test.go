//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerCacheSnapshotUsesSlimMetadataButKeepsFullAccount(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache, err := NewSchedulerCache(rdb, newDomainMigrationTestEncryptor(t))
	require.NoError(t, err)

	bucket := service.SchedulerBucket{GroupID: 2, Platform: service.PlatformGemini, Mode: service.SchedulerModeSingle}
	now := time.Now().UTC().Truncate(time.Second)
	limitReset := now.Add(10 * time.Minute)
	overloadUntil := now.Add(2 * time.Minute)
	tempUnschedUntil := now.Add(3 * time.Minute)
	windowEnd := now.Add(5 * time.Hour)

	account := service.Account{
		ID:          101,
		Name:        "gemini-heavy",
		Platform:    service.PlatformGemini,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 3,
		Priority:    7,
		LastUsedAt:  &now,
		Credentials: map[string]any{
			"api_key":       "gemini-api-key",
			"access_token":  "secret-access-token",
			"project_id":    "proj-1",
			"oauth_type":    "ai_studio",
			"model_mapping": map[string]any{"gemini-2.5-pro": "gemini-2.5-pro"},
			"huge_blob":     strings.Repeat("x", 4096),
		},
		Extra: map[string]any{
			"mixed_scheduling":             true,
			"window_cost_limit":            12.5,
			"window_cost_sticky_reserve":   8.0,
			"max_sessions":                 4,
			"session_idle_timeout_minutes": 11,
			"unused_large_field":           strings.Repeat("y", 4096),
		},
		RateLimitResetAt:       &limitReset,
		OverloadUntil:          &overloadUntil,
		TempUnschedulableUntil: &tempUnschedUntil,
		SessionWindowStart:     &now,
		SessionWindowEnd:       &windowEnd,
		SessionWindowStatus:    "active",
		GroupIDs:               []int64{bucket.GroupID},
		AccountGroups: []service.AccountGroup{
			{
				AccountID: 101,
				GroupID:   bucket.GroupID,
				Priority:  5,
				Group:     &service.Group{ID: bucket.GroupID, Name: "gemini-group"},
			},
		},
	}

	require.NoError(t, cache.SetSnapshot(ctx, bucket, []service.Account{account}))

	storedFull, err := rdb.Get(ctx, schedulerAccountKey("101")).Result()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(storedFull, secretDomainCiphertextPrefix))
	require.NotContains(t, storedFull, "secret-access-token")
	require.NotContains(t, storedFull, "gemini-api-key")
	storedMetadata, err := rdb.Get(ctx, schedulerAccountMetaKey("101")).Result()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(storedMetadata, secretDomainCiphertextPrefix))
	require.NotContains(t, storedMetadata, "gemini-api-key")

	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)

	got := snapshot[0]
	require.NotNil(t, got)
	require.Empty(t, got.GetCredential("api_key"))
	require.Equal(t, true, got.Credentials[service.SchedulerMetadataAPIKeyConfigured])
	require.Equal(t, "proj-1", got.GetCredential("project_id"))
	require.Equal(t, "ai_studio", got.GetCredential("oauth_type"))
	require.NotEmpty(t, got.GetModelMapping())
	require.Empty(t, got.GetCredential("access_token"))
	require.Empty(t, got.GetCredential("huge_blob"))
	require.Equal(t, true, got.Extra["mixed_scheduling"])
	require.Equal(t, 12.5, got.GetWindowCostLimit())
	require.Equal(t, 8.0, got.GetWindowCostStickyReserve())
	require.Equal(t, 4, got.GetMaxSessions())
	require.Equal(t, 11, got.GetSessionIdleTimeoutMinutes())
	require.Nil(t, got.Extra["unused_large_field"])
	require.Equal(t, []int64{bucket.GroupID}, got.GroupIDs)
	require.Len(t, got.AccountGroups, 1)
	require.Equal(t, account.ID, got.AccountGroups[0].AccountID)
	require.Equal(t, bucket.GroupID, got.AccountGroups[0].GroupID)
	require.Nil(t, got.AccountGroups[0].Group)

	full, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, full)
	require.Equal(t, "secret-access-token", full.GetCredential("access_token"))
	require.Equal(t, strings.Repeat("x", 4096), full.GetCredential("huge_blob"))
	require.Len(t, full.AccountGroups, 1)
	require.NotNil(t, full.AccountGroups[0].Group)
}

func TestPurgeLegacySchedulerCacheSecretsRemovesOnlyPlaintextNamespace(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)

	require.NoError(t, rdb.Set(ctx, "sched:acc:7", `{"Credentials":{"access_token":"legacy-secret"}}`, 0).Err())
	require.NoError(t, rdb.Set(ctx, "sched:meta:7", `{"Credentials":{"api_key":"legacy-key"}}`, 0).Err())
	require.NoError(t, rdb.Set(ctx, schedulerAccountKey("7"), "new-encrypted-namespace", 0).Err())

	removed, err := PurgeLegacySchedulerCacheSecrets(ctx, rdb)
	require.NoError(t, err)
	require.EqualValues(t, 2, removed)
	require.EqualValues(t, 0, rdb.Exists(ctx, "sched:acc:7", "sched:meta:7").Val())
	require.EqualValues(t, 1, rdb.Exists(ctx, schedulerAccountKey("7")).Val())
}

func TestSeedSchedulerCacheV2WatermarkCopiesLegacyOnlyOnce(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	require.NoError(t, rdb.Del(ctx, schedulerOutboxWatermarkKey, schedulerLegacyWatermarkKey).Err())
	require.NoError(t, rdb.Set(ctx, schedulerLegacyWatermarkKey, "123", 0).Err())

	seeded, err := SeedSchedulerCacheV2Watermark(ctx, rdb)
	require.NoError(t, err)
	require.True(t, seeded)
	require.Equal(t, "123", rdb.Get(ctx, schedulerOutboxWatermarkKey).Val())

	require.NoError(t, rdb.Set(ctx, schedulerLegacyWatermarkKey, "456", 0).Err())
	seeded, err = SeedSchedulerCacheV2Watermark(ctx, rdb)
	require.NoError(t, err)
	require.False(t, seeded)
	require.Equal(t, "123", rdb.Get(ctx, schedulerOutboxWatermarkKey).Val())
}
