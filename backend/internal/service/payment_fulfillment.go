package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// ErrOrderNotFound is returned by HandlePaymentNotification when the webhook
// references an out_trade_no that does not exist in our DB. Callers (webhook
// handlers) should treat this as a terminal, non-retryable condition and still
// respond with a 2xx success to the provider — otherwise the provider will keep
// retrying forever (e.g. when a foreign environment's webhook endpoint is
// misconfigured to point at us, or when our orders table has been wiped).
var ErrOrderNotFound = errors.New("payment order not found")

// --- Payment Notification & Fulfillment ---

func (s *PaymentService) HandlePaymentNotification(ctx context.Context, n *payment.PaymentNotification, pk string) error {
	if n.Status != payment.NotificationStatusSuccess {
		return nil
	}
	// Look up order by out_trade_no (the external order ID we sent to the provider)
	order, err := s.entClient.PaymentOrder.Query().Where(paymentorder.OutTradeNo(n.OrderID)).Only(ctx)
	if err != nil {
		// Fallback only for true legacy "sub2_N" DB-ID payloads when the
		// current out_trade_no lookup genuinely did not find an order.
		if oid, ok := parseLegacyPaymentOrderID(n.OrderID, err); ok {
			return s.confirmPayment(ctx, oid, n.TradeNo, n.Amount, pk, n.Metadata)
		}
		if dbent.IsNotFound(err) {
			return fmt.Errorf("%w: out_trade_no=%s", ErrOrderNotFound, n.OrderID)
		}
		return fmt.Errorf("lookup order failed for out_trade_no %s: %w", n.OrderID, err)
	}
	return s.confirmPayment(ctx, order.ID, n.TradeNo, n.Amount, pk, n.Metadata)
}

func parseLegacyPaymentOrderID(orderID string, lookupErr error) (int64, bool) {
	if !dbent.IsNotFound(lookupErr) {
		return 0, false
	}
	orderID = strings.TrimSpace(orderID)
	if !strings.HasPrefix(orderID, orderIDPrefix) {
		return 0, false
	}
	trimmed := strings.TrimPrefix(orderID, orderIDPrefix)
	if trimmed == "" || trimmed == orderID {
		return 0, false
	}
	oid, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || oid <= 0 {
		return 0, false
	}
	return oid, true
}

func (s *PaymentService) confirmPayment(ctx context.Context, oid int64, tradeNo string, paid float64, pk string, metadata map[string]string) error {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		slog.Error("order not found", "orderID", oid)
		return nil
	}
	instanceProviderKey := ""
	if inst, instErr := s.getOrderProviderInstance(ctx, o); instErr == nil && inst != nil {
		instanceProviderKey = inst.ProviderKey
	}
	expectedProviderKey := expectedNotificationProviderKeyForOrder(s.registry, o, instanceProviderKey)
	if expectedProviderKey != "" && strings.TrimSpace(pk) != "" && !strings.EqualFold(expectedProviderKey, strings.TrimSpace(pk)) {
		s.writeAuditLog(ctx, o.ID, "PAYMENT_PROVIDER_MISMATCH", pk, map[string]any{
			"expectedProvider": expectedProviderKey,
			"actualProvider":   pk,
			"tradeNo":          tradeNo,
		})
		return fmt.Errorf("provider mismatch: expected %s, got %s", expectedProviderKey, pk)
	}
	if err := validateProviderNotificationMetadata(o, pk, metadata); err != nil {
		s.writeAuditLog(ctx, o.ID, "PAYMENT_PROVIDER_METADATA_MISMATCH", pk, map[string]any{
			"detail":  err.Error(),
			"tradeNo": tradeNo,
		})
		return err
	}
	if !isValidProviderAmount(paid) {
		s.writeAuditLog(ctx, o.ID, "PAYMENT_INVALID_AMOUNT", pk, map[string]any{
			"expected": o.PayAmount,
			"paid":     paid,
			"tradeNo":  tradeNo,
		})
		return fmt.Errorf("invalid paid amount from provider: %v", paid)
	}
	if math.Abs(paid-o.PayAmount) > amountToleranceCNY {
		s.writeAuditLog(ctx, o.ID, "PAYMENT_AMOUNT_MISMATCH", pk, map[string]any{"expected": o.PayAmount, "paid": paid, "tradeNo": tradeNo})
		return fmt.Errorf("amount mismatch: expected %.2f, got %.2f", o.PayAmount, paid)
	}
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		tradeNo = strings.TrimSpace(o.PaymentTradeNo)
	}
	if tradeNo == "" {
		s.writeAuditLog(ctx, o.ID, "PAYMENT_TRADE_EVIDENCE_MISSING", pk, map[string]any{
			"paidAmount": paid,
		})
		return infraerrors.BadRequest("PAYMENT_TRADE_EVIDENCE_MISSING", "successful payment is missing provider trade evidence")
	}
	return s.toPaid(ctx, o, tradeNo, paid, pk)
}

func isValidProviderAmount(amount float64) bool {
	return amount > 0 && !math.IsNaN(amount) && !math.IsInf(amount, 0)
}

func validateProviderNotificationMetadata(order *dbent.PaymentOrder, providerKey string, metadata map[string]string) error {
	return validateProviderSnapshotMetadata(order, providerKey, metadata)
}

func expectedNotificationProviderKey(registry *payment.Registry, orderPaymentType string, orderProviderKey string, instanceProviderKey string) string {
	if key := strings.TrimSpace(instanceProviderKey); key != "" {
		return key
	}
	if key := strings.TrimSpace(orderProviderKey); key != "" {
		return key
	}
	if registry != nil {
		if key := strings.TrimSpace(registry.GetProviderKey(payment.PaymentType(orderPaymentType))); key != "" {
			return key
		}
	}
	return strings.TrimSpace(orderPaymentType)
}

func (s *PaymentService) toPaid(ctx context.Context, o *dbent.PaymentOrder, tradeNo string, paid float64, pk string) error {
	previousStatus := o.Status
	now := time.Now()
	c, err := s.entClient.PaymentOrder.Update().Where(
		paymentorder.IDEQ(o.ID),
		paymentorder.Or(
			paymentorder.StatusEQ(OrderStatusPending),
			paymentorder.StatusEQ(OrderStatusFailed),
			paymentorder.StatusEQ(OrderStatusCancelled),
			paymentorder.StatusEQ(OrderStatusExpired),
		),
	).SetStatus(OrderStatusPaid).SetPayAmount(paid).SetPaymentTradeNo(tradeNo).SetPaidAt(now).ClearFailedAt().ClearFailedReason().Save(ctx)
	if err != nil {
		return fmt.Errorf("update to PAID: %w", err)
	}
	if c == 0 {
		return s.alreadyProcessed(ctx, o)
	}
	if previousStatus == OrderStatusCancelled || previousStatus == OrderStatusExpired {
		slog.Info("order recovered from webhook payment success",
			"orderID", o.ID,
			"previousStatus", previousStatus,
			"tradeNo", tradeNo,
			"provider", pk,
		)
		s.writeAuditLog(ctx, o.ID, "ORDER_RECOVERED", pk, map[string]any{
			"previous_status": previousStatus,
			"tradeNo":         tradeNo,
			"paidAmount":      paid,
			"reason":          "webhook payment success received after order " + previousStatus,
		})
	}
	s.writeAuditLog(ctx, o.ID, "ORDER_PAID", pk, map[string]any{"tradeNo": tradeNo, "paidAmount": paid})
	return s.executeFulfillmentFromNotification(ctx, o.ID)
}

func (s *PaymentService) alreadyProcessed(ctx context.Context, o *dbent.PaymentOrder) error {
	cur, err := s.entClient.PaymentOrder.Get(ctx, o.ID)
	if err != nil {
		return nil
	}
	switch cur.Status {
	case OrderStatusCompleted, OrderStatusRefunded:
		return nil
	case OrderStatusFailed:
		if !paymentOrderHasPaidEvidence(cur) {
			return paymentNotConfirmedError()
		}
		return s.executeFulfillmentFromNotification(ctx, o.ID)
	case OrderStatusPaid:
		return s.executeFulfillmentFromNotification(ctx, o.ID)
	case OrderStatusRecharging:
		if !isFulfillmentLeaseStale(cur.UpdatedAt, time.Now()) {
			// A duplicate successful webhook has already been accepted. Returning
			// nil prevents the provider from retrying while the active worker owns
			// the lease.
			return nil
		}
		if err := s.executeFulfillmentFromNotification(ctx, o.ID); err != nil {
			return err
		}
		return nil
	case OrderStatusExpired:
		slog.Error("expired paid order recovery lost a concurrent state transition",
			"orderID", o.ID,
			"status", cur.Status,
			"updatedAt", cur.UpdatedAt,
		)
		s.writeAuditLog(ctx, o.ID, "PAYMENT_RECOVERY_CONFLICT", "system", map[string]any{
			"status":    cur.Status,
			"updatedAt": cur.UpdatedAt,
			"reason":    "trusted paid evidence could not transition expired order",
		})
		return infraerrors.Conflict("PAYMENT_RECOVERY_CONFLICT", "paid order recovery conflicted with a concurrent state change")
	default:
		return nil
	}
}

func (s *PaymentService) executeFulfillmentFromNotification(ctx context.Context, orderID int64) error {
	err := s.executeFulfillment(ctx, orderID)
	if infraerrors.Reason(err) == "FULFILLMENT_IN_PROGRESS" {
		return nil
	}
	return err
}

func (s *PaymentService) executeFulfillment(ctx context.Context, oid int64) error {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return fmt.Errorf("get order: %w", err)
	}
	if o.OrderType == payment.OrderTypeSubscription {
		return s.ExecuteSubscriptionFulfillment(ctx, oid)
	}
	return s.ExecuteBalanceFulfillment(ctx, oid)
}

func (s *PaymentService) ExecuteBalanceFulfillment(ctx context.Context, oid int64) error {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.Status == OrderStatusCompleted {
		return nil
	}
	if psIsRefundStatus(o.Status) {
		return infraerrors.BadRequest("INVALID_STATUS", "refund-related order cannot fulfill")
	}
	if !paymentOrderHasPaidEvidence(o) {
		return paymentNotConfirmedError()
	}
	if o.Status != OrderStatusPaid && o.Status != OrderStatusFailed && o.Status != OrderStatusRecharging {
		return infraerrors.BadRequest("INVALID_STATUS", "order cannot fulfill in status "+o.Status)
	}
	claimed, err := s.claimFulfillmentLease(ctx, o)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	if err := s.doBalance(ctx, o); err != nil {
		s.markFailed(ctx, o, err)
		return err
	}
	return nil
}

// redeemAction represents the idempotency decision for balance fulfillment.
type redeemAction int

const (
	// redeemActionCreate: code does not exist — create it, then redeem.
	redeemActionCreate redeemAction = iota
	// redeemActionRedeem: code exists but is unused — skip creation, redeem only.
	redeemActionRedeem
	// redeemActionSkipCompleted: code exists and is already used — skip to mark completed.
	redeemActionSkipCompleted
)

// resolveRedeemAction decides the idempotency action based on an existing redeem code lookup.
// existing is the result of GetByCode; lookupErr is the error from that call.
func resolveRedeemAction(existing *RedeemCode, lookupErr error) redeemAction {
	if existing == nil || lookupErr != nil {
		return redeemActionCreate
	}
	if existing.IsUsed() {
		return redeemActionSkipCompleted
	}
	return redeemActionRedeem
}

func (s *PaymentService) doBalance(ctx context.Context, o *dbent.PaymentOrder) error {
	// Idempotency: check if redeem code already exists (from a previous partial run)
	existing, lookupErr := s.redeemService.GetByCode(ctx, o.RechargeCode)
	action := resolveRedeemAction(existing, lookupErr)

	switch action {
	case redeemActionSkipCompleted:
		if err := s.applyAffiliateRebateForOrder(ctx, o); err != nil {
			return err
		}
		// Code already created and redeemed — just mark completed
		return s.markCompleted(ctx, o, "RECHARGE_SUCCESS")
	case redeemActionCreate:
		rc := &RedeemCode{Code: o.RechargeCode, Type: RedeemTypeBalance, Value: o.Amount, Status: StatusUnused}
		if err := s.redeemService.CreateCode(ctx, rc); err != nil {
			return fmt.Errorf("create redeem code: %w", err)
		}
	case redeemActionRedeem:
		// Code exists but unused — skip creation, proceed to redeem
	}
	if _, err := s.redeemService.Redeem(ContextSkipRedeemAffiliate(ctx), o.UserID, o.RechargeCode); err != nil {
		return fmt.Errorf("redeem balance: %w", err)
	}
	if err := s.applyAffiliateRebateForOrder(ctx, o); err != nil {
		return err
	}
	return s.markCompleted(ctx, o, "RECHARGE_SUCCESS")
}

func (s *PaymentService) markCompleted(ctx context.Context, o *dbent.PaymentOrder, auditAction string) error {
	now := time.Now()
	updated, err := s.entClient.PaymentOrder.Update().Where(
		paymentorder.IDEQ(o.ID),
		paymentorder.StatusEQ(OrderStatusRecharging),
		paymentorder.UpdatedAtEQ(o.UpdatedAt),
	).SetStatus(OrderStatusCompleted).SetCompletedAt(now).Save(ctx)
	if err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("mark completed: status changed concurrently")
	}
	s.writeAuditLog(ctx, o.ID, auditAction, "system", map[string]any{
		"rechargeCode":   o.RechargeCode,
		"creditedAmount": o.Amount,
		"payAmount":      o.PayAmount,
	})
	return nil
}

func (s *PaymentService) ExecuteSubscriptionFulfillment(ctx context.Context, oid int64) error {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.Status == OrderStatusCompleted {
		return nil
	}
	if psIsRefundStatus(o.Status) {
		return infraerrors.BadRequest("INVALID_STATUS", "refund-related order cannot fulfill")
	}
	if !paymentOrderHasPaidEvidence(o) {
		return paymentNotConfirmedError()
	}
	if o.Status != OrderStatusPaid && o.Status != OrderStatusFailed && o.Status != OrderStatusRecharging {
		return infraerrors.BadRequest("INVALID_STATUS", "order cannot fulfill in status "+o.Status)
	}
	if o.SubscriptionDays == nil || (o.SubscriptionGroupID == nil && o.PlanID == nil) {
		return infraerrors.BadRequest("INVALID_STATUS", "missing subscription info")
	}
	claimed, err := s.claimFulfillmentLease(ctx, o)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	if err := s.doSub(ctx, o); err != nil {
		s.markFailed(ctx, o, err)
		return err
	}
	return nil
}

func (s *PaymentService) doSub(ctx context.Context, o *dbent.PaymentOrder) error {
	days := *o.SubscriptionDays
	// Idempotency: check audit log to see if subscription was already assigned.
	// Prevents double-extension on retry after markCompleted fails.
	alreadyAssigned, err := s.hasAuditLog(ctx, o.ID, "SUBSCRIPTION_SUCCESS")
	if err != nil {
		return fmt.Errorf("check subscription fulfillment audit: %w", err)
	}
	if alreadyAssigned {
		slog.Info("subscription already assigned for order, skipping", "orderID", o.ID)
		return s.markCompleted(ctx, o, "SUBSCRIPTION_SUCCESS")
	}
	orderNote := fmt.Sprintf("payment order %d", o.ID)
	var snapshot *planFulfillmentSnapshot
	if o.PlanID != nil {
		snapshot, err = loadPlanFulfillmentSnapshot(ctx, s.entClient, o.ID)
		if err != nil {
			return err
		}
		if err := snapshot.ValidateForOrder(o); err != nil {
			return infraerrors.Conflict("FULFILLMENT_SNAPSHOT_INVALID", err.Error())
		}
	}

	if snapshot != nil && snapshot.PlanType == PlanTypeCredits {
		return s.doWalletSub(ctx, o, orderNote, snapshot)
	}
	if o.SubscriptionGroupID == nil {
		return infraerrors.BadRequest("INVALID_STATUS", "missing subscription group snapshot")
	}
	return s.doGroupSub(ctx, o, days, orderNote, *o.SubscriptionGroupID, snapshot)
}

func (s *PaymentService) doGroupSub(ctx context.Context, o *dbent.PaymentOrder, days int, orderNote string, groupID int64, snapshot *planFulfillmentSnapshot) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin group fulfillment transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	g, err := s.groupRepo.GetByID(txCtx, groupID)
	if err != nil || g == nil || g.Status != payment.EntityStatusActive {
		return fmt.Errorf("group %d no longer exists or inactive", groupID)
	}
	assignInput := &AssignSubscriptionInput{
		UserID: o.UserID, GroupID: groupID, ValidityDays: days, AssignedBy: 0, Notes: orderNote,
	}
	if snapshot != nil {
		assignInput.PlanID = &snapshot.PlanID
		assignInput.PlanType = snapshot.PlanType
	}
	sub, _, err := s.subscriptionSvc.AssignOrExtendSubscription(txCtx, assignInput)
	if err != nil {
		return fmt.Errorf("assign subscription: %w", err)
	}
	if snapshot != nil {
		lockedRates := planSnapshotRates(snapshot)
		if len(lockedRates) == 0 {
			return errors.New("monthly plan snapshot has no locked rates")
		}
		updatedSub, updateErr := client.UserSubscription.UpdateOneID(sub.ID).
			SetLockedRates(lockedRates).
			Save(txCtx)
		if updateErr != nil {
			return fmt.Errorf("persist subscription locked rates: %w", updateErr)
		}
		sub.LockedRates = planSnapshotRates(snapshot)
		sub.ExpiresAt = updatedSub.ExpiresAt
		grantStartsAt, grantExpiresAt := planSnapshotGrantWindow(sub, snapshot, time.Now())
		if err := attachPlanSnapshotGrant(txCtx, client, o.ID, sub.ID, grantStartsAt, grantExpiresAt); err != nil {
			return err
		}
	}

	now := time.Now()
	updated, err := client.PaymentOrder.Update().
		Where(
			paymentorder.IDEQ(o.ID),
			paymentorder.StatusEQ(OrderStatusRecharging),
			paymentorder.UpdatedAtEQ(o.UpdatedAt),
		).
		SetStatus(OrderStatusCompleted).
		SetCompletedAt(now).
		Save(txCtx)
	if err != nil {
		return fmt.Errorf("complete group order: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("complete group order: status changed concurrently")
	}
	detail, _ := json.Marshal(map[string]any{
		"subscriptionID":  sub.ID,
		"groupID":         groupID,
		"days":            days,
		"payAmount":       o.PayAmount,
		"planID":          snapshotPlanID(snapshot),
		"snapshotVersion": snapshotVersion(snapshot),
	})
	if _, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(o.ID, 10)).
		SetAction("SUBSCRIPTION_SUCCESS").
		SetDetail(string(detail)).
		SetOperator("system").
		Save(txCtx); err != nil {
		return fmt.Errorf("record group fulfillment audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit group fulfillment transaction: %w", err)
	}
	s.subscriptionSvc.invalidateSubscriptionCaches(ctx, o.UserID, groupID)
	return nil
}

func (s *PaymentService) doWalletSub(ctx context.Context, o *dbent.PaymentOrder, orderNote string, snapshot *planFulfillmentSnapshot) error {
	if snapshot == nil || snapshot.PlanType != PlanTypeCredits || snapshot.WalletQuotaUSD == nil {
		return infraerrors.BadRequest("INVALID_STATUS", "missing credits plan fulfillment snapshot")
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin wallet fulfillment transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	walletInitial := *snapshot.WalletQuotaUSD
	planID := snapshot.PlanID
	sub, err := s.subscriptionSvc.AssignSubscription(txCtx, &AssignSubscriptionInput{
		UserID:           o.UserID,
		ValidityDays:     snapshot.SubscriptionDays,
		AssignedBy:       0,
		Notes:            orderNote,
		WalletInitialUSD: &walletInitial,
		PlanID:           &planID,
		PlanType:         snapshot.PlanType,
		PaymentOrderID:   &o.ID,
	})
	if err != nil {
		return fmt.Errorf("assign wallet subscription: %w", err)
	}
	grantStartsAt, grantExpiresAt := planSnapshotGrantWindow(sub, snapshot, time.Now())
	if err := attachPlanSnapshotGrant(txCtx, client, o.ID, sub.ID, grantStartsAt, grantExpiresAt); err != nil {
		return err
	}

	now := time.Now()
	updated, err := client.PaymentOrder.Update().
		Where(
			paymentorder.IDEQ(o.ID),
			paymentorder.StatusEQ(OrderStatusRecharging),
			paymentorder.UpdatedAtEQ(o.UpdatedAt),
		).
		SetStatus(OrderStatusCompleted).
		SetCompletedAt(now).
		Save(txCtx)
	if err != nil {
		return fmt.Errorf("complete wallet order: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("complete wallet order: status changed concurrently")
	}

	detail, _ := json.Marshal(map[string]any{
		"subscriptionID":  sub.ID,
		"creditedAmount":  walletInitial,
		"payAmount":       o.PayAmount,
		"planID":          snapshot.PlanID,
		"planType":        snapshot.PlanType,
		"snapshotVersion": snapshot.SchemaVersion,
	})
	if _, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(o.ID, 10)).
		SetAction("SUBSCRIPTION_SUCCESS").
		SetDetail(string(detail)).
		SetOperator("system").
		Save(txCtx); err != nil {
		return fmt.Errorf("record wallet fulfillment audit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit wallet fulfillment transaction: %w", err)
	}
	return nil
}

func (s *PaymentService) hasAuditLog(ctx context.Context, orderID int64, action string) (bool, error) {
	oid := strconv.FormatInt(orderID, 10)
	c, err := s.entClient.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(oid), paymentauditlog.ActionEQ(action)).
		Limit(1).Count(ctx)
	if err != nil {
		return false, fmt.Errorf("query payment audit log: %w", err)
	}
	return c > 0, nil
}

func (s *PaymentService) applyAffiliateRebateForOrder(ctx context.Context, o *dbent.PaymentOrder) error {
	if o == nil || o.OrderType != payment.OrderTypeBalance || o.Amount <= 0 {
		return nil
	}
	if s.affiliateService == nil {
		return nil
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": fmt.Sprintf("begin affiliate rebate tx: %v", err),
		})
		return fmt.Errorf("begin affiliate rebate tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	claimed, err := s.tryClaimAffiliateRebateAudit(txCtx, tx.Client(), o.ID, o.Amount)
	if err != nil {
		s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": err.Error(),
		})
		return fmt.Errorf("claim affiliate rebate audit: %w", err)
	}
	if !claimed {
		return nil
	}

	sourceOrderID := o.ID
	rebateAmount, err := s.affiliateService.AccrueInviteRebateForOrder(txCtx, o.UserID, o.Amount, &sourceOrderID)
	if err != nil {
		s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": err.Error(),
		})
		return fmt.Errorf("accrue affiliate rebate: %w", err)
	}

	if rebateAmount <= 0 {
		if err := s.updateClaimedAffiliateRebateAudit(txCtx, tx.Client(), o.ID, "AFFILIATE_REBATE_SKIPPED", map[string]any{
			"baseAmount": o.Amount,
			"reason":     "no inviter bound or rebate amount <= 0",
		}); err != nil {
			s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
				"error": err.Error(),
			})
			return fmt.Errorf("update affiliate rebate skipped audit: %w", err)
		}
		if err := tx.Commit(); err != nil {
			s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
				"error": fmt.Sprintf("commit affiliate rebate tx: %v", err),
			})
			return fmt.Errorf("commit affiliate rebate tx: %w", err)
		}
		return nil
	}

	if err := s.updateClaimedAffiliateRebateAudit(txCtx, tx.Client(), o.ID, "AFFILIATE_REBATE_APPLIED", map[string]any{
		"baseAmount":   o.Amount,
		"rebateAmount": rebateAmount,
	}); err != nil {
		s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": err.Error(),
		})
		return fmt.Errorf("update affiliate rebate applied audit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		s.writeAuditLog(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": fmt.Sprintf("commit affiliate rebate tx: %v", err),
		})
		return fmt.Errorf("commit affiliate rebate tx: %w", err)
	}
	return nil
}

func (s *PaymentService) tryClaimAffiliateRebateAudit(ctx context.Context, client *dbent.Client, orderID int64, baseAmount float64) (bool, error) {
	if client == nil {
		return false, errors.New("nil payment client")
	}
	oid := strconv.FormatInt(orderID, 10)
	detail, _ := json.Marshal(map[string]any{
		"baseAmount": baseAmount,
		"status":     "reserved",
	})
	rows, err := client.QueryContext(ctx, `
INSERT INTO payment_audit_logs (order_id, action, detail, operator, created_at)
SELECT $1::text, 'AFFILIATE_REBATE_APPLIED', $2::text, 'system', NOW()
WHERE NOT EXISTS (
	SELECT 1
	FROM payment_audit_logs
	WHERE order_id = $1::text
	  AND action IN ('AFFILIATE_REBATE_APPLIED', 'AFFILIATE_REBATE_SKIPPED')
)
ON CONFLICT (order_id, action) DO NOTHING
RETURNING id`, oid, string(detail))
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	var claimID int64
	if err := rows.Scan(&claimID); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PaymentService) updateClaimedAffiliateRebateAudit(ctx context.Context, client *dbent.Client, orderID int64, action string, detail map[string]any) error {
	if client == nil {
		return errors.New("nil payment client")
	}
	oid := strconv.FormatInt(orderID, 10)
	detailJSON, _ := json.Marshal(detail)
	updated, err := client.PaymentAuditLog.Update().
		Where(
			paymentauditlog.OrderIDEQ(oid),
			paymentauditlog.ActionEQ("AFFILIATE_REBATE_APPLIED"),
		).
		SetAction(action).
		SetDetail(string(detailJSON)).
		SetOperator("system").
		Save(ctx)
	if err != nil {
		return err
	}
	if updated == 0 {
		return errors.New("affiliate rebate claim log not found")
	}
	return nil
}

func (s *PaymentService) markFailed(ctx context.Context, order *dbent.PaymentOrder, cause error) {
	if order == nil {
		return
	}
	now := time.Now()
	r := fulfillmentFailureReasonPrefix + psErrMsg(cause)
	// Only mark FAILED if still in RECHARGING state — prevents overwriting
	// a COMPLETED order or a lease reclaimed by another worker.
	c, e := s.entClient.PaymentOrder.Update().
		Where(
			paymentorder.IDEQ(order.ID),
			paymentorder.StatusEQ(OrderStatusRecharging),
			paymentorder.UpdatedAtEQ(order.UpdatedAt),
		).
		SetStatus(OrderStatusFailed).SetFailedAt(now).SetFailedReason(r).Save(ctx)
	if e != nil {
		slog.Error("mark FAILED", "orderID", order.ID, "error", e)
	}
	if c > 0 {
		s.writeAuditLog(ctx, order.ID, "FULFILLMENT_FAILED", "system", map[string]any{"reason": r})
	}
}

func isFulfillmentLeaseStale(updatedAt, now time.Time) bool {
	return !updatedAt.After(now.Add(-paymentFulfillmentLeaseTimeout))
}

func paymentOrderHasPaidEvidence(order *dbent.PaymentOrder) bool {
	return order != nil && order.PaidAt != nil && strings.TrimSpace(order.PaymentTradeNo) != ""
}

func paymentNotConfirmedError() error {
	return infraerrors.BadRequest("PAYMENT_NOT_CONFIRMED", "order payment is not confirmed")
}

func (s *PaymentService) claimFulfillmentLease(ctx context.Context, order *dbent.PaymentOrder) (bool, error) {
	if order == nil {
		return false, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if !paymentOrderHasPaidEvidence(order) {
		return false, paymentNotConfirmedError()
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	cutoff := now.Add(-paymentFulfillmentLeaseTimeout)
	previousUpdatedAt := order.UpdatedAt
	if order.Status == OrderStatusRecharging && !isFulfillmentLeaseStale(order.UpdatedAt, now) {
		return false, infraerrors.Conflict("FULFILLMENT_IN_PROGRESS", "order is being processed")
	}

	eligible := paymentorder.StatusIn(OrderStatusPaid, OrderStatusFailed)
	if order.Status == OrderStatusRecharging {
		eligible = paymentorder.And(
			paymentorder.StatusEQ(OrderStatusRecharging),
			paymentorder.UpdatedAtLTE(cutoff),
		)
	}
	updated, err := s.entClient.PaymentOrder.Update().
		Where(paymentorder.IDEQ(order.ID), eligible).
		SetStatus(OrderStatusRecharging).
		SetUpdatedAt(now).
		ClearFailedAt().
		ClearFailedReason().
		Save(ctx)
	if err != nil {
		return false, fmt.Errorf("claim fulfillment lease: %w", err)
	}
	if updated == 1 {
		// Carry the fencing token through the rest of fulfillment. Every final
		// order transition compares this exact updated_at value, so a worker
		// whose lease was reclaimed cannot commit wallet/group side effects.
		order.UpdatedAt = now
		if order.Status == OrderStatusRecharging {
			s.writeAuditLog(ctx, order.ID, "FULFILLMENT_LEASE_RECLAIMED", "system", map[string]any{
				"previous_updated_at": previousUpdatedAt,
				"lease_timeout":       paymentFulfillmentLeaseTimeout.String(),
			})
		}
		return true, nil
	}

	current, err := s.entClient.PaymentOrder.Get(ctx, order.ID)
	if err != nil {
		return false, fmt.Errorf("reload fulfillment lease: %w", err)
	}
	if current.Status == OrderStatusCompleted {
		return false, nil
	}
	if current.Status == OrderStatusRecharging {
		return false, infraerrors.Conflict("FULFILLMENT_IN_PROGRESS", "order is being processed")
	}
	return false, infraerrors.BadRequest("INVALID_STATUS", "order cannot fulfill in status "+current.Status)
}

// RecoverStaleFulfillments is the background crash-recovery path for paid
// orders that never entered fulfillment and RECHARGING leases whose worker
// disappeared. The per-order compare-and-swap in claimFulfillmentLease keeps
// multiple application instances safe.
func (s *PaymentService) RecoverStaleFulfillments(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = defaultStaleRecoveryBatchSize
	}
	if limit > maxStaleRecoveryBatchSize {
		limit = maxStaleRecoveryBatchSize
	}
	cutoff := time.Now().Add(-paymentFulfillmentLeaseTimeout)
	orders, err := s.entClient.PaymentOrder.Query().
		Where(
			paymentorder.StatusIn(OrderStatusPaid, OrderStatusRecharging),
			paymentorder.PaidAtNotNil(),
			paymentorder.PaymentTradeNoNEQ(""),
			paymentorder.UpdatedAtLTE(cutoff),
		).
		Order(dbent.Asc(paymentorder.FieldUpdatedAt)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("query stale fulfillments: %w", err)
	}

	recovered := 0
	for _, order := range orders {
		if err := s.executeFulfillment(ctx, order.ID); err != nil {
			if infraerrors.Reason(err) != "FULFILLMENT_IN_PROGRESS" {
				slog.Error("recover stale payment fulfillment failed", "orderID", order.ID, "status", order.Status, "error", err)
			}
			continue
		}
		current, err := s.entClient.PaymentOrder.Get(ctx, order.ID)
		if err == nil && current.Status == OrderStatusCompleted {
			recovered++
		}
	}
	return recovered, nil
}

func (s *PaymentService) RetryFulfillment(ctx context.Context, oid int64) error {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if !paymentOrderHasPaidEvidence(o) {
		return paymentNotConfirmedError()
	}
	if psIsRefundStatus(o.Status) {
		return infraerrors.BadRequest("INVALID_STATUS", "refund-related order cannot retry")
	}
	if o.Status == OrderStatusRecharging && !isFulfillmentLeaseStale(o.UpdatedAt, time.Now()) {
		return infraerrors.Conflict("CONFLICT", "order is being processed")
	}
	if o.Status == OrderStatusCompleted {
		return infraerrors.BadRequest("INVALID_STATUS", "order already completed")
	}
	if o.Status != OrderStatusFailed && o.Status != OrderStatusPaid && o.Status != OrderStatusRecharging {
		return infraerrors.BadRequest("INVALID_STATUS", "only paid, failed, and stale processing orders can retry")
	}
	if err := s.executeFulfillment(ctx, oid); err != nil {
		return err
	}
	s.writeAuditLog(ctx, oid, "RECHARGE_RETRY", "admin", map[string]any{
		"detail":          "admin manual retry",
		"previous_status": o.Status,
	})
	return nil
}
