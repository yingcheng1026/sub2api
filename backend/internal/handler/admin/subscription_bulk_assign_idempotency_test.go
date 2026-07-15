package admin

import (
	"bytes"
	"context"
	"encoding/json"
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

type bulkSubscriptionAssignerStub struct {
	mu     sync.Mutex
	calls  int
	inputs []service.BulkAssignSubscriptionInput
}

func (s *bulkSubscriptionAssignerStub) BulkAssignSubscription(
	_ context.Context,
	input *service.BulkAssignSubscriptionInput,
) (*service.BulkAssignResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if input != nil {
		cloned := *input
		cloned.UserIDs = append([]int64(nil), input.UserIDs...)
		s.inputs = append(s.inputs, cloned)
	}
	return &service.BulkAssignResult{
		SuccessCount: 2,
		CreatedCount: 2,
		Statuses: map[int64]string{
			101: "created",
			102: "created",
		},
		Subscriptions: []service.UserSubscription{
			{ID: 9001, UserID: 101, Status: service.SubscriptionStatusActive, StartsAt: time.Now(), ExpiresAt: time.Now().Add(30 * 24 * time.Hour)},
			{ID: 9002, UserID: 102, Status: service.SubscriptionStatusActive, StartsAt: time.Now(), ExpiresAt: time.Now().Add(30 * 24 * time.Hour)},
		},
	}, nil
}

func (s *bulkSubscriptionAssignerStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newAdminSubscriptionBulkAssignRouter(t *testing.T, observeOnly bool) (*gin.Engine, *bulkSubscriptionAssignerStub) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = observeOnly
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	assigner := &bulkSubscriptionAssignerStub{}
	handler := &SubscriptionHandler{subscriptionBulkAssigner: assigner}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 99})
		c.Next()
	})
	router.POST("/api/v1/admin/subscriptions/bulk-assign", handler.BulkAssign)
	return router, assigner
}

func performAdminSubscriptionBulkAssign(router *gin.Engine, body, idempotencyKey string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/subscriptions/bulk-assign", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func decodeBulkAssignResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return body
}

func TestAdminSubscriptionBulkAssignSameKeyReplaysWithoutDuplicateAssignments(t *testing.T) {
	router, assigner := newAdminSubscriptionBulkAssignRouter(t, false)
	body := `{"user_ids":[101,102],"group_id":7,"validity_days":30,"notes":"bulk"}`

	first := performAdminSubscriptionBulkAssign(router, body, "bulk-ack-lost")
	second := performAdminSubscriptionBulkAssign(router, body, "bulk-ack-lost")

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Empty(t, first.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, 1, assigner.callCount(), "an ACK-loss retry must not execute bulk assignment again")
	require.Equal(t, float64(2), decodeBulkAssignResponse(t, first)["data"].(map[string]any)["success_count"])
	require.Equal(t, float64(2), decodeBulkAssignResponse(t, second)["data"].(map[string]any)["success_count"])
}

func TestAdminSubscriptionBulkAssignSameKeyDifferentPayloadConflicts(t *testing.T) {
	router, assigner := newAdminSubscriptionBulkAssignRouter(t, false)

	first := performAdminSubscriptionBulkAssign(router, `{"user_ids":[101,102],"group_id":7,"validity_days":30}`, "bulk-conflict")
	second := performAdminSubscriptionBulkAssign(router, `{"user_ids":[101,103],"group_id":7,"validity_days":30}`, "bulk-conflict")

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", decodeBulkAssignResponse(t, second)["reason"])
	require.Equal(t, 1, assigner.callCount())
}

func TestAdminSubscriptionBulkAssignRequiresKeyEvenInObserveOnlyMode(t *testing.T) {
	router, assigner := newAdminSubscriptionBulkAssignRouter(t, true)

	response := performAdminSubscriptionBulkAssign(router, `{"user_ids":[101,102],"group_id":7}`, "")

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Equal(t, "IDEMPOTENCY_KEY_REQUIRED", decodeBulkAssignResponse(t, response)["reason"])
	require.Zero(t, assigner.callCount())
}

func TestAdminSubscriptionBulkAssignFailsClosedWithoutCoordinator(t *testing.T) {
	router, assigner := newAdminSubscriptionBulkAssignRouter(t, false)
	service.SetDefaultIdempotencyCoordinator(nil)

	response := performAdminSubscriptionBulkAssign(router, `{"user_ids":[101,102],"group_id":7}`, "bulk-no-coordinator")

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Equal(t, "IDEMPOTENCY_STORE_UNAVAILABLE", decodeBulkAssignResponse(t, response)["reason"])
	require.Zero(t, assigner.callCount())
}
