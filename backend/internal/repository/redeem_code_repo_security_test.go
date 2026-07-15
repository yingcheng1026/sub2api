package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestRedeemCodeRepositoryDeleteIfUnusedProtectsUsedAuditRow(t *testing.T) {
	ctx := context.Background()
	client := newSecuritySecretTestClient(t)
	repo := &redeemCodeRepository{client: client}
	user, err := client.User.Create().
		SetEmail("redeem-delete-guard@example.com").
		SetPasswordHash("not-a-real-password-hash").
		Save(ctx)
	require.NoError(t, err)
	usedBy := user.ID
	usedAt := time.Now().UTC().Truncate(time.Second)
	used, err := client.RedeemCode.Create().
		SetCode("used-delete-guard").
		SetType(service.RedeemTypeBalance).
		SetValue(10).
		SetStatus(service.StatusUsed).
		SetUsedBy(usedBy).
		SetUsedAt(usedAt).
		Save(ctx)
	require.NoError(t, err)
	unused, err := client.RedeemCode.Create().
		SetCode("unused-delete-guard").
		SetType(service.RedeemTypeBalance).
		SetValue(10).
		SetStatus(service.StatusUnused).
		Save(ctx)
	require.NoError(t, err)
	expired, err := client.RedeemCode.Create().
		SetCode("expired-delete-guard").
		SetType(service.RedeemTypeBalance).
		SetValue(10).
		SetStatus(service.StatusExpired).
		Save(ctx)
	require.NoError(t, err)

	deleted, err := repo.DeleteIfUnused(ctx, used.ID)
	require.NoError(t, err)
	require.False(t, deleted)
	stillUsed, err := client.RedeemCode.Get(ctx, used.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusUsed, stillUsed.Status)
	require.Equal(t, usedBy, *stillUsed.UsedBy)
	require.WithinDuration(t, usedAt, *stillUsed.UsedAt, time.Second)

	deleted, err = repo.DeleteIfUnused(ctx, unused.ID)
	require.NoError(t, err)
	require.True(t, deleted)
	exists, err := client.RedeemCode.Query().Where(redeemcode.IDEQ(unused.ID)).Exist(ctx)
	require.NoError(t, err)
	require.False(t, exists)

	deleted, err = repo.DeleteIfUnused(ctx, expired.ID)
	require.NoError(t, err)
	require.False(t, deleted)
	stillExpired, err := client.RedeemCode.Get(ctx, expired.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusExpired, stillExpired.Status)
}

func TestRedeemCodeRepositoryExpireIfUnusedCannotOverwriteUsedState(t *testing.T) {
	ctx := context.Background()
	client := newSecuritySecretTestClient(t)
	repo := &redeemCodeRepository{client: client}
	user, err := client.User.Create().
		SetEmail("redeem-expire-guard@example.com").
		SetPasswordHash("not-a-real-password-hash").
		Save(ctx)
	require.NoError(t, err)
	usedBy := user.ID
	usedAt := time.Now().UTC().Truncate(time.Second)
	used, err := client.RedeemCode.Create().
		SetCode("used-expire-guard").
		SetType(service.RedeemTypeBalance).
		SetValue(5).
		SetStatus(service.StatusUsed).
		SetUsedBy(usedBy).
		SetUsedAt(usedAt).
		Save(ctx)
	require.NoError(t, err)
	unused, err := client.RedeemCode.Create().
		SetCode("unused-expire-guard").
		SetType(service.RedeemTypeBalance).
		SetValue(5).
		SetStatus(service.StatusUnused).
		Save(ctx)
	require.NoError(t, err)

	expired, err := repo.ExpireIfUnused(ctx, used.ID)
	require.NoError(t, err)
	require.False(t, expired)
	stillUsed, err := client.RedeemCode.Get(ctx, used.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusUsed, stillUsed.Status)
	require.Equal(t, usedBy, *stillUsed.UsedBy)
	require.WithinDuration(t, usedAt, *stillUsed.UsedAt, time.Second)

	expired, err = repo.ExpireIfUnused(ctx, unused.ID)
	require.NoError(t, err)
	require.True(t, expired)
	nowExpired, err := client.RedeemCode.Get(ctx, unused.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusExpired, nowExpired.Status)
	require.Nil(t, nowExpired.UsedBy)
	require.Nil(t, nowExpired.UsedAt)
}
