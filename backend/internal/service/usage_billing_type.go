package service

func shouldUseSubscriptionBilling(subscription *UserSubscription, group *Group) bool {
	if subscription == nil {
		return false
	}
	if subscription.IsWalletMode() {
		return true
	}
	return group != nil && group.IsSubscriptionType()
}
