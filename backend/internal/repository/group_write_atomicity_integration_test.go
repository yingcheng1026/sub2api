//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestAdminCreateGroupCopyFailureRollsBackGroupAndRetryCreatesOnce(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := uuid.NewString()
	source := mustCreateGroup(t, client, &service.Group{
		Name:           "group-copy-source-" + suffix,
		Platform:       service.PlatformOpenAI,
		RateMultiplier: 1,
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name:     "group-copy-account-" + suffix,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
	})
	mustBindAccountToGroup(t, client, account.ID, source.ID, 50)
	targetName := "group-copy-target-" + suffix
	cleanupGroupWriteFixtures(t, []int64{source.ID}, []int64{account.ID}, targetName)

	removeFailure := installGroupAccountBindFailure(t, ctx, "name", targetName)
	svc := newGroupWriteAtomicityAdminService(client, nil)

	_, err := svc.CreateGroup(ctx, &service.CreateGroupInput{
		Name:                        targetName,
		Platform:                    service.PlatformOpenAI,
		RateMultiplier:              1,
		CopyAccountsFromGroupIDs:    []int64{source.ID},
		MessagesDispatchModelConfig: service.OpenAIMessagesDispatchModelConfig{},
	})
	require.ErrorContains(t, err, "injected group account bind failure")
	require.Equal(t, 0, groupCountByName(t, ctx, targetName),
		"a failed account copy must not leave a committed group")

	removeFailure()
	created, err := svc.CreateGroup(ctx, &service.CreateGroupInput{
		Name:                     targetName,
		Platform:                 service.PlatformOpenAI,
		RateMultiplier:           1,
		CopyAccountsFromGroupIDs: []int64{source.ID},
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, 1, groupCountByName(t, ctx, targetName))
	require.Equal(t, []int64{account.ID}, groupAccountIDs(t, ctx, created.ID),
		"retry must create exactly one group with the requested bindings")
}

func TestAdminUpdateGroupCopyFailureRollsBackFieldsBindingsAndCacheInvalidation(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := uuid.NewString()
	target := mustCreateGroup(t, client, &service.Group{
		Name:           "group-update-target-" + suffix,
		Description:    "original description",
		Platform:       service.PlatformOpenAI,
		RateMultiplier: 1,
	})
	source := mustCreateGroup(t, client, &service.Group{
		Name:           "group-update-source-" + suffix,
		Platform:       service.PlatformOpenAI,
		RateMultiplier: 1,
	})
	originalAccount := mustCreateAccount(t, client, &service.Account{
		Name:     "group-update-original-account-" + suffix,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
	})
	replacementAccount := mustCreateAccount(t, client, &service.Account{
		Name:     "group-update-replacement-account-" + suffix,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
	})
	mustBindAccountToGroup(t, client, originalAccount.ID, target.ID, 50)
	mustBindAccountToGroup(t, client, replacementAccount.ID, source.ID, 50)
	cleanupGroupWriteFixtures(t, []int64{target.ID, source.ID}, []int64{originalAccount.ID, replacementAccount.ID}, "")

	removeFailure := installGroupAccountBindFailure(t, ctx, "id", target.ID)
	invalidator := &groupWriteAuthInvalidator{}
	svc := newGroupWriteAtomicityAdminService(client, invalidator)
	newRate := 2.5

	_, err := svc.UpdateGroup(ctx, target.ID, &service.UpdateGroupInput{
		Description:              "new description",
		RateMultiplier:           &newRate,
		CopyAccountsFromGroupIDs: []int64{source.ID},
	})
	require.ErrorContains(t, err, "injected group account bind failure")
	persisted := loadGroupWriteState(t, ctx, target.ID)
	require.Equal(t, "original description", persisted.description)
	require.InDelta(t, 1, persisted.rateMultiplier, 0.0001)
	require.Equal(t, []int64{originalAccount.ID}, groupAccountIDs(t, ctx, target.ID),
		"failed replacement must preserve the original account bindings")
	require.Empty(t, invalidator.groupIDs,
		"auth cache must not be invalidated for a transaction that rolls back")

	removeFailure()
	updated, err := svc.UpdateGroup(ctx, target.ID, &service.UpdateGroupInput{
		Description:              "new description",
		RateMultiplier:           &newRate,
		CopyAccountsFromGroupIDs: []int64{source.ID},
	})
	require.NoError(t, err)
	require.Equal(t, "new description", updated.Description)
	require.InDelta(t, newRate, updated.RateMultiplier, 0.0001)
	require.Equal(t, []int64{replacementAccount.ID}, groupAccountIDs(t, ctx, target.ID))
	require.Equal(t, []int64{target.ID}, invalidator.groupIDs,
		"auth cache invalidation must happen once, after the successful commit")
}

func TestAdminUpdateReservedGroupCopyFailurePreservesPolicyAndBindings(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := uuid.NewString()
	reserved := mustCreateGroup(t, client, &service.Group{
		Name:             service.WalletDefaultOpenAIGroupName,
		Description:      "reserved original",
		Platform:         service.PlatformOpenAI,
		RateMultiplier:   1,
		SubscriptionType: service.SubscriptionTypeStandard,
		Status:           service.StatusActive,
		IsExclusive:      false,
	})
	source := mustCreateGroup(t, client, &service.Group{
		Name:           "reserved-copy-source-" + suffix,
		Platform:       service.PlatformOpenAI,
		RateMultiplier: 1,
	})
	originalAccount := mustCreateAccount(t, client, &service.Account{
		Name:     "reserved-original-account-" + suffix,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
	})
	replacementAccount := mustCreateAccount(t, client, &service.Account{
		Name:     "reserved-replacement-account-" + suffix,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
	})
	mustBindAccountToGroup(t, client, originalAccount.ID, reserved.ID, 50)
	mustBindAccountToGroup(t, client, replacementAccount.ID, source.ID, 50)
	cleanupGroupWriteFixtures(t, []int64{reserved.ID, source.ID}, []int64{originalAccount.ID, replacementAccount.ID}, "")

	removeFailure := installGroupAccountBindFailure(t, ctx, "id", reserved.ID)
	svc := newGroupWriteAtomicityAdminService(client, &groupWriteAuthInvalidator{})
	newRate := 1.25

	_, err := svc.UpdateGroup(ctx, reserved.ID, &service.UpdateGroupInput{
		Description:              "reserved changed",
		RateMultiplier:           &newRate,
		CopyAccountsFromGroupIDs: []int64{source.ID},
	})
	require.ErrorContains(t, err, "injected group account bind failure")
	persisted := loadGroupWriteState(t, ctx, reserved.ID)
	require.Equal(t, service.WalletDefaultOpenAIGroupName, persisted.name)
	require.Equal(t, service.PlatformOpenAI, persisted.platform)
	require.Equal(t, service.StatusActive, persisted.status)
	require.Equal(t, service.SubscriptionTypeStandard, persisted.subscriptionType)
	require.False(t, persisted.isExclusive)
	require.Equal(t, "reserved original", persisted.description)
	require.InDelta(t, 1, persisted.rateMultiplier, 0.0001)
	require.Equal(t, []int64{originalAccount.ID}, groupAccountIDs(t, ctx, reserved.ID))

	removeFailure()
}

func newGroupWriteAtomicityAdminService(client *dbent.Client, invalidator service.APIKeyAuthCacheInvalidator) service.AdminService {
	return service.NewAdminService(
		nil,
		NewGroupRepository(client, integrationDB),
		NewAccountRepository(client, integrationDB, nil, nil),
		nil,
		nil,
		nil,
		nil,
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

type groupWriteAuthInvalidator struct {
	groupIDs []int64
}

func (*groupWriteAuthInvalidator) InvalidateAuthCacheByKey(context.Context, string)   {}
func (*groupWriteAuthInvalidator) InvalidateAuthCacheByUserID(context.Context, int64) {}
func (s *groupWriteAuthInvalidator) InvalidateAuthCacheByGroupID(_ context.Context, groupID int64) {
	s.groupIDs = append(s.groupIDs, groupID)
}

func installGroupAccountBindFailure(t *testing.T, ctx context.Context, selector string, value any) func() {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "test_fail_group_bind_" + suffix
	triggerName := "trg_fail_group_bind_" + suffix

	var predicate string
	switch selector {
	case "id":
		predicate = fmt.Sprintf("NEW.group_id = %d", value)
	case "name":
		predicate = fmt.Sprintf("EXISTS (SELECT 1 FROM groups WHERE id = NEW.group_id AND name = %s)", quoteSQLLiteral(fmt.Sprint(value)))
	default:
		t.Fatalf("unsupported bind failure selector %q", selector)
	}

	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF %s THEN
				RAISE EXCEPTION 'injected group account bind failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE INSERT ON account_groups
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, predicate, triggerName, functionName))
	require.NoError(t, err)

	removed := false
	remove := func() {
		if removed {
			return
		}
		removed = true
		_, dropErr := integrationDB.ExecContext(context.Background(), fmt.Sprintf(`
			DROP TRIGGER IF EXISTS %s ON account_groups;
			DROP FUNCTION IF EXISTS %s();
		`, triggerName, functionName))
		require.NoError(t, dropErr)
	}
	t.Cleanup(remove)
	return remove
}

func quoteSQLLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func groupCountByName(t *testing.T, ctx context.Context, name string) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM groups WHERE name = $1 AND deleted_at IS NULL
	`, name).Scan(&count))
	return count
}

func groupAccountIDs(t *testing.T, ctx context.Context, groupID int64) []int64 {
	t.Helper()
	rows, err := integrationDB.QueryContext(ctx, `
		SELECT account_id FROM account_groups WHERE group_id = $1 ORDER BY account_id
	`, groupID)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var ids []int64
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

type groupWriteState struct {
	name             string
	description      string
	platform         string
	status           string
	subscriptionType string
	rateMultiplier   float64
	isExclusive      bool
}

func loadGroupWriteState(t *testing.T, ctx context.Context, groupID int64) groupWriteState {
	t.Helper()
	var state groupWriteState
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT name, description, platform, status, subscription_type, rate_multiplier, is_exclusive
		FROM groups WHERE id = $1 AND deleted_at IS NULL
	`, groupID).Scan(
		&state.name,
		&state.description,
		&state.platform,
		&state.status,
		&state.subscriptionType,
		&state.rateMultiplier,
		&state.isExclusive,
	))
	return state
}

func cleanupGroupWriteFixtures(t *testing.T, groupIDs, accountIDs []int64, targetName string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if targetName != "" {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_groups WHERE group_id IN (SELECT id FROM groups WHERE name = $1)`, targetName)
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE name = $1`, targetName)
		}
		if len(groupIDs) > 0 {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_groups WHERE group_id = ANY($1)`, pq.Array(groupIDs))
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id = ANY($1)`, pq.Array(groupIDs))
		}
		if len(accountIDs) > 0 {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id = ANY($1)`, pq.Array(accountIDs))
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id = ANY($1)`, pq.Array(accountIDs))
		}
	})
}
