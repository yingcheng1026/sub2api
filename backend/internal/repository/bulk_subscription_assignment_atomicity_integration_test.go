//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestBulkAssignSubscriptionPerUserAtomicityAndRetryRepair(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := uuid.NewString()
	firstUser := mustCreateUser(t, client, &service.User{
		Email:    "bulk-atomic-first-" + suffix + "@example.test",
		Username: "bulk-atomic-first-" + suffix,
	})
	secondUser := mustCreateUser(t, client, &service.User{
		Email:    "bulk-atomic-second-" + suffix + "@example.test",
		Username: "bulk-atomic-second-" + suffix,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "bulk-atomic-" + suffix,
		Platform:         service.PlatformOpenAI,
		SubscriptionType: service.SubscriptionTypeSubscription,
		Status:           service.StatusActive,
	})

	svc := service.NewSubscriptionService(
		NewGroupRepository(client, integrationDB),
		NewUserSubscriptionRepository(client),
		nil,
		client,
		nil,
	)
	removeFailure := installBulkAllowedGroupInsertFailure(t, ctx, firstUser.ID, group.ID)
	input := &service.BulkAssignSubscriptionInput{
		UserIDs:      []int64{firstUser.ID, secondUser.ID},
		GroupID:      group.ID,
		ValidityDays: 30,
		AssignedBy:   secondUser.ID,
		Notes:        "bulk atomic assignment",
	}

	first, err := svc.BulkAssignSubscription(ctx, input)
	require.NoError(t, err)
	require.Equal(t, 1, first.SuccessCount)
	require.Equal(t, 1, first.CreatedCount)
	require.Equal(t, 1, first.FailedCount)
	require.Equal(t, "failed", first.Statuses[firstUser.ID])
	require.Equal(t, "created", first.Statuses[secondUser.ID])
	require.Equal(t, 0, bulkSubscriptionCount(t, ctx, firstUser.ID, group.ID),
		"a grant failure must roll back the same user's subscription row")
	require.Equal(t, 0, bulkAllowedGroupCount(t, ctx, firstUser.ID, group.ID))
	require.Equal(t, 1, bulkSubscriptionCount(t, ctx, secondUser.ID, group.ID),
		"one user's failure must not roll back another user's committed assignment")
	require.Equal(t, 1, bulkAllowedGroupCount(t, ctx, secondUser.ID, group.ID))

	removeFailure()
	retry, err := svc.BulkAssignSubscription(ctx, input)
	require.NoError(t, err)
	require.Equal(t, 2, retry.SuccessCount)
	require.Equal(t, 1, retry.CreatedCount)
	require.Equal(t, 1, retry.ReusedCount)
	require.Zero(t, retry.FailedCount)
	require.Equal(t, "created", retry.Statuses[firstUser.ID])
	require.Equal(t, "reused", retry.Statuses[secondUser.ID])
	require.Equal(t, 1, bulkSubscriptionCount(t, ctx, firstUser.ID, group.ID))
	require.Equal(t, 1, bulkAllowedGroupCount(t, ctx, firstUser.ID, group.ID))
	require.Equal(t, 1, bulkSubscriptionCount(t, ctx, secondUser.ID, group.ID),
		"an ACK-loss style retry must reuse, not duplicate, an already committed subscription")
	require.Equal(t, 1, bulkAllowedGroupCount(t, ctx, secondUser.ID, group.ID))
}

func TestBulkAssignSubscriptionReusedAssignmentRepairsMissingGrant(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := uuid.NewString()
	user := mustCreateUser(t, client, &service.User{
		Email:    "bulk-repair-" + suffix + "@example.test",
		Username: "bulk-repair-" + suffix,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "bulk-repair-" + suffix,
		Platform:         service.PlatformOpenAI,
		SubscriptionType: service.SubscriptionTypeSubscription,
		Status:           service.StatusActive,
	})
	svc := service.NewSubscriptionService(
		NewGroupRepository(client, integrationDB),
		NewUserSubscriptionRepository(client),
		nil,
		client,
		nil,
	)

	created, err := svc.AssignSubscription(ctx, &service.AssignSubscriptionInput{
		UserID:       user.ID,
		GroupID:      group.ID,
		ValidityDays: 30,
		Notes:        "repair missing grant",
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	_, err = integrationDB.ExecContext(ctx, `
		DELETE FROM user_allowed_groups WHERE user_id = $1 AND group_id = $2
	`, user.ID, group.ID)
	require.NoError(t, err)
	require.Equal(t, 0, bulkAllowedGroupCount(t, ctx, user.ID, group.ID))

	result, err := svc.BulkAssignSubscription(ctx, &service.BulkAssignSubscriptionInput{
		UserIDs:      []int64{user.ID},
		GroupID:      group.ID,
		ValidityDays: 30,
		Notes:        "repair missing grant",
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.SuccessCount)
	require.Equal(t, 1, result.ReusedCount)
	require.Equal(t, "reused", result.Statuses[user.ID])
	require.Equal(t, 1, bulkSubscriptionCount(t, ctx, user.ID, group.ID))
	require.Equal(t, 1, bulkAllowedGroupCount(t, ctx, user.ID, group.ID),
		"the reused branch must idempotently repair the access grant")
}

func installBulkAllowedGroupInsertFailure(t *testing.T, ctx context.Context, userID, groupID int64) func() {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "test_fail_bulk_allowed_group_" + suffix
	triggerName := "trg_fail_bulk_allowed_group_" + suffix
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.user_id = %d AND NEW.group_id = %d THEN
				RAISE EXCEPTION 'injected bulk allowed-group failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE INSERT ON user_allowed_groups
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, userID, groupID, triggerName, functionName))
	require.NoError(t, err)
	removed := false
	remove := func() {
		if removed {
			return
		}
		removed = true
		_, dropErr := integrationDB.ExecContext(context.Background(), fmt.Sprintf(`
			DROP TRIGGER IF EXISTS %s ON user_allowed_groups;
			DROP FUNCTION IF EXISTS %s();
		`, triggerName, functionName))
		require.NoError(t, dropErr)
	}
	t.Cleanup(remove)
	return remove
}

func bulkSubscriptionCount(t *testing.T, ctx context.Context, userID, groupID int64) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM user_subscriptions
		WHERE user_id = $1 AND group_id = $2 AND deleted_at IS NULL
	`, userID, groupID).Scan(&count))
	return count
}

func bulkAllowedGroupCount(t *testing.T, ctx context.Context, userID, groupID int64) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM user_allowed_groups WHERE user_id = $1 AND group_id = $2
	`, userID, groupID).Scan(&count))
	return count
}
