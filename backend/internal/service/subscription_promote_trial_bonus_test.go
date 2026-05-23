package service

import (
	"context"
	"database/sql"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

type subscriptionPromoteAuthInvalidator struct {
	keys []string
}

func (s *subscriptionPromoteAuthInvalidator) InvalidateAuthCacheByKey(_ context.Context, key string) {
	s.keys = append(s.keys, key)
}

func (s *subscriptionPromoteAuthInvalidator) InvalidateAuthCacheByUserID(context.Context, int64)  {}
func (s *subscriptionPromoteAuthInvalidator) InvalidateAuthCacheByGroupID(context.Context, int64) {}

func newSubscriptionPromoteSQLite(t *testing.T) *dbent.Client {
	t.Helper()

	db, err := sql.Open("sqlite", "file:subscription_promote_trial_bonus?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestPromoteTrialBonusKeysToSubscriptionGroup(t *testing.T) {
	ctx := context.Background()
	client := newSubscriptionPromoteSQLite(t)

	user := client.User.Create().
		SetEmail("promote-trial@example.com").
		SetPasswordHash("test-password-hash").
		SetRole(RoleUser).
		SetStatus(StatusActive).
		SaveX(ctx)
	trialGroup := client.Group.Create().
		SetName(trialBonusGroupName).
		SetStatus(StatusActive).
		SetPlatform(PlatformOpenAI).
		SetSubscriptionType(SubscriptionTypeStandard).
		SetMonthlyLimitUsd(15).
		SaveX(ctx)
	paidGroup := client.Group.Create().
		SetName("paid-standard-v3").
		SetStatus(StatusActive).
		SetPlatform(PlatformOpenAI).
		SetSubscriptionType(SubscriptionTypeSubscription).
		SetMonthlyLimitUsd(1500).
		SaveX(ctx)

	activeTrialKey := client.APIKey.Create().
		SetUserID(user.ID).
		SetKey("sk-active-trial-key").
		SetName("trial").
		SetStatus(StatusActive).
		SetGroupID(trialGroup.ID).
		SaveX(ctx)
	exhaustedTrialKey := client.APIKey.Create().
		SetUserID(user.ID).
		SetKey("sk-exhausted-trial-key").
		SetName("exhausted").
		SetStatus(StatusAPIKeyQuotaExhausted).
		SetGroupID(trialGroup.ID).
		SaveX(ctx)

	invalidator := &subscriptionPromoteAuthInvalidator{}
	svc := &SubscriptionService{
		entClient:            client,
		authCacheInvalidator: invalidator,
	}

	err := svc.promoteTrialBonusKeysToSubscriptionGroup(ctx, user.ID, &Group{
		ID:               paidGroup.ID,
		Name:             paidGroup.Name,
		Platform:         paidGroup.Platform,
		SubscriptionType: paidGroup.SubscriptionType,
	})
	require.NoError(t, err)

	gotActive := client.APIKey.GetX(ctx, activeTrialKey.ID)
	require.NotNil(t, gotActive.GroupID)
	require.Equal(t, paidGroup.ID, *gotActive.GroupID)

	gotExhausted := client.APIKey.GetX(ctx, exhaustedTrialKey.ID)
	require.NotNil(t, gotExhausted.GroupID)
	require.Equal(t, trialGroup.ID, *gotExhausted.GroupID)
	require.Equal(t, []string{"sk-active-trial-key"}, invalidator.keys)
}
