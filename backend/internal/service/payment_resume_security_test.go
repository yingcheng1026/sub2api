package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestWeChatPaymentResumeTokenBindsUserAndReplayJTI(t *testing.T) {
	svc := NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := svc.CreateWeChatPaymentResumeToken(WeChatPaymentResumeClaims{
		UserID:      17,
		OpenID:      "openid-17",
		PaymentType: payment.TypeWxpay,
	})
	require.NoError(t, err)

	claims, err := svc.ParseWeChatPaymentResumeToken(token)
	require.NoError(t, err)
	require.EqualValues(t, 17, claims.UserID)
	require.Len(t, claims.JTI, 32)
	require.Greater(t, claims.ExpiresAt, time.Now().Unix())
}

func TestWeChatPaymentResumeTokenRejectsLegacyUnboundClaims(t *testing.T) {
	svc := NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := svc.createSignedToken(WeChatPaymentResumeClaims{
		TokenType:   wechatPaymentResumeTokenType,
		OpenID:      "legacy-openid",
		PaymentType: payment.TypeWxpay,
		ExpiresAt:   time.Now().Add(time.Minute).Unix(),
	})
	require.NoError(t, err)

	_, err = svc.ParseWeChatPaymentResumeToken(token)
	require.Error(t, err)
	require.Equal(t, "INVALID_WECHAT_PAYMENT_RESUME_TOKEN", infraerrors.Reason(err))
}

func TestWeChatPaymentOAuthSubjectTokenBindsUser(t *testing.T) {
	svc := NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := svc.CreateWeChatPaymentOAuthSubjectToken(42)
	require.NoError(t, err)

	claims, err := svc.ParseWeChatPaymentOAuthSubjectToken(token)
	require.NoError(t, err)
	require.EqualValues(t, 42, claims.UserID)
	require.Len(t, claims.JTI, 32)
}

func TestValidateWeChatResumeOrderBindingFailsClosed(t *testing.T) {
	validJTI := "0123456789abcdef0123456789abcdef"
	tests := []struct {
		name string
		req  CreateOrderRequest
	}{
		{
			name: "unsigned openid",
			req: CreateOrderRequest{
				UserID:        17,
				OpenID:        "openid-17",
				PaymentSource: PaymentSourceWechatInAppResume,
			},
		},
		{
			name: "cross user",
			req: CreateOrderRequest{
				UserID:            18,
				ResumeTokenUserID: 17,
				ResumeTokenJTI:    validJTI,
				OpenID:            "openid-17",
				PaymentSource:     PaymentSourceWechatInAppResume,
			},
		},
		{
			name: "malformed replay jti",
			req: CreateOrderRequest{
				UserID:            17,
				ResumeTokenUserID: 17,
				ResumeTokenJTI:    "not-a-jti",
				OpenID:            "openid-17",
				PaymentSource:     PaymentSourceWechatInAppResume,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, validateWeChatResumeOrderBinding(tt.req))
		})
	}
}
