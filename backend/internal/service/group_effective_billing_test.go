//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEffectiveBillingContext 验证 2026-05-17 follow-up 引入的 EffectiveBillingContext helper:
// 用户在 admin 切 key 到 plan_groups 链内 standard group 时,billing 必须按订阅扣 sub quota
// 而不是 fallback 主余额(老 bug)。
func TestEffectiveBillingContext(t *testing.T) {
	bal := 100.0
	subGroup := &Group{
		ID:               13,
		Name:             "paid-trial-v3",
		Status:           StatusActive,
		SubscriptionType: SubscriptionTypeSubscription,
	}
	calledGroup := &Group{
		ID:               3,
		Name:             "openai-default",
		Status:           StatusActive,
		SubscriptionType: SubscriptionTypeStandard,
	}

	t.Run("nil_subscription_returns_balance_mode", func(t *testing.T) {
		isSubBilling, effective := EffectiveBillingContext(calledGroup, nil)
		require.False(t, isSubBilling, "无订阅应为余额模式")
		require.Equal(t, calledGroup, effective)
	})

	t.Run("wallet_subscription_keeps_called_group", func(t *testing.T) {
		walletSub := &UserSubscription{
			ID:               201,
			UserID:           42,
			GroupID:          nil,
			WalletBalanceUSD: &bal,
			Status:           SubscriptionStatusActive,
		}
		isSubBilling, effective := EffectiveBillingContext(calledGroup, walletSub)
		require.True(t, isSubBilling, "钱包订阅是订阅模式")
		require.Equal(t, calledGroup, effective, "钱包模式 effective group 不变(limits 不绑 group)")
	})

	t.Run("monthly_sub_exact_match_uses_sub_group", func(t *testing.T) {
		subID := subGroup.ID
		monthlySub := &UserSubscription{
			ID:      301,
			UserID:  42,
			GroupID: &subID,
			Group:   subGroup,
			Status:  SubscriptionStatusActive,
		}
		// exact match 场景:called group == sub.Group(用户没切)
		isSubBilling, effective := EffectiveBillingContext(subGroup, monthlySub)
		require.True(t, isSubBilling)
		require.Equal(t, subGroup, effective, "exact match 时返回 sub group(跟 called group 一致)")
	})

	t.Run("monthly_sub_covering_uses_sub_group_not_called_group", func(t *testing.T) {
		// 关键场景:用户买月卡 paid-trial-v3 (group 13),后台把 key 切到 openai-default (group 3)。
		// middleware 通过 GetActiveSubscriptionCoveringGroup 找到 sub 13,billing 必须用 sub.Group
		// 来检 limits + 扣 quota,而不是用 called group 3 (limits=0 → 老 bug 静默扣 balance)。
		subID := subGroup.ID
		coveringSub := &UserSubscription{
			ID:      401,
			UserID:  42,
			GroupID: &subID,
			Group:   subGroup,
			Status:  SubscriptionStatusActive,
		}
		isSubBilling, effective := EffectiveBillingContext(calledGroup, coveringSub)
		require.True(t, isSubBilling, "plan_groups 覆盖时仍是订阅模式")
		require.Equal(t, subGroup, effective, "covering 时 effective group 必须是 sub 主 group 不是 called group")
		require.NotEqual(t, calledGroup, effective)
	})

	t.Run("monthly_sub_with_nil_group_falls_back_to_called", func(t *testing.T) {
		// 防御性:sub.Group 没被 preload 的情况,fallback 到 called group(保持老行为)
		subID := int64(13)
		subWithoutGroup := &UserSubscription{
			ID:      501,
			UserID:  42,
			GroupID: &subID,
			Group:   nil,
			Status:  SubscriptionStatusActive,
		}
		isSubBilling, effective := EffectiveBillingContext(calledGroup, subWithoutGroup)
		require.True(t, isSubBilling)
		require.Equal(t, calledGroup, effective, "sub.Group=nil 时 fallback 到 called group")
	})

	t.Run("nil_called_group_with_subscription", func(t *testing.T) {
		// 边界:nil called group 不应导致 panic
		subID := int64(13)
		monthlySub := &UserSubscription{
			ID:      601,
			UserID:  42,
			GroupID: &subID,
			Group:   subGroup,
			Status:  SubscriptionStatusActive,
		}
		isSubBilling, effective := EffectiveBillingContext(nil, monthlySub)
		require.True(t, isSubBilling)
		require.Equal(t, subGroup, effective)
	})
}
