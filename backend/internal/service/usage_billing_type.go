package service

// shouldUseSubscriptionBilling keeps older call sites/tests on the same decision
// path as EffectiveBillingContext.
func shouldUseSubscriptionBilling(subscription *UserSubscription, group *Group) bool {
	isSubscriptionBilling, _ := EffectiveBillingContext(group, subscription)
	return isSubscriptionBilling
}

// effectiveBillingGroup keeps older call sites/tests on the same effective-group
// path as EffectiveBillingContext.
func effectiveBillingGroup(calledGroup *Group, subscription *UserSubscription) *Group {
	_, effectiveGroup := EffectiveBillingContext(calledGroup, subscription)
	return effectiveGroup
}
