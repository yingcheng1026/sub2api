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
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionwalletledger"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	walletRefundDebitAppliedAction     = "WALLET_REFUND_DEBIT_APPLIED"
	walletRefundDebitCompensatedAction = "WALLET_REFUND_DEBIT_COMPENSATED"
	walletRefundGatewayStartedAction   = "WALLET_REFUND_GATEWAY_STARTED"
	walletRefundGatewayAcceptedAction  = "WALLET_REFUND_GATEWAY_ACCEPTED"

	refundLeaseTimeout = 5 * time.Minute
)

type walletFulfillmentEvidence struct {
	AuditID        int64
	SubscriptionID int64
	CreditedAmount float64
	PlanID         int64
	PlanType       string
	FulfilledAt    time.Time
}

type walletFulfillmentAuditDetail struct {
	SubscriptionID int64   `json:"subscriptionID"`
	CreditedAmount float64 `json:"creditedAmount"`
	PlanID         int64   `json:"planID"`
	PlanType       string  `json:"planType"`
}

type walletRefundDebitAuditDetail struct {
	SubscriptionID     int64   `json:"subscriptionID"`
	FulfillmentAuditID int64   `json:"fulfillmentAuditID"`
	CreditDeducted     float64 `json:"creditDeducted"`
	RefundAmount       float64 `json:"refundAmount"`
}

func isWalletCreditsPaymentOrder(order *dbent.PaymentOrder) bool {
	return order != nil &&
		order.OrderType == payment.OrderTypeSubscription &&
		order.SubscriptionGroupID == nil &&
		order.PlanID != nil
}

func isRefundLeaseStale(updatedAt, now time.Time) bool {
	return !updatedAt.After(now.Add(-refundLeaseTimeout))
}

func refundGatewayIdempotencyKey(orderID int64) string {
	return "sub2-refund-" + strconv.FormatInt(orderID, 10)
}

func (s *PaymentService) prepareWalletCreditDeduction(ctx context.Context, order *dbent.PaymentOrder, plan *RefundPlan, force bool) (*RefundResult, error) {
	evidence, err := s.loadWalletFulfillmentEvidence(ctx, order)
	if err != nil {
		return nil, err
	}
	creditToReverse, err := walletCreditForRefund(evidence.CreditedAmount, plan.RefundAmount, order.Amount)
	if err != nil {
		return nil, err
	}
	wallet, err := s.entClient.UserSubscription.Get(mixins.SkipSoftDelete(ctx), evidence.SubscriptionID)
	if err != nil {
		return nil, infraerrors.Conflict("WALLET_REFUND_EVIDENCE_INVALID", "fulfilled wallet no longer exists; manual reconciliation is required")
	}
	if wallet.UserID != order.UserID || wallet.WalletBalanceUsd == nil || wallet.WalletInitialUsd == nil {
		return nil, infraerrors.Conflict("WALLET_REFUND_EVIDENCE_INVALID", "fulfillment evidence does not identify this user's wallet")
	}
	if *wallet.WalletInitialUsd+amountToleranceCNY < creditToReverse {
		return nil, infraerrors.Conflict("WALLET_REFUND_INITIAL_MISMATCH", "wallet cumulative credit is lower than this order's immutable refund delta")
	}
	usedAfterFulfillment, err := s.walletUsageAfterFulfillment(ctx, s.entClient, evidence)
	if err != nil {
		return nil, fmt.Errorf("check wallet usage after fulfillment: %w", err)
	}

	plan.DeductionType = payment.DeductionTypeWallet
	plan.SubscriptionID = evidence.SubscriptionID
	plan.WalletCreditToDeduct = creditToReverse
	plan.WalletFulfillmentAuditID = evidence.AuditID
	plan.WalletFulfilledAt = evidence.FulfilledAt
	if !force && (usedAfterFulfillment || *wallet.WalletBalanceUsd+amountToleranceCNY < creditToReverse) {
		return &RefundResult{
			Success:      false,
			Warning:      "wallet credits may have been consumed after this purchase; use force to create an explicit wallet debt",
			RequireForce: true,
		}, nil
	}
	return nil, nil
}

func (s *PaymentService) loadWalletFulfillmentEvidence(ctx context.Context, order *dbent.PaymentOrder) (*walletFulfillmentEvidence, error) {
	if s == nil || s.entClient == nil || !isWalletCreditsPaymentOrder(order) {
		return nil, infraerrors.BadRequest("INVALID_ORDER_TYPE", "order is not a credits wallet purchase")
	}
	return loadWalletFulfillmentEvidenceFromClient(ctx, s.entClient, order)
}

func loadWalletFulfillmentEvidenceFromClient(ctx context.Context, client *dbent.Client, order *dbent.PaymentOrder) (*walletFulfillmentEvidence, error) {
	if client == nil || !isWalletCreditsPaymentOrder(order) {
		return nil, infraerrors.BadRequest("INVALID_ORDER_TYPE", "order is not a credits wallet purchase")
	}
	credits, err := client.SubscriptionWalletLedger.Query().
		Where(
			subscriptionwalletledger.PaymentOrderIDEQ(order.ID),
			subscriptionwalletledger.ReasonIn(WalletLedgerReasonActivation, WalletLedgerReasonTopup),
			subscriptionwalletledger.DeltaUsdGT(0),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query wallet payment source: %w", err)
	}
	if len(credits) != 1 {
		return nil, infraerrors.Conflict("WALLET_REFUND_SOURCE_UNPROVEN", "payment order has no unique immutable wallet credit source; manual reconciliation is required")
	}
	logs, err := client.PaymentAuditLog.Query().
		Where(
			paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)),
			paymentauditlog.ActionEQ("SUBSCRIPTION_SUCCESS"),
		).
		Order(paymentauditlog.ByCreatedAt(), paymentauditlog.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query wallet fulfillment evidence: %w", err)
	}
	if len(logs) != 1 {
		return nil, infraerrors.Conflict("WALLET_REFUND_EVIDENCE_AMBIGUOUS", "exactly one wallet fulfillment audit is required; manual reconciliation is required")
	}
	var detail walletFulfillmentAuditDetail
	if err := json.Unmarshal([]byte(logs[0].Detail), &detail); err != nil {
		return nil, infraerrors.Conflict("WALLET_REFUND_EVIDENCE_INVALID", "wallet fulfillment audit is malformed; manual reconciliation is required")
	}
	if detail.SubscriptionID <= 0 || !isFinitePositive(detail.CreditedAmount) {
		return nil, infraerrors.Conflict("WALLET_REFUND_EVIDENCE_INVALID", "wallet fulfillment audit is missing the applied credit delta")
	}
	if detail.SubscriptionID != credits[0].SubscriptionID || math.Abs(detail.CreditedAmount-credits[0].DeltaUsd) > 0.0000000001 {
		return nil, infraerrors.Conflict("WALLET_REFUND_EVIDENCE_INVALID", "wallet source ledger and fulfillment audit disagree")
	}
	if detail.PlanID > 0 && order.PlanID != nil && detail.PlanID != *order.PlanID {
		return nil, infraerrors.Conflict("WALLET_REFUND_EVIDENCE_INVALID", "wallet fulfillment plan snapshot does not match the payment order")
	}
	if detail.PlanType != "" && detail.PlanType != PlanTypeCredits {
		return nil, infraerrors.Conflict("WALLET_REFUND_EVIDENCE_INVALID", "wallet fulfillment snapshot is not a credits purchase")
	}
	return &walletFulfillmentEvidence{
		AuditID:        logs[0].ID,
		SubscriptionID: credits[0].SubscriptionID,
		CreditedAmount: credits[0].DeltaUsd,
		PlanID:         detail.PlanID,
		PlanType:       detail.PlanType,
		FulfilledAt:    credits[0].CreatedAt,
	}, nil
}

func walletCreditForRefund(creditedAmount, refundAmount, orderAmount float64) (float64, error) {
	if !isFinitePositive(creditedAmount) || !isFinitePositive(refundAmount) || !isFinitePositive(orderAmount) {
		return 0, infraerrors.BadRequest("INVALID_AMOUNT", "wallet refund amounts must be finite and positive")
	}
	if refundAmount-orderAmount > amountToleranceCNY {
		return 0, infraerrors.BadRequest("REFUND_AMOUNT_EXCEEDED", "refund amount exceeds wallet purchase")
	}
	if math.Abs(refundAmount-orderAmount) <= amountToleranceCNY {
		return creditedAmount, nil
	}
	credit := math.Round((creditedAmount*refundAmount/orderAmount)*1e10) / 1e10
	if !isFinitePositive(credit) || credit-creditedAmount > 0.0000000001 {
		return 0, infraerrors.BadRequest("INVALID_AMOUNT", "wallet refund credit delta is invalid")
	}
	return credit, nil
}

func isFinitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func (s *PaymentService) walletUsageAfterFulfillment(ctx context.Context, client *dbent.Client, evidence *walletFulfillmentEvidence) (bool, error) {
	if client == nil || evidence == nil {
		return false, fmt.Errorf("wallet fulfillment evidence is unavailable")
	}
	return client.SubscriptionWalletLedger.Query().
		Where(
			subscriptionwalletledger.SubscriptionIDEQ(evidence.SubscriptionID),
			subscriptionwalletledger.ReasonEQ(WalletLedgerReasonUsage),
			subscriptionwalletledger.DeltaUsdLT(0),
			subscriptionwalletledger.CreatedAtGT(evidence.FulfilledAt),
		).
		Exist(ctx)
}

func (s *PaymentService) executeWalletRefund(ctx context.Context, plan *RefundPlan) (*RefundResult, error) {
	if err := s.claimWalletRefund(ctx, plan); err != nil {
		return nil, err
	}
	disposition, attempted, _, gatewayErr := s.executeRefundGatewayCall(
		ctx,
		plan,
		walletRefundGatewayStartedAction,
		walletRefundGatewayAcceptedAction,
		plan.WalletRefundRecovered,
	)
	if !attempted || disposition == refundGatewayRejected {
		cause := gatewayErr
		if cause == nil {
			cause = fmt.Errorf("provider rejected wallet refund")
		}
		if compensationErr := s.compensateWalletRefundBeforeGateway(ctx, plan, cause); compensationErr != nil {
			return nil, fmt.Errorf("wallet refund failed definitively: %v; compensation failed: %w", cause, compensationErr)
		}
		if disposition == refundGatewayRejected {
			return &RefundResult{Success: false, Warning: "provider rejected the refund; wallet debit was compensated and manual review is required"}, nil
		}
		return nil, infraerrors.InternalServer("REFUND_PRE_GATEWAY_FAILED", psErrMsg(cause))
	}
	switch disposition {
	case refundGatewaySucceeded:
		return s.markWalletRefundOK(ctx, plan)
	case refundGatewayPending:
		return &RefundResult{
			Success:              false,
			Warning:              "refund was accepted by the provider and remains pending; retry after the lease expires to reconcile status",
			WalletCreditDeducted: plan.WalletCreditToDeduct,
		}, nil
	default:
		return nil, infraerrors.InternalServer("REFUND_GATEWAY_AMBIGUOUS", "wallet refund gateway result is ambiguous; debit remains fenced for idempotent recovery")
	}
}

func (s *PaymentService) claimWalletRefund(ctx context.Context, plan *RefundPlan) error {
	if plan == nil || plan.Order == nil || plan.DeductionType != payment.DeductionTypeWallet {
		return infraerrors.BadRequest("INVALID_REFUND_PLAN", "wallet refund plan is invalid")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin wallet refund transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()
	current, err := lockPaymentOrderForRefund(txCtx, client, plan.OrderID)
	if err != nil {
		return err
	}

	activeDebit, err := walletRefundDebitIsActive(txCtx, client, plan.OrderID)
	if err != nil {
		return err
	}
	compensated, err := paymentAuditActionExists(txCtx, client, plan.OrderID, walletRefundDebitCompensatedAction)
	if err != nil {
		return fmt.Errorf("check wallet refund compensation state: %w", err)
	}
	if compensated {
		return infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "wallet refund debit was compensated; manual reconciliation is required")
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	switch current.Status {
	case OrderStatusCompleted, OrderStatusRefundRequested:
	case OrderStatusRefunding:
		if !isRefundLeaseStale(current.UpdatedAt, now) {
			return infraerrors.Conflict("REFUND_IN_PROGRESS", "refund is being processed")
		}
		if !activeDebit {
			return infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "legacy REFUNDING order has no idempotent wallet debit marker; manual reconciliation is required")
		}
	default:
		return infraerrors.BadRequest("INVALID_STATUS", "order status does not allow wallet refund")
	}

	if activeDebit {
		plan.WalletRefundRecovered = true
	} else if err := s.applyWalletRefundDebit(txCtx, client, current, plan); err != nil {
		return err
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
		return fmt.Errorf("claim wallet refund lease: %w", err)
	}
	if updated != 1 {
		return infraerrors.Conflict("REFUND_IN_PROGRESS", "refund state changed concurrently")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit wallet refund claim: %w", err)
	}
	plan.RefundLeaseUpdatedAt = now
	return nil
}

func lockPaymentOrderForRefund(ctx context.Context, client *dbent.Client, orderID int64) (*dbent.PaymentOrder, error) {
	query := client.PaymentOrder.Query().Where(paymentorder.IDEQ(orderID))
	if client.Driver().Dialect() == dialect.Postgres {
		query = query.ForUpdate()
	}
	order, err := query.Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if err != nil {
		return nil, fmt.Errorf("lock payment order for refund: %w", err)
	}
	return order, nil
}

func walletRefundDebitIsActive(ctx context.Context, client *dbent.Client, orderID int64) (bool, error) {
	rows, err := client.SubscriptionWalletLedger.Query().
		Where(
			subscriptionwalletledger.PaymentOrderIDEQ(orderID),
			subscriptionwalletledger.ReasonEQ(WalletLedgerReasonRefund),
		).
		All(ctx)
	if err != nil {
		return false, fmt.Errorf("query wallet refund source state: %w", err)
	}
	negative, positive, total := 0, 0, 0.0
	for _, row := range rows {
		total += row.DeltaUsd
		if row.DeltaUsd < 0 {
			negative++
		} else if row.DeltaUsd > 0 {
			positive++
		}
	}
	if negative > 1 || positive > 1 || positive > negative || total > 0.0000000001 {
		return false, infraerrors.Conflict("WALLET_REFUND_STATE_INVALID", "wallet refund source ledger is inconsistent; manual reconciliation is required")
	}
	return total < -0.0000000001, nil
}

func (s *PaymentService) applyWalletRefundDebit(ctx context.Context, client *dbent.Client, order *dbent.PaymentOrder, plan *RefundPlan) error {
	evidence, err := s.loadWalletFulfillmentEvidenceWithClient(ctx, client, order)
	if err != nil {
		return err
	}
	if evidence.AuditID != plan.WalletFulfillmentAuditID || evidence.SubscriptionID != plan.SubscriptionID {
		return infraerrors.Conflict("WALLET_REFUND_EVIDENCE_CHANGED", "wallet fulfillment evidence changed after refund preparation")
	}
	expectedCredit, err := walletCreditForRefund(evidence.CreditedAmount, plan.RefundAmount, order.Amount)
	if err != nil {
		return err
	}
	if math.Abs(expectedCredit-plan.WalletCreditToDeduct) > 0.0000000001 {
		return infraerrors.Conflict("WALLET_REFUND_EVIDENCE_CHANGED", "wallet refund credit delta changed after preparation")
	}

	userID, initial, balance, err := lockWalletForRefund(ctx, client, plan.SubscriptionID)
	if err != nil {
		return err
	}
	if userID != order.UserID {
		return infraerrors.Conflict("WALLET_REFUND_EVIDENCE_INVALID", "wallet owner does not match payment order")
	}
	if initial+amountToleranceCNY < plan.WalletCreditToDeduct {
		return infraerrors.Conflict("WALLET_REFUND_INITIAL_MISMATCH", "wallet cumulative credit is lower than this order's immutable refund delta")
	}
	usedAfterFulfillment, err := s.walletUsageAfterFulfillment(ctx, client, evidence)
	if err != nil {
		return fmt.Errorf("check wallet usage after fulfillment: %w", err)
	}
	if !plan.Force && (usedAfterFulfillment || balance+amountToleranceCNY < plan.WalletCreditToDeduct) {
		return infraerrors.Conflict("WALLET_REFUND_FORCE_REQUIRED", "wallet credits may have been consumed; force is required to create wallet debt")
	}

	newInitial := initial - plan.WalletCreditToDeduct
	if newInitial < 0 && math.Abs(newInitial) <= amountToleranceCNY {
		newInitial = 0
	}
	newBalance := balance - plan.WalletCreditToDeduct
	if !plan.Force && newBalance < -amountToleranceCNY {
		return infraerrors.Conflict("WALLET_REFUND_FORCE_REQUIRED", "wallet balance is lower than the credit being refunded")
	}
	if _, err := client.UserSubscription.UpdateOneID(plan.SubscriptionID).
		SetWalletInitialUsd(newInitial).
		SetWalletBalanceUsd(newBalance).
		Save(ctx); err != nil {
		return fmt.Errorf("reverse wallet credit: %w", err)
	}
	if _, err := client.SubscriptionWalletLedger.Create().
		SetSubscriptionID(plan.SubscriptionID).
		SetPaymentOrderID(plan.OrderID).
		SetDeltaUsd(-plan.WalletCreditToDeduct).
		SetBalanceAfter(newBalance).
		SetReason(WalletLedgerReasonRefund).
		SetNotes(fmt.Sprintf("payment order %d credits refund debit", plan.OrderID)).
		Save(ctx); err != nil {
		return fmt.Errorf("record wallet refund ledger: %w", err)
	}
	return createPaymentAuditLog(ctx, client, plan.OrderID, walletRefundDebitAppliedAction, "admin", map[string]any{
		"subscriptionID":     plan.SubscriptionID,
		"fulfillmentAuditID": plan.WalletFulfillmentAuditID,
		"creditedAmount":     evidence.CreditedAmount,
		"creditDeducted":     plan.WalletCreditToDeduct,
		"refundAmount":       plan.RefundAmount,
		"balanceBefore":      balance,
		"balanceAfter":       newBalance,
		"initialBefore":      initial,
		"initialAfter":       newInitial,
		"force":              plan.Force,
	})
}

func (s *PaymentService) loadWalletFulfillmentEvidenceWithClient(ctx context.Context, client *dbent.Client, order *dbent.PaymentOrder) (*walletFulfillmentEvidence, error) {
	return loadWalletFulfillmentEvidenceFromClient(ctx, client, order)
}

func (s *PaymentService) restoreWalletRefundPlanFromAudit(ctx context.Context, plan *RefundPlan) error {
	if plan == nil || plan.Order == nil {
		return infraerrors.BadRequest("INVALID_REFUND_PLAN", "wallet refund plan is invalid")
	}
	compensated, err := paymentAuditActionExists(ctx, s.entClient, plan.OrderID, walletRefundDebitCompensatedAction)
	if err != nil {
		return fmt.Errorf("check wallet refund compensation audit: %w", err)
	}
	if compensated {
		return infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "wallet refund debit was compensated; manual reconciliation is required")
	}
	active, err := walletRefundDebitIsActive(ctx, s.entClient, plan.OrderID)
	if err != nil {
		return err
	}
	if !active {
		return infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "REFUNDING wallet order has no active immutable wallet debit")
	}
	log, err := s.entClient.PaymentAuditLog.Query().
		Where(
			paymentauditlog.OrderIDEQ(strconv.FormatInt(plan.OrderID, 10)),
			paymentauditlog.ActionEQ(walletRefundDebitAppliedAction),
		).
		Only(ctx)
	if dbent.IsNotFound(err) {
		return infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "REFUNDING wallet order has no immutable wallet debit marker")
	}
	if err != nil {
		return fmt.Errorf("load wallet refund debit marker: %w", err)
	}
	var detail walletRefundDebitAuditDetail
	if err := json.Unmarshal([]byte(log.Detail), &detail); err != nil {
		return fmt.Errorf("decode wallet refund debit marker: %w", err)
	}
	if detail.SubscriptionID <= 0 ||
		detail.FulfillmentAuditID <= 0 ||
		!isFinitePositive(detail.CreditDeducted) ||
		math.Abs(detail.RefundAmount-plan.RefundAmount) > amountToleranceCNY {
		return infraerrors.Conflict("REFUND_RECOVERY_MANUAL_REQUIRED", "wallet refund debit marker does not match the claimed refund")
	}
	plan.DeductionType = payment.DeductionTypeWallet
	plan.SubscriptionID = detail.SubscriptionID
	plan.WalletFulfillmentAuditID = detail.FulfillmentAuditID
	plan.WalletCreditToDeduct = detail.CreditDeducted
	plan.WalletRefundRecovered = true
	return nil
}

func lockWalletForRefund(ctx context.Context, client *dbent.Client, subscriptionID int64) (int64, float64, float64, error) {
	query := client.UserSubscription.Query().Where(usersubscription.IDEQ(subscriptionID))
	if client.Driver().Dialect() == dialect.Postgres {
		query = query.ForUpdate()
	}
	wallet, err := query.Only(mixins.SkipSoftDelete(ctx))
	if dbent.IsNotFound(err) {
		return 0, 0, 0, ErrWalletNotFound
	}
	if err != nil {
		return 0, 0, 0, fmt.Errorf("lock wallet for refund: %w", err)
	}
	if wallet.WalletInitialUsd == nil || wallet.WalletBalanceUsd == nil {
		return 0, 0, 0, ErrWalletNotFound
	}
	return wallet.UserID, *wallet.WalletInitialUsd, *wallet.WalletBalanceUsd, nil
}

func refundProviderSupportsStableIdempotency(providerKey string) bool {
	switch payment.GetBasePaymentType(strings.TrimSpace(providerKey)) {
	case payment.TypeAlipay, payment.TypeWxpay, payment.TypeStripe:
		return true
	default:
		return false
	}
}

func (s *PaymentService) markWalletRefundOK(ctx context.Context, plan *RefundPlan) (*RefundResult, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin wallet refund completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()
	current, err := lockPaymentOrderForRefund(txCtx, client, plan.OrderID)
	if err != nil {
		return nil, err
	}
	if current.Status != OrderStatusRefunding || !current.UpdatedAt.Equal(plan.RefundLeaseUpdatedAt) {
		return nil, infraerrors.Conflict("REFUND_LEASE_LOST", "wallet refund lease was reclaimed by another worker")
	}
	active, err := walletRefundDebitIsActive(txCtx, client, plan.OrderID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, infraerrors.Conflict("WALLET_REFUND_STATE_INVALID", "wallet refund debit is not active")
	}
	finalStatus := OrderStatusRefunded
	if plan.RefundAmount < plan.Order.Amount-amountToleranceCNY {
		finalStatus = OrderStatusPartiallyRefunded
	}
	now := time.Now()
	updated, err := client.PaymentOrder.Update().
		Where(paymentorder.IDEQ(plan.OrderID), paymentorder.StatusEQ(OrderStatusRefunding), paymentorder.UpdatedAtEQ(plan.RefundLeaseUpdatedAt)).
		SetStatus(finalStatus).
		SetRefundAmount(plan.RefundAmount).
		SetRefundReason(plan.Reason).
		SetRefundAt(now).
		SetForceRefund(plan.Force).
		Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("mark wallet refund: %w", err)
	}
	if updated != 1 {
		return nil, infraerrors.Conflict("REFUND_LEASE_LOST", "wallet refund lease was reclaimed by another worker")
	}
	if err := createPaymentAuditLog(txCtx, client, plan.OrderID, "REFUND_SUCCESS", "admin", map[string]any{
		"refundAmount":         plan.RefundAmount,
		"reason":               plan.Reason,
		"force":                plan.Force,
		"walletSubscriptionID": plan.SubscriptionID,
		"walletCreditDeducted": plan.WalletCreditToDeduct,
		"fulfillmentAuditID":   plan.WalletFulfillmentAuditID,
		"idempotencyKey":       refundGatewayIdempotencyKey(plan.OrderID),
	}); err != nil {
		return nil, fmt.Errorf("record wallet refund success: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit wallet refund completion: %w", err)
	}
	return &RefundResult{Success: true, WalletCreditDeducted: plan.WalletCreditToDeduct}, nil
}

func (s *PaymentService) compensateWalletRefundBeforeGateway(ctx context.Context, plan *RefundPlan, cause error) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin wallet refund compensation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()
	current, err := lockPaymentOrderForRefund(txCtx, client, plan.OrderID)
	if err != nil {
		return err
	}
	if current.Status != OrderStatusRefunding || !current.UpdatedAt.Equal(plan.RefundLeaseUpdatedAt) {
		return infraerrors.Conflict("REFUND_LEASE_LOST", "cannot compensate a wallet refund owned by another worker")
	}
	active, err := walletRefundDebitIsActive(txCtx, client, plan.OrderID)
	if err != nil {
		return err
	}
	if !active {
		return nil
	}
	_, initial, balance, err := lockWalletForRefund(txCtx, client, plan.SubscriptionID)
	if err != nil {
		return err
	}
	newInitial := initial + plan.WalletCreditToDeduct
	newBalance := balance + plan.WalletCreditToDeduct
	if _, err := client.UserSubscription.UpdateOneID(plan.SubscriptionID).
		SetWalletInitialUsd(newInitial).
		SetWalletBalanceUsd(newBalance).
		Save(txCtx); err != nil {
		return fmt.Errorf("compensate wallet refund credit: %w", err)
	}
	if _, err := client.SubscriptionWalletLedger.Create().
		SetSubscriptionID(plan.SubscriptionID).
		SetPaymentOrderID(plan.OrderID).
		SetDeltaUsd(plan.WalletCreditToDeduct).
		SetBalanceAfter(newBalance).
		SetReason(WalletLedgerReasonRefund).
		SetNotes(fmt.Sprintf("payment order %d credits refund pre-gateway compensation", plan.OrderID)).
		Save(txCtx); err != nil {
		return fmt.Errorf("record wallet refund compensation ledger: %w", err)
	}
	if err := createPaymentAuditLog(txCtx, client, plan.OrderID, walletRefundDebitCompensatedAction, "admin", map[string]any{
		"subscriptionID": plan.SubscriptionID,
		"creditRestored": plan.WalletCreditToDeduct,
		"reason":         cause.Error(),
	}); err != nil {
		return err
	}
	restoreStatus := plan.Order.Status
	if restoreStatus == OrderStatusRefunding {
		restoreStatus = OrderStatusRefundFailed
	}
	updated, err := client.PaymentOrder.Update().
		Where(paymentorder.IDEQ(plan.OrderID), paymentorder.StatusEQ(OrderStatusRefunding), paymentorder.UpdatedAtEQ(plan.RefundLeaseUpdatedAt)).
		SetStatus(restoreStatus).
		SetRefundAmount(0).
		ClearRefundReason().
		SetForceRefund(false).
		Save(txCtx)
	if err != nil {
		return fmt.Errorf("restore wallet refund order: %w", err)
	}
	if updated != 1 {
		return infraerrors.Conflict("REFUND_LEASE_LOST", "cannot restore wallet refund order after lease loss")
	}
	return tx.Commit()
}

func createPaymentAuditLog(ctx context.Context, client *dbent.Client, orderID int64, action, operator string, detail map[string]any) error {
	encoded, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode payment audit %s: %w", action, err)
	}
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(orderID, 10)).
		SetAction(action).
		SetDetail(string(encoded)).
		SetOperator(operator).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("record payment audit %s: %w", action, err)
	}
	return nil
}
