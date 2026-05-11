package service

import (
	"context"
	"errors"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// TestAssignWalletSubscriptionCreatesNewWallet 验证 A8：当 input.WalletInitialUSD
// 非 nil 时，AssignSubscription 跳过 group 路径，创建一条 group_id=NULL 的钱包
// 订阅，wallet_initial_usd 和 wallet_balance_usd 都等于初始值。
func TestAssignWalletSubscriptionCreatesNewWallet(t *testing.T) {
	subRepo := newSubscriptionUserSubRepoStub()
	// groupRepo 不应被调用：钱包路径不查 group
	groupRepo := groupRepoNoop{}

	svc := NewSubscriptionService(groupRepo, subRepo, nil, nil, nil)

	initial := 1500.0
	sub, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{
		UserID:           1001,
		ValidityDays:     30,
		AssignedBy:       9, // admin id
		Notes:            "¥299 月度套餐",
		WalletInitialUSD: &initial,
	})
	require.NoError(t, err)
	require.NotNil(t, sub)
	require.Nil(t, sub.GroupID, "钱包订阅 group_id 必须为 NULL")
	require.NotNil(t, sub.WalletInitialUSD)
	require.NotNil(t, sub.WalletBalanceUSD)
	require.Equal(t, 1500.0, *sub.WalletInitialUSD)
	require.Equal(t, 1500.0, *sub.WalletBalanceUSD, "新建时余额=初始值")
	require.True(t, sub.IsWalletMode())
	require.Equal(t, 1, subRepo.createCalls)
}

// TestAssignWalletSubscriptionConflictWhenActiveExists 验证：用户已有 active
// 钱包订阅时，再次分配返回 ErrSubscriptionAssignConflict（reason=wallet_already_active），
// 防止误开重复钱包导致 partial unique index 撞车。
func TestAssignWalletSubscriptionConflictWhenActiveExists(t *testing.T) {
	subRepo := newSubscriptionUserSubRepoStub()
	existing := 100.0
	subRepo.seed(&UserSubscription{
		ID:               7,
		UserID:           1001,
		Status:           SubscriptionStatusActive,
		WalletBalanceUSD: &existing,
		WalletInitialUSD: &existing,
	})

	svc := NewSubscriptionService(groupRepoNoop{}, subRepo, nil, nil, nil)

	initial := 1500.0
	_, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{
		UserID:           1001,
		ValidityDays:     30,
		WalletInitialUSD: &initial,
	})
	require.Error(t, err)

	var coded *infraerrors.Error
	require.True(t, errors.As(err, &coded), "应返回 *infraerrors.Error")
	require.Equal(t, ErrSubscriptionAssignConflict.Code, coded.Code)
	require.Equal(t, "wallet_already_active", coded.Metadata["conflict_reason"])
	require.Equal(t, 0, subRepo.createCalls, "冲突时不应创建新订阅")
}

// TestAssignWalletSubscriptionRejectsNonPositiveBalance 验证：初始余额 <= 0
// 直接 service 层报错（handler 层 binding 也会拦截，service 是兜底）。
func TestAssignWalletSubscriptionRejectsNonPositiveBalance(t *testing.T) {
	subRepo := newSubscriptionUserSubRepoStub()
	svc := NewSubscriptionService(groupRepoNoop{}, subRepo, nil, nil, nil)

	for _, bad := range []float64{0, -1, -100} {
		v := bad
		_, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{
			UserID:           1001,
			ValidityDays:     30,
			WalletInitialUSD: &v,
		})
		require.Error(t, err, "balance=%v 应被拒绝", bad)
	}
	require.Equal(t, 0, subRepo.createCalls)
}
