//go:build unit

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdminUsageListRejectsDeepOffsetBeforeRepository(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?page=102&page_size=100", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Zero(t, repo.listCalls)

	req = httptest.NewRequest(http.MethodGet, "/admin/usage?page=101&page_size=100", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 101, repo.listParams.Page)
	require.Equal(t, 100, repo.listParams.PageSize)
}

func TestAdminUsageListBoundsRangeAndExactTotal(t *testing.T) {
	for _, target := range []string{
		"/admin/usage?start_date=2025-01-01&end_date=2026-01-01",
		"/admin/usage?start_date=2026-01-01",
		"/admin/usage?exact_total=true",
		"/admin/usage?user_id=42&exact_total=true&start_date=2026-01-01&end_date=2026-02-15",
	} {
		repo := &adminUsageRepoCapture{}
		router := newAdminUsageRequestTypeTestRouter(repo)
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, target)
		require.Zero(t, repo.listCalls, target)
	}

	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)
	req := httptest.NewRequest(http.MethodGet, "/admin/usage?user_id=42&exact_total=true&start_date=2026-01-01&end_date=2026-01-31", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, repo.listFilters.ExactTotal)
	require.NotNil(t, repo.listFilters.StartTime)
	require.NotNil(t, repo.listFilters.EndTime)
	require.False(t, repo.listFilters.EndTime.After(repo.listFilters.StartTime.AddDate(0, 0, maxAdminUsageExactTotalDays)))
}

func TestAdminUsageListDefaultsToBoundedRecentWindowAndDeadline(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.listFilters.StartTime)
	require.NotNil(t, repo.listFilters.EndTime)
	require.False(t, repo.listFilters.EndTime.After(repo.listFilters.StartTime.AddDate(0, 0, defaultAdminUsageLookbackDays)))
	require.True(t, repo.listContextHadDeadline)
}

func TestAdminUsageStatsRejectsBroadUnscopedRangeAndUsesDeadline(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/stats?start_date=2026-01-01&end_date=2026-03-01", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Zero(t, repo.statsCalls)

	req = httptest.NewRequest(http.MethodGet, "/admin/usage/stats?user_id=42&start_date=2026-01-01&end_date=2026-03-01", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, repo.statsCalls)
	require.True(t, repo.statsContextHadDeadline)
}

func TestBoundedAdminUsageContextHasQueryDeadline(t *testing.T) {
	ctx, cancel := boundedAdminUsageContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.Greater(t, time.Until(deadline), time.Duration(0))
	require.LessOrEqual(t, time.Until(deadline), maxAdminUsageQueryTime)
}
