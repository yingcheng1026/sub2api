package service

// shouldUseSubscriptionBilling 判断本次请求是否走订阅扣费,而不是 fallback 到 user.balance。
//
// 三种走订阅模式的场景:
//  1. subscription 是钱包(IsWalletMode):任何 group 都走钱包扣费(钱包覆盖一切)
//  2. subscription 是月卡 + called group 本身是 subscription 类型:exact match,经典 v3 path
//  3. subscription 是月卡 + called group 是 standard 类型,但 plan_groups 已覆盖该 group:
//     用户在 admin 切了 key.group_id 到链内 standard group。middleware 通过
//     GetActiveSubscriptionCoveringGroup 已经把这种 subscription 装好了,billing 不能再
//     fallback 到 user.balance(老 bug,2026-05-16 王哥发现 + 修方案 C / 2026-05-17 PR #8)
//
// 见 docs/plans/2026-05-16-wallet-v4-group-switch-billing-fix.md
func shouldUseSubscriptionBilling(subscription *UserSubscription, group *Group) bool {
	if subscription == nil {
		return false
	}
	if subscription.IsWalletMode() {
		return true
	}
	// 月卡:exact match (called group 是 subscription 类型) OR plan_groups 覆盖 (middleware 已找到 sub)
	// 一旦 middleware 注入了非钱包 sub,billing 就该信它走订阅。standard 类型 group 也认。
	return true
}

// effectiveBillingGroup 决定 limits 检查 + usage 记录用哪个 group。
//
// 对于 plan_groups 覆盖场景(用户切到链内 standard group),必须用 sub 主 group(sub.Group)
// 来检 limits,否则 standard group 的 limits=0 会被视为"无限"过关。
func effectiveBillingGroup(calledGroup *Group, subscription *UserSubscription) *Group {
	if subscription == nil || subscription.IsWalletMode() {
		return calledGroup
	}
	if subscription.Group != nil {
		return subscription.Group
	}
	return calledGroup
}
