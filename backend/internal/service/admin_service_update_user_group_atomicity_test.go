//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminServiceUpdateUserGroupRatesFailsClosedWithoutRepository(t *testing.T) {
	base := &userRepoStub{user: &User{ID: 77, Email: "atomic@example.test", Status: StatusActive, Role: RoleUser}}
	userRepo := &balanceUserRepoStub{userRepoStub: base}
	svc := &adminServiceImpl{userRepo: userRepo}
	rate := 1.5

	_, err := svc.UpdateUser(context.Background(), 77, &UpdateUserInput{
		GroupRates: map[int64]*float64{3: &rate},
	})
	require.Error(t, err)
	require.Empty(t, userRepo.updated, "configuration failure must be detected before the user is mutated")
}

func TestAdminServiceUpdateUserGroupRatesFailsClosedWithoutTransactionClient(t *testing.T) {
	base := &userRepoStub{user: &User{ID: 77, Email: "atomic@example.test", Status: StatusActive, Role: RoleUser}}
	userRepo := &balanceUserRepoStub{userRepoStub: base}
	rateRepo := &userGroupRateRepoStubForGroupRate{}
	svc := &adminServiceImpl{
		userRepo:          userRepo,
		userGroupRateRepo: rateRepo,
	}
	rate := 1.5

	_, err := svc.UpdateUser(context.Background(), 77, &UpdateUserInput{
		GroupRates: map[int64]*float64{3: &rate},
	})
	require.Error(t, err)
	require.False(t, rateRepo.userRatesSynced)
	require.Empty(t, userRepo.updated, "missing transaction support must fail before either side is mutated")
}
