package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestLockUsageBillingPlanCoverageUsesSubscriptionBoundDecision(t *testing.T) {
	for _, want := range []bool{false, true} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectQuery("WITH attached_snapshot AS").
			WithArgs(int64(41), int64(42), int64(43), service.SubscriptionTypeSubscription, service.PlanTypeSubscription).
			WillReturnRows(sqlmock.NewRows([]string{"covered"}).AddRow(want))

		covered, err := lockUsageBillingPlanCoverage(context.Background(), db, 41, 42, 43)
		require.NoError(t, err)
		require.Equal(t, want, covered)
		require.NoError(t, mock.ExpectationsWereMet())
	}
}

func TestLockUsageBillingPlanCoverageFailsClosedOnQueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sentinel := errors.New("coverage query unavailable")
	mock.ExpectQuery("WITH attached_snapshot AS").
		WithArgs(int64(41), int64(42), int64(43), service.SubscriptionTypeSubscription, service.PlanTypeSubscription).
		WillReturnError(sentinel)

	covered, err := lockUsageBillingPlanCoverage(context.Background(), db, 41, 42, 43)
	require.False(t, covered)
	require.ErrorIs(t, err, sentinel)
	require.NoError(t, mock.ExpectationsWereMet())
}
