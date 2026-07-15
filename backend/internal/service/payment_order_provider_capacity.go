package service

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// reserveSelectedProviderCapacity closes the gap between load-balancer
// selection and order insertion. On PostgreSQL, every creator for one provider
// instance serializes on that instance row, then re-reads committed PENDING and
// paid exposure before inserting its own PENDING reservation in the same
// transaction.
func (s *PaymentService) reserveSelectedProviderCapacity(
	ctx context.Context,
	tx *dbent.Tx,
	paymentType string,
	orderAmount float64,
	sel *payment.InstanceSelection,
) error {
	if sel == nil {
		return nil
	}
	if tx == nil || math.IsNaN(orderAmount) || math.IsInf(orderAmount, 0) || orderAmount <= 0 {
		return infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_ADMISSION_INVALID", "payment provider admission is invalid")
	}

	instanceID, err := strconv.ParseInt(strings.TrimSpace(sel.InstanceID), 10, 64)
	if err != nil || instanceID <= 0 {
		return infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_INSTANCE_CHANGED", "payment provider instance changed")
	}
	client := tx.Client()
	if err := lockPaymentProviderAdmission(ctx, client, instanceID); err != nil {
		return infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_INSTANCE_CHANGED", "payment provider instance changed").WithCause(err)
	}
	instance, err := client.PaymentProviderInstance.Get(ctx, instanceID)
	if err != nil {
		return infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_INSTANCE_CHANGED", "payment provider instance changed").WithCause(err)
	}
	if strings.TrimSpace(instance.ProviderKey) != strings.TrimSpace(sel.ProviderKey) ||
		!providerInstanceSupportsSelectedType(instance, paymentType) {
		return infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_INSTANCE_CHANGED", "payment provider instance changed")
	}

	limits, err := payment.GetInstanceChannelLimits(instance, paymentType)
	if err != nil {
		return infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_LIMITS_INVALID", "payment provider limits are invalid").WithCause(err)
	}
	if limits.SingleMin > 0 && orderAmount < limits.SingleMin ||
		limits.SingleMax > 0 && orderAmount > limits.SingleMax {
		return infraerrors.TooManyRequests("PAYMENT_PROVIDER_CAPACITY_EXCEEDED", "payment provider capacity is unavailable")
	}
	if limits.DailyLimit <= 0 {
		return nil
	}

	used, err := selectedProviderDailyExposure(ctx, client, sel.InstanceID, time.Now())
	if err != nil {
		return infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_USAGE_UNAVAILABLE", "payment provider capacity could not be verified").WithCause(err)
	}
	if used+orderAmount > limits.DailyLimit {
		return infraerrors.TooManyRequests("PAYMENT_PROVIDER_CAPACITY_EXCEEDED", "payment provider capacity is unavailable").
			WithMetadata(map[string]string{"remaining": fmt.Sprintf("%.2f", math.Max(0, limits.DailyLimit-used))})
	}
	return nil
}

func lockPaymentProviderAdmission(ctx context.Context, client *dbent.Client, instanceID int64) error {
	if client == nil || instanceID <= 0 {
		return fmt.Errorf("payment provider admission requires a valid instance")
	}
	query := client.PaymentProviderInstance.Query().Where(
		paymentproviderinstance.IDEQ(instanceID),
		paymentproviderinstance.EnabledEQ(true),
	)
	if client.Driver().Dialect() == dialect.Postgres {
		query = query.ForUpdate()
	}
	if _, err := query.OnlyID(ctx); err != nil {
		return fmt.Errorf("lock payment provider admission: %w", err)
	}
	return nil
}

func providerInstanceSupportsSelectedType(instance *dbent.PaymentProviderInstance, paymentType string) bool {
	if instance == nil {
		return false
	}
	if paymentType == payment.TypeStripe {
		return instance.ProviderKey == payment.TypeStripe
	}
	return payment.InstanceSupportsType(instance.SupportedTypes, paymentType)
}

func selectedProviderDailyExposure(ctx context.Context, client *dbent.Client, instanceID string, now time.Time) (float64, error) {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var rows []struct {
		Sum float64 `json:"sum"`
	}
	err := client.PaymentOrder.Query().Where(
		paymentorder.ProviderInstanceIDEQ(strings.TrimSpace(instanceID)),
		paymentorder.StatusIn(
			OrderStatusPending,
			OrderStatusPaid,
			OrderStatusCompleted,
			OrderStatusRecharging,
		),
		paymentorder.CreatedAtGTE(start),
	).
		Aggregate(dbent.As(dbent.Sum(paymentorder.FieldPayAmount), "sum")).
		Scan(ctx, &rows)
	if err != nil {
		return 0, fmt.Errorf("query selected provider daily exposure: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Sum, nil
}
