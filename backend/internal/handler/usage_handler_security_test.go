//go:build unit

package handler

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func usageSecurityTestContext(target string) *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", target, nil)
	return c
}

func TestBoundedUsageContextHasQueryDeadline(t *testing.T) {
	ctx, cancel := boundedUsageContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.LessOrEqual(t, time.Until(deadline), maxUserUsageQueryTime)
	require.Greater(t, time.Until(deadline), 0*time.Second)
}

func TestParseUsagePaginationRejectsDeepAndOverflowingOffsets(t *testing.T) {
	for _, target := range []string{
		"/?page=1001",
		"/?page=102&page_size=100",
		"/?page=999999999999999999999999999999",
		"/?page_size=101",
		"/?limit=101",
	} {
		_, _, err := parseUsagePagination(usageSecurityTestContext(target))
		require.Error(t, err, target)
	}

	page, pageSize, err := parseUsagePagination(usageSecurityTestContext("/?page=101&page_size=100"))
	require.NoError(t, err)
	require.Equal(t, 101, page)
	require.Equal(t, 100, pageSize)
	require.Equal(t, maxUserUsageOffset, (page-1)*pageSize)
}

func TestParseBoundedUsageTimeRangeRejectsInvertedAndOversizedRanges(t *testing.T) {
	for _, target := range []string{
		"/?timezone=UTC&start_date=2026-07-10&end_date=2026-07-01",
		"/?timezone=UTC&start_date=2020-01-01&end_date=2026-07-01",
		"/?timezone=UTC&start_date=2026-07-01",
	} {
		_, _, err := parseBoundedUsageTimeRange(usageSecurityTestContext(target), 365, true)
		require.Error(t, err, target)
	}

	start, end, err := parseBoundedUsageTimeRange(
		usageSecurityTestContext("/?timezone=UTC&start_date=2025-07-02&end_date=2026-07-01"),
		365,
		true,
	)
	require.NoError(t, err)
	require.True(t, start.Before(end))
	require.False(t, end.After(start.AddDate(0, 0, maxUserUsageRangeDays)))
}
