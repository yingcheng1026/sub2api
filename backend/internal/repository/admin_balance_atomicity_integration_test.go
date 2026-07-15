//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type failingBalanceAuditRepository struct {
	service.RedeemCodeRepository
}

func (failingBalanceAuditRepository) Create(context.Context, *service.RedeemCode) error {
	return errors.New("injected balance audit failure")
}

func TestAdminBalanceConcurrentAddsSerializeAndWriteAuditOnceEach(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	redeemRepo := NewRedeemCodeRepository(client)
	adminService := service.NewAdminService(
		userRepo, nil, nil, nil, nil, redeemRepo, nil, nil, nil,
		nil, nil, nil, client, nil, nil, nil, nil,
	)
	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("admin-balance-concurrency-%s@example.com", uuid.NewString()),
		Username: "admin-balance-concurrency",
		Balance:  0,
	})

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := adminService.UpdateUserBalance(ctx, user.ID, 10, "add", "concurrent topup")
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	updated, err := userRepo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 20, updated.Balance, 0.000001)

	var auditCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM redeem_codes
		WHERE used_by = $1 AND type = 'admin_balance' AND value = 10
	`, user.ID).Scan(&auditCount))
	require.Equal(t, 2, auditCount)
}

func TestAdminBalanceAuditFailureRollsBackBalance(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	redeemRepo := NewRedeemCodeRepository(client)
	adminService := service.NewAdminService(
		userRepo, nil, nil, nil, nil,
		failingBalanceAuditRepository{RedeemCodeRepository: redeemRepo},
		nil, nil, nil, nil, nil, nil, client, nil, nil, nil, nil,
	)
	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("admin-balance-rollback-%s@example.com", uuid.NewString()),
		Username: "admin-balance-rollback",
		Balance:  5,
	})

	_, err := adminService.UpdateUserBalance(ctx, user.ID, 10, "add", "must roll back")
	require.ErrorContains(t, err, "injected balance audit failure")

	unchanged, err := userRepo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 5, unchanged.Balance, 0.000001)
}
