package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpsRealtimeTrafficLifecycleOmittedForFilteredSlices(t *testing.T) {
	svc := &OpsService{opsRepo: &opsRepoMock{}}
	start := time.Now().UTC().Add(-time.Minute)
	end := time.Now().UTC()

	global := &OpsDashboardFilter{StartTime: start, EndTime: end}
	summary, err := svc.GetRealtimeTrafficSummary(context.Background(), global)
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.NotNil(t, summary.Lifecycle)

	platform := &OpsDashboardFilter{StartTime: start, EndTime: end, Platform: PlatformOpenAI}
	summary, err = svc.GetRealtimeTrafficSummary(context.Background(), platform)
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.Nil(t, summary.Lifecycle)

	groupID := int64(42)
	group := &OpsDashboardFilter{StartTime: start, EndTime: end, GroupID: &groupID}
	summary, err = svc.GetRealtimeTrafficSummary(context.Background(), group)
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.Nil(t, summary.Lifecycle)
}
