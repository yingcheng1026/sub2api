package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration196UsesPostgresSafeEvidenceRefValidation(t *testing.T) {
	content, err := FS.ReadFile("196_fix_reconciliation_evidence_ref_check.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "char_length(evidence_ref) BETWEEN 8 AND 500")
	require.Contains(t, sql, "evidence_ref ~ '^[A-Za-z0-9][A-Za-z0-9._:/#-]*$'")
	require.NotContains(t, sql, "{7,499}")
}
