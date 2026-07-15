package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type subscriptionAssignerStub struct {
	mu                       sync.Mutex
	calls                    int
	inputs                   []service.AssignSubscriptionInput
	planWalletInitialUSD     float64
	planWalletBalanceUSD     float64
	planWalletCreditDeltaUSD float64
	planType                 string
}

func (s *subscriptionAssignerStub) AssignAdminSubscription(_ context.Context, input *service.AssignSubscriptionInput) (*service.UserSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls++
	if input != nil {
		s.inputs = append(s.inputs, *input)
	}
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	sub := &service.UserSubscription{
		ID:         701,
		UserID:     input.UserID,
		StartsAt:   now,
		ExpiresAt:  service.MaxExpiresAt,
		Status:     service.SubscriptionStatusActive,
		AssignedAt: now,
		Notes:      input.Notes,
	}
	if input.WalletInitialUSD != nil {
		initial := *input.WalletInitialUSD
		sub.WalletInitialUSD = &initial
		sub.WalletBalanceUSD = &initial
		return sub, nil
	}
	if input.PlanID != nil && s.planWalletInitialUSD > 0 {
		initial := s.planWalletInitialUSD
		balance := s.planWalletBalanceUSD
		creditDelta := s.planWalletCreditDeltaUSD
		sub.WalletInitialUSD = &initial
		sub.WalletBalanceUSD = &balance
		sub.WalletCreditDeltaUSD = &creditDelta
		sub.AssignmentPlanType = s.planType
		return sub, nil
	}

	groupID := input.GroupID
	monthlyLimit := 99.0
	sub.GroupID = &groupID
	sub.Group = &service.Group{ID: groupID, MonthlyLimitUSD: &monthlyLimit}
	sub.AssignmentPlanType = s.planType
	return sub, nil
}

func (s *subscriptionAssignerStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *subscriptionAssignerStub) RunAssignmentTransaction(ctx context.Context, execute func(context.Context) error) error {
	return execute(ctx)
}

type affiliateRebateAccruerStub struct {
	mu           sync.Mutex
	calls        int
	inviteeID    int64
	baseAmount   float64
	rateOverride *float64
	err          error
}

func (s *affiliateRebateAccruerStub) AccrueInviteRebateForOrderWithOverride(
	_ context.Context,
	inviteeUserID int64,
	baseRechargeAmount float64,
	rateOverride *float64,
	_ *int64,
) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.inviteeID = inviteeUserID
	s.baseAmount = baseRechargeAmount
	s.rateOverride = rateOverride
	return 0, s.err
}

func (s *affiliateRebateAccruerStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newAdminSubscriptionAssignRouter(t *testing.T, observeOnly bool) (*gin.Engine, *subscriptionAssignerStub, *affiliateRebateAccruerStub) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = observeOnly
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	assigner := &subscriptionAssignerStub{}
	rebate := &affiliateRebateAccruerStub{}
	handler := &SubscriptionHandler{
		subscriptionAssigner:        assigner,
		assignmentTransactionRunner: assigner,
		affiliateRebateAccruer:      rebate,
	}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 99})
		c.Next()
	})
	router.POST("/api/v1/admin/subscriptions/assign", handler.Assign)
	return router, assigner, rebate
}

func performAdminSubscriptionAssign(router *gin.Engine, body, idempotencyKey string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/subscriptions/assign", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func decodeAssignResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return body
}

func TestAdminSubscriptionAssignSameKeyReplaysWalletWithoutDuplicateSideEffects(t *testing.T) {
	router, assigner, rebate := newAdminSubscriptionAssignRouter(t, false)
	body := `{"user_id":173,"wallet_initial_usd":50,"notes":"manual topup"}`

	first := performAdminSubscriptionAssign(router, body, "wallet-topup-ack-lost")
	second := performAdminSubscriptionAssign(router, body, "wallet-topup-ack-lost")

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Empty(t, first.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, float64(701), decodeAssignResponse(t, first)["data"].(map[string]any)["id"])
	require.Equal(t, float64(701), decodeAssignResponse(t, second)["data"].(map[string]any)["id"])
	require.Equal(t, 1, assigner.callCount(), "wallet topup must execute once after an ACK-loss retry")
	require.Equal(t, 1, rebate.callCount(), "affiliate rebate must stay inside the same idempotency boundary")
	require.Equal(t, int64(173), rebate.inviteeID)
	require.Equal(t, 50.0, rebate.baseAmount)
	require.NotNil(t, rebate.rateOverride)
	require.Equal(t, service.AffiliateRebateSubscriptionRate, *rebate.rateOverride,
		"manual PlanID=nil wallet topups keep the zero-rebate policy")
}

func TestAdminSubscriptionAssignCreditsPlanTopupRebateUsesCurrentDeltaOnce(t *testing.T) {
	router, assigner, rebate := newAdminSubscriptionAssignRouter(t, false)
	assigner.planWalletInitialUSD = 150
	assigner.planWalletBalanceUSD = 145
	assigner.planWalletCreditDeltaUSD = 50
	assigner.planType = service.PlanTypeCredits
	body := `{"user_id":173,"plan_id":987654,"notes":"credits plan topup"}`

	first := performAdminSubscriptionAssign(router, body, "credits-plan-topup-ack-lost")
	second := performAdminSubscriptionAssign(router, body, "credits-plan-topup-ack-lost")

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, 1, assigner.callCount(), "credits plan topup must execute once")
	require.Equal(t, 1, rebate.callCount(), "credits plan rebate must execute once")
	require.Equal(t, 50.0, rebate.baseAmount,
		"rebate must use this plan's wallet credit delta, not cumulative wallet_initial_usd")
	require.NotNil(t, rebate.rateOverride)
	require.Equal(t, service.AffiliateRebateCreditsCardRate, *rebate.rateOverride)
}

func TestAdminSubscriptionAssignRejectsAmbiguousAssignmentModes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "plan and wallet", body: `{"user_id":173,"plan_id":11,"wallet_initial_usd":50}`},
		{name: "plan and group", body: `{"user_id":173,"plan_id":11,"group_id":22}`},
		{name: "wallet and group", body: `{"user_id":173,"wallet_initial_usd":50,"group_id":22}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, assigner, rebate := newAdminSubscriptionAssignRouter(t, false)
			response := performAdminSubscriptionAssign(router, tt.body, "ambiguous-"+tt.name)

			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Zero(t, assigner.callCount())
			require.Zero(t, rebate.callCount())
		})
	}
}

func TestAdminSubscriptionAssignRejectsInvalidModeSpecificFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "negative user", body: `{"user_id":-1,"plan_id":11}`},
		{name: "negative group", body: `{"user_id":173,"group_id":-22}`},
		{name: "negative group validity", body: `{"user_id":173,"group_id":22,"validity_days":-1}`},
		{name: "plan validity", body: `{"user_id":173,"plan_id":11,"validity_days":30}`},
		{name: "wallet validity", body: `{"user_id":173,"wallet_initial_usd":50,"validity_days":30}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, assigner, rebate := newAdminSubscriptionAssignRouter(t, false)
			response := performAdminSubscriptionAssign(router, tt.body, "invalid-"+tt.name)

			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Zero(t, assigner.callCount())
			require.Zero(t, rebate.callCount())
		})
	}
}

func TestAdminSubscriptionAssignReusedLegacyCreditsIDCannotChangeMonthlyRebate(t *testing.T) {
	router, assigner, rebate := newAdminSubscriptionAssignRouter(t, false)
	assigner.planType = service.PlanTypeSubscription
	body := `{"user_id":208,"plan_id":11,"notes":"monthly plan with reused id"}`

	response := performAdminSubscriptionAssign(router, body, "monthly-reused-plan-id")

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, 1, assigner.callCount())
	require.Equal(t, 1, rebate.callCount())
	require.NotNil(t, rebate.rateOverride)
	require.Equal(t, service.AffiliateRebateSubscriptionRate, *rebate.rateOverride)
}

func TestAdminSubscriptionAssignSameKeyDifferentPayloadConflicts(t *testing.T) {
	router, assigner, rebate := newAdminSubscriptionAssignRouter(t, false)

	first := performAdminSubscriptionAssign(router, `{"user_id":173,"wallet_initial_usd":50}`, "wallet-topup-conflict")
	second := performAdminSubscriptionAssign(router, `{"user_id":173,"wallet_initial_usd":75}`, "wallet-topup-conflict")

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", decodeAssignResponse(t, second)["reason"])
	require.Equal(t, 1, assigner.callCount())
	require.Equal(t, 1, rebate.callCount())
}

func TestAdminSubscriptionAssignRequiresKeyWhenEnforcementEnabled(t *testing.T) {
	router, assigner, rebate := newAdminSubscriptionAssignRouter(t, false)

	response := performAdminSubscriptionAssign(router, `{"user_id":173,"wallet_initial_usd":50}`, "")

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Equal(t, "IDEMPOTENCY_KEY_REQUIRED", decodeAssignResponse(t, response)["reason"])
	require.Zero(t, assigner.callCount())
	require.Zero(t, rebate.callCount())
}

func TestAdminSubscriptionAssignObserveOnlyCannotBypassStrictMissingKeyPolicy(t *testing.T) {
	router, assigner, rebate := newAdminSubscriptionAssignRouter(t, true)

	response := performAdminSubscriptionAssign(router, `{"user_id":173,"wallet_initial_usd":50}`, "")

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Equal(t, "IDEMPOTENCY_KEY_REQUIRED", decodeAssignResponse(t, response)["reason"])
	require.Zero(t, assigner.callCount(), "financial writes must ignore the global observe-only bypass")
	require.Zero(t, rebate.callCount())
}

func TestAdminSubscriptionAssignFailsClosedWhenCoordinatorIsMissing(t *testing.T) {
	router, assigner, rebate := newAdminSubscriptionAssignRouter(t, false)
	service.SetDefaultIdempotencyCoordinator(nil)

	response := performAdminSubscriptionAssign(router, `{"user_id":173,"wallet_initial_usd":50}`, "wallet-no-coordinator")

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Equal(t, "IDEMPOTENCY_STORE_UNAVAILABLE", decodeAssignResponse(t, response)["reason"])
	require.Zero(t, assigner.callCount())
	require.Zero(t, rebate.callCount())
}

func TestAdminSubscriptionAssignSameKeyReplaysMonthlyAssignment(t *testing.T) {
	router, assigner, rebate := newAdminSubscriptionAssignRouter(t, false)
	body := `{"user_id":208,"group_id":22,"validity_days":30,"notes":"monthly vip"}`

	first := performAdminSubscriptionAssign(router, body, "monthly-assign-ack-lost")
	second := performAdminSubscriptionAssign(router, body, "monthly-assign-ack-lost")

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, 1, assigner.callCount(), "monthly assignment must not execute twice")
	require.Equal(t, 1, rebate.callCount(), "monthly affiliate decision must not execute twice")
	require.Equal(t, 99.0, rebate.baseAmount, "monthly assignment keeps the group quota base")
	require.NotNil(t, rebate.rateOverride)
	require.Equal(t, service.AffiliateRebateSubscriptionRate, *rebate.rateOverride)
}

func TestAdminSubscriptionAssignAffiliateFailureFailsClosed(t *testing.T) {
	router, assigner, rebate := newAdminSubscriptionAssignRouter(t, false)
	rebate.err = errors.New("affiliate repository unavailable")

	response := performAdminSubscriptionAssign(router, `{"user_id":173,"wallet_initial_usd":50}`, "wallet-affiliate-failure")

	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.Equal(t, 1, assigner.callCount())
	require.Equal(t, 1, rebate.callCount())
}

func TestAdminAssignBaseAmountUsesCurrentOperationValue(t *testing.T) {
	manualDelta := 50.0
	creditDelta := 50.0
	cumulativeInitial := 150.0
	monthlyLimit := 99.0
	creditsPlanID := int64(11)
	monthlyPlanID := int64(20)

	tests := []struct {
		name  string
		input *service.AssignSubscriptionInput
		sub   *service.UserSubscription
		want  float64
	}{
		{
			name:  "manual wallet uses request delta instead of cumulative initial",
			input: &service.AssignSubscriptionInput{WalletInitialUSD: &manualDelta},
			sub:   &service.UserSubscription{WalletInitialUSD: &cumulativeInitial},
			want:  manualDelta,
		},
		{
			name:  "credits plan uses applied plan quota delta",
			input: &service.AssignSubscriptionInput{PlanID: &creditsPlanID},
			sub: &service.UserSubscription{
				WalletInitialUSD:     &cumulativeInitial,
				WalletCreditDeltaUSD: &creditDelta,
			},
			want: creditDelta,
		},
		{
			name:  "credits plan fails closed without current operation delta",
			input: &service.AssignSubscriptionInput{PlanID: &creditsPlanID},
			sub:   &service.UserSubscription{WalletInitialUSD: &cumulativeInitial},
			want:  0,
		},
		{
			name:  "monthly plan keeps group monthly quota",
			input: &service.AssignSubscriptionInput{PlanID: &monthlyPlanID},
			sub:   &service.UserSubscription{Group: &service.Group{MonthlyLimitUSD: &monthlyLimit}},
			want:  monthlyLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, adminAssignBaseAmount(tt.input, tt.sub))
		})
	}
}
