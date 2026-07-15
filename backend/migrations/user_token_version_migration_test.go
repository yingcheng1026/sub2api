package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration189PersistsNonNegativeUserTokenVersion(t *testing.T) {
	content, err := FS.ReadFile("189_persist_user_token_version.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS token_version bigint NOT NULL DEFAULT 0")
	require.Contains(t, sql, "CHECK (token_version >= 0)")
	require.Contains(t, sql, "users_token_version_nonnegative")
}
