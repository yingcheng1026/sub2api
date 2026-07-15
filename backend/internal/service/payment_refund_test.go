//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestClassifyRefundGatewayResultSeparatesDefiniteRejectionFromAmbiguity(t *testing.T) {
	tests := []struct {
		name     string
		response *payment.RefundResponse
		err      error
		want     refundGatewayDisposition
	}{
		{name: "success", response: &payment.RefundResponse{Status: payment.ProviderStatusSuccess}, want: refundGatewaySucceeded},
		{name: "pending", response: &payment.RefundResponse{Status: payment.ProviderStatusPending}, want: refundGatewayPending},
		{name: "explicit failed", response: &payment.RefundResponse{Status: payment.ProviderStatusFailed}, want: refundGatewayRejected},
		{name: "transport error", err: errors.New("connection reset after send"), want: refundGatewayAmbiguous},
		{name: "nil response", want: refundGatewayAmbiguous},
		{name: "unknown response", response: &payment.RefundResponse{Status: "mystery"}, want: refundGatewayAmbiguous},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, classifyRefundGatewayResult(tt.response, tt.err))
		})
	}
}

func TestSubscriptionDaysForPartialRefundAreProportional(t *testing.T) {
	require.Equal(t, 30, subscriptionDaysForRefund(30, 30, 30))
	require.Equal(t, 15, subscriptionDaysForRefund(30, 15, 30))
	require.Equal(t, 1, subscriptionDaysForRefund(30, 0.01, 30), "a positive partial refund must reverse at least one whole day")
}

func TestPrepareRefundFailsClosedBeforeDeductionWithoutSafeGatewayReplay(t *testing.T) {
	tests := []struct {
		name        string
		providerKey string
		tradeNo     string
	}{
		{name: "missing provider trade evidence", providerKey: payment.TypeAlipay, tradeNo: ""},
		{name: "EasyPay lacks refund idempotency", providerKey: payment.TypeEasyPay, tradeNo: "provider-trade"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			user, err := client.User.Create().
				SetEmail(fmt.Sprintf("refund-preflight-%s@example.com", tt.providerKey)).
				SetPasswordHash("hash").
				SetUsername("refund-preflight-" + tt.providerKey).
				Save(ctx)
			require.NoError(t, err)
			inst, err := client.PaymentProviderInstance.Create().
				SetProviderKey(tt.providerKey).
				SetName("refund-preflight-" + tt.providerKey).
				SetConfig("{}").
				SetSupportedTypes(tt.providerKey).
				SetEnabled(true).
				SetRefundEnabled(true).
				Save(ctx)
			require.NoError(t, err)
			builder := client.PaymentOrder.Create().
				SetUserID(user.ID).
				SetUserEmail(user.Email).
				SetUserName(user.Username).
				SetAmount(30).
				SetPayAmount(30).
				SetFeeRate(0).
				SetRechargeCode("REFUND-PREFLIGHT-" + tt.providerKey).
				SetOutTradeNo("sub2_refund_preflight_" + tt.providerKey).
				SetPaymentType(tt.providerKey).
				SetOrderType(payment.OrderTypeBalance).
				SetStatus(OrderStatusCompleted).
				SetExpiresAt(time.Now().Add(time.Hour)).
				SetPaidAt(time.Now()).
				SetCompletedAt(time.Now()).
				SetClientIP("127.0.0.1").
				SetSrcHost("api.example.com").
				SetProviderInstanceID(strconv.FormatInt(inst.ID, 10)).
				SetProviderKey(tt.providerKey)
			builder.SetPaymentTradeNo(tt.tradeNo)
			order, err := builder.Save(ctx)
			require.NoError(t, err)

			svc := &PaymentService{entClient: client}
			plan, result, err := svc.PrepareRefund(ctx, order.ID, 0, "", false, false)
			require.Nil(t, plan)
			require.Nil(t, result)
			require.Error(t, err)
			require.Equal(t, "REFUND_MANUAL_REQUIRED", infraerrors.Reason(err))
		})
	}
}

type refundAuditFailClosedUserRepo struct {
	UserRepository
	deductCalls int
}

func (r *refundAuditFailClosedUserRepo) DeductBalance(context.Context, int64, float64) error {
	r.deductCalls++
	return nil
}

type refundBalanceUserRepo struct {
	UserRepository
	user *User
}

func (r *refundBalanceUserRepo) GetByID(context.Context, int64) (*User, error) {
	return r.user, nil
}

func TestPrepDeductBalanceShortfallRequiresExplicitForce(t *testing.T) {
	ctx := context.Background()
	order := &dbent.PaymentOrder{
		UserID:    42,
		OrderType: payment.OrderTypeBalance,
	}
	svc := &PaymentService{
		userRepo: &refundBalanceUserRepo{user: &User{ID: 42, Balance: 10}},
	}

	plan := &RefundPlan{RefundAmount: 100}
	result, err := svc.prepDeduct(ctx, order, plan, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.RequireForce)
	require.Zero(t, plan.BalanceToDeduct, "a non-forced shortfall must not produce an executable partial deduction plan")

	forcedPlan := &RefundPlan{RefundAmount: 100}
	result, err = svc.prepDeduct(ctx, order, forcedPlan, true)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, 10.0, forcedPlan.BalanceToDeduct)
}

func TestExecuteRefundFailsClosedWhenRollbackAuditLookupFails(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("refund-audit-fail-closed@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-audit-fail-closed").
		Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("REFUND-AUDIT-FAIL-CLOSED").
		SetOutTradeNo("sub2_refund_audit_fail_closed").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-refund-audit-fail-closed").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	_, err = client.ExecContext(ctx, "DROP TABLE payment_audit_logs")
	require.NoError(t, err)

	userRepo := &refundAuditFailClosedUserRepo{}
	svc := &PaymentService{entClient: client, userRepo: userRepo}
	result, err := svc.ExecuteRefund(ctx, &RefundPlan{
		OrderID:         order.ID,
		Order:           order,
		RefundAmount:    order.Amount,
		GatewayAmount:   order.Amount,
		DeductionType:   payment.DeductionTypeBalance,
		BalanceToDeduct: order.Amount,
	})
	require.Nil(t, result)
	require.ErrorContains(t, err, "check refund rollback audit")
	require.Zero(t, userRepo.deductCalls, "refund must not deduct when idempotency evidence is unavailable")
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
}

type refundTransactionalUserRepo struct {
	UserRepository
	client *dbent.Client
}

func (r *refundTransactionalUserRepo) DeductBalance(ctx context.Context, id int64, amount float64) error {
	client := r.client
	if tx := dbent.TxFromContext(ctx); tx != nil {
		client = tx.Client()
	}
	_, err := client.User.UpdateOneID(id).AddBalance(-amount).Save(ctx)
	return err
}

func newStandardRefundClaimFixture(t *testing.T, balance float64) (context.Context, *dbent.Client, *dbent.User, *dbent.PaymentOrder) {
	t.Helper()
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	fixedNow := time.Date(2026, time.July, 14, 3, 30, 0, 0, time.UTC)
	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("refund-claim-%g@example.com", balance)).
		SetPasswordHash("hash").
		SetUsername(fmt.Sprintf("refund-claim-%g", balance)).
		SetBalance(balance).
		Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode(fmt.Sprintf("REFUND-CLAIM-%g", balance)).
		SetOutTradeNo(fmt.Sprintf("sub2_refund_claim_%g", balance)).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo(fmt.Sprintf("trade-refund-claim-%g", balance)).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(fixedNow.Add(time.Hour)).
		SetPaidAt(fixedNow).
		SetCreatedAt(fixedNow).
		SetUpdatedAt(fixedNow).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	return ctx, client, user, order
}

func TestClaimStandardRefundRechecksNonForcedBalanceBeforeDeduction(t *testing.T) {
	ctx, client, user, order := newStandardRefundClaimFixture(t, 0)
	svc := &PaymentService{
		entClient: client,
		userRepo:  &refundTransactionalUserRepo{client: client},
	}
	plan := &RefundPlan{
		OrderID:         order.ID,
		Order:           order,
		RefundAmount:    order.Amount,
		GatewayAmount:   order.Amount,
		DeductionType:   payment.DeductionTypeBalance,
		BalanceToDeduct: order.Amount,
	}

	recovered, err := svc.claimStandardRefund(ctx, plan)
	require.False(t, recovered)
	require.Error(t, err)
	require.Equal(t, "REFUND_BALANCE_CHANGED_REQUIRE_FORCE", infraerrors.Reason(err))

	reloadedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Zero(t, reloadedUser.Balance, "failed non-forced claim must not overdraw the balance")
	reloadedOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloadedOrder.Status)
	auditCount, err := client.PaymentAuditLog.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, auditCount, "failed claim must not leave a deduction marker")
}

func TestClaimStandardRefundForcedDeductionUsesCurrentNonNegativeBalance(t *testing.T) {
	ctx, client, user, order := newStandardRefundClaimFixture(t, 25)
	svc := &PaymentService{
		entClient: client,
		userRepo:  &refundTransactionalUserRepo{client: client},
	}
	plan := &RefundPlan{
		OrderID:         order.ID,
		Order:           order,
		RefundAmount:    order.Amount,
		GatewayAmount:   order.Amount,
		Force:           true,
		DeductionType:   payment.DeductionTypeBalance,
		BalanceToDeduct: order.Amount,
	}

	recovered, err := svc.claimStandardRefund(ctx, plan)
	require.NoError(t, err)
	require.False(t, recovered)
	require.Equal(t, 25.0, plan.BalanceToDeduct)

	reloadedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Zero(t, reloadedUser.Balance, "forced refund may use the available balance but must not overdraw it")
	detail, found, err := loadStandardRefundDeductionDetail(ctx, client, order.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 25.0, detail.BalanceDeducted, "immutable audit evidence must record the actual deduction")
}

func TestValidateRefundRequestRejectsLegacyGuessedProviderInstance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-legacy@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-legacy-user").
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-refund-instance").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetAllowUserRefund(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("REFUND-LEGACY-ORDER").
		SetOutTradeNo("sub2_refund_legacy_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-legacy-refund").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient: client,
	}

	_, err = svc.validateRefundRequest(ctx, order.ID, user.ID)
	require.Error(t, err)
	require.Equal(t, "USER_REFUND_DISABLED", infraerrors.Reason(err))
}

func TestPrepareRefundRejectsLegacyGuessedProviderInstance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-legacy-admin@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-legacy-admin-user").
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-refund-admin-instance").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetAllowUserRefund(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(188).
		SetPayAmount(188).
		SetFeeRate(0).
		SetRechargeCode("REFUND-LEGACY-ADMIN-ORDER").
		SetOutTradeNo("sub2_refund_legacy_admin_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-legacy-admin-refund").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient: client,
	}

	plan, result, err := svc.PrepareRefund(ctx, order.ID, 0, "", false, false)
	require.Nil(t, plan)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "REFUND_DISABLED", infraerrors.Reason(err))
}

func TestGwRefundRejectsAlipayMerchantIdentitySnapshotMismatch(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-snapshot-mismatch@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-snapshot-mismatch-user").
		Save(ctx)
	require.NoError(t, err)

	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-refund-mismatch-instance").
		SetConfig(encryptWebhookProviderConfig(t, map[string]string{
			"appId":      "runtime-alipay-app",
			"privateKey": "runtime-private-key",
		})).
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	instID := strconv.FormatInt(inst.ID, 10)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("REFUND-SNAPSHOT-MISMATCH-ORDER").
		SetOutTradeNo("sub2_refund_snapshot_mismatch_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-refund-snapshot-mismatch").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instID).
		SetProviderKey(payment.TypeAlipay).
		SetProviderSnapshot(map[string]any{
			"schema_version":       2,
			"provider_instance_id": instID,
			"provider_key":         payment.TypeAlipay,
			"merchant_app_id":      "expected-alipay-app",
		}).
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient:    client,
		loadBalancer: newWebhookProviderTestLoadBalancer(client),
	}

	err = svc.gwRefund(ctx, &RefundPlan{
		OrderID:       order.ID,
		Order:         order,
		RefundAmount:  order.Amount,
		GatewayAmount: order.Amount,
		Reason:        "snapshot mismatch",
	})
	require.ErrorContains(t, err, "alipay app_id mismatch")
}
