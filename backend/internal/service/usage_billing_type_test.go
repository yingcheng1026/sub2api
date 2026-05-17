//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestShouldUseSubscriptionBilling_CoveringMonthly 验证 2026-05-17 follow-up:
// 月卡用户切 key 到 plan_groups 链内 standard group 时,billing 应该走订阅扣 sub quota
// 而不是 fallback 主余额(老 bug,818 次静默扣 balance 调用)。
//
// 见 docs/plans/2026-05-16-wallet-v4-group-switch-billing-fix.md。
func TestShouldUseSubscriptionBilling_CoveringMonthly(t *testing.T) {
	bal := 100.0
	subGroupID := int64(13)
	subGroup := &Group{
		ID:               13,
		Name:             "paid-trial-v3",
		Status:           StatusActive,
		SubscriptionType: SubscriptionTypeSubscription,
	}
	standardGroup := &Group{
		ID:               3,
		Name:             "openai-default",
		Status:           StatusActive,
		SubscriptionType: SubscriptionTypeStandard,
	}

	t.Run("nil_subscription_uses_balance", func(t *testing.T) {
		isSubBilling, _ := EffectiveBillingContext(standardGroup, nil)
		require.False(t, isSubBilling)
	})

	t.Run("wallet_subscription_any_group_uses_subscription", func(t *testing.T) {
		walletSub := &UserSubscription{
			ID:               201,
			UserID:           42,
			WalletBalanceUSD: &bal,
			Status:           SubscriptionStatusActive,
		}
		isSubBilling, _ := EffectiveBillingContext(standardGroup, walletSub)
		require.True(t, isSubBilling,
			"钱包覆盖一切非订阅 group")
	})

	t.Run("monthly_exact_match_uses_subscription", func(t *testing.T) {
		monthlySub := &UserSubscription{
			ID:      301,
			UserID:  42,
			GroupID: &subGroupID,
			Group:   subGroup,
			Status:  SubscriptionStatusActive,
		}
		isSubBilling, _ := EffectiveBillingContext(subGroup, monthlySub)
		require.True(t, isSubBilling,
			"exact match 走订阅 (经典 v3 path)")
	})

	t.Run("monthly_covering_standard_group_uses_subscription", func(t *testing.T) {
		// 核心场景:月卡 sub.Group=13 (paid-trial-v3),用户切 key 到 group 3 (openai-default standard)
		// middleware 通过 GetActiveSubscriptionCoveringGroup 找到 sub,billing 必须走订阅
		monthlySub := &UserSubscription{
			ID:      401,
			UserID:  42,
			GroupID: &subGroupID,
			Group:   subGroup,
			Status:  SubscriptionStatusActive,
		}
		isSubBilling, _ := EffectiveBillingContext(standardGroup, monthlySub)
		require.True(t, isSubBilling,
			"plan_groups 覆盖时月卡也要走订阅,不能 fallback balance")
	})
}

func TestEffectiveBillingGroup(t *testing.T) {
	bal := 100.0
	subGroupID := int64(13)
	subGroup := &Group{ID: 13, Name: "paid-trial-v3", SubscriptionType: SubscriptionTypeSubscription}
	standardGroup := &Group{ID: 3, Name: "openai-default", SubscriptionType: SubscriptionTypeStandard}

	t.Run("nil_subscription_returns_called", func(t *testing.T) {
		_, effectiveGroup := EffectiveBillingContext(standardGroup, nil)
		require.Equal(t, standardGroup, effectiveGroup)
	})

	t.Run("wallet_keeps_called_group", func(t *testing.T) {
		walletSub := &UserSubscription{WalletBalanceUSD: &bal, Status: SubscriptionStatusActive}
		_, effectiveGroup := EffectiveBillingContext(standardGroup, walletSub)
		require.Equal(t, standardGroup, effectiveGroup,
			"钱包模式不绑 group,limits 检查不依赖 group")
	})

	t.Run("monthly_returns_sub_primary_group", func(t *testing.T) {
		monthlySub := &UserSubscription{
			GroupID: &subGroupID,
			Group:   subGroup,
			Status:  SubscriptionStatusActive,
		}
		// 关键:返回 sub 主 group 而不是 called group → limits 用 sub.Group 的 limits 检查
		_, effectiveGroup := EffectiveBillingContext(standardGroup, monthlySub)
		require.Equal(t, subGroup, effectiveGroup,
			"月卡 covering 用 sub.Group 不是 called group")
	})

	t.Run("monthly_with_nil_group_falls_back_to_called", func(t *testing.T) {
		// 防御:sub.Group 没 preload → fallback 老行为
		subWithoutGroup := &UserSubscription{
			GroupID: &subGroupID,
			Group:   nil,
			Status:  SubscriptionStatusActive,
		}
		_, effectiveGroup := EffectiveBillingContext(standardGroup, subWithoutGroup)
		require.Equal(t, standardGroup, effectiveGroup)
	})
}
