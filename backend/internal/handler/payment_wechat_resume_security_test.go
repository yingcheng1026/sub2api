package handler

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestApplyWeChatPaymentResumeClaimsBindsAuthenticatedUser(t *testing.T) {
	req := CreateOrderRequest{PaymentType: payment.TypeWxpay}
	err := applyWeChatPaymentResumeClaims(&req, &service.WeChatPaymentResumeClaims{
		UserID:      17,
		JTI:         "signed-jti",
		OpenID:      "openid-17",
		PaymentType: payment.TypeWxpay,
		Amount:      "12.50",
		OrderType:   payment.OrderTypeBalance,
	}, 17)
	require.NoError(t, err)
	require.EqualValues(t, 17, req.ResumeTokenUserID)
	require.Equal(t, "signed-jti", req.ResumeTokenJTI)
	require.Equal(t, service.PaymentSourceWechatInAppResume, req.PaymentSource)
}

func TestApplyWeChatPaymentResumeClaimsRejectsCrossUserAndNonFiniteAmount(t *testing.T) {
	base := service.WeChatPaymentResumeClaims{
		UserID:      17,
		JTI:         "signed-jti",
		OpenID:      "openid-17",
		PaymentType: payment.TypeWxpay,
		OrderType:   payment.OrderTypeBalance,
	}

	req := CreateOrderRequest{PaymentType: payment.TypeWxpay}
	require.Error(t, applyWeChatPaymentResumeClaims(&req, &base, 18))

	base.Amount = "NaN"
	req = CreateOrderRequest{PaymentType: payment.TypeWxpay, Amount: math.NaN()}
	require.Error(t, applyWeChatPaymentResumeClaims(&req, &base, 17))
}

func TestExecuteWeChatResumeOrderCreateConsumesJTIOnce(t *testing.T) {
	repo := newUserMemoryIdempotencyRepoStub()
	coordinator := service.NewIdempotencyCoordinator(repo, service.DefaultIdempotencyConfig())
	req := service.CreateOrderRequest{
		UserID:            17,
		ResumeTokenUserID: 17,
		ResumeTokenJTI:    "0123456789abcdef0123456789abcdef",
		Amount:            12.5,
		PaymentType:       payment.TypeWxpay,
		OpenID:            "openid-17",
		PaymentSource:     service.PaymentSourceWechatInAppResume,
		OrderType:         payment.OrderTypeBalance,
	}

	calls := 0
	execute := func(context.Context, service.CreateOrderRequest) (*service.CreateOrderResponse, error) {
		calls++
		return &service.CreateOrderResponse{OrderID: 99, PaymentType: payment.TypeWxpay}, nil
	}

	first, err := executeWeChatResumeOrderCreate(context.Background(), coordinator, req, execute)
	require.NoError(t, err)
	require.EqualValues(t, 99, first.OrderID)

	second, err := executeWeChatResumeOrderCreate(context.Background(), coordinator, req, execute)
	require.NoError(t, err)
	require.EqualValues(t, 99, second.OrderID)
	require.Equal(t, 1, calls)

	record, err := repo.GetByScopeAndKeyHash(context.Background(), service.WeChatPaymentResumeIdempotencyScope, service.HashIdempotencyKey(req.ResumeTokenJTI))
	require.NoError(t, err)
	require.NotNil(t, record)
	require.False(t, record.Persistent)
	require.NotNil(t, record.ResponseBody)
	require.Contains(t, *record.ResponseBody, `"order_id":99`)
}

func TestExecuteWeChatResumeOrderCreateFailsClosedWithoutCoordinator(t *testing.T) {
	calls := 0
	_, err := executeWeChatResumeOrderCreate(context.Background(), nil, service.CreateOrderRequest{
		UserID:         17,
		ResumeTokenJTI: "0123456789abcdef0123456789abcdef",
	}, func(context.Context, service.CreateOrderRequest) (*service.CreateOrderResponse, error) {
		calls++
		return &service.CreateOrderResponse{OrderID: 99}, nil
	})
	require.Error(t, err)
	require.Equal(t, 0, calls)
}

func TestExecuteWeChatResumeOrderCreateFencesAmbiguousFailurePastTokenTTL(t *testing.T) {
	repo := newUserMemoryIdempotencyRepoStub()
	coordinator := service.NewIdempotencyCoordinator(repo, service.DefaultIdempotencyConfig())
	req := service.CreateOrderRequest{
		UserID:         17,
		ResumeTokenJTI: "fedcba9876543210fedcba9876543210",
	}
	calls := 0
	execute := func(context.Context, service.CreateOrderRequest) (*service.CreateOrderResponse, error) {
		calls++
		return nil, errors.New("ambiguous provider failure")
	}

	_, err := executeWeChatResumeOrderCreate(context.Background(), coordinator, req, execute)
	require.Error(t, err)
	record, err := repo.GetByScopeAndKeyHash(context.Background(), service.WeChatPaymentResumeIdempotencyScope, service.HashIdempotencyKey(req.ResumeTokenJTI))
	require.NoError(t, err)
	require.NotNil(t, record)
	require.False(t, record.Persistent)
	require.NotNil(t, record.LockedUntil)
	require.WithinDuration(t, time.Now().Add(20*time.Minute), *record.LockedUntil, 5*time.Second)

	_, err = executeWeChatResumeOrderCreate(context.Background(), coordinator, req, execute)
	require.Error(t, err)
	require.Equal(t, 1, calls)
}
