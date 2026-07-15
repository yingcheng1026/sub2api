package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

const (
	usageBillingAdmissionWalletLease     = 2 * time.Minute
	usageBillingAdmissionHeartbeatPeriod = 30 * time.Second
)

type UsageBillingAdmissionSession struct {
	RequestID  string
	APIKeyID   int64
	OwnerToken string
	AttemptID  string
	Wallet     bool
}

func (s *UsageBillingAdmissionSession) Ref() UsageBillingAdmissionAttemptRef {
	if s == nil {
		return UsageBillingAdmissionAttemptRef{}
	}
	return UsageBillingAdmissionAttemptRef{
		RequestID: s.RequestID, APIKeyID: s.APIKeyID,
		OwnerToken: s.OwnerToken, AttemptID: s.AttemptID,
	}
}

func buildUsageBillingAdmission(
	ctx context.Context,
	apiKey *APIKey,
	user *User,
	account *Account,
	subscription *UserSubscription,
	quote UsageBillingReservationQuote,
) (context.Context, UsageBillingAdmission, error) {
	if apiKey == nil || user == nil || account == nil || apiKey.GroupID == nil || apiKey.Group == nil {
		return ctx, UsageBillingAdmission{}, ErrUsageBillingAdmissionInvalid
	}
	if err := quote.Validate(); err != nil {
		return ctx, UsageBillingAdmission{}, ErrUsageBillingAdmissionInvalid
	}
	var err error
	ctx, err = PrepareUsageBillingRequestContext(ctx)
	if err != nil {
		return ctx, UsageBillingAdmission{}, err
	}
	requestID, _ := ctx.Value(ctxkey.UsageBillingRequestID).(string)
	ownerToken, _ := ctx.Value(ctxkey.UsageBillingOwnerToken).(string)
	attemptID, err := newUsageBillingFenceToken()
	if err != nil {
		return ctx, UsageBillingAdmission{}, err
	}
	isSubscriptionBilling, effectiveGroup := EffectiveBillingContext(apiKey.Group, subscription)
	effectiveGroupID := resolveEffectiveBillingGroupID(apiKey.GroupID, effectiveGroup, subscription)
	billingType := BillingTypeBalance
	var subscriptionID *int64
	if isSubscriptionBilling {
		if subscription == nil {
			return ctx, UsageBillingAdmission{}, ErrUsageBillingAdmissionInvalid
		}
		billingType = BillingTypeSubscription
		subscriptionID = &subscription.ID
	}
	admission, err := NewUsageBillingAdmission(UsageBillingAdmissionInput{
		RequestID: requestID, APIKeyID: apiKey.ID, AuthCacheLocator: APIKeyStoredAuthCacheLocator(apiKey),
		UserID: user.ID, AccountID: account.ID, SubscriptionID: subscriptionID,
		GroupID: apiKey.GroupID, EffectiveBillingGroupID: effectiveGroupID,
		AccountType: account.Type, BillingType: billingType,
		OwnerToken: ownerToken, AttemptID: attemptID,
		BillingModel: quote.BillingModel, RequestPayloadHash: quote.RequestPayloadHash,
		PricingSource: quote.PricingSource, PricingRevision: quote.PricingRevision,
		PricingHash: quote.PricingHash, RateMultiplier: quote.RateMultiplier,
		WorstCaseCostUSD:         quote.WorstCaseCostUSD,
		AlternateBillingModel:    quote.AlternateBillingModel,
		AlternatePricingSource:   quote.AlternatePricingSource,
		AlternatePricingRevision: quote.AlternatePricingRevision,
		AlternatePricingHash:     quote.AlternatePricingHash,
		AlternateRateMultiplier:  quote.AlternateRateMultiplier,
	})
	return ctx, admission, err
}

// PrepareUsageBillingRequestContext freezes the admission base identity once
// for all account attempts and for the later detached RecordUsage call.
func PrepareUsageBillingRequestContext(ctx context.Context) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestID, _ := ctx.Value(ctxkey.UsageBillingRequestID).(string)
	if strings.TrimSpace(requestID) == "" {
		requestID = resolveUsageBillingRequestID(ctx, "")
		ctx = context.WithValue(ctx, ctxkey.UsageBillingRequestID, requestID)
	}
	ownerToken, _ := ctx.Value(ctxkey.UsageBillingOwnerToken).(string)
	if !validFenceToken(strings.TrimSpace(ownerToken)) {
		var err error
		ownerToken, err = newUsageBillingFenceToken()
		if err != nil {
			return ctx, err
		}
		ctx = context.WithValue(ctx, ctxkey.UsageBillingOwnerToken, ownerToken)
	}
	return ctx, nil
}

func newUsageBillingFenceToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate usage billing fencing token: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func admitUsageBillingRequest(
	ctx context.Context,
	cfg *config.Config,
	require bool,
	repo UsageBillingAdmissionRepository,
	apiKey *APIKey,
	user *User,
	account *Account,
	subscription *UserSubscription,
	quote UsageBillingReservationQuote,
) (context.Context, *UsageBillingAdmissionSession, error) {
	if cfg != nil && cfg.RunMode == config.RunModeSimple {
		return ctx, nil, nil
	}
	if !require {
		return ctx, nil, nil
	}
	if repo == nil {
		return ctx, nil, ErrUsageBillingOutboxUnavailable
	}
	ctx, admission, err := buildUsageBillingAdmission(ctx, apiKey, user, account, subscription, quote)
	if err != nil {
		return ctx, nil, err
	}
	if err := repo.Admit(ctx, admission); err != nil {
		return ctx, nil, fmt.Errorf("usage billing pre-admission: %w", err)
	}
	return ctx, &UsageBillingAdmissionSession{
		RequestID: admission.RequestID(), APIKeyID: admission.APIKeyID(),
		OwnerToken: admission.OwnerToken(), AttemptID: admission.AttemptID(),
		Wallet: subscription != nil && subscription.IsWalletMode(),
	}, nil
}

func abandonUsageBillingRequest(ctx context.Context, repo UsageBillingAdmissionRepository, session *UsageBillingAdmissionSession) error {
	if repo == nil || session == nil {
		return nil
	}
	abandonCtx, cancel := newUsageBillingLifecycleMutationContext(ctx)
	defer cancel()
	if err := repo.Abandon(abandonCtx, session.Ref()); err != nil && !errors.Is(err, ErrUsageBillingAdmissionLeaseLost) {
		return err
	}
	return nil
}

func newUsageBillingLifecycleMutationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(baseCtx, usageBillingOutboxAdmissionTimeout)
}

func dispatchUsageBillingRequest(
	ctx context.Context,
	repo UsageBillingAdmissionRepository,
	session *UsageBillingAdmissionSession,
) error {
	if session == nil {
		return nil
	}
	if repo == nil {
		return ErrUsageBillingOutboxUnavailable
	}
	if err := repo.MarkDispatched(ctx, session.Ref()); err != nil {
		abandonErr := abandonUsageBillingRequest(ctx, repo, session)
		return errors.Join(err, abandonErr)
	}
	return nil
}

func resolveUsageBillingAdmissionFromGin(c interface{ Get(string) (any, bool) }) (*APIKey, *UserSubscription, error) {
	if c == nil {
		return nil, nil, ErrUsageBillingAdmissionInvalid
	}
	value, ok := c.Get("api_key")
	if !ok {
		return nil, nil, ErrUsageBillingAdmissionInvalid
	}
	apiKey, ok := value.(*APIKey)
	if !ok || apiKey == nil || apiKey.User == nil {
		return nil, nil, ErrUsageBillingAdmissionInvalid
	}
	var subscription *UserSubscription
	if value, exists := c.Get("subscription"); exists {
		subscription, _ = value.(*UserSubscription)
	}
	return apiKey, subscription, nil
}
