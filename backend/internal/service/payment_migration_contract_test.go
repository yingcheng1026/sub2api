//go:build unit

package service

import (
	"regexp"
	"strconv"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestPaymentLifecycleDeletionGraceMatchesMigrationGuards(t *testing.T) {
	content, err := dbmigrations.FS.ReadFile("176_enforce_monthly_group_not_wallet.sql")
	require.NoError(t, err)

	intervalPattern := regexp.MustCompile(`INTERVAL '([0-9]+) minutes'`)
	matches := intervalPattern.FindAllSubmatch(content, -1)
	require.NotEmpty(t, matches, "migration 176 must encode the payment recovery grace")

	for _, match := range matches {
		minutes, parseErr := strconv.Atoi(string(match[1]))
		require.NoError(t, parseErr)
		require.Equal(t, paymentGraceMinutes, minutes,
			"database fulfillability guards drifted from recent-expiry deletion protection")
	}
}
