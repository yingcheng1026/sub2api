package service

import "context"

const (
	RateMultiplierSourceSystemDefault = "system_default"
	RateMultiplierSourceGroupDefault  = "group_default"
	RateMultiplierSourceUserGroup     = "user_group_rate"
	RateMultiplierSourceLockedRates   = "locked_rates"
)

type RateMultiplierResolution struct {
	Multiplier float64
	Source     string
}

func resolveGroupRateContext(groupID *int64, group *Group, systemDefault float64) (int64, float64, string) {
	defaultRate := systemDefault
	source := RateMultiplierSourceSystemDefault
	if group != nil {
		if group.RateMultiplier >= 0 {
			defaultRate = group.RateMultiplier
			source = RateMultiplierSourceGroupDefault
		}
		if group.ID > 0 {
			return group.ID, defaultRate, source
		}
	}
	if groupID != nil && *groupID > 0 {
		return *groupID, defaultRate, source
	}
	return 0, defaultRate, source
}

func resolveEffectiveRateMultiplier(
	ctx context.Context,
	resolver *userGroupRateResolver,
	userID int64,
	groupID *int64,
	group *Group,
	subscription *UserSubscription,
	systemDefault float64,
) RateMultiplierResolution {
	resolvedGroupID, defaultRate, defaultSource := resolveGroupRateContext(groupID, group, systemDefault)
	if resolvedGroupID > 0 {
		if lockedRate, ok := subscription.LockedRateForGroup(resolvedGroupID); ok {
			return RateMultiplierResolution{Multiplier: lockedRate, Source: RateMultiplierSourceLockedRates}
		}
	}
	if resolver != nil && userID > 0 && resolvedGroupID > 0 {
		resolved := resolver.Resolve(ctx, userID, resolvedGroupID, defaultRate)
		if resolved != defaultRate {
			return RateMultiplierResolution{Multiplier: resolved, Source: RateMultiplierSourceUserGroup}
		}
		return RateMultiplierResolution{Multiplier: resolved, Source: defaultSource}
	}
	return RateMultiplierResolution{Multiplier: defaultRate, Source: defaultSource}
}

func ResolveSubscriptionDisplayRateMultiplier(groupID *int64, group *Group, subscription *UserSubscription, systemDefault float64) RateMultiplierResolution {
	resolvedGroupID, defaultRate, defaultSource := resolveGroupRateContext(groupID, group, systemDefault)
	if resolvedGroupID > 0 {
		if lockedRate, ok := subscription.LockedRateForGroup(resolvedGroupID); ok {
			return RateMultiplierResolution{Multiplier: lockedRate, Source: RateMultiplierSourceLockedRates}
		}
	}
	return RateMultiplierResolution{Multiplier: defaultRate, Source: defaultSource}
}
