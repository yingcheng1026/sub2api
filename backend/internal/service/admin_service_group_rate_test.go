//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// userGroupRateRepoStubForGroupRate implements UserGroupRateRepository for group rate tests.
type userGroupRateRepoStubForGroupRate struct {
	getByGroupIDData map[int64][]UserGroupRateEntry
	getByGroupIDErr  error

	deletedGroupIDs  []int64
	deleteByGroupErr error

	syncedGroupID int64
	syncedEntries []GroupRateMultiplierInput
	syncGroupErr  error

	rpmSyncedGroupID int64
	rpmSyncedEntries []GroupRPMOverrideInput
	rpmSyncErr       error
}

func (s *userGroupRateRepoStubForGroupRate) GetByUserID(_ context.Context, _ int64) (map[int64]float64, error) {
	panic("unexpected GetByUserID call")
}

func (s *userGroupRateRepoStubForGroupRate) GetByUserAndGroup(_ context.Context, _, _ int64) (*float64, error) {
	panic("unexpected GetByUserAndGroup call")
}

func (s *userGroupRateRepoStubForGroupRate) GetRPMOverrideByUserAndGroup(_ context.Context, _, _ int64) (*int, error) {
	panic("unexpected GetRPMOverrideByUserAndGroup call")
}

func (s *userGroupRateRepoStubForGroupRate) GetByGroupID(_ context.Context, groupID int64) ([]UserGroupRateEntry, error) {
	if s.getByGroupIDErr != nil {
		return nil, s.getByGroupIDErr
	}
	return s.getByGroupIDData[groupID], nil
}

func (s *userGroupRateRepoStubForGroupRate) SyncUserGroupRates(_ context.Context, _ int64, _ map[int64]*float64) error {
	panic("unexpected SyncUserGroupRates call")
}

func (s *userGroupRateRepoStubForGroupRate) SyncGroupRateMultipliers(_ context.Context, groupID int64, entries []GroupRateMultiplierInput) error {
	s.syncedGroupID = groupID
	s.syncedEntries = entries
	return s.syncGroupErr
}

func (s *userGroupRateRepoStubForGroupRate) SyncGroupRPMOverrides(_ context.Context, groupID int64, entries []GroupRPMOverrideInput) error {
	s.rpmSyncedGroupID = groupID
	s.rpmSyncedEntries = entries
	return s.rpmSyncErr
}

func (s *userGroupRateRepoStubForGroupRate) ClearGroupRPMOverrides(_ context.Context, _ int64) error {
	panic("unexpected ClearGroupRPMOverrides call")
}

func (s *userGroupRateRepoStubForGroupRate) DeleteByGroupID(_ context.Context, groupID int64) error {
	s.deletedGroupIDs = append(s.deletedGroupIDs, groupID)
	return s.deleteByGroupErr
}

func (s *userGroupRateRepoStubForGroupRate) DeleteByUserID(_ context.Context, _ int64) error {
	panic("unexpected DeleteByUserID call")
}

func TestAdminService_GetGroupRateMultipliers(t *testing.T) {
	t.Run("returns only rpm overrides and hides legacy rates", func(t *testing.T) {
		rpmOverride := 120
		repo := &userGroupRateRepoStubForGroupRate{
			getByGroupIDData: map[int64][]UserGroupRateEntry{
				10: {
					{UserID: 1, UserName: "alice", UserEmail: "alice@test.com", RateMultiplier: ptrFloat(1.5)},
					{UserID: 2, UserName: "bob", UserEmail: "bob@test.com", RateMultiplier: ptrFloat(0.8), RPMOverride: &rpmOverride},
				},
			},
		}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		entries, err := svc.GetGroupRateMultipliers(context.Background(), 10)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, int64(2), entries[0].UserID)
		require.Nil(t, entries[0].RateMultiplier)
		require.Equal(t, 120, *entries[0].RPMOverride)
	})

	t.Run("returns nil when repo is nil", func(t *testing.T) {
		svc := &adminServiceImpl{userGroupRateRepo: nil}

		entries, err := svc.GetGroupRateMultipliers(context.Background(), 10)
		require.NoError(t, err)
		require.Empty(t, entries)
	})

	t.Run("returns empty slice for group with no entries", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{
			getByGroupIDData: map[int64][]UserGroupRateEntry{},
		}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		entries, err := svc.GetGroupRateMultipliers(context.Background(), 99)
		require.NoError(t, err)
		require.Empty(t, entries)
	})

	t.Run("propagates repo error for clearing empty entries", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{
			getByGroupIDErr: errors.New("db error"),
		}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		entries, err := svc.GetGroupRateMultipliers(context.Background(), 10)
		require.Error(t, err)
		require.Nil(t, entries)
	})
}

func TestAdminService_ClearGroupRateMultipliers(t *testing.T) {
	t.Run("clears only rate multiplier part", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		err := svc.ClearGroupRateMultipliers(context.Background(), 42)
		require.NoError(t, err)
		require.Equal(t, int64(42), repo.syncedGroupID)
		require.Nil(t, repo.syncedEntries)
		require.Empty(t, repo.deletedGroupIDs)
	})

	t.Run("returns nil when repo is nil", func(t *testing.T) {
		svc := &adminServiceImpl{userGroupRateRepo: nil}

		err := svc.ClearGroupRateMultipliers(context.Background(), 42)
		require.NoError(t, err)
	})

	t.Run("propagates repo error", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{
			syncGroupErr: errors.New("sync failed"),
		}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		err := svc.ClearGroupRateMultipliers(context.Background(), 42)
		require.Error(t, err)
		require.Contains(t, err.Error(), "sync failed")
	})
}

func TestAdminService_BatchSetGroupRateMultipliers(t *testing.T) {
	t.Run("rejects non-empty entries", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		entries := []GroupRateMultiplierInput{
			{UserID: 1, RateMultiplier: 1.5},
			{UserID: 2, RateMultiplier: 0.8},
		}
		err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, entries)
		require.ErrorIs(t, err, ErrUserGroupRateMultiplierDisabled)
		require.Zero(t, repo.syncedGroupID)
		require.Nil(t, repo.syncedEntries)
	})

	t.Run("returns nil when repo is nil", func(t *testing.T) {
		svc := &adminServiceImpl{userGroupRateRepo: nil}

		err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, nil)
		require.NoError(t, err)
	})

	t.Run("propagates repo error", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{
			syncGroupErr: errors.New("sync failed"),
		}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "sync failed")
	})
}

func TestAdminService_BatchSetGroupRPMOverrides(t *testing.T) {
	t.Run("syncs entries to repo", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := &adminServiceImpl{userGroupRateRepo: repo}
		override := 20
		entries := []GroupRPMOverrideInput{{UserID: 2, RPMOverride: &override}}

		err := svc.BatchSetGroupRPMOverrides(context.Background(), 10, entries)
		require.NoError(t, err)
		require.Equal(t, int64(10), repo.rpmSyncedGroupID)
		require.Equal(t, entries, repo.rpmSyncedEntries)
	})

	t.Run("rejects negative override as bad request", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := &adminServiceImpl{userGroupRateRepo: repo}
		negative := -1

		err := svc.BatchSetGroupRPMOverrides(context.Background(), 10, []GroupRPMOverrideInput{
			{UserID: 2, RPMOverride: &negative},
		})
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
		require.Zero(t, repo.rpmSyncedGroupID)
	})
}
