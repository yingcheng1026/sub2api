//go:build unit

package admin

import (
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestParsePaymentDashboardDaysRejectsInvalidOrUnboundedValues(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"0", "-1", "not-a-number", "367", "999999999999999999999999"} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := parsePaymentDashboardDays(raw)
			require.Equal(t, "INVALID_DASHBOARD_DAYS", infraerrors.Reason(err))
		})
	}

	days, err := parsePaymentDashboardDays("")
	require.NoError(t, err)
	require.Equal(t, 30, days)

	days, err = parsePaymentDashboardDays("366")
	require.NoError(t, err)
	require.Equal(t, service.MaxPaymentDashboardDays, days)
}
