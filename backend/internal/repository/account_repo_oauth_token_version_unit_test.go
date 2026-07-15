//go:build unit

package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type credentialPatchWithoutCallerTokenVersion struct{}

func (credentialPatchWithoutCallerTokenVersion) Match(value driver.Value) bool {
	payload, ok := value.([]byte)
	if !ok {
		return false
	}
	var credentials map[string]any
	if err := json.Unmarshal(payload, &credentials); err != nil {
		return false
	}
	_, hasCallerVersion := credentials["_token_version"]
	return !hasCallerVersion && credentials["project_id"] == "replacement-project"
}

func TestAccountRepositoryBulkUpdateAdvancesOAuthTokenVersionInSQL(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	mock.ExpectExec(`UPDATE accounts SET credentials = .*CASE WHEN type = 'oauth'.*jsonb_build_object\('_token_version'.*WHERE id = ANY`).
		WithArgs(credentialPatchWithoutCallerTokenVersion{}, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err = repo.BulkUpdate(context.Background(), []int64{17}, service.AccountBulkUpdate{
		Credentials: map[string]any{
			"project_id":     "replacement-project",
			"_token_version": int64(1),
		},
	})

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
