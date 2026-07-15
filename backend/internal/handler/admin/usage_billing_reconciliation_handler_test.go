package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type reconciliationHandlerRepoStub struct {
	items    []service.UsageBillingReconciliationCase
	resolved *service.UsageBillingReconciliationResolveInput
	err      error
}

func (s *reconciliationHandlerRepoStub) ListUsageBillingReconciliationCases(
	context.Context,
	int,
) ([]service.UsageBillingReconciliationCase, error) {
	return s.items, s.err
}

func (s *reconciliationHandlerRepoStub) ResolveUsageBillingReconciliation(
	_ context.Context,
	input service.UsageBillingReconciliationResolveInput,
) error {
	copy := input
	s.resolved = &copy
	return s.err
}

type reconciliationIdempotencyRepoStub struct{}

func (reconciliationIdempotencyRepoStub) CreateProcessing(_ context.Context, record *service.IdempotencyRecord) (bool, error) {
	record.ID = 1
	return true, nil
}

func (reconciliationIdempotencyRepoStub) GetByScopeAndKeyHash(context.Context, string, string) (*service.IdempotencyRecord, error) {
	return nil, nil
}

func (reconciliationIdempotencyRepoStub) TryReclaim(context.Context, int64, string, time.Time, time.Time, time.Time) (bool, error) {
	return false, nil
}

func (reconciliationIdempotencyRepoStub) ExtendProcessingLock(context.Context, int64, string, time.Time, time.Time) (bool, error) {
	return true, nil
}

func (reconciliationIdempotencyRepoStub) MarkSucceeded(context.Context, int64, int, string, time.Time) error {
	return nil
}

func (reconciliationIdempotencyRepoStub) MarkFailedRetryable(context.Context, int64, string, time.Time, time.Time) error {
	return nil
}

func (reconciliationIdempotencyRepoStub) DeleteExpired(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func setupReconciliationRouter(repo *reconciliationHandlerRepoStub, userID int64) *gin.Engine {
	return setupReconciliationRouterWithAuth(repo, userID, "jwt")
}

func setupReconciliationRouterWithAuth(
	repo *reconciliationHandlerRepoStub,
	userID int64,
	authMethod string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if userID > 0 {
		router.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
			c.Set("auth_method", authMethod)
			c.Next()
		})
	}
	var reconciliationService *service.UsageBillingReconciliationService
	if repo != nil {
		reconciliationService = service.NewUsageBillingReconciliationService(repo)
	}
	handler := NewUsageHandler(nil, nil, nil, nil, reconciliationService)
	router.GET("/api/v1/admin/usage/reconciliation", handler.ListBillingReconciliationCases)
	router.POST("/api/v1/admin/usage/reconciliation/resolve", handler.ResolveBillingReconciliation)
	return router
}

func TestUsageHandlerRejectsSharedAdminKeyForBillingReconciliation(t *testing.T) {
	repo := &reconciliationHandlerRepoStub{}
	router := setupReconciliationRouterWithAuth(repo, 7, "admin_api_key")

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/reconciliation/resolve", bytes.NewReader(reconciliationRequestBody(t)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "shared-key-reconciliation-denied")
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Nil(t, repo.resolved)
}

func TestUsageHandlerListsBillingReconciliationCasesForAuthenticatedAdmin(t *testing.T) {
	repo := &reconciliationHandlerRepoStub{items: []service.UsageBillingReconciliationCase{{RequestID: "req-list", APIKeyID: 4}}}
	router := setupReconciliationRouter(repo, 7)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/usage/reconciliation?limit=25", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "req-list")
}

func TestUsageHandlerRejectsUnauthenticatedBillingReconciliation(t *testing.T) {
	repo := &reconciliationHandlerRepoStub{}
	router := setupReconciliationRouter(repo, 0)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/reconciliation/resolve", bytes.NewBufferString(`{}`)))

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
	require.Nil(t, repo.resolved)
}

func TestUsageHandlerRequiresPersistentIdempotencyKeyForBillingReconciliation(t *testing.T) {
	repo := &reconciliationHandlerRepoStub{}
	router := setupReconciliationRouter(repo, 7)
	body := reconciliationRequestBody(t)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/reconciliation/resolve", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Nil(t, repo.resolved)
}

func TestUsageHandlerResolvesBillingReconciliationWithOperatorEvidence(t *testing.T) {
	repo := &reconciliationHandlerRepoStub{}
	router := setupReconciliationRouter(repo, 7)
	coordinator := service.NewIdempotencyCoordinator(reconciliationIdempotencyRepoStub{}, service.DefaultIdempotencyConfig())
	service.SetDefaultIdempotencyCoordinator(coordinator)
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/reconciliation/resolve", bytes.NewReader(reconciliationRequestBody(t)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "reconcile-req-handler-20260712")
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotNil(t, repo.resolved)
	require.Equal(t, int64(7), repo.resolved.OperatorID)
	require.Equal(t, "incident:HFC-2026-0712", repo.resolved.EvidenceRef)
}

func reconciliationRequestBody(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(service.UsageBillingReconciliationResolveInput{
		RequestID: "req-handler", APIKeyID: 4,
		Action:      service.UsageBillingReconciliationActionReleaseUndelivered,
		EvidenceRef: "incident:HFC-2026-0712",
	})
	require.NoError(t, err)
	return body
}
