//go:build integration

package repository

import (
	"context"
	"database/sql"
	"sort"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestAdminUpdateUserGroupPolicyFailureRollsBackAllChanges(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := uuid.NewString()
	groupA := mustCreateGroup(t, client, &service.Group{
		Name:           "admin-user-policy-a-" + suffix,
		Platform:       service.PlatformOpenAI,
		RateMultiplier: 1,
	})
	groupB := mustCreateGroup(t, client, &service.Group{
		Name:           "admin-user-policy-b-" + suffix,
		Platform:       service.PlatformOpenAI,
		RateMultiplier: 1,
	})
	userRepo := NewUserRepository(client, integrationDB)
	user := &service.User{
		Email:         "admin-user-policy-" + suffix + "@example.test",
		PasswordHash:  "test-password-hash",
		Role:          service.RoleUser,
		Status:        service.StatusActive,
		Concurrency:   5,
		RPMLimit:      10,
		AllowedGroups: []int64{groupA.ID},
	}
	require.NoError(t, userRepo.Create(ctx, user))
	cleanupAdminUserGroupFixtures(t, user.ID, []int64{groupA.ID, groupB.ID})

	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO user_group_rate_multipliers
			(user_id, group_id, rate_multiplier, created_at, updated_at)
		VALUES ($1, $2, 1.25, NOW(), NOW())
	`, user.ID, groupA.ID)
	require.NoError(t, err)

	invalidator := &adminUserGroupAuthInvalidator{}
	svc := newAdminUserGroupAtomicityService(client, userRepo, invalidator)
	newRPM := 60
	newRate := 2.5
	allowedGroups := []int64{groupB.ID}
	missingGroupID := groupB.ID + 9_000_000_000

	_, err = svc.UpdateUser(ctx, user.ID, &service.UpdateUserInput{
		RPMLimit:      &newRPM,
		AllowedGroups: &allowedGroups,
		GroupRates: map[int64]*float64{
			missingGroupID: &newRate,
		},
	})
	require.Error(t, err, "invalid group-rate foreign key must fail the whole admin update")
	require.Equal(t, []int64{groupA.ID}, loadAdminAllowedGroupIDs(t, ctx, user.ID))
	require.Equal(t, 10, loadAdminUserRPMLimit(t, ctx, user.ID))
	require.InDelta(t, 1.25, loadAdminUserGroupRate(t, ctx, user.ID, groupA.ID), 0.000001)
	require.Empty(t, invalidator.userIDs, "rolled-back changes must not invalidate committed auth state")

	_, err = svc.UpdateUser(ctx, user.ID, &service.UpdateUserInput{
		RPMLimit:      &newRPM,
		AllowedGroups: &allowedGroups,
		GroupRates: map[int64]*float64{
			groupB.ID: &newRate,
		},
	})
	require.NoError(t, err)
	require.Equal(t, []int64{groupB.ID}, loadAdminAllowedGroupIDs(t, ctx, user.ID))
	require.Equal(t, 60, loadAdminUserRPMLimit(t, ctx, user.ID))
	require.InDelta(t, 1.25, loadAdminUserGroupRate(t, ctx, user.ID, groupA.ID), 0.000001,
		"partial user rate maps must preserve unspecified group rates")
	require.InDelta(t, newRate, loadAdminUserGroupRate(t, ctx, user.ID, groupB.ID), 0.000001)
	require.Equal(t, []int64{user.ID}, invalidator.userIDs)
}

func TestAdminBatchGroupRatesFailureDoesNotPartiallyClearExistingRows(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := uuid.NewString()
	group := mustCreateGroup(t, client, &service.Group{
		Name:           "admin-group-rate-atomic-" + suffix,
		Platform:       service.PlatformOpenAI,
		RateMultiplier: 1,
	})
	user := mustCreateUser(t, client, &service.User{Email: "admin-group-rate-" + suffix + "@example.test"})
	cleanupAdminUserGroupFixtures(t, user.ID, []int64{group.ID})

	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO user_group_rate_multipliers
			(user_id, group_id, rate_multiplier, created_at, updated_at)
		VALUES ($1, $2, 1.75, NOW(), NOW())
	`, user.ID, group.ID)
	require.NoError(t, err)

	invalidator := &adminUserGroupAuthInvalidator{}
	svc := newAdminUserGroupAtomicityService(client, NewUserRepository(client, integrationDB), invalidator)
	err = svc.BatchSetGroupRateMultipliers(ctx, group.ID, []service.GroupRateMultiplierInput{
		{UserID: user.ID + 9_000_000_000, RateMultiplier: 2.5},
	})
	require.Error(t, err)
	require.InDelta(t, 1.75, loadAdminUserGroupRate(t, ctx, user.ID, group.ID), 0.000001,
		"failed replacement must preserve the previous group rates")
	require.Empty(t, invalidator.groupIDs, "rolled-back rate changes must not invalidate committed auth state")
}

func TestAdminBatchGroupRPMFailureDoesNotPartiallyClearExistingRows(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := uuid.NewString()
	group := mustCreateGroup(t, client, &service.Group{
		Name:           "admin-group-rpm-atomic-" + suffix,
		Platform:       service.PlatformOpenAI,
		RateMultiplier: 1,
	})
	user := mustCreateUser(t, client, &service.User{Email: "admin-group-rpm-" + suffix + "@example.test"})
	cleanupAdminUserGroupFixtures(t, user.ID, []int64{group.ID})

	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO user_group_rate_multipliers
			(user_id, group_id, rpm_override, created_at, updated_at)
		VALUES ($1, $2, 25, NOW(), NOW())
	`, user.ID, group.ID)
	require.NoError(t, err)

	invalidator := &adminUserGroupAuthInvalidator{}
	svc := newAdminUserGroupAtomicityService(client, NewUserRepository(client, integrationDB), invalidator)
	newRPM := 50
	err = svc.BatchSetGroupRPMOverrides(ctx, group.ID, []service.GroupRPMOverrideInput{
		{UserID: user.ID + 9_000_000_000, RPMOverride: &newRPM},
	})
	require.Error(t, err)
	require.Equal(t, 25, loadAdminUserGroupRPM(t, ctx, user.ID, group.ID),
		"failed replacement must preserve the previous RPM overrides")
	require.Empty(t, invalidator.groupIDs, "rolled-back RPM changes must not invalidate committed auth state")
}

func TestAdminClearGroupRatesPreservesRPMOverrideAndInvalidatesAfterCommit(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := uuid.NewString()
	group := mustCreateGroup(t, client, &service.Group{
		Name:           "admin-clear-group-rate-" + suffix,
		Platform:       service.PlatformOpenAI,
		RateMultiplier: 1,
	})
	user := mustCreateUser(t, client, &service.User{Email: "admin-clear-group-rate-" + suffix + "@example.test"})
	cleanupAdminUserGroupFixtures(t, user.ID, []int64{group.ID})

	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO user_group_rate_multipliers
			(user_id, group_id, rate_multiplier, rpm_override, created_at, updated_at)
		VALUES ($1, $2, 1.75, 25, NOW(), NOW())
	`, user.ID, group.ID)
	require.NoError(t, err)

	invalidator := &adminUserGroupAuthInvalidator{}
	svc := newAdminUserGroupAtomicityService(client, NewUserRepository(client, integrationDB), invalidator)
	require.NoError(t, svc.ClearGroupRateMultipliers(ctx, group.ID))

	var rate sql.NullFloat64
	var rpm sql.NullInt64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT rate_multiplier, rpm_override
		FROM user_group_rate_multipliers
		WHERE user_id = $1 AND group_id = $2
	`, user.ID, group.ID).Scan(&rate, &rpm))
	require.False(t, rate.Valid)
	require.True(t, rpm.Valid)
	require.EqualValues(t, 25, rpm.Int64)
	require.Equal(t, []int64{group.ID}, invalidator.groupIDs)
}

func newAdminUserGroupAtomicityService(
	client *dbent.Client,
	userRepo service.UserRepository,
	invalidator service.APIKeyAuthCacheInvalidator,
) service.AdminService {
	return service.NewAdminService(
		userRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		NewUserGroupRateRepository(integrationDB),
		nil,
		nil,
		nil,
		nil,
		invalidator,
		client,
		nil,
		nil,
		nil,
		nil,
	)
}

type adminUserGroupAuthInvalidator struct {
	userIDs  []int64
	groupIDs []int64
}

func (*adminUserGroupAuthInvalidator) InvalidateAuthCacheByKey(context.Context, string) {}
func (s *adminUserGroupAuthInvalidator) InvalidateAuthCacheByGroupID(_ context.Context, groupID int64) {
	s.groupIDs = append(s.groupIDs, groupID)
}
func (s *adminUserGroupAuthInvalidator) InvalidateAuthCacheByUserID(_ context.Context, userID int64) {
	s.userIDs = append(s.userIDs, userID)
}

func loadAdminAllowedGroupIDs(t *testing.T, ctx context.Context, userID int64) []int64 {
	t.Helper()
	rows, err := integrationDB.QueryContext(ctx, `
		SELECT group_id FROM user_allowed_groups WHERE user_id = $1 ORDER BY group_id
	`, userID)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var ids []int64
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func loadAdminUserRPMLimit(t *testing.T, ctx context.Context, userID int64) int {
	t.Helper()
	var rpm int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT rpm_limit FROM users WHERE id = $1`, userID).Scan(&rpm))
	return rpm
}

func loadAdminUserGroupRate(t *testing.T, ctx context.Context, userID, groupID int64) float64 {
	t.Helper()
	var rate float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT rate_multiplier FROM user_group_rate_multipliers WHERE user_id = $1 AND group_id = $2
	`, userID, groupID).Scan(&rate))
	return rate
}

func loadAdminUserGroupRPM(t *testing.T, ctx context.Context, userID, groupID int64) int {
	t.Helper()
	var rpm int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT rpm_override FROM user_group_rate_multipliers WHERE user_id = $1 AND group_id = $2
	`, userID, groupID).Scan(&rpm))
	return rpm
}

func cleanupAdminUserGroupFixtures(t *testing.T, userID int64, groupIDs []int64) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM user_group_rate_multipliers WHERE user_id = $1`, userID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM user_allowed_groups WHERE user_id = $1`, userID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM auth_identity_channels WHERE identity_id IN (SELECT id FROM auth_identities WHERE user_id = $1)`, userID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM auth_identities WHERE user_id = $1`, userID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, userID)
		if len(groupIDs) > 0 {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id = ANY($1)`, pq.Array(groupIDs))
		}
	})
}
