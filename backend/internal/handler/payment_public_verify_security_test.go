package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func TestVerifyOrderPublicRejectsUnsignedOutTradeNo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &PaymentHandler{}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/payment/public/orders/verify",
		bytes.NewBufferString(`{"out_trade_no":"guessable-order-number"}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("unsigned out_trade_no reached the payment service: %v", recovered)
			}
		}()
		h.VerifyOrderPublic(ctx)
	}()

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if strings.Contains(recorder.Body.String(), "guessable-order-number") {
		t.Fatalf("response leaked unsigned order reference: %s", recorder.Body.String())
	}
}

func TestPublicOrderResultOmitsRefundPlanAndLifecycleDetails(t *testing.T) {
	reason := "private refund reason"
	actor := "admin:7"
	requestReason := "private request reason"
	now := time.Now()
	planID := int64(9)
	result := buildPublicOrderResult(&dbent.PaymentOrder{
		ID:                  42,
		OutTradeNo:          "sub2_public_42",
		Amount:              88,
		PayAmount:           90.64,
		FeeRate:             0.03,
		PaymentType:         "alipay",
		OrderType:           "balance",
		Status:              "PENDING",
		CreatedAt:           now,
		ExpiresAt:           now.Add(time.Hour),
		PaidAt:              &now,
		CompletedAt:         &now,
		RefundAmount:        12.34,
		RefundReason:        &reason,
		RefundRequestedAt:   &now,
		RefundRequestedBy:   &actor,
		RefundRequestReason: &requestReason,
		PlanID:              &planID,
	})

	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal public result: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("unmarshal public result: %v", err)
	}
	for _, forbidden := range []string{
		"created_at",
		"completed_at",
		"refund_amount",
		"refund_reason",
		"refund_requested_at",
		"refund_requested_by",
		"refund_request_reason",
		"plan_id",
	} {
		if _, ok := fields[forbidden]; ok {
			t.Errorf("public result exposed %q", forbidden)
		}
	}
}

func TestPaymentResumeTokenRequiresUserBinding(t *testing.T) {
	resumeService := service.NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	if _, err := resumeService.CreateToken(service.ResumeTokenClaims{OrderID: 42}); err == nil {
		t.Fatal("CreateToken accepted an order token without a user binding")
	}
}

func TestPaymentResumeTokenRejectsLegacyTokenWithoutExpiry(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	claims, err := json.Marshal(service.ResumeTokenClaims{OrderID: 42, UserID: 7})
	if err != nil {
		t.Fatalf("marshal legacy claims: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(claims)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	token := payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	resumeService := service.NewPaymentResumeService(key)
	if _, err := resumeService.ParseToken(token); err == nil {
		t.Fatal("ParseToken accepted a legacy token without an expiry")
	}
}

func TestVerifyOrderPublicAcceptsMatchingSignedResumeToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_RESUME_SIGNING_KEY", "0123456789abcdef0123456789abcdef")

	database, err := sql.Open("sqlite", "file:payment_public_verify_security?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	driver := entsql.OpenDB(dialect.SQLite, database)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(driver)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	user, err := client.User.Create().
		SetEmail("public-verify-signed@example.com").
		SetPasswordHash("hash").
		SetUsername("public-verify-signed-user").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(90.64).
		SetFeeRate(0.03).
		SetRechargeCode("PUBLIC-SIGNED-VERIFY").
		SetOutTradeNo("signed-order-no").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("signed-trade-no").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(service.OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("create order: %v", err)
	}

	resumeService := service.NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := resumeService.CreateToken(service.ResumeTokenClaims{
		OrderID:            order.ID,
		UserID:             user.ID,
		PaymentType:        payment.TypeAlipay,
		CanonicalReturnURL: "https://app.example.com/payment/result",
	})
	if err != nil {
		t.Fatalf("create resume token: %v", err)
	}
	configService := service.NewPaymentConfigService(client, nil, []byte("0123456789abcdef0123456789abcdef"))
	paymentService := service.NewPaymentService(client, payment.NewRegistry(), nil, nil, nil, configService, nil, nil, nil)
	h := NewPaymentHandler(paymentService, nil, nil, nil)

	recorder := httptest.NewRecorder()
	requestContext, _ := gin.CreateTestContext(recorder)
	requestContext.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/payment/public/orders/verify",
		bytes.NewBufferString(`{"resume_token":"`+token+`"}`),
	)
	requestContext.Request.Header.Set("Content-Type", "application/json")
	h.VerifyOrderPublic(requestContext)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var responseBody struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &responseBody); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if responseBody.Data["out_trade_no"] != order.OutTradeNo {
		t.Fatalf("out_trade_no = %v, want %q", responseBody.Data["out_trade_no"], order.OutTradeNo)
	}
}
