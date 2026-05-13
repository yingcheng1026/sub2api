package service

import (
	"context"
	"errors"
	"testing"
	"time"

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

// walletGroupKeyEnsurerStub mock 出 EnsureWalletGroupKeys 行为。
// 注：因为 SubscriptionService 在 input.PlanID 为 nil 时不会调 ensureWalletGroupKeys，
// 所以本 stub 只有当传入 PlanID 时才被调用。
//
// 但 SubscriptionService 还需要查 plan_groups → groupIDs，依赖 entClient.SubscriptionPlanGroup。
// 单元测试用 entClient=nil 跳过 lookupPlanGroupIDs（返回 nil, nil），所以 stub 收不到调用。
// 真正覆盖按 plan_groups 建 N 把 key 的逻辑由 integration test (wallet_mode_e2e) 负责。
type walletGroupKeyEnsurerStub struct {
	calls    int
	userIDs  []int64
	groupIDs [][]int64
	keys     []APIKey
	created  int
	err      error
}

func (s *walletGroupKeyEnsurerStub) EnsureWalletGroupKeys(_ context.Context, userID int64, groupIDs []int64) ([]APIKey, int, error) {
	s.calls++
	s.userIDs = append(s.userIDs, userID)
	s.groupIDs = append(s.groupIDs, groupIDs)
	return s.keys, s.created, s.err
}

// TestAssignWalletSubscriptionSkipsKeyCreationWhenNoPlanID 验证：input.PlanID==nil 时
// 仅创建钱包订阅，不触发 ensureWalletGroupKeys（admin 手动 /assign 不带 plan_id 的场景）。
func TestAssignWalletSubscriptionSkipsKeyCreationWhenNoPlanID(t *testing.T) {
	subRepo := newSubscriptionUserSubRepoStub()
	svc := NewSubscriptionService(groupRepoNoop{}, subRepo, nil, nil, nil)
	keyEnsurer := &walletGroupKeyEnsurerStub{}
	svc.SetWalletGroupKeyService(keyEnsurer)

	initial := 1500.0
	sub, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{
		UserID:           1001,
		ValidityDays:     30,
		WalletInitialUSD: &initial,
		// PlanID 不传 → 跳过自动建 key
	})

	require.NoError(t, err)
	require.NotNil(t, sub)
	require.Equal(t, 0, keyEnsurer.calls, "无 plan_id 时不应触发建 key")
	require.Nil(t, sub.WalletGroupKeys)
	require.Equal(t, 0, sub.WalletGroupKeysCreatedCount)
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

// TestAssignWalletSubscriptionCreditsPlanForcesMaxExpiresAt 验证 B2.3：
// plan_type='credits' 走永久 expires_at（截断到 MaxExpiresAt 2099-12-31），
// validity_days 被忽略；plan_type='subscription' 或空串走 days。
//
// 见 docs/plans/2026-05-13-wallet-multikey-credits-design.md §2.2。
func TestAssignWalletSubscriptionCreditsPlanForcesMaxExpiresAt(t *testing.T) {
	t.Run("credits 永久有效", func(t *testing.T) {
		subRepo := newSubscriptionUserSubRepoStub()
		svc := NewSubscriptionService(groupRepoNoop{}, subRepo, nil, nil, nil)

		initial := 500.0
		sub, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{
			UserID:           2001,
			ValidityDays:     7, // 应该被忽略
			WalletInitialUSD: &initial,
			PlanType:         PlanTypeCredits,
		})
		require.NoError(t, err)
		require.NotNil(t, sub)
		require.Equal(t, MaxExpiresAt, sub.ExpiresAt, "额度卡 expires_at 必须 == MaxExpiresAt (2099)")
	})

	t.Run("subscription 按 days 计算", func(t *testing.T) {
		subRepo := newSubscriptionUserSubRepoStub()
		svc := NewSubscriptionService(groupRepoNoop{}, subRepo, nil, nil, nil)

		initial := 1500.0
		before := time.Now()
		sub, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{
			UserID:           2002,
			ValidityDays:     30,
			WalletInitialUSD: &initial,
			PlanType:         PlanTypeSubscription,
		})
		require.NoError(t, err)
		// 大约 30 天后过期，但绝不应该是 MaxExpiresAt
		require.NotEqual(t, MaxExpiresAt, sub.ExpiresAt, "月卡不应永久有效")
		// 区间检查：在 before+29.9d ~ before+30.1d 之间
		require.True(t, sub.ExpiresAt.After(before.Add(29*24*time.Hour)))
		require.True(t, sub.ExpiresAt.Before(before.Add(31*24*time.Hour)))
	})

	t.Run("空串等价 subscription", func(t *testing.T) {
		subRepo := newSubscriptionUserSubRepoStub()
		svc := NewSubscriptionService(groupRepoNoop{}, subRepo, nil, nil, nil)

		initial := 100.0
		sub, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{
			UserID:           2003,
			ValidityDays:     30,
			WalletInitialUSD: &initial,
			// PlanType 留空
		})
		require.NoError(t, err)
		require.NotEqual(t, MaxExpiresAt, sub.ExpiresAt, "默认走月卡路径，不应永久有效")
	})
}

// walletTopupServiceStub 记录 Topup 调用，模拟 repo 返回叠加后的 balance。
type walletTopupServiceStub struct {
	calls           int
	lastSubID       int64
	lastDelta       float64
	lastOperator    *int64
	lastNotes       string
	returnEntry     WalletLedgerEntry
	err             error
}

func (s *walletTopupServiceStub) Topup(_ context.Context, subscriptionID int64, deltaUSD float64, operatorID *int64, notes string) (WalletLedgerEntry, error) {
	s.calls++
	s.lastSubID = subscriptionID
	s.lastDelta = deltaUSD
	s.lastOperator = operatorID
	s.lastNotes = notes
	return s.returnEntry, s.err
}

// TestAssignWalletSubscriptionToppedUpWhenCreditsAndExistingActive 验证 B2.4：
// 用户已有 active 钱包 + 再来一张额度卡 (plan_type='credits') →
// 走 Topup 路径而不是 conflict。
//
// 见 docs/plans/2026-05-13-wallet-multikey-credits-design.md §2.3。
func TestAssignWalletSubscriptionToppedUpWhenCreditsAndExistingActive(t *testing.T) {
	subRepo := newSubscriptionUserSubRepoStub()
	existingBalance := 100.0
	existingInitial := 100.0
	subRepo.seed(&UserSubscription{
		ID:               7,
		UserID:           1001,
		Status:           SubscriptionStatusActive,
		WalletBalanceUSD: &existingBalance,
		WalletInitialUSD: &existingInitial,
	})

	svc := NewSubscriptionService(groupRepoNoop{}, subRepo, nil, nil, nil)
	topupStub := &walletTopupServiceStub{
		returnEntry: WalletLedgerEntry{ID: 555, BalanceAfter: 600.0},
	}
	svc.SetWalletTopupService(topupStub)

	delta := 500.0
	sub, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{
		UserID:           1001,
		ValidityDays:     7, // 额度卡忽略
		AssignedBy:       9,
		Notes:            "credits-500 ¥149",
		WalletInitialUSD: &delta,
		PlanType:         PlanTypeCredits,
	})
	require.NoError(t, err)
	require.NotNil(t, sub)
	require.Equal(t, int64(7), sub.ID, "叠加必须复用现有 subscription，不新建")
	require.Equal(t, 0, subRepo.createCalls, "topup 路径不应触发 Create")

	require.Equal(t, 1, topupStub.calls)
	require.Equal(t, int64(7), topupStub.lastSubID)
	require.Equal(t, 500.0, topupStub.lastDelta)
	require.NotNil(t, topupStub.lastOperator)
	require.Equal(t, int64(9), *topupStub.lastOperator)
	require.Contains(t, topupStub.lastNotes, "credits topup:")
	require.Contains(t, topupStub.lastNotes, "credits-500")

	require.NotNil(t, sub.WalletBalanceUSD)
	require.Equal(t, 600.0, *sub.WalletBalanceUSD, "余额必须 == ledger.balance_after")
	require.NotNil(t, sub.WalletInitialUSD)
	require.Equal(t, 600.0, *sub.WalletInitialUSD, "initial 必须 == 旧 initial + delta")
}

// TestAssignWalletSubscriptionConflictWhenSubscriptionPlanAndExistingActive 验证 B2.4：
// 已有 active 钱包 + plan_type='subscription'（月卡）→ 仍 conflict_reason=wallet_already_active，
// 不走 topup（防止月卡叠月卡导致语义混乱）。
func TestAssignWalletSubscriptionConflictWhenSubscriptionPlanAndExistingActive(t *testing.T) {
	subRepo := newSubscriptionUserSubRepoStub()
	existing := 100.0
	subRepo.seed(&UserSubscription{
		ID:               8,
		UserID:           1002,
		Status:           SubscriptionStatusActive,
		WalletBalanceUSD: &existing,
		WalletInitialUSD: &existing,
	})

	svc := NewSubscriptionService(groupRepoNoop{}, subRepo, nil, nil, nil)
	topupStub := &walletTopupServiceStub{}
	svc.SetWalletTopupService(topupStub)

	initial := 1500.0
	_, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{
		UserID:           1002,
		ValidityDays:     30,
		WalletInitialUSD: &initial,
		PlanType:         PlanTypeSubscription,
	})
	require.Error(t, err)
	var coded *infraerrors.Error
	require.True(t, errors.As(err, &coded))
	require.Equal(t, ErrSubscriptionAssignConflict.Code, coded.Code)
	require.Equal(t, "wallet_already_active", coded.Metadata["conflict_reason"])
	require.Equal(t, 0, topupStub.calls, "月卡分支不应触发 topup")
}

// TestAssignWalletSubscriptionConflictWhenCreditsButTopupServiceMissing 验证 B2.4
// 兜底：plan_type='credits' 但没注入 WalletTopupService（旧 wire 路径）→
// conflict_reason=wallet_topup_unsupported，提示运维注入服务。
func TestAssignWalletSubscriptionConflictWhenCreditsButTopupServiceMissing(t *testing.T) {
	subRepo := newSubscriptionUserSubRepoStub()
	existing := 100.0
	subRepo.seed(&UserSubscription{
		ID:               9,
		UserID:           1003,
		Status:           SubscriptionStatusActive,
		WalletBalanceUSD: &existing,
		WalletInitialUSD: &existing,
	})

	svc := NewSubscriptionService(groupRepoNoop{}, subRepo, nil, nil, nil)
	// 不调 SetWalletTopupService

	delta := 500.0
	_, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{
		UserID:           1003,
		WalletInitialUSD: &delta,
		PlanType:         PlanTypeCredits,
	})
	require.Error(t, err)
	var coded *infraerrors.Error
	require.True(t, errors.As(err, &coded))
	require.Equal(t, ErrSubscriptionAssignConflict.Code, coded.Code)
	require.Equal(t, "wallet_topup_unsupported", coded.Metadata["conflict_reason"])
}
