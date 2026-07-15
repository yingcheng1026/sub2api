package service

import (
	"strings"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyMigration188WidenAndHistoricalReplayScrubContract(t *testing.T) {
	raw, err := dbmigrations.FS.ReadFile("188_encrypt_api_keys_at_rest.sql")
	require.NoError(t, err)
	sql := string(raw)
	require.Contains(t, sql, "ALTER COLUMN key TYPE varchar(512)")
	require.Contains(t, sql, "DROP INDEX IF EXISTS idx_api_keys_key_trgm")
	require.Contains(t, sql, "user.api_keys.create")
	require.Contains(t, sql, "admin.subscriptions.assign")
	require.Contains(t, sql, "regexp_replace")
	require.Contains(t, sql, "chk_api_keys_encrypted_storage")
	require.Contains(t, sql, "NOT VALID")
	require.False(t, strings.Contains(sql, "DROP COLUMN key"), "runtime compatibility still requires encrypted key storage")
}
