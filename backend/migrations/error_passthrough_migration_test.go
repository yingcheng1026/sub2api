package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrorPassthroughSecurityMigrationsSeparateSchemaAndData(t *testing.T) {
	defaultMigration, err := FS.ReadFile("192_error_passthrough_safe_default.sql")
	require.NoError(t, err)
	backfillMigration, err := FS.ReadFile("193_disable_legacy_error_body_passthrough.sql")
	require.NoError(t, err)

	defaultSQL := strings.ToUpper(string(defaultMigration))
	assert.Contains(t, defaultSQL, "ALTER COLUMN PASSTHROUGH_BODY SET DEFAULT FALSE")
	assert.NotContains(t, defaultSQL, "UPDATE ERROR_PASSTHROUGH_RULES")

	backfillSQL := strings.ToUpper(string(backfillMigration))
	assert.Contains(t, backfillSQL, "UPDATE ERROR_PASSTHROUGH_RULES")
	assert.Contains(t, backfillSQL, "PASSTHROUGH_BODY = FALSE")
	assert.Contains(t, backfillSQL, "WHERE PASSTHROUGH_BODY = TRUE")
	assert.Contains(t, backfillSQL, "UPSTREAM REQUEST FAILED")
	assert.NotContains(t, backfillSQL, "ALTER TABLE")
}
