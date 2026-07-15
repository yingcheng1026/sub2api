package repository

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestTranslateWalletSubscriptionDeleteErrorMapsOpenAdmissionGuard(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &pq.Error{
		Code:       "23514",
		Constraint: "hfc_wallet_open_admission_revoke",
	})

	translated := translateWalletSubscriptionDeleteError(err)
	require.ErrorIs(t, translated, service.ErrSubscriptionUsageBillingInFlight)
	require.ErrorIs(t, translated, err)
}

func TestTranslateWalletSubscriptionDeleteErrorPreservesUnrelatedFailure(t *testing.T) {
	sentinel := errors.New("database unavailable")
	require.ErrorIs(t, translateWalletSubscriptionDeleteError(sentinel), sentinel)
}
