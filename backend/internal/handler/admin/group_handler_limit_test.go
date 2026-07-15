//go:build unit

package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateGroupRequestOmittedUsageLimitsRemainUnset(t *testing.T) {
	var req UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"description":"metadata only"}`), &req))

	require.Nil(t, req.DailyLimitUSD.ToServiceInput())
	require.Nil(t, req.WeeklyLimitUSD.ToServiceInput())
	require.Nil(t, req.MonthlyLimitUSD.ToServiceInput())
}

func TestUpdateGroupRequestExplicitNegativeUsageLimitRequestsRemoval(t *testing.T) {
	var req UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"daily_limit_usd":-1}`), &req))

	require.NotNil(t, req.DailyLimitUSD.ToServiceInput())
	require.Equal(t, -1.0, *req.DailyLimitUSD.ToServiceInput())
}
