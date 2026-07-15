package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	standardRefundDeductionAppliedAction     = "REFUND_DEDUCTION_APPLIED"
	standardRefundDeductionCompensatedAction = "REFUND_DEDUCTION_COMPENSATED"
	standardRefundGatewayStartedAction       = "REFUND_GATEWAY_STARTED"
	standardRefundGatewayAcceptedAction      = "REFUND_GATEWAY_ACCEPTED"
)

type refundGatewayDisposition int

const (
	refundGatewayAmbiguous refundGatewayDisposition = iota
	refundGatewaySucceeded
	refundGatewayPending
	refundGatewayRejected
)

type standardRefundDeductionDetail struct {
	DeductionType              string    `json:"deductionType"`
	UserID                     int64     `json:"userID"`
	BalanceDeducted            float64   `json:"balanceDeducted,omitempty"`
	SubscriptionID             int64     `json:"subscriptionID,omitempty"`
	SubscriptionDaysDeducted   int       `json:"subscriptionDaysDeducted,omitempty"`
	SubscriptionPreviousExpiry time.Time `json:"subscriptionPreviousExpiry,omitempty"`
	SubscriptionPreviousStatus string    `json:"subscriptionPreviousStatus,omitempty"`
}

func classifyRefundGatewayResult(response *payment.RefundResponse, callErr error) refundGatewayDisposition {
	if callErr != nil || response == nil {
		return refundGatewayAmbiguous
	}
	switch strings.TrimSpace(response.Status) {
	case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded:
		return refundGatewaySucceeded
	case payment.ProviderStatusPending:
		return refundGatewayPending
	case payment.ProviderStatusFailed:
		return refundGatewayRejected
	default:
		return refundGatewayAmbiguous
	}
}

func validateAutomaticRefundPreflight(order *dbent.PaymentOrder, instance *dbent.PaymentProviderInstance) error {
	if order == nil || instance == nil {
		return infraerrors.Conflict("REFUND_MANUAL_REQUIRED", "refund provider evidence is incomplete; manual reconciliation is required")
	}
	if strings.TrimSpace(order.PaymentTradeNo) == "" {
		return infraerrors.Conflict("REFUND_MANUAL_REQUIRED", "provider trade evidence is missing; automatic refund is unsafe")
	}
	if !refundProviderSupportsStableIdempotency(instance.ProviderKey) {
		return infraerrors.Conflict("REFUND_MANUAL_REQUIRED", "this provider does not support idempotent automatic refunds; reconcile it manually")
	}
	return nil
}

// subscriptionDaysForRefund converts a monetary partial refund into whole
// entitlement days. We round upward so a refunded fraction can never leave the
// user with more paid entitlement than the retained payment covers.
func subscriptionDaysForRefund(totalDays int, refundAmount, orderAmount float64) int {
	if totalDays <= 0 || !isFinitePositive(refundAmount) || !isFinitePositive(orderAmount) {
		return 0
	}
	if refundAmount >= orderAmount-amountToleranceCNY {
		return totalDays
	}
	days := int(math.Ceil(float64(totalDays) * refundAmount / orderAmount))
	if days < 1 {
		return 1
	}
	if days > totalDays {
		return totalDays
	}
	return days
}

func (s *PaymentService) executeStandardRefund(ctx context.Context, plan *RefundPlan) (*RefundResult, error) {
	recovered, err := s.claimStandardRefund(ctx, plan)
	if err != nil {
		return nil, err
	}

	disposition, attempted, _, gatewayErr := s.executeRefundGatewayCall(
		ctx,
		plan,
		standardRefundGatewayStartedAction,
		standardRefundGatewayAcceptedAction,
		recovered,
	)
	if !attempted {
		if compensationErr := s.compensateStandardRefund(ctx, plan, gatewayErr); compensationErr != nil {
			return nil, fmt.Errorf("refund failed before gateway: %v; compensation failed: %w", gatewayErr, compensationErr)
		}
		return nil, infraerrors.InternalServer("REFUND_PRE_GATEWAY_FAILED", psErrMsg(gatewayErr))
	}

	switch disposition {
	case refundGatewaySucceeded:
		return s.markStandardRefundOK(ctx, plan)
	case refundGatewayPending:
		return &RefundResult{
			Success:         false,
			Warning:         "refund was accepted by the provider and remains pending; retry after the lease expires to reconcile status",
			BalanceDeducted: plan.BalanceToDeduct,
			SubDaysDeducted: plan.SubDaysToDeduct,
		}, nil
	case refundGatewayRejected:
		if compensationErr := s.compensateStandardRefund(ctx, plan, gatewayErr); compensationErr != nil {
			return nil, fmt.Errorf("provider rejected refund; compensation failed: %w", compensationErr)
		}
		return &RefundResult{Success: false, Warning: "provider rejected the refund; local deduction was compensated and manual review is required"}, nil
	default:
		return nil, infraerrors.InternalServer("REFUND_GATEWAY_AMBIGUOUS", "refund gateway result is ambiguous; local deduction remains fenced for idempotent recovery")
	}
}

func (s *PaymentService) claimStandardRefund(ctx context.Context, plan *RefundPlan) (bool, error) {
	if plan == nil || plan.Order == nil || plan.DeductionType == payment.DeductionTypeWallet {
		return false, infraerrors.BadRequest("INVALID_REFUND_PLAN", "refund plan is invalid")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return false, fmt.Errorf("begin refund transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()
	current, err := lockPaymentOrderForRefund(txCtx, client, plan.OrderID)
	if err != nil {
		return false, err
	}

	detail, applied, err := loadStandardRefundDeductionDetail(txCtx, client, plan.OrderID)
	if err != nil {
		return false, fmt.Errorf("check refund rollback audit: %w", err)
	}
	compensated, err := paymentAuditActionExists(txCtx, client, plan.OrderID, standardRefundDeductionCompensatedAction)
	if err != nil {
		return false, fmt.Errorf("check refund compensation audit: %w", err)
	}
	if compensated {
		return false, infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "refund deduction was already compensated; manual reconciliation is required")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	recovered := false
	switch current.Status {
	case OrderStatusCompleted, OrderStatusRefundRequested:
		if applied {
			return false, infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "refund deduction marker conflicts with the order status")
		}
	case OrderStatusRefunding:
		if !isRefundLeaseStale(current.UpdatedAt, now) {
			return false, infraerrors.Conflict("REFUND_IN_PROGRESS", "refund is being processed")
		}
		if !applied {
			return false, infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "REFUNDING order has no immutable deduction marker")
		}
		if err := applyStandardRefundDetailToPlan(plan, detail); err != nil {
			return false, err
		}
		recovered = true
	default:
		return false, infraerrors.BadRequest("INVALID_STATUS", "order status does not allow refund")
	}

	actualBalanceDeducted := plan.BalanceToDeduct
	if !applied {
		actualBalanceDeducted, err = s.applyStandardRefundDeduction(txCtx, client, current, plan)
		if err != nil {
			return false, err
		}
	}
	updated, err := client.PaymentOrder.Update().
		Where(paymentorder.IDEQ(plan.OrderID), paymentorder.StatusEQ(current.Status), paymentorder.UpdatedAtEQ(current.UpdatedAt)).
		SetStatus(OrderStatusRefunding).
		SetRefundAmount(plan.RefundAmount).
		SetRefundReason(plan.Reason).
		SetForceRefund(plan.Force).
		SetUpdatedAt(now).
		Save(txCtx)
	if err != nil {
		return false, fmt.Errorf("claim refund lease: %w", err)
	}
	if updated != 1 {
		return false, infraerrors.Conflict("REFUND_IN_PROGRESS", "refund state changed concurrently")
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit refund claim: %w", err)
	}
	if !applied && plan.DeductionType == payment.DeductionTypeBalance {
		plan.BalanceToDeduct = actualBalanceDeducted
	}
	plan.RefundLeaseUpdatedAt = now
	return recovered, nil
}

func (s *PaymentService) applyStandardRefundDeduction(ctx context.Context, client *dbent.Client, order *dbent.PaymentOrder, plan *RefundPlan) (float64, error) {
	detail := standardRefundDeductionDetail{DeductionType: plan.DeductionType, UserID: order.UserID}
	switch plan.DeductionType {
	case payment.DeductionTypeBalance:
		actual, err := deductStandardRefundBalance(ctx, client, order.UserID, plan)
		if err != nil {
			return 0, err
		}
		detail.BalanceDeducted = actual
	case payment.DeductionTypeSubscription:
		if plan.SubDaysToDeduct > 0 && plan.SubscriptionID > 0 {
			sub, err := lockSubscriptionForRefund(ctx, client, plan.SubscriptionID)
			if err != nil {
				return 0, err
			}
			if sub.UserID != order.UserID {
				return 0, infraerrors.Conflict("REFUND_DEDUCTION_EVIDENCE_CHANGED", "subscription owner changed after refund preparation")
			}
			detail.SubscriptionID = sub.ID
			detail.SubscriptionDaysDeducted = plan.SubDaysToDeduct
			detail.SubscriptionPreviousExpiry = sub.ExpiresAt
			detail.SubscriptionPreviousStatus = sub.Status

			newExpiry := sub.ExpiresAt.AddDate(0, 0, -plan.SubDaysToDeduct)
			newStatus := sub.Status
			now := time.Now().UTC().Truncate(time.Microsecond)
			if !newExpiry.After(now) {
				newExpiry = now
				newStatus = SubscriptionStatusExpired
			}
			if _, err := client.UserSubscription.UpdateOneID(sub.ID).
				SetExpiresAt(newExpiry).
				SetStatus(newStatus).
				Save(ctx); err != nil {
				return 0, fmt.Errorf("deduct subscription entitlement: %w", err)
			}
		}
	case payment.DeductionTypeNone, "":
	default:
		return 0, infraerrors.BadRequest("INVALID_REFUND_PLAN", "unsupported refund deduction type")
	}
	if err := createPaymentAuditLog(ctx, client, plan.OrderID, standardRefundDeductionAppliedAction, "admin", detail.asMap()); err != nil {
		return 0, err
	}
	return detail.BalanceDeducted, nil
}

func deductStandardRefundBalance(ctx context.Context, client *dbent.Client, userID int64, plan *RefundPlan) (float64, error) {
	if client == nil || plan == nil || !isFinitePositive(plan.RefundAmount) ||
		math.IsNaN(plan.BalanceToDeduct) || math.IsInf(plan.BalanceToDeduct, 0) {
		return 0, infraerrors.BadRequest("INVALID_REFUND_PLAN", "refund balance deduction plan is invalid")
	}
	if plan.BalanceToDeduct < 0 || plan.BalanceToDeduct > plan.RefundAmount+amountToleranceCNY {
		return 0, infraerrors.BadRequest("INVALID_REFUND_PLAN", "refund balance deduction exceeds the refund amount")
	}
	if !plan.Force && math.Abs(plan.BalanceToDeduct-plan.RefundAmount) > amountToleranceCNY {
		return 0, infraerrors.BadRequest("INVALID_REFUND_PLAN", "non-forced refund must deduct the full refund amount")
	}

	query := client.User.Query().Where(dbuser.IDEQ(userID))
	if client.Driver().Dialect() == dialect.Postgres {
		query = query.ForUpdate()
	}
	current, err := query.Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return 0, infraerrors.Conflict("REFUND_DEDUCTION_EVIDENCE_CHANGED", "refund user no longer exists")
		}
		return 0, fmt.Errorf("lock refund user balance: %w", err)
	}
	if math.IsNaN(current.Balance) || math.IsInf(current.Balance, 0) {
		return 0, infraerrors.Conflict("REFUND_DEDUCTION_EVIDENCE_CHANGED", "refund user balance is invalid")
	}
	if !plan.Force && current.Balance < plan.RefundAmount {
		return 0, infraerrors.Conflict("REFUND_BALANCE_CHANGED_REQUIRE_FORCE", "user balance changed after refund preparation; review and retry with force")
	}

	available := math.Max(0, current.Balance)
	target := plan.RefundAmount
	if plan.Force {
		target = plan.BalanceToDeduct
	}
	actual := math.Min(target, available)
	if actual <= 0 {
		return 0, nil
	}
	updated, err := client.User.Update().
		Where(dbuser.IDEQ(userID), dbuser.BalanceGTE(actual)).
		AddBalance(-actual).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("deduct refund balance: %w", err)
	}
	if updated != 1 {
		if !plan.Force {
			return 0, infraerrors.Conflict("REFUND_BALANCE_CHANGED_REQUIRE_FORCE", "user balance changed during refund claim; review and retry with force")
		}
		return 0, infraerrors.Conflict("REFUND_BALANCE_CHANGED_RETRY", "user balance changed during forced refund claim; prepare the refund again")
	}
	return actual, nil
}

func (d standardRefundDeductionDetail) asMap() map[string]any {
	return map[string]any{
		"deductionType":              d.DeductionType,
		"userID":                     d.UserID,
		"balanceDeducted":            d.BalanceDeducted,
		"subscriptionID":             d.SubscriptionID,
		"subscriptionDaysDeducted":   d.SubscriptionDaysDeducted,
		"subscriptionPreviousExpiry": d.SubscriptionPreviousExpiry,
		"subscriptionPreviousStatus": d.SubscriptionPreviousStatus,
	}
}

func lockSubscriptionForRefund(ctx context.Context, client *dbent.Client, subscriptionID int64) (*dbent.UserSubscription, error) {
	query := client.UserSubscription.Query().Where(usersubscription.IDEQ(subscriptionID))
	if client.Driver().Dialect() == dialect.Postgres {
		query = query.ForUpdate()
	}
	sub, err := query.Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, infraerrors.Conflict("REFUND_DEDUCTION_EVIDENCE_CHANGED", "subscription no longer exists")
		}
		return nil, fmt.Errorf("lock subscription for refund: %w", err)
	}
	return sub, nil
}

func (s *PaymentService) restoreStandardRefundPlanFromAudit(ctx context.Context, plan *RefundPlan) error {
	if plan == nil {
		return infraerrors.BadRequest("INVALID_REFUND_PLAN", "refund plan is invalid")
	}
	compensated, err := paymentAuditActionExists(ctx, s.entClient, plan.OrderID, standardRefundDeductionCompensatedAction)
	if err != nil {
		return fmt.Errorf("check refund compensation audit: %w", err)
	}
	if compensated {
		return infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "refund deduction was compensated; manual reconciliation is required")
	}
	detail, found, err := loadStandardRefundDeductionDetail(ctx, s.entClient, plan.OrderID)
	if err != nil {
		return fmt.Errorf("load refund deduction evidence: %w", err)
	}
	if !found {
		return infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "REFUNDING order has no immutable deduction marker")
	}
	return applyStandardRefundDetailToPlan(plan, detail)
}

func applyStandardRefundDetailToPlan(plan *RefundPlan, detail standardRefundDeductionDetail) error {
	if plan == nil || plan.Order == nil || detail.UserID != plan.Order.UserID {
		return infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "refund deduction marker does not match the payment order")
	}
	plan.DeductionType = detail.DeductionType
	plan.BalanceToDeduct = detail.BalanceDeducted
	plan.SubscriptionID = detail.SubscriptionID
	plan.SubDaysToDeduct = detail.SubscriptionDaysDeducted
	return nil
}

func loadStandardRefundDeductionDetail(ctx context.Context, client *dbent.Client, orderID int64) (standardRefundDeductionDetail, bool, error) {
	var detail standardRefundDeductionDetail
	log, err := client.PaymentAuditLog.Query().
		Where(
			paymentauditlog.OrderIDEQ(strconv.FormatInt(orderID, 10)),
			paymentauditlog.ActionEQ(standardRefundDeductionAppliedAction),
		).
		Only(ctx)
	if dbent.IsNotFound(err) {
		return detail, false, nil
	}
	if err != nil {
		return detail, false, err
	}
	if err := json.Unmarshal([]byte(log.Detail), &detail); err != nil {
		return detail, false, fmt.Errorf("decode refund deduction marker: %w", err)
	}
	return detail, true, nil
}

func paymentAuditActionExists(ctx context.Context, client *dbent.Client, orderID int64, action string) (bool, error) {
	return client.PaymentAuditLog.Query().
		Where(
			paymentauditlog.OrderIDEQ(strconv.FormatInt(orderID, 10)),
			paymentauditlog.ActionEQ(action),
		).
		Exist(ctx)
}

func (s *PaymentService) executeRefundGatewayCall(
	ctx context.Context,
	plan *RefundPlan,
	startedAction string,
	acceptedAction string,
	recovered bool,
) (refundGatewayDisposition, bool, *payment.RefundResponse, error) {
	provider, err := s.getRefundProvider(ctx, plan.Order)
	if err != nil {
		return refundGatewayAmbiguous, false, nil, fmt.Errorf("get refund provider: %w", err)
	}
	if err := validateProviderSnapshotMetadata(plan.Order, provider.ProviderKey(), providerMerchantIdentityMetadata(provider)); err != nil {
		return refundGatewayAmbiguous, false, nil, err
	}
	if recovered && !refundProviderSupportsStableIdempotency(provider.ProviderKey()) {
		return refundGatewayAmbiguous, false, nil, infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "provider does not support safe automatic refund replay")
	}
	key := refundGatewayIdempotencyKey(plan.OrderID)
	if err := putPaymentAuditLog(ctx, s.entClient, plan.OrderID, startedAction, "admin", map[string]any{
		"idempotencyKey": key,
		"providerKey":    provider.ProviderKey(),
		"recovered":      recovered,
	}); err != nil {
		return refundGatewayAmbiguous, false, nil, fmt.Errorf("record refund gateway attempt: %w", err)
	}

	response, callErr := provider.Refund(ctx, payment.RefundRequest{
		TradeNo:        strings.TrimSpace(plan.Order.PaymentTradeNo),
		OrderID:        plan.Order.OutTradeNo,
		Amount:         strconv.FormatFloat(plan.GatewayAmount, 'f', 2, 64),
		Reason:         plan.Reason,
		IdempotencyKey: key,
	})
	disposition := classifyRefundGatewayResult(response, callErr)
	if disposition == refundGatewayAmbiguous {
		return disposition, true, response, callErr
	}
	detail := map[string]any{
		"idempotencyKey": key,
		"providerStatus": response.Status,
		"refundID":       response.RefundID,
	}
	if err := putPaymentAuditLog(ctx, s.entClient, plan.OrderID, acceptedAction, "admin", detail); err != nil {
		return refundGatewayAmbiguous, true, response, fmt.Errorf("record refund gateway response: %w", err)
	}
	return disposition, true, response, callErr
}

func putPaymentAuditLog(ctx context.Context, client *dbent.Client, orderID int64, action, operator string, detail map[string]any) error {
	encoded, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode payment audit %s: %w", action, err)
	}
	orderIDText := strconv.FormatInt(orderID, 10)
	existing, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(orderIDText), paymentauditlog.ActionEQ(action)).
		Only(ctx)
	if err == nil {
		_, err = client.PaymentAuditLog.UpdateOneID(existing.ID).
			SetDetail(string(encoded)).
			SetOperator(operator).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("update payment audit %s: %w", action, err)
		}
		return nil
	}
	if !dbent.IsNotFound(err) {
		return fmt.Errorf("query payment audit %s: %w", action, err)
	}
	return createPaymentAuditLog(ctx, client, orderID, action, operator, detail)
}

func (s *PaymentService) compensateStandardRefund(ctx context.Context, plan *RefundPlan, cause error) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin refund compensation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()
	current, err := lockPaymentOrderForRefund(txCtx, client, plan.OrderID)
	if err != nil {
		return err
	}
	if current.Status != OrderStatusRefunding || !current.UpdatedAt.Equal(plan.RefundLeaseUpdatedAt) {
		return infraerrors.Conflict("REFUND_LEASE_LOST", "cannot compensate a refund owned by another worker")
	}
	detail, found, err := loadStandardRefundDeductionDetail(txCtx, client, plan.OrderID)
	if err != nil {
		return err
	}
	if !found {
		return infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "refund deduction marker is missing")
	}
	alreadyCompensated, err := paymentAuditActionExists(txCtx, client, plan.OrderID, standardRefundDeductionCompensatedAction)
	if err != nil {
		return err
	}
	if alreadyCompensated {
		return nil
	}
	switch detail.DeductionType {
	case payment.DeductionTypeBalance:
		if detail.BalanceDeducted > 0 {
			if _, err := client.User.UpdateOneID(detail.UserID).AddBalance(detail.BalanceDeducted).Save(txCtx); err != nil {
				return fmt.Errorf("compensate refund balance: %w", err)
			}
		}
	case payment.DeductionTypeSubscription:
		if detail.SubscriptionID > 0 && !detail.SubscriptionPreviousExpiry.IsZero() {
			if _, err := client.UserSubscription.UpdateOneID(detail.SubscriptionID).
				SetExpiresAt(detail.SubscriptionPreviousExpiry).
				SetStatus(detail.SubscriptionPreviousStatus).
				Save(txCtx); err != nil {
				return fmt.Errorf("compensate subscription entitlement: %w", err)
			}
		}
	}
	if err := createPaymentAuditLog(txCtx, client, plan.OrderID, standardRefundDeductionCompensatedAction, "admin", map[string]any{
		"reason": psErrMsg(cause),
	}); err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	updated, err := client.PaymentOrder.Update().
		Where(paymentorder.IDEQ(plan.OrderID), paymentorder.StatusEQ(OrderStatusRefunding), paymentorder.UpdatedAtEQ(plan.RefundLeaseUpdatedAt)).
		SetStatus(OrderStatusRefundFailed).
		SetFailedAt(now).
		SetFailedReason(psErrMsg(cause)).
		SetUpdatedAt(now).
		Save(txCtx)
	if err != nil {
		return fmt.Errorf("mark compensated refund failed: %w", err)
	}
	if updated != 1 {
		return infraerrors.Conflict("REFUND_LEASE_LOST", "refund lease changed during compensation")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit refund compensation: %w", err)
	}
	if detail.SubscriptionID > 0 && plan.Order.SubscriptionGroupID != nil && s.subscriptionSvc != nil {
		s.subscriptionSvc.invalidateSubscriptionCaches(ctx, plan.Order.UserID, *plan.Order.SubscriptionGroupID)
	}
	return nil
}

func (s *PaymentService) markStandardRefundOK(ctx context.Context, plan *RefundPlan) (*RefundResult, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin refund completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()
	current, err := lockPaymentOrderForRefund(txCtx, client, plan.OrderID)
	if err != nil {
		return nil, err
	}
	if current.Status != OrderStatusRefunding || !current.UpdatedAt.Equal(plan.RefundLeaseUpdatedAt) {
		return nil, infraerrors.Conflict("REFUND_LEASE_LOST", "refund lease was reclaimed by another worker")
	}
	finalStatus := OrderStatusRefunded
	if plan.RefundAmount < plan.Order.Amount-amountToleranceCNY {
		finalStatus = OrderStatusPartiallyRefunded
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	updated, err := client.PaymentOrder.Update().
		Where(paymentorder.IDEQ(plan.OrderID), paymentorder.StatusEQ(OrderStatusRefunding), paymentorder.UpdatedAtEQ(plan.RefundLeaseUpdatedAt)).
		SetStatus(finalStatus).
		SetRefundAmount(plan.RefundAmount).
		SetRefundReason(plan.Reason).
		SetRefundAt(now).
		SetForceRefund(plan.Force).
		SetUpdatedAt(now).
		Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("mark refund complete: %w", err)
	}
	if updated != 1 {
		return nil, infraerrors.Conflict("REFUND_LEASE_LOST", "refund lease changed during completion")
	}
	if err := createPaymentAuditLog(txCtx, client, plan.OrderID, "REFUND_SUCCESS", "admin", map[string]any{
		"refundAmount":    plan.RefundAmount,
		"reason":          plan.Reason,
		"balanceDeducted": plan.BalanceToDeduct,
		"subDaysDeducted": plan.SubDaysToDeduct,
		"force":           plan.Force,
		"idempotencyKey":  refundGatewayIdempotencyKey(plan.OrderID),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit refund completion: %w", err)
	}
	if plan.SubscriptionID > 0 && plan.Order.SubscriptionGroupID != nil && s.subscriptionSvc != nil {
		s.subscriptionSvc.invalidateSubscriptionCaches(ctx, plan.Order.UserID, *plan.Order.SubscriptionGroupID)
	}
	return &RefundResult{
		Success:         true,
		BalanceDeducted: plan.BalanceToDeduct,
		SubDaysDeducted: plan.SubDaysToDeduct,
	}, nil
}
