package service

import (
	"context"
	"errors"
)

type usageBillingReplayWriter struct {
	usageLogRepo UsageLogRepository
}

func NewUsageBillingReplayWriter(usageLogRepo UsageLogRepository) UsageBillingReplayWriter {
	return &usageBillingReplayWriter{usageLogRepo: usageLogRepo}
}

func (w *usageBillingReplayWriter) WriteUsageBillingReplay(ctx context.Context, envelope UsageBillingEnvelope) error {
	if w == nil || w.usageLogRepo == nil {
		return errors.New("usage billing replay log repository is unavailable")
	}
	_, err := w.usageLogRepo.Create(ctx, envelope.UsageLog())
	return err
}

type UsageBillingReplayCacheInvalidator interface {
	InvalidateUserBalance(ctx context.Context, userID int64) error
	InvalidateSubscription(ctx context.Context, userID, groupID int64) error
	InvalidateAPIKeyRateLimit(ctx context.Context, apiKeyID int64) error
}

type UsageBillingReplayAccountToucher interface {
	ScheduleLastUsedUpdate(accountID int64)
}

type UsageBillingReplayAuthCacheInvalidator interface {
	InvalidateAuthCacheByLocatorReliable(ctx context.Context, locator string) error
}

type usageBillingReplayFinalizer struct {
	cache        UsageBillingReplayCacheInvalidator
	authCache    UsageBillingReplayAuthCacheInvalidator
	accountTouch UsageBillingReplayAccountToucher
}

func NewUsageBillingReplayFinalizer(
	cache UsageBillingReplayCacheInvalidator,
	authCache UsageBillingReplayAuthCacheInvalidator,
	accountTouch UsageBillingReplayAccountToucher,
) UsageBillingReplayFinalizer {
	return &usageBillingReplayFinalizer{cache: cache, authCache: authCache, accountTouch: accountTouch}
}

func (f *usageBillingReplayFinalizer) FinalizeUsageBillingReplay(
	ctx context.Context,
	envelope UsageBillingEnvelope,
	_ *UsageBillingApplyResult,
) error {
	if f == nil {
		return errors.New("usage billing replay finalizer is unavailable")
	}
	var finalizationErrors []error
	if envelope.BalanceCost() > 0 {
		if f.cache == nil {
			finalizationErrors = append(finalizationErrors, errors.New("usage billing balance cache invalidator is unavailable"))
		} else if err := f.cache.InvalidateUserBalance(ctx, envelope.UserID()); err != nil {
			finalizationErrors = append(finalizationErrors, err)
		}
	}
	if envelope.SubscriptionCost() > 0 || envelope.WalletCost() > 0 {
		groupID := envelope.EffectiveBillingGroupID()
		if f.cache == nil || groupID == nil {
			finalizationErrors = append(finalizationErrors, errors.New("usage billing subscription cache invalidator is unavailable"))
		} else if err := f.cache.InvalidateSubscription(ctx, envelope.UserID(), *groupID); err != nil {
			finalizationErrors = append(finalizationErrors, err)
		}
	}
	if envelope.APIKeyRateLimitCost() > 0 {
		if f.cache == nil {
			finalizationErrors = append(finalizationErrors, errors.New("usage billing rate limit cache invalidator is unavailable"))
		} else if err := f.cache.InvalidateAPIKeyRateLimit(ctx, envelope.APIKeyID()); err != nil {
			finalizationErrors = append(finalizationErrors, err)
		}
	}
	if envelope.APIKeyQuotaCost() > 0 {
		if f.authCache == nil {
			finalizationErrors = append(finalizationErrors, errors.New("usage billing auth cache invalidator is unavailable"))
		} else if err := f.authCache.InvalidateAuthCacheByLocatorReliable(ctx, envelope.AuthCacheLocator()); err != nil {
			finalizationErrors = append(finalizationErrors, err)
		}
	}
	if f.accountTouch == nil {
		finalizationErrors = append(finalizationErrors, errors.New("usage billing account touch service is unavailable"))
	} else {
		f.accountTouch.ScheduleLastUsedUpdate(envelope.AccountID())
	}
	return errors.Join(finalizationErrors...)
}
