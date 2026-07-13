package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAssignSubscriptionInputFromRequestMapsManualWalletToCredits(t *testing.T) {
	amount := 50.0
	input := assignSubscriptionInputFromRequest(AssignSubscriptionRequest{
		UserID:           173,
		ValidityDays:     30,
		Notes:            "manual topup",
		WalletInitialUSD: &amount,
	}, 1)

	require.Equal(t, int64(173), input.UserID)
	require.Equal(t, int64(1), input.AssignedBy)
	require.Equal(t, service.PlanTypeCredits, input.PlanType)
	require.Same(t, &amount, input.WalletInitialUSD)
	require.Equal(t, 30, input.ValidityDays)
}

func TestAssignSubscriptionInputFromRequestKeepsPlanModeForPlanIDOnly(t *testing.T) {
	planID := int64(8)
	input := assignSubscriptionInputFromRequest(AssignSubscriptionRequest{
		UserID: 88,
		PlanID: &planID,
	}, 1)

	require.Equal(t, int64(88), input.UserID)
	require.Same(t, &planID, input.PlanID)
	require.Empty(t, input.PlanType)
	require.Nil(t, input.WalletInitialUSD)
}

func TestAssignSubscriptionRejectsMissingIdempotencyKeyBeforeFinancialWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.SetDefaultIdempotencyCoordinator(nil)
	handler := &SubscriptionHandler{}
	router := gin.New()
	router.POST("/admin/subscriptions/assign", handler.Assign)

	req := httptest.NewRequest(
		http.MethodPost,
		"/admin/subscriptions/assign",
		bytes.NewBufferString(`{"user_id":173,"wallet_initial_usd":50}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAssignSubscriptionFailsClosedWithoutIdempotencyCoordinator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.SetDefaultIdempotencyCoordinator(nil)
	handler := &SubscriptionHandler{}
	router := gin.New()
	router.POST("/admin/subscriptions/assign", handler.Assign)

	req := httptest.NewRequest(
		http.MethodPost,
		"/admin/subscriptions/assign",
		bytes.NewBufferString(`{"user_id":173,"wallet_initial_usd":50}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "admin-assign-handler-test")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
