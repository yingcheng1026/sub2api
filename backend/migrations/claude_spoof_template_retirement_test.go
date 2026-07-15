package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration195RetiresClaudeCodeSpoofTemplateAndSnapshots(t *testing.T) {
	content, err := FS.ReadFile("195_retire_claude_code_spoof_template.sql")
	require.NoError(t, err)

	sql := string(content)
	upper := strings.ToUpper(sql)
	require.Contains(t, upper, "UPDATE CHANNEL_MONITORS")
	require.Contains(t, upper, "SET TEMPLATE_ID = NULL")
	require.Contains(t, upper, "EXTRA_HEADERS = '{}'::JSONB")
	require.Contains(t, upper, "BODY_OVERRIDE_MODE = 'OFF'")
	require.Contains(t, upper, "BODY_OVERRIDE = NULL")
	require.Contains(t, upper, "DELETE FROM CHANNEL_MONITOR_REQUEST_TEMPLATES")
	require.Contains(t, sql, "provider = 'anthropic'")
	require.Contains(t, sql, "name = 'Claude Code 伪装'")
	require.Contains(t, sql, "claude-cli/%")
	require.Contains(t, upper, "JSONB_EACH_TEXT")
	require.Contains(t, upper, "JSONB_TYPEOF(EXTRA_HEADERS) = 'OBJECT'")
	require.Contains(t, sql, "lower(header.key)")
	require.Contains(t, sql, "lower(header.value)")
	require.Contains(t, sql, "anthropic-dangerous-direct-browser-access")
	require.Contains(t, sql, "%oauth-%")
	require.Contains(t, sql, `"cc_entrypoint"`)
	require.Contains(t, sql, "<billing_attribution>")
	require.Contains(t, sql, "anthropic''s official cli for claude")
	require.Less(t,
		strings.Index(upper, "UPDATE CHANNEL_MONITORS"),
		strings.Index(upper, "DELETE FROM CHANNEL_MONITOR_REQUEST_TEMPLATES"),
		"snapshots must be cleared before ON DELETE SET NULL loses the template relation",
	)
}
