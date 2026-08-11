//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type systemHandlerUpdateServiceStub struct {
	updateInfo           *service.UpdateInfo
	checkErr             error
	checkForces          []bool
	performCall          int
	rollbackCall         int
	rollbackToCall       int
	rollbackVersions     []service.RollbackVersion
	rollbackVersionsErr  error
	rollbackVersionsCall int
}

func (s *systemHandlerUpdateServiceStub) CheckUpdate(_ context.Context, force bool) (*service.UpdateInfo, error) {
	s.checkForces = append(s.checkForces, force)
	return s.updateInfo, s.checkErr
}

func (s *systemHandlerUpdateServiceStub) PerformUpdate(context.Context) error {
	s.performCall++
	return nil
}

func (s *systemHandlerUpdateServiceStub) Rollback() error {
	s.rollbackCall++
	return nil
}

func (s *systemHandlerUpdateServiceStub) ListRollbackVersions(context.Context) ([]service.RollbackVersion, error) {
	s.rollbackVersionsCall++
	return s.rollbackVersions, s.rollbackVersionsErr
}

func (s *systemHandlerUpdateServiceStub) RollbackToVersion(context.Context, string) error {
	s.rollbackToCall++
	return nil
}

type systemUpdateErrorEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newSystemHandlerTestRouter(t *testing.T, updateSvc *systemHandlerUpdateServiceStub, repo *memoryIdempotencyRepoStub) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	service.SetDefaultIdempotencyCoordinator(nil)
	t.Cleanup(func() {
		service.SetDefaultIdempotencyCoordinator(nil)
	})

	lockSvc := service.NewSystemOperationLockService(repo, service.IdempotencyConfig{
		ProcessingTimeout:  time.Second,
		SystemOperationTTL: time.Minute,
	})
	handler := NewSystemHandler(updateSvc, lockSvc)

	router := gin.New()
	router.POST("/api/v1/admin/system/update", handler.PerformUpdate)
	router.POST("/api/v1/admin/system/rollback", handler.Rollback)
	router.POST("/api/v1/admin/system/restart", handler.RestartService)
	router.GET("/api/v1/admin/system/rollback-versions", handler.GetRollbackVersions)
	return router
}

func requireIdempotencyStoreEmpty(t *testing.T, repo *memoryIdempotencyRepoStub) {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Empty(t, repo.data, "immutable rejection must not write idempotency records")
}

func requireImmutableRejection(t *testing.T, rec *httptest.ResponseRecorder, updateSvc *systemHandlerUpdateServiceStub) {
	t.Helper()
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "IMMUTABLE_DEPLOYMENT_REQUIRED")

	var body systemUpdateErrorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, http.StatusConflict, body.Code)
	require.NotEmpty(t, body.Message)

	require.Zero(t, updateSvc.performCall, "update service must not be invoked")
	require.Zero(t, updateSvc.rollbackCall, "legacy rollback must not be invoked")
	require.Zero(t, updateSvc.rollbackToCall, "versioned rollback must not be invoked")
	require.Empty(t, updateSvc.checkForces, "version check must not be invoked")
}

func TestSystemHandlerPerformUpdateRequiresImmutableDeployment(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/update", nil)
	req.Header.Set("Idempotency-Key", "immutable-update")
	router.ServeHTTP(rec, req)

	requireImmutableRejection(t, rec, updateSvc)
	requireIdempotencyStoreEmpty(t, repo)
}

func TestSystemHandlerRollbackRequiresImmutableDeployment(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	for name, body := range map[string]string{
		"legacy backup":  "",
		"versioned":      `{"version":"0.1.146"}`,
		"malformed body": `{"version":`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			var req *http.Request
			if body == "" {
				req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/rollback", nil)
			} else {
				req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/rollback", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("Idempotency-Key", "immutable-rollback-"+name)
			router.ServeHTTP(rec, req)

			requireImmutableRejection(t, rec, updateSvc)
			requireIdempotencyStoreEmpty(t, repo)
		})
	}
}

func TestSystemHandlerRestartRequiresImmutableDeployment(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/restart", nil)
	req.Header.Set("Idempotency-Key", "immutable-restart")
	router.ServeHTTP(rec, req)

	requireImmutableRejection(t, rec, updateSvc)
	requireIdempotencyStoreEmpty(t, repo)
}

func TestSystemHandlerGetRollbackVersions(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{
		rollbackVersions: []service.RollbackVersion{
			{Version: "0.1.146", PublishedAt: "2026-07-07T00:00:00Z", HTMLURL: "https://example.com/v0.1.146"},
			{Version: "0.1.145", PublishedAt: "2026-07-06T00:00:00Z", HTMLURL: "https://example.com/v0.1.145"},
		},
	}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/system/rollback-versions", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, updateSvc.rollbackVersionsCall)

	var body struct {
		Code int `json:"code"`
		Data struct {
			Versions []service.RollbackVersion `json:"versions"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, 0, body.Code)
	require.Len(t, body.Data.Versions, 2)
	require.Equal(t, "0.1.146", body.Data.Versions[0].Version)
}

func TestSystemHandlerGetRollbackVersionsError(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{
		rollbackVersionsErr: errors.New("github unavailable"),
	}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/system/rollback-versions", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, 1, updateSvc.rollbackVersionsCall)
}
