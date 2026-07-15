package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

type paymentStatusQuerySpyProvider struct {
	queryCalls  int
	cancelCalls int
	queryErr    error
	cancelErr   error
}

func (p *paymentStatusQuerySpyProvider) Name() string        { return "status-query-spy" }
func (p *paymentStatusQuerySpyProvider) ProviderKey() string { return payment.TypeAlipay }
func (p *paymentStatusQuerySpyProvider) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeAlipay}
}
func (p *paymentStatusQuerySpyProvider) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	panic("unexpected CreatePayment")
}
func (p *paymentStatusQuerySpyProvider) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	p.queryCalls++
	if p.queryErr != nil {
		return nil, p.queryErr
	}
	return &payment.QueryOrderResponse{Status: payment.ProviderStatusPending}, nil
}
func (p *paymentStatusQuerySpyProvider) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	panic("unexpected VerifyNotification")
}
func (p *paymentStatusQuerySpyProvider) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	panic("unexpected Refund")
}
func (p *paymentStatusQuerySpyProvider) CancelPayment(context.Context, string) error {
	p.cancelCalls++
	return p.cancelErr
}

func TestPaymentCancellationFailsClosedOnAmbiguousProviderState(t *testing.T) {
	tests := []struct {
		name          string
		provider      *paymentStatusQuerySpyProvider
		wantErrorCode string
	}{
		{
			name:          "query failure",
			provider:      &paymentStatusQuerySpyProvider{queryErr: errors.New("provider unavailable")},
			wantErrorCode: "PAYMENT_STATUS_UNAVAILABLE",
		},
		{
			name:          "provider cancel failure",
			provider:      &paymentStatusQuerySpyProvider{cancelErr: errors.New("cancel unavailable")},
			wantErrorCode: "PAYMENT_PROVIDER_CANCEL_FAILED",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			user, err := client.User.Create().
				SetEmail("cancel-ambiguous@example.com").
				SetPasswordHash("hash").
				SetUsername("cancel-ambiguous-user").
				Save(ctx)
			require.NoError(t, err)
			order, err := client.PaymentOrder.Create().
				SetUserID(user.ID).
				SetUserEmail(user.Email).
				SetUserName(user.Username).
				SetAmount(10).
				SetPayAmount(10).
				SetFeeRate(0).
				SetRechargeCode("CANCEL-AMBIGUOUS").
				SetOutTradeNo("abcdef0123456789abcdef0123456789").
				SetPaymentType(payment.TypeAlipay).
				SetPaymentTradeNo("").
				SetOrderType(payment.OrderTypeBalance).
				SetStatus(OrderStatusPending).
				SetExpiresAt(time.Now().Add(time.Hour)).
				SetClientIP("127.0.0.1").
				SetSrcHost("api.example.com").
				Save(ctx)
			require.NoError(t, err)

			registry := payment.NewRegistry()
			registry.Register(tt.provider)
			svc := &PaymentService{entClient: client, registry: registry, providersLoaded: true}

			_, err = svc.CancelOrder(ctx, order.ID, user.ID)
			require.Error(t, err)
			require.Equal(t, tt.wantErrorCode, infraerrorsReason(err))
			persisted, reloadErr := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, reloadErr)
			require.Equal(t, OrderStatusPending, persisted.Status, "ambiguous upstream state must not become a local cancellation")
		})
	}
}

func TestPaymentStatusQueriesNeverCancelPendingProviderOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("status-query@example.com").
		SetPasswordHash("hash").
		SetUsername("status-query-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("STATUS-QUERY").
		SetOutTradeNo("0123456789abcdef0123456789abcdef").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	resumeService := NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := resumeService.CreateToken(ResumeTokenClaims{
		OrderID:     order.ID,
		UserID:      user.ID,
		PaymentType: payment.TypeAlipay,
	})
	require.NoError(t, err)

	provider := &paymentStatusQuerySpyProvider{}
	registry := payment.NewRegistry()
	registry.Register(provider)
	svc := &PaymentService{
		entClient:       client,
		registry:        registry,
		resumeService:   resumeService,
		providersLoaded: true,
	}

	_, err = svc.GetPublicOrderByResumeToken(ctx, token)
	require.NoError(t, err)
	require.Equal(t, 1, provider.queryCalls, "signed public status lookup should reconcile a missed webhook")
	require.Zero(t, provider.cancelCalls, "public status polling must never close an in-flight payment")
	_, err = svc.GetPublicOrderByResumeToken(ctx, token)
	require.NoError(t, err)
	require.Equal(t, 1, provider.queryCalls, "per-order cooldown should suppress rapid repeated provider queries")

	_, err = svc.VerifyOrderByOutTradeNo(ctx, order.OutTradeNo, user.ID)
	require.NoError(t, err)
	require.Equal(t, 2, provider.queryCalls)
	require.Zero(t, provider.cancelCalls, "authenticated verification must never close an in-flight payment")

	_, err = svc.CancelOrder(ctx, order.ID, user.ID)
	require.NoError(t, err)
	require.Equal(t, 3, provider.queryCalls)
	require.Equal(t, 1, provider.cancelCalls, "only an explicit cancellation path may close the provider order")
}
